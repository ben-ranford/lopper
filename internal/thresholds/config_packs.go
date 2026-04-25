package thresholds

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

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
	sources := make([]string, 0, len(cfg.Policy.Packs)+1)
	for idx, packRef := range cfg.Policy.Packs {
		packResult, err := r.resolvePack(canonical, trust, idx, packRef)
		if err != nil {
			return resolveMergeResult{}, err
		}
		merged = mergeOverrides(merged, packResult.overrides)
		mergedScope = mergeScope(mergedScope, packResult.scope)
		mergedFeatures = mergeFeatures(mergedFeatures, packResult.features)
		sources = append(sources, packResult.appliedSourcesLow...)
	}

	selfOverrides, err := cfg.toOverrides()
	if err != nil {
		return resolveMergeResult{}, fmt.Errorf(parseConfigErrFmt, canonical, err)
	}
	merged = mergeOverrides(merged, selfOverrides)
	mergedScope = mergeScope(mergedScope, cfg.Scope.toPathScope())
	mergedFeatures = mergeFeatures(mergedFeatures, cfg.Features.toFeatureConfig())
	sources = append(sources, canonical)

	return resolveMergeResult{
		overrides:         merged,
		scope:             mergedScope,
		features:          mergedFeatures,
		appliedSourcesLow: dedupeStable(sources),
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
