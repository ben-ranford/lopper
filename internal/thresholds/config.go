package thresholds

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ben-ranford/lopper/internal/safeio"
	"gopkg.in/yaml.v3"
)

const (
	readConfigFileErrFmt = "read config file %s: %w"
	defaultPolicySource  = "defaults"
)

type LoadResult struct {
	Overrides     Overrides
	Resolved      Values
	ConfigPath    string
	PolicySources []string
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
			PolicySources: []string{defaultPolicySource},
		}, nil
	}

	resolver := newPackResolver(repoAbs)
	mergeResult, err := resolver.resolveFile(configPath, explicitProvided)
	if err != nil {
		return LoadResult{}, err
	}
	if err := mergeResult.overrides.Validate(); err != nil {
		return LoadResult{}, fmt.Errorf("parse config file %s: %w", configPath, err)
	}

	policySources := mergeResult.policySourcesHighToLow()
	resolved := mergeResult.overrides.Apply(Defaults())
	if err := resolved.Validate(); err != nil {
		return LoadResult{}, fmt.Errorf("parse config file %s: %w", configPath, err)
	}

	return LoadResult{
		Overrides:     mergeResult.overrides,
		Resolved:      resolved,
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
	ext := strings.ToLower(filepath.Ext(path))
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

type rawConfig struct {
	Policy rawPolicy `yaml:"policy" json:"policy"`

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

func mergeOverrides(base Overrides, higher Overrides) Overrides {
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

type packResolver struct {
	repoPath string
	stack    []string
}

type resolveMergeResult struct {
	overrides         Overrides
	appliedSourcesLow []string
}

func (r resolveMergeResult) policySourcesHighToLow() []string {
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
	canonical := filepath.Clean(path)
	if !filepath.IsAbs(canonical) {
		var err error
		canonical, err = filepath.Abs(canonical)
		if err != nil {
			return resolveMergeResult{}, fmt.Errorf("resolve policy path: %w", err)
		}
	}
	if err := r.push(canonical); err != nil {
		return resolveMergeResult{}, err
	}
	defer r.pop(canonical)

	data, err := readConfigFile(r.repoPath, canonical, explicitProvided)
	if err != nil {
		return resolveMergeResult{}, fmt.Errorf(readConfigFileErrFmt, canonical, err)
	}

	cfg, err := parseConfig(canonical, data)
	if err != nil {
		return resolveMergeResult{}, err
	}

	merged := Overrides{}
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
		sources = append(sources, packResult.appliedSourcesLow...)
	}

	selfOverrides, err := cfg.toOverrides()
	if err != nil {
		return resolveMergeResult{}, fmt.Errorf("parse config file %s: %w", canonical, err)
	}
	merged = mergeOverrides(merged, selfOverrides)
	sources = append(sources, canonical)

	return resolveMergeResult{overrides: merged, appliedSourcesLow: dedupeStable(sources)}, nil
}

func resolvePackRef(currentPath, ref string) (string, error) {
	trimmed := strings.TrimSpace(ref)
	if trimmed == "" {
		return "", fmt.Errorf("pack reference must not be empty")
	}
	if strings.HasPrefix(trimmed, "http://") || strings.HasPrefix(trimmed, "https://") {
		return "", fmt.Errorf("remote policy packs are not supported; use local file paths")
	}
	if filepath.IsAbs(trimmed) {
		return filepath.Clean(trimmed), nil
	}
	return filepath.Clean(filepath.Join(filepath.Dir(currentPath), trimmed)), nil
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
