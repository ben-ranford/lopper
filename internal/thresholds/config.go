package thresholds

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ben-ranford/lopper/internal/safeio"
	"gopkg.in/yaml.v3"
)

const (
	readConfigFileErrFmt = "read config file %s: %w"
	parseConfigErrFmt    = "parse config file %s: %w"
	defaultPolicySource  = "defaults"
	remotePolicyPinKey   = "sha256"
	maxRemotePolicyBytes = 1 << 20
)

var remotePolicyHTTPClient = &http.Client{Timeout: 10 * time.Second}

type LoadResult struct {
	Overrides     Overrides
	Resolved      Values
	Scope         PathScope
	ConfigPath    string
	PolicySources []string
}

type PathScope struct {
	Include []string
	Exclude []string
}

func Load(repoPath, explicitPath string) (Overrides, string, error) {
	result, err := LoadWithPolicy(repoPath, explicitPath)
	if err != nil {
		return Overrides{}, "", err
	}
	return result.Overrides, result.ConfigPath, nil
}

func LoadWithPolicy(repoPath, explicitPath string) (LoadResult, error) {
	repoAbs, err := filepath.Abs(repoPath)
	if err != nil {
		return LoadResult{}, fmt.Errorf("resolve repo path: %w", err)
	}
	explicitProvided := strings.TrimSpace(explicitPath) != ""

	configPath, found, err := resolveConfigPath(repoAbs, strings.TrimSpace(explicitPath))
	if err != nil {
		return LoadResult{}, err
	}
	if !found {
		return LoadResult{
			Resolved:      Defaults(),
			Scope:         PathScope{},
			PolicySources: []string{defaultPolicySource},
		}, nil
	}

	resolver := newPackResolver(repoAbs)
	mergeResult, err := resolver.resolveFile(configPath, explicitProvided)
	if err != nil {
		return LoadResult{}, err
	}
	if err := mergeResult.overrides.Validate(); err != nil {
		return LoadResult{}, fmt.Errorf(parseConfigErrFmt, configPath, err)
	}

	policySources := mergeResult.policySourcesHighToLow()
	resolved := mergeResult.overrides.Apply(Defaults())
	if err := resolved.Validate(); err != nil {
		return LoadResult{}, fmt.Errorf(parseConfigErrFmt, configPath, err)
	}

	return LoadResult{
		Overrides:     mergeResult.overrides,
		Resolved:      resolved,
		Scope:         mergeResult.scope,
		ConfigPath:    configPath,
		PolicySources: policySources,
	}, nil
}

func resolveConfigPath(repoPath, explicitPath string) (string, bool, error) {
	if explicitPath != "" {
		candidate := explicitPath
		if !filepath.IsAbs(candidate) {
			candidate = filepath.Join(repoPath, candidate)
		}
		candidate = filepath.Clean(candidate)
		if _, err := os.Stat(candidate); err != nil {
			if os.IsNotExist(err) {
				return "", false, fmt.Errorf("config file not found: %s", candidate)
			}
			return "", false, fmt.Errorf(readConfigFileErrFmt, candidate, err)
		}
		return candidate, true, nil
	}

	for _, name := range []string{".lopper.yml", ".lopper.yaml", "lopper.json"} {
		candidate := filepath.Join(repoPath, name)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, true, nil
		} else if err != nil && !os.IsNotExist(err) {
			return "", false, fmt.Errorf(readConfigFileErrFmt, candidate, err)
		}
	}

	return "", false, nil
}

func readConfigFile(repoPath, path string, explicitProvided bool) ([]byte, error) {
	if !explicitProvided || isPathUnderRoot(repoPath, path) {
		return safeio.ReadFileUnder(repoPath, path)
	}
	return safeio.ReadFile(path)
}

func parseConfig(path string, data []byte) (rawConfig, error) {
	var cfg rawConfig
	ext := configExtension(path)
	switch ext {
	case ".json":
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&cfg); err != nil {
			return rawConfig{}, fmt.Errorf("invalid JSON config: %w", err)
		}
		if decoder.More() {
			return rawConfig{}, fmt.Errorf("invalid JSON config: multiple JSON values")
		}
	default:
		decoder := yaml.NewDecoder(bytes.NewReader(data))
		decoder.KnownFields(true)
		if err := decoder.Decode(&cfg); err != nil {
			return rawConfig{}, fmt.Errorf("invalid YAML config: %w", err)
		}
	}
	return cfg, nil
}

