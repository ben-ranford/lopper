package thresholds

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/ben-ranford/lopper/internal/report"
)

const duplicateThresholdErrFmt = "threshold %s is defined more than once"

type optionalStringPair struct {
	first     []string
	second    []string
	firstSet  bool
	secondSet bool
}

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
		ReachableVulnerabilityPriority:    c.ReachableVulnerabilityPriority,
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
	if err := applyNestedStringOverride("reachable_vulnerability_priority", &overrides.ReachableVulnerabilityPriority, c.Thresholds.ReachableVulnerabilityPriority); err != nil {
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
	if higher.ReachableVulnerabilityPriority != nil {
		merged.ReachableVulnerabilityPriority = higher.ReachableVulnerabilityPriority
	}
	return merged
}

func (s *rawScope) toPathScope() PathScope {
	if s == nil {
		return scopeFromOptionalStringPair(emptyOptionalStringPair())
	}
	return scopeFromOptionalStringPair(rawOptionalStringPair(s.Include, normalizePathPatterns, s.Exclude, normalizePathPatterns))
}

func normalizePathPatterns(patterns []string) []string {
	return normalizeUniqueStrings(patterns, func(pattern string) string {
		return filepath.ToSlash(strings.ReplaceAll(strings.TrimSpace(pattern), "\\", "/"))
	})
}

func mergeScope(base, higher PathScope) PathScope {
	return scopeFromOptionalStringPair(mergeOptionalStringPair(pathScopeToOptionalStringPair(base), pathScopeToOptionalStringPair(higher)))
}

func (f *rawFeatures) toFeatureConfig() FeatureConfig {
	if f == nil {
		return featureConfigFromOptionalStringPair(emptyOptionalStringPair())
	}
	return featureConfigFromOptionalStringPair(rawOptionalStringPair(f.Enable, normalizeFeatureRefs, f.Disable, normalizeFeatureRefs))
}

func normalizeFeatureRefs(refs []string) []string {
	return normalizeUniqueStrings(refs, strings.TrimSpace)
}

func mergeFeatures(base, higher FeatureConfig) FeatureConfig {
	return featureConfigFromOptionalStringPair(mergeOptionalStringPair(featureConfigToOptionalStringPair(base), featureConfigToOptionalStringPair(higher)))
}

type advisorySourceConfig struct {
	source string
	set    bool
}

type vulnerabilityExceptionConfig struct {
	exceptions []report.VulnerabilityException
	set        bool
}

func (a *rawAdvisories) toAdvisorySourceConfig(configPath string) advisorySourceConfig {
	if a == nil || a.Source == nil {
		return advisorySourceConfig{}
	}
	source := strings.TrimSpace(*a.Source)
	if source == "" {
		return advisorySourceConfig{set: true}
	}
	if filepath.IsAbs(source) {
		return advisorySourceConfig{source: filepath.Clean(source), set: true}
	}
	return advisorySourceConfig{source: filepath.Clean(filepath.Join(filepath.Dir(configPath), source)), set: true}
}

func (a *rawAdvisories) toVulnerabilityExceptionConfig(configPath string) (vulnerabilityExceptionConfig, error) {
	if a == nil || len(a.Exceptions) == 0 {
		return vulnerabilityExceptionConfig{}, nil
	}
	exceptions := make([]report.VulnerabilityException, 0, len(a.Exceptions))
	for index, exception := range a.Exceptions {
		normalized, err := normalizeVulnerabilityException(configPath, index, exception)
		if err != nil {
			return vulnerabilityExceptionConfig{}, err
		}
		exceptions = append(exceptions, normalized)
	}
	return vulnerabilityExceptionConfig{exceptions: exceptions, set: true}, nil
}

func normalizeVulnerabilityException(configPath string, index int, exception report.VulnerabilityException) (report.VulnerabilityException, error) {
	normalized := exception
	normalized.VulnerabilityID = strings.TrimSpace(normalized.VulnerabilityID)
	normalized.PURL = strings.TrimSpace(normalized.PURL)
	normalized.Package = strings.TrimSpace(normalized.Package)
	normalized.Repository = strings.TrimSpace(normalized.Repository)
	normalized.Path = strings.TrimSpace(normalized.Path)
	normalized.Owner = strings.TrimSpace(normalized.Owner)
	normalized.Reason = strings.TrimSpace(normalized.Reason)
	normalized.Status = strings.ToLower(strings.TrimSpace(normalized.Status))
	normalized.Justification = strings.TrimSpace(normalized.Justification)
	normalized.Issued = strings.TrimSpace(normalized.Issued)
	normalized.Expires = strings.TrimSpace(normalized.Expires)
	normalized.Source = strings.TrimSpace(normalized.Source)
	if normalized.Source == "" {
		normalized.Source = configPath
	}
	switch {
	case normalized.VulnerabilityID == "":
		return report.VulnerabilityException{}, fmt.Errorf("advisories.exceptions[%d].vulnerability_id is required", index)
	case normalized.Owner == "":
		return report.VulnerabilityException{}, fmt.Errorf("advisories.exceptions[%d].owner is required", index)
	case normalized.Reason == "":
		return report.VulnerabilityException{}, fmt.Errorf("advisories.exceptions[%d].reason is required", index)
	case normalized.Expires == "":
		return report.VulnerabilityException{}, fmt.Errorf("advisories.exceptions[%d].expires is required", index)
	}
	if normalized.Status == "" {
		normalized.Status = "accepted-risk"
	}
	switch normalized.Status {
	case "accepted-risk", "not-affected", "affected", "resolved", "under-investigation":
	default:
		return report.VulnerabilityException{}, fmt.Errorf("advisories.exceptions[%d].status is unsupported: %s", index, normalized.Status)
	}
	if normalized.Justification != "" && report.CycloneDXVEXJustification(normalized.Justification) == "" {
		return report.VulnerabilityException{}, fmt.Errorf("advisories.exceptions[%d].justification is unsupported: %s", index, normalized.Justification)
	}
	if normalized.PURL == "" && normalized.Package == "" {
		return report.VulnerabilityException{}, fmt.Errorf("advisories.exceptions[%d] must define purl or package scope", index)
	}
	if normalized.PURL == "*" || normalized.Package == "*" {
		return report.VulnerabilityException{}, fmt.Errorf("advisories.exceptions[%d] wildcard scopes are not allowed in preview", index)
	}
	if !validVulnerabilityExceptionExpiry(normalized.Expires) {
		return report.VulnerabilityException{}, fmt.Errorf("advisories.exceptions[%d].expires must be RFC3339 or YYYY-MM-DD", index)
	}
	return normalized, nil
}

