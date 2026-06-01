package thresholds

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ben-ranford/lopper/internal/report"
)

const (
	// -1 disables threshold enforcement; 0 is strict zero-tolerance.
	DefaultFailOnIncreasePercent             = -1
	DefaultLowConfidenceWarningPercent       = 40
	DefaultMinUsagePercentForRecommendations = 40
	// -1 disables threshold enforcement; 0 is strict zero-tolerance.
	DefaultMaxUncertainImportCount          = -1
	DefaultLockfileDriftPolicy              = "warn"
	DefaultLicenseFailOnDeny                = false
	DefaultLicenseIncludeRegistryProvenance = false
)

var validLockfileDriftPolicies = map[string]struct{}{
	"off":  {},
	"warn": {},
	"fail": {},
}

var lockfileDriftPolicyValues = []string{"off", "warn", "fail"}

type Values struct {
	FailOnIncreasePercent             int
	LowConfidenceWarningPercent       int
	MinUsagePercentForRecommendations int
	MaxUncertainImportCount           int
	RemovalCandidateWeightUsage       float64
	RemovalCandidateWeightImpact      float64
	RemovalCandidateWeightConfidence  float64
	LockfileDriftPolicy               string
	LicenseDenyList                   []string
	LicenseFailOnDeny                 bool
	LicenseIncludeRegistryProvenance  bool
}

type Overrides struct {
	FailOnIncreasePercent             *int
	LowConfidenceWarningPercent       *int
	MinUsagePercentForRecommendations *int
	MaxUncertainImportCount           *int
	RemovalCandidateWeightUsage       *float64
	RemovalCandidateWeightImpact      *float64
	RemovalCandidateWeightConfidence  *float64
	LockfileDriftPolicy               *string
	LicenseDenyList                   []string
	LicenseFailOnDeny                 *bool
	LicenseIncludeRegistryProvenance  *bool
}

func RemovalCandidateWeights(v Values) report.RemovalCandidateWeights {
	return report.RemovalCandidateWeights{
		Usage:      v.RemovalCandidateWeightUsage,
		Impact:     v.RemovalCandidateWeightImpact,
		Confidence: v.RemovalCandidateWeightConfidence,
	}
}

func Defaults() Values {
	defaultWeights := report.DefaultRemovalCandidateWeights()
	return Values{
		FailOnIncreasePercent:             DefaultFailOnIncreasePercent,
		LowConfidenceWarningPercent:       DefaultLowConfidenceWarningPercent,
		MinUsagePercentForRecommendations: DefaultMinUsagePercentForRecommendations,
		MaxUncertainImportCount:           DefaultMaxUncertainImportCount,
		RemovalCandidateWeightUsage:       defaultWeights.Usage,
		RemovalCandidateWeightImpact:      defaultWeights.Impact,
		RemovalCandidateWeightConfidence:  defaultWeights.Confidence,
		LockfileDriftPolicy:               DefaultLockfileDriftPolicy,
		LicenseFailOnDeny:                 DefaultLicenseFailOnDeny,
		LicenseIncludeRegistryProvenance:  DefaultLicenseIncludeRegistryProvenance,
	}
}

func (v *Values) Validate() error {
	if err := validateFailOnIncrease(v.FailOnIncreasePercent); err != nil {
		return err
	}
	if err := validatePercentageRange("low_confidence_warning_percent", v.LowConfidenceWarningPercent); err != nil {
		return err
	}
	if err := validatePercentageRange("min_usage_percent_for_recommendations", v.MinUsagePercentForRecommendations); err != nil {
		return err
	}
	if err := validateThresholdWithDisableSentinel("max_uncertain_import_count", v.MaxUncertainImportCount); err != nil {
		return err
	}
	if err := report.ValidateRemovalCandidateWeightSet(RemovalCandidateWeights(*v)); err != nil {
		return err
	}
	if err := validateLockfileDriftPolicy(v.LockfileDriftPolicy); err != nil {
		return err
	}
	v.LicenseDenyList = normalizeDenyList(v.LicenseDenyList)
	return nil
}

