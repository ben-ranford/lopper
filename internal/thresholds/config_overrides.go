package thresholds

import (
	"fmt"
	"path/filepath"
	"strings"
)

const duplicateThresholdErrFmt = "threshold %s is defined more than once"

func (c *rawConfig) toOverrides() (Overrides, error) {
	overrides := Overrides{
		FailOnIncreasePercent:             c.FailOnIncreasePercent,
		LowConfidenceWarningPercent:       c.LowConfidenceWarningPercent,
		MinUsagePercentForRecommendations: c.MinUsagePercentForRecommendations,
		MaxUncertainImportCount:           c.MaxUncertainImportCount,
		RemovalCandidateWeightUsage:       c.RemovalCandidateWeightUsage,
		RemovalCandidateWeightImpact:      c.RemovalCandidateWeightImpact,
		RemovalCandidateWeightConfidence:  c.RemovalCandidateWeightConfidence,
		LockfileDriftPolicy:               c.LockfileDriftPolicy,
		LicenseFailOnDeny:                 c.LicenseFailOnDeny,
		LicenseIncludeRegistryProvenance:  c.LicenseIncludeRegistryProvenance,
	}
	if c.LicenseDeny != nil {
		overrides.LicenseDenyList = cloneStrings(*c.LicenseDeny)
		overrides.licenseDenyListSet = true
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
	if err := applyNestedOverride("max_uncertain_import_count", &overrides.MaxUncertainImportCount, c.Thresholds.MaxUncertainImportCount); err != nil {
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
	if err := applyNestedStringOverride("lockfile_drift_policy", &overrides.LockfileDriftPolicy, c.Thresholds.LockfileDriftPolicy); err != nil {
		return Overrides{}, err
	}
	if err := applyNestedListOverride("license_deny", &overrides.LicenseDenyList, &overrides.licenseDenyListSet, c.Thresholds.LicenseDeny); err != nil {
		return Overrides{}, err
	}
	if err := applyNestedBoolOverride("license_fail_on_deny", &overrides.LicenseFailOnDeny, c.Thresholds.LicenseFailOnDeny); err != nil {
		return Overrides{}, err
	}
	if err := applyNestedBoolOverride("license_include_registry_provenance", &overrides.LicenseIncludeRegistryProvenance, c.Thresholds.LicenseIncludeRegistryProvenance); err != nil {
		return Overrides{}, err
	}
	return overrides, nil
}

func applyNestedOverride(name string, target **int, nested *int) error {
	if nested == nil {
		return nil
	}
	if *target != nil {
		return fmt.Errorf(duplicateThresholdErrFmt, name)
	}
	*target = nested
	return nil
}

func applyNestedFloatOverride(name string, target **float64, nested *float64) error {
	if nested == nil {
		return nil
	}
	if *target != nil {
		return fmt.Errorf(duplicateThresholdErrFmt, name)
	}
	*target = nested
	return nil
}

func applyNestedStringOverride(name string, target **string, nested *string) error {
	if nested == nil {
		return nil
	}
	if *target != nil {
		return fmt.Errorf(duplicateThresholdErrFmt, name)
	}
	*target = nested
	return nil
}

func applyNestedListOverride(name string, target *[]string, targetSet *bool, nested *[]string) error {
	if nested == nil {
		return nil
	}
	if *targetSet {
		return fmt.Errorf(duplicateThresholdErrFmt, name)
	}
	*target = cloneStrings(*nested)
	*targetSet = true
	return nil
}

func applyNestedBoolOverride(name string, target **bool, nested *bool) error {
	if nested == nil {
		return nil
	}
	if *target != nil {
		return fmt.Errorf(duplicateThresholdErrFmt, name)
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
	if higher.MaxUncertainImportCount != nil {
		merged.MaxUncertainImportCount = higher.MaxUncertainImportCount
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
	if higher.LockfileDriftPolicy != nil {
		merged.LockfileDriftPolicy = higher.LockfileDriftPolicy
	}
	if higher.licenseDenyListSet || len(higher.LicenseDenyList) > 0 {
		merged.LicenseDenyList = cloneStrings(higher.LicenseDenyList)
		merged.licenseDenyListSet = true
	}
	if higher.LicenseFailOnDeny != nil {
		merged.LicenseFailOnDeny = higher.LicenseFailOnDeny
	}
	if higher.LicenseIncludeRegistryProvenance != nil {
		merged.LicenseIncludeRegistryProvenance = higher.LicenseIncludeRegistryProvenance
	}
	return merged
}

func (s *rawScope) toPathScope() PathScope {
	scope := PathScope{
		Include: make([]string, 0),
		Exclude: make([]string, 0),
	}
	if s == nil {
		return scope
	}
	if s.Include != nil {
		scope.Include = normalizePathPatterns(*s.Include)
		scope.includeSet = true
	}
	if s.Exclude != nil {
		scope.Exclude = normalizePathPatterns(*s.Exclude)
		scope.excludeSet = true
	}
	return scope
}

func normalizePathPatterns(patterns []string) []string {
	seen := make(map[string]struct{}, len(patterns))
	normalized := make([]string, 0, len(patterns))
	for _, pattern := range patterns {
		normalizedPattern := filepath.ToSlash(strings.ReplaceAll(strings.TrimSpace(pattern), "\\", "/"))
		if normalizedPattern == "" {
			continue
		}
		if _, exists := seen[normalizedPattern]; exists {
			continue
		}
		seen[normalizedPattern] = struct{}{}
		normalized = append(normalized, normalizedPattern)
	}
	if len(normalized) == 0 {
		return normalized
	}
	return normalized
}

func mergeScope(base, higher PathScope) PathScope {
	merged := base
	if higher.includeSet || len(higher.Include) > 0 {
		merged.Include = cloneStrings(higher.Include)
		merged.includeSet = true
	}
	if higher.excludeSet || len(higher.Exclude) > 0 {
		merged.Exclude = cloneStrings(higher.Exclude)
		merged.excludeSet = true
	}
	return merged
}

func (f *rawFeatures) toFeatureConfig() FeatureConfig {
	features := FeatureConfig{
		Enable:  make([]string, 0),
		Disable: make([]string, 0),
	}
	if f == nil {
		return features
	}
	if f.Enable != nil {
		features.Enable = normalizeFeatureRefs(*f.Enable)
		features.enableSet = true
	}
	if f.Disable != nil {
		features.Disable = normalizeFeatureRefs(*f.Disable)
		features.disableSet = true
	}
	return features
}

func normalizeFeatureRefs(refs []string) []string {
	seen := make(map[string]struct{}, len(refs))
	normalized := make([]string, 0, len(refs))
	for _, ref := range refs {
		trimmed := strings.TrimSpace(ref)
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
		return normalized
	}
	return normalized
}

func mergeFeatures(base, higher FeatureConfig) FeatureConfig {
	merged := base
	if higher.enableSet || len(higher.Enable) > 0 {
		merged.Enable = cloneStrings(higher.Enable)
		merged.enableSet = true
	}
	if higher.disableSet || len(higher.Disable) > 0 {
		merged.Disable = cloneStrings(higher.Disable)
		merged.disableSet = true
	}
	return merged
}

func normalizePathScope(scope PathScope) PathScope {
	if len(scope.Include) == 0 {
		scope.Include = make([]string, 0)
	}
	if len(scope.Exclude) == 0 {
		scope.Exclude = make([]string, 0)
	}
	return scope
}

func normalizeFeatureConfig(features FeatureConfig) FeatureConfig {
	if len(features.Enable) == 0 {
		features.Enable = make([]string, 0)
	}
	if len(features.Disable) == 0 {
		features.Disable = make([]string, 0)
	}
	return features
}

func normalizeOverrides(overrides Overrides) Overrides {
	if len(overrides.LicenseDenyList) == 0 {
		overrides.LicenseDenyList = make([]string, 0)
	}
	return overrides
}

func cloneStrings(values []string) []string {
	return append(make([]string, 0, len(values)), values...)
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