func validVulnerabilityExceptionExpiry(value string) bool {
	for _, layout := range []string{time.RFC3339, "2006-01-02"} {
		if _, err := time.Parse(layout, value); err == nil {
			return true
		}
	}
	return false
}

func mergeAdvisorySource(base, higher advisorySourceConfig) advisorySourceConfig {
	if higher.set {
		return higher
	}
	return base
}

func mergeVulnerabilityExceptions(base, higher vulnerabilityExceptionConfig) vulnerabilityExceptionConfig {
	if !higher.set && len(higher.exceptions) == 0 {
		return base
	}
	merged := vulnerabilityExceptionConfig{set: base.set || higher.set}
	merged.exceptions = append(append([]report.VulnerabilityException{}, base.exceptions...), higher.exceptions...)
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

func normalizeOptionalStringList(values *[]string, normalize func([]string) []string) ([]string, bool) {
	if values == nil {
		return make([]string, 0), false
	}
	return normalize(*values), true
}

func normalizeOptionalStringPair(first *[]string, normalizeFirst func([]string) []string, second *[]string, normalizeSecond func([]string) []string) optionalStringPair {
	firstValues, firstSet := normalizeOptionalStringList(first, normalizeFirst)
	secondValues, secondSet := normalizeOptionalStringList(second, normalizeSecond)
	return optionalStringPair{
		first:     firstValues,
		second:    secondValues,
		firstSet:  firstSet,
		secondSet: secondSet,
	}
}

func normalizeUniqueStrings(values []string, normalize func(string) string) []string {
	seen := make(map[string]struct{}, len(values))
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		normalizedValue := normalize(value)
		if normalizedValue == "" {
			continue
		}
		if _, exists := seen[normalizedValue]; exists {
			continue
		}
		seen[normalizedValue] = struct{}{}
		normalized = append(normalized, normalizedValue)
	}
	if len(normalized) == 0 {
		return normalized
	}
	return normalized
}

func mergeOptionalStringList(base []string, baseSet bool, higher []string, higherSet bool) ([]string, bool) {
	if higherSet || len(higher) > 0 {
		return cloneStrings(higher), true
	}
	return base, baseSet
}

func emptyOptionalStringPair() optionalStringPair {
	return optionalStringPair{
		first:  make([]string, 0),
		second: make([]string, 0),
	}
}

func rawOptionalStringPair(first *[]string, normalizeFirst func([]string) []string, second *[]string, normalizeSecond func([]string) []string) optionalStringPair {
	return normalizeOptionalStringPair(first, normalizeFirst, second, normalizeSecond)
}

func mergeOptionalStringPair(base, higher optionalStringPair) optionalStringPair {
	firstValues, firstSet := mergeOptionalStringList(base.first, base.firstSet, higher.first, higher.firstSet)
	secondValues, secondSet := mergeOptionalStringList(base.second, base.secondSet, higher.second, higher.secondSet)
	return optionalStringPair{
		first:     firstValues,
		second:    secondValues,
		firstSet:  firstSet,
		secondSet: secondSet,
	}
}

func scopeFromOptionalStringPair(values optionalStringPair) PathScope {
	return PathScope{
		Include:    values.first,
		Exclude:    values.second,
		includeSet: values.firstSet,
		excludeSet: values.secondSet,
	}
}

func pathScopeToOptionalStringPair(scope PathScope) optionalStringPair {
	return optionalStringPair{
		first:     scope.Include,
		second:    scope.Exclude,
		firstSet:  scope.includeSet,
		secondSet: scope.excludeSet,
	}
}

func featureConfigFromOptionalStringPair(values optionalStringPair) FeatureConfig {
	return FeatureConfig{
		Enable:     values.first,
		Disable:    values.second,
		enableSet:  values.firstSet,
		disableSet: values.secondSet,
	}
}

func featureConfigToOptionalStringPair(features FeatureConfig) optionalStringPair {
	return optionalStringPair{
		first:     features.Enable,
		second:    features.Disable,
		firstSet:  features.enableSet,
		secondSet: features.disableSet,
	}
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
