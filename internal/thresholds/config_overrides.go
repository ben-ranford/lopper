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
		LicenseDenyList:                   append([]string{}, c.LicenseDeny...),
		LicenseFailOnDeny:                 c.LicenseFailOnDeny,
		LicenseIncludeRegistryProvenance:  c.LicenseIncludeRegistryProvenance,
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
	if err := applyNestedListOverride("license_deny", &overrides.LicenseDenyList, c.Thresholds.LicenseDeny); err != nil {
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

func applyNestedListOverride(name string, target *[]string, nested []string) error {
	if len(nested) == 0 {
		return nil
	}
	if len(*target) > 0 {
		return fmt.Errorf(duplicateThresholdErrFmt, name)
	}
	*target = append([]string{}, nested...)
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
	if len(higher.LicenseDenyList) > 0 {
		merged.LicenseDenyList = append([]string{}, higher.LicenseDenyList...)
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
	if s == nil {
		return PathScope{}
	}
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