func configExtension(location string) string {
	if parsed, ok := parseRemoteURL(location); ok {
		return strings.ToLower(filepath.Ext(parsed.Path))
	}
	return strings.ToLower(filepath.Ext(location))
}

type rawConfig struct {
	Policy rawPolicy `yaml:"policy" json:"policy"`
	Scope  rawScope  `yaml:"scope" json:"scope"`

	Thresholds rawThresholds `yaml:"thresholds" json:"thresholds"`

	FailOnIncreasePercent             *int     `yaml:"fail_on_increase_percent" json:"fail_on_increase_percent"`
	LowConfidenceWarningPercent       *int     `yaml:"low_confidence_warning_percent" json:"low_confidence_warning_percent"`
	MinUsagePercentForRecommendations *int     `yaml:"min_usage_percent_for_recommendations" json:"min_usage_percent_for_recommendations"`
	RemovalCandidateWeightUsage       *float64 `yaml:"removal_candidate_weight_usage" json:"removal_candidate_weight_usage"`
	RemovalCandidateWeightImpact      *float64 `yaml:"removal_candidate_weight_impact" json:"removal_candidate_weight_impact"`
	RemovalCandidateWeightConfidence  *float64 `yaml:"removal_candidate_weight_confidence" json:"removal_candidate_weight_confidence"`
}

type rawPolicy struct {
	Packs []string `yaml:"packs" json:"packs"`
}

type rawScope struct {
	Include []string `yaml:"include" json:"include"`
	Exclude []string `yaml:"exclude" json:"exclude"`
}

type rawThresholds struct {
	FailOnIncreasePercent             *int     `yaml:"fail_on_increase_percent" json:"fail_on_increase_percent"`
	LowConfidenceWarningPercent       *int     `yaml:"low_confidence_warning_percent" json:"low_confidence_warning_percent"`
	MinUsagePercentForRecommendations *int     `yaml:"min_usage_percent_for_recommendations" json:"min_usage_percent_for_recommendations"`
	RemovalCandidateWeightUsage       *float64 `yaml:"removal_candidate_weight_usage" json:"removal_candidate_weight_usage"`
	RemovalCandidateWeightImpact      *float64 `yaml:"removal_candidate_weight_impact" json:"removal_candidate_weight_impact"`
	RemovalCandidateWeightConfidence  *float64 `yaml:"removal_candidate_weight_confidence" json:"removal_candidate_weight_confidence"`
}

func (c *rawConfig) toOverrides() (Overrides, error) {
	overrides := Overrides{
		FailOnIncreasePercent:             c.FailOnIncreasePercent,
		LowConfidenceWarningPercent:       c.LowConfidenceWarningPercent,
		MinUsagePercentForRecommendations: c.MinUsagePercentForRecommendations,
		RemovalCandidateWeightUsage:       c.RemovalCandidateWeightUsage,
		RemovalCandidateWeightImpact:      c.RemovalCandidateWeightImpact,
		RemovalCandidateWeightConfidence:  c.RemovalCandidateWeightConfidence,
	}
	if err := applyNestedOverride("fail_on_increase_percent", &overrides.FailOnIncreasePercent, c.Thresholds.FailOnIncreasePercent); err != nil {
		return Overrides{}, err
	}
	if err := applyNestedOverride("low_confidence_warning_percent", &overrides.LowConfidenceWarningPercent, c.Thresholds.LowConfidenceWarningPercent); err != nil {
		return Overrides{}, err
	}
	if err := applyNestedOverride("min_usage_percent_for_recommendations", &overrides.MinUsagePercentForRecommendations, c.Thresholds.MinUsagePercentForRecommendations); err != nil {
		return Overrides{}, err
	}
	if err := applyNestedFloatOverride("removal_candidate_weight_usage", &overrides.RemovalCandidateWeightUsage, c.Thresholds.RemovalCandidateWeightUsage); err != nil {
		return Overrides{}, err
	}
	if err := applyNestedFloatOverride("removal_candidate_weight_impact", &overrides.RemovalCandidateWeightImpact, c.Thresholds.RemovalCandidateWeightImpact); err != nil {
		return Overrides{}, err
	}
	if err := applyNestedFloatOverride("removal_candidate_weight_confidence", &overrides.RemovalCandidateWeightConfidence, c.Thresholds.RemovalCandidateWeightConfidence); err != nil {
		return Overrides{}, err
	}
	return overrides, nil
}