func (o *Overrides) Apply(base Values) Values {
	resolved := base
	if o.FailOnIncreasePercent != nil {
		resolved.FailOnIncreasePercent = *o.FailOnIncreasePercent
	}
	if o.LowConfidenceWarningPercent != nil {
		resolved.LowConfidenceWarningPercent = *o.LowConfidenceWarningPercent
	}
	if o.MinUsagePercentForRecommendations != nil {
		resolved.MinUsagePercentForRecommendations = *o.MinUsagePercentForRecommendations
	}
	if o.MaxUncertainImportCount != nil {
		resolved.MaxUncertainImportCount = *o.MaxUncertainImportCount
	}
	if o.RemovalCandidateWeightUsage != nil {
		resolved.RemovalCandidateWeightUsage = *o.RemovalCandidateWeightUsage
	}
	if o.RemovalCandidateWeightImpact != nil {
		resolved.RemovalCandidateWeightImpact = *o.RemovalCandidateWeightImpact
	}
	if o.RemovalCandidateWeightConfidence != nil {
		resolved.RemovalCandidateWeightConfidence = *o.RemovalCandidateWeightConfidence
	}
	if o.LockfileDriftPolicy != nil {
		resolved.LockfileDriftPolicy = *o.LockfileDriftPolicy
	}
	if len(o.LicenseDenyList) > 0 {
		resolved.LicenseDenyList = append([]string{}, o.LicenseDenyList...)
	}
	if o.LicenseFailOnDeny != nil {
		resolved.LicenseFailOnDeny = *o.LicenseFailOnDeny
	}
	if o.LicenseIncludeRegistryProvenance != nil {
		resolved.LicenseIncludeRegistryProvenance = *o.LicenseIncludeRegistryProvenance
	}
	resolved.LicenseDenyList = normalizeDenyList(resolved.LicenseDenyList)
	return resolved
}

func (o *Overrides) Validate() error {
	if err := validateOptionalInt(o.FailOnIncreasePercent, validateFailOnIncrease); err != nil {
		return err
	}
	if err := validateOptionalInt(o.LowConfidenceWarningPercent, func(value int) error {
		return validatePercentageRange("low_confidence_warning_percent", value)
	}); err != nil {
		return err
	}
	if err := validateOptionalInt(o.MinUsagePercentForRecommendations, func(value int) error {
		return validatePercentageRange("min_usage_percent_for_recommendations", value)
	}); err != nil {
		return err
	}
	if err := validateOptionalInt(o.MaxUncertainImportCount, func(value int) error {
		return validateThresholdWithDisableSentinel("max_uncertain_import_count", value)
	}); err != nil {
		return err
	}
	if err := validateOptionalWeights(o); err != nil {
		return err
	}
	if err := validateOptionalString(o.LockfileDriftPolicy, validateLockfileDriftPolicy); err != nil {
		return err
	}
	o.LicenseDenyList = normalizeDenyList(o.LicenseDenyList)
	return nil
}

func validateFailOnIncrease(value int) error {
	return validateThresholdWithDisableSentinel("fail_on_increase_percent", value)
}

func validateThresholdWithDisableSentinel(name string, value int) error {
	if value < -1 {
		return fmt.Errorf("invalid threshold %s: %d (must be -1 (disabled) or >= 0)", name, value)
	}
	return nil
}

func validateLockfileDriftPolicy(value string) error {
	if _, ok := validLockfileDriftPolicies[value]; ok {
		return nil
	}
	return fmt.Errorf("invalid threshold lockfile_drift_policy: %q (must be one of: %s)", value, strings.Join(lockfileDriftPolicyValues, ", "))
}

func validatePercentageRange(name string, value int) error {
	if value < 0 || value > 100 {
		return fmt.Errorf("invalid threshold %s: %d (must be between 0 and 100)", name, value)
	}
	return nil
}

func validateOptionalInt(value *int, validate func(int) error) error {
	if value == nil {
		return nil
	}
	return validate(*value)
}

func validateOptionalString(value *string, validate func(string) error) error {
	if value == nil {
		return nil
	}
	return validate(*value)
}

func validateOptionalWeights(overrides *Overrides) error {
	if overrides.RemovalCandidateWeightUsage == nil &&
		overrides.RemovalCandidateWeightImpact == nil &&
		overrides.RemovalCandidateWeightConfidence == nil {
		return nil
	}
	values := overrides.Apply(Defaults())
	return report.ValidateRemovalCandidateWeightSet(RemovalCandidateWeights(values))
}

func normalizeDenyList(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		item := normalizeSPDXID(value)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	if len(out) == 0 {
		return nil
	}
	sort.Strings(out)
	return out
}

func normalizeSPDXID(value string) string {
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r - 'a' + 'A')
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-', r == '.', r == '+':
			b.WriteRune(r)
		}
	}
	return b.String()
}
