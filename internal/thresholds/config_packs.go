package thresholds

import (
	"fmt"
	"net/url"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/safeio"
)

const invalidPolicyPackErrFmt = "parse config file %s: invalid policy.packs[%d]: %w"

type packResolver struct {
	repoPath string
	stack    []string
}

type packTrust struct {
	explicit  bool
	localRoot string
}

type resolveMergeResult struct {
	overrides         Overrides
	scope             PathScope
	features          FeatureConfig
	appliedSourcesLow []string
	policyTrace       map[string]string
}

func (r *resolveMergeResult) policySourcesHighToLow() []string {
	sources := make([]string, 0, len(r.appliedSourcesLow)+1)
	seen := map[string]struct{}{defaultPolicySource: {}}
	for i := len(r.appliedSourcesLow) - 1; i >= 0; i-- {
		source := r.appliedSourcesLow[i]
		if _, ok := seen[source]; ok {
			continue
		}
		seen[source] = struct{}{}
		sources = append(sources, source)
	}
	sources = append(sources, defaultPolicySource)
	return sources
}

func newPackResolver(repoPath string) *packResolver {
	return &packResolver{
		repoPath: repoPath,
		stack:    make([]string, 0, 8),
	}
}

func initialPackTrust(repoPath string, explicitProvided bool) packTrust {
	if explicitProvided {
		return packTrust{explicit: true}
	}
	return packTrust{localRoot: repoPath}
}

func (r *packResolver) nestedPackTrust(path string, remote bool) packTrust {
	if remote {
		return packTrust{}
	}
	if isPathUnderRoot(r.repoPath, path) {
		return packTrust{localRoot: r.repoPath}
	}
	return packTrust{}
}

func (r *packResolver) resolveFile(path string, trust packTrust) (resolveMergeResult, error) {
	canonical, remote, err := canonicalPolicyLocation(path)
	if err != nil {
		return resolveMergeResult{}, err
	}
	if err := r.push(canonical); err != nil {
		return resolveMergeResult{}, err
	}
	defer r.pop(canonical)

	data, err := readPolicyLocation(canonical, trust, remote)
	if err != nil {
		return resolveMergeResult{}, err
	}

	cfg, err := parseConfig(canonical, data)
	if err != nil {
		return resolveMergeResult{}, err
	}

	merged := Overrides{}
	mergedScope := PathScope{}
	mergedFeatures := FeatureConfig{}
	mergedTrace := defaultPolicyTrace()
	sources := make([]string, 0, len(cfg.Policy.Packs)+1)
	for idx, packRef := range cfg.Policy.Packs {
		packResult, err := r.resolvePack(canonical, trust, idx, packRef)
		if err != nil {
			return resolveMergeResult{}, err
		}
		merged = mergeOverrides(merged, packResult.overrides)
		mergedScope = mergeScope(mergedScope, packResult.scope)
		mergedFeatures = mergeFeatures(mergedFeatures, packResult.features)
		mergedTrace = mergePolicyTrace(mergedTrace, packResult.policyTrace)
		sources = append(sources, packResult.appliedSourcesLow...)
	}

	selfOverrides, err := cfg.toOverrides()
	if err != nil {
		return resolveMergeResult{}, fmt.Errorf(parseConfigErrFmt, canonical, err)
	}
	merged = mergeOverrides(merged, selfOverrides)
	mergedScope = mergeScope(mergedScope, cfg.Scope.toPathScope())
	mergedFeatures = mergeFeatures(mergedFeatures, cfg.Features.toFeatureConfig())
	mergedTrace = mergePolicyTrace(mergedTrace, traceForOverrides(canonical, selfOverrides))
	sources = append(sources, canonical)

	return resolveMergeResult{
		overrides:         merged,
		scope:             mergedScope,
		features:          mergedFeatures,
		appliedSourcesLow: dedupeStable(sources),
		policyTrace:       mergedTrace,
	}, nil
}

func (r *packResolver) resolvePack(canonical string, trust packTrust, idx int, packRef string) (resolveMergeResult, error) {
	resolvedRef, err := resolvePackRef(canonical, packRef)
	if err != nil {
		return resolveMergeResult{}, fmt.Errorf(invalidPolicyPackErrFmt, canonical, idx, err)
	}

	childCanonical, childRemote, err := canonicalPolicyLocation(resolvedRef)
	if err != nil {
		return resolveMergeResult{}, fmt.Errorf(invalidPolicyPackErrFmt, canonical, idx, err)
	}
	if childRemote && !trust.explicit {
		return resolveMergeResult{}, fmt.Errorf("parse config file %s: invalid policy.packs[%d]: remote policy packs require an explicit config path", canonical, idx)
	}
	if err := validatePackBoundary(childCanonical, childRemote, trust); err != nil {
		return resolveMergeResult{}, fmt.Errorf(invalidPolicyPackErrFmt, canonical, idx, err)
	}

	return r.resolveFile(childCanonical, r.nestedPackTrust(childCanonical, childRemote))
}

func validatePackBoundary(path string, remote bool, trust packTrust) error {
	if remote || trust.localRoot == "" {
		return nil
	}
	if isPathUnderRoot(trust.localRoot, path) {
		return nil
	}
	return fmt.Errorf("local policy packs must remain under %q", trust.localRoot)
}