func applyNestedOverride(name string, target **int, nested *int) error {
	if nested == nil {
		return nil
	}
	if *target != nil {
		return fmt.Errorf("threshold %s is defined more than once", name)
	}
	*target = nested
	return nil
}

func applyNestedFloatOverride(name string, target **float64, nested *float64) error {
	if nested == nil {
		return nil
	}
	if *target != nil {
		return fmt.Errorf("threshold %s is defined more than once", name)
	}
	*target = nested
	return nil
}

func mergeOverrides(base, higher Overrides) Overrides {
	merged := base
	if higher.FailOnIncreasePercent != nil {
		merged.FailOnIncreasePercent = higher.FailOnIncreasePercent
	}
	if higher.LowConfidenceWarningPercent != nil {
		merged.LowConfidenceWarningPercent = higher.LowConfidenceWarningPercent
	}
	if higher.MinUsagePercentForRecommendations != nil {
		merged.MinUsagePercentForRecommendations = higher.MinUsagePercentForRecommendations
	}
	if higher.RemovalCandidateWeightUsage != nil {
		merged.RemovalCandidateWeightUsage = higher.RemovalCandidateWeightUsage
	}
	if higher.RemovalCandidateWeightImpact != nil {
		merged.RemovalCandidateWeightImpact = higher.RemovalCandidateWeightImpact
	}
	if higher.RemovalCandidateWeightConfidence != nil {
		merged.RemovalCandidateWeightConfidence = higher.RemovalCandidateWeightConfidence
	}
	return merged
}

func (s rawScope) toPathScope() PathScope {
	return PathScope{
		Include: normalizePathPatterns(s.Include),
		Exclude: normalizePathPatterns(s.Exclude),
	}
}

func normalizePathPatterns(patterns []string) []string {
	if len(patterns) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(patterns))
	normalized := make([]string, 0, len(patterns))
	for _, pattern := range patterns {
		trimmed := strings.TrimSpace(pattern)
		if trimmed == "" {
			continue
		}
		if _, exists := seen[trimmed]; exists {
			continue
		}
		seen[trimmed] = struct{}{}
		normalized = append(normalized, trimmed)
	}
	if len(normalized) == 0 {
		return nil
	}
	return normalized
}

func mergeScope(base, higher PathScope) PathScope {
	merged := base
	if len(higher.Include) > 0 {
		merged.Include = append([]string{}, higher.Include...)
	}
	if len(higher.Exclude) > 0 {
		merged.Exclude = append([]string{}, higher.Exclude...)
	}
	return merged
}

type packResolver struct {
	repoPath string
	stack    []string
}

type resolveMergeResult struct {
	overrides         Overrides
	scope             PathScope
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

func (r *packResolver) resolveFile(path string, explicitProvided bool) (resolveMergeResult, error) {
	canonical, remote, err := canonicalPolicyLocation(path)
	if err != nil {
		return resolveMergeResult{}, err
	}
	if err := r.push(canonical); err != nil {
		return resolveMergeResult{}, err
	}
	defer r.pop(canonical)

	data, err := readPolicyLocation(r.repoPath, canonical, explicitProvided, remote)
	if err != nil {
		return resolveMergeResult{}, err
	}

	cfg, err := parseConfig(canonical, data)
	if err != nil {
		return resolveMergeResult{}, err
	}

	merged := Overrides{}
	mergedScope := PathScope{}
	sources := make([]string, 0, len(cfg.Policy.Packs)+1)
	for idx, packRef := range cfg.Policy.Packs {
		resolvedRef, err := resolvePackRef(canonical, packRef)
		if err != nil {
			return resolveMergeResult{}, fmt.Errorf("parse config file %s: invalid policy.packs[%d]: %w", canonical, idx, err)
		}
		packResult, err := r.resolveFile(resolvedRef, true)
		if err != nil {
			return resolveMergeResult{}, err
		}
		merged = mergeOverrides(merged, packResult.overrides)
		mergedScope = mergeScope(mergedScope, packResult.scope)
		sources = append(sources, packResult.appliedSourcesLow...)
	}

	selfOverrides, err := cfg.toOverrides()
	if err != nil {
		return resolveMergeResult{}, fmt.Errorf(parseConfigErrFmt, canonical, err)
	}
	merged = mergeOverrides(merged, selfOverrides)
	mergedScope = mergeScope(mergedScope, cfg.Scope.toPathScope())
	sources = append(sources, canonical)

	return resolveMergeResult{overrides: merged, scope: mergedScope, appliedSourcesLow: dedupeStable(sources)}, nil
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

func readPolicyLocation(repoPath, location string, explicitProvided, remote bool) ([]byte, error) {
	if remote {
		data, err := readRemotePolicyFile(location)
		if err != nil {
			return nil, fmt.Errorf("read remote policy file %s: %w", location, err)
		}
		return data, nil
	}
	data, err := readConfigFile(repoPath, location, explicitProvided)
	if err != nil {
		return nil, fmt.Errorf(readConfigFileErrFmt, location, err)
	}
	return data, nil
}

func parseRemoteURL(raw string) (*url.URL, bool) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, false
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, false
	}
	if strings.TrimSpace(parsed.Host) == "" {
		return nil, false
	}
	return parsed, true
}

func canonicalRemotePolicyURL(raw string) (string, error) {
	parsed, ok := parseRemoteURL(raw)
	if !ok {
		return "", fmt.Errorf("invalid remote policy URL: %s", raw)
	}
	pin, err := extractRemotePolicyPin(parsed.Fragment)
	if err != nil {
		return "", err
	}
	parsed.Fragment = remotePolicyPinKey + "=" + pin
	return parsed.String(), nil
}

func extractRemotePolicyPin(fragment string) (string, error) {
	trimmed := strings.TrimSpace(fragment)
	if trimmed == "" {
		return "", fmt.Errorf("remote policy packs must include a sha256 pin (example: #sha256=<hex>)")
	}
	key, value, ok := strings.Cut(trimmed, "=")
	if !ok {
		return "", fmt.Errorf("invalid remote policy pin %q; expected sha256=<hex>", fragment)
	}
	if strings.ToLower(strings.TrimSpace(key)) != remotePolicyPinKey {
		return "", fmt.Errorf("unsupported remote policy pin key %q; expected sha256", key)
	}
	normalized := strings.ToLower(strings.TrimSpace(value))
	if len(normalized) != 64 {
		return "", fmt.Errorf("invalid remote policy sha256 pin length: got %d, expected 64", len(normalized))
	}
	if _, err := hex.DecodeString(normalized); err != nil {
		return "", fmt.Errorf("invalid remote policy sha256 pin: %w", err)
	}
	return normalized, nil
}

func readRemotePolicyFile(location string) ([]byte, error) {
	parsed, err := url.Parse(location)
	if err != nil {
		return nil, fmt.Errorf("parse remote policy URL: %w", err)
	}
	expectedHash, err := extractRemotePolicyPin(parsed.Fragment)
	if err != nil {
		return nil, err
	}
	parsed.Fragment = ""

	response, err := remotePolicyHTTPClient.Get(parsed.String())
	if err != nil {
		return nil, fmt.Errorf("fetch remote policy: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode < 200 || response.StatusCode > 299 {
		return nil, fmt.Errorf("fetch remote policy: unexpected status %d", response.StatusCode)
	}

	limited := io.LimitReader(response.Body, maxRemotePolicyBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("read remote policy response: %w", err)
	}
	if len(data) > maxRemotePolicyBytes {
		return nil, fmt.Errorf("remote policy exceeded size limit of %d bytes", maxRemotePolicyBytes)
	}

	sum := sha256.Sum256(data)
	got := hex.EncodeToString(sum[:])
	if got != expectedHash {
		return nil, fmt.Errorf("remote policy sha256 mismatch: expected %s, got %s", expectedHash, got)
	}
	return data, nil
}

func dedupeStable(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
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

func isPathUnderRoot(rootPath, targetPath string) bool {
	relative, err := filepath.Rel(rootPath, targetPath)
	if err != nil {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator))
}