func resolvePackRef(currentPath, ref string) (string, error) {
	trimmed := strings.TrimSpace(ref)
	if trimmed == "" {
		return "", fmt.Errorf("pack reference must not be empty")
	}

	if parentURL, ok := parseRemoteURL(currentPath); ok {
		parentBase := *parentURL
		parentBase.Fragment = ""
		relativeRef, err := url.Parse(trimmed)
		if err != nil {
			return "", fmt.Errorf("invalid remote pack reference %q: %w", trimmed, err)
		}
		return canonicalRemotePolicyURL(parentBase.ResolveReference(relativeRef).String())
	}

	if _, ok := parseRemoteURL(trimmed); ok {
		return canonicalRemotePolicyURL(trimmed)
	}
	if filepath.IsAbs(trimmed) {
		return filepath.Clean(trimmed), nil
	}
	return filepath.Clean(filepath.Join(filepath.Dir(currentPath), trimmed)), nil
}

func canonicalPolicyLocation(path string) (string, bool, error) {
	if _, ok := parseRemoteURL(path); ok {
		canonical, err := canonicalRemotePolicyURL(path)
		if err != nil {
			return "", false, err
		}
		return canonical, true, nil
	}

	canonical := filepath.Clean(path)
	if !filepath.IsAbs(canonical) {
		var err error
		canonical, err = filepath.Abs(canonical)
		if err != nil {
			return "", false, fmt.Errorf("resolve policy path: %w", err)
		}
	}
	return canonical, false, nil
}

func readPolicyLocation(location string, trust packTrust, remote bool) ([]byte, error) {
	if remote {
		data, err := readRemotePolicyFile(location)
		if err != nil {
			return nil, fmt.Errorf("read remote policy file %s: %w", location, err)
		}
		return data, nil
	}

	var (
		data []byte
		err  error
	)
	if trust.localRoot == "" {
		data, err = safeio.ReadFileLimit(location, maxRemotePolicyBytes)
	} else {
		data, err = safeio.ReadFileUnderLimit(trust.localRoot, location, maxRemotePolicyBytes)
	}
	if err != nil {
		return nil, fmt.Errorf(readConfigFileErrFmt, location, err)
	}
	return data, nil
}

var policyTraceFieldNames = []string{
	"thresholds.fail_on_increase_percent",
	"thresholds.low_confidence_warning_percent",
	"thresholds.min_usage_percent_for_recommendations",
	"thresholds.max_uncertain_import_count",
	"thresholds.lockfile_drift_policy",
	"removal_candidate_weights.usage",
	"removal_candidate_weights.impact",
	"removal_candidate_weights.confidence",
	"license.deny",
	"license.fail_on_deny",
	"license.include_registry_provenance",
}

func defaultPolicyTrace() map[string]string {
	trace := make(map[string]string, len(policyTraceFieldNames))
	for _, field := range policyTraceFieldNames {
		trace[field] = defaultPolicySource
	}
	return trace
}

func mergePolicyTrace(base, higher map[string]string) map[string]string {
	merged := make(map[string]string, len(base)+len(higher))
	for field, source := range base {
		merged[field] = source
	}
	for field, source := range higher {
		if strings.TrimSpace(source) == "" {
			continue
		}
		merged[field] = source
	}
	return merged
}

func traceForOverrides(source string, overrides Overrides) map[string]string {
	trace := make(map[string]string)
	if overrides.FailOnIncreasePercent != nil {
		trace["thresholds.fail_on_increase_percent"] = source
	}
	if overrides.LowConfidenceWarningPercent != nil {
		trace["thresholds.low_confidence_warning_percent"] = source
	}
	if overrides.MinUsagePercentForRecommendations != nil {
		trace["thresholds.min_usage_percent_for_recommendations"] = source
	}
	if overrides.MaxUncertainImportCount != nil {
		trace["thresholds.max_uncertain_import_count"] = source
	}
	if overrides.LockfileDriftPolicy != nil {
		trace["thresholds.lockfile_drift_policy"] = source
	}
	if overrides.RemovalCandidateWeightUsage != nil {
		trace["removal_candidate_weights.usage"] = source
	}
	if overrides.RemovalCandidateWeightImpact != nil {
		trace["removal_candidate_weights.impact"] = source
	}
	if overrides.RemovalCandidateWeightConfidence != nil {
		trace["removal_candidate_weights.confidence"] = source
	}
	if overrides.licenseDenyListSet {
		trace["license.deny"] = source
	}
	if overrides.LicenseFailOnDeny != nil {
		trace["license.fail_on_deny"] = source
	}
	if overrides.LicenseIncludeRegistryProvenance != nil {
		trace["license.include_registry_provenance"] = source
	}
	return trace
}

func policyTraceFromMap(trace map[string]string) []report.PolicyMergeTrace {
	if len(trace) == 0 {
		return nil
	}
	items := make([]report.PolicyMergeTrace, 0, len(trace))
	seen := make(map[string]struct{}, len(trace))
	for _, field := range policyTraceFieldNames {
		source, ok := trace[field]
		if !ok {
			continue
		}
		items = append(items, report.PolicyMergeTrace{Field: field, Source: source})
		seen[field] = struct{}{}
	}
	extras := make([]string, 0)
	for field := range trace {
		if _, ok := seen[field]; ok {
			continue
		}
		extras = append(extras, field)
	}
	sort.Strings(extras)
	for _, field := range extras {
		items = append(items, report.PolicyMergeTrace{Field: field, Source: trace[field]})
	}
	return items
}

func (r *packResolver) push(path string) error {
	for _, current := range r.stack {
		if current == path {
			chain := append(append([]string{}, r.stack...), path)
			return fmt.Errorf("policy pack cycle detected: %s", strings.Join(chain, " -> "))
		}
	}
	r.stack = append(r.stack, path)
	return nil
}

func (r *packResolver) pop(path string) {
	if len(r.stack) == 0 {
		return
	}
	if r.stack[len(r.stack)-1] == path {
		r.stack = r.stack[:len(r.stack)-1]
		return
	}
	for i := range r.stack {
		if r.stack[i] == path {
			r.stack = append(r.stack[:i], r.stack[i+1:]...)
			break
		}
	}
}
