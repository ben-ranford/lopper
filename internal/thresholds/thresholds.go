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
	licenseDenyListSet                bool
	LicenseFailOnDeny                 *bool
	LicenseIncludeRegistryProvenance  *bool
}

type intThresholdValidator struct {
	value    func(*Values) int
	override func(*Overrides) *int
	validate func(int) error
}

type stringThresholdValidator struct {
	value    func(*Values) string
	override func(*Overrides) *string
	validate func(string) error
}

var intThresholdValidators = []intThresholdValidator{
	{
		value:    func(v *Values) int { return v.FailOnIncreasePercent },
		override: func(o *Overrides) *int { return o.FailOnIncreasePercent },
		validate: validateFailOnIncrease,
	},
	{
		value:    func(v *Values) int { return v.LowConfidenceWarningPercent },
		override: func(o *Overrides) *int { return o.LowConfidenceWarningPercent },
		validate: func(value int) error {
			return validatePercentageRange("low_confidence_warning_percent", value)
		},
	},
	{
		value:    func(v *Values) int { return v.MinUsagePercentForRecommendations },
		override: func(o *Overrides) *int { return o.MinUsagePercentForRecommendations },
		validate: func(value int) error {
			return validatePercentageRange("min_usage_percent_for_recommendations", value)
		},
	},
	{
		value:    func(v *Values) int { return v.MaxUncertainImportCount },
		override: func(o *Overrides) *int { return o.MaxUncertainImportCount },
		validate: func(value int) error {
			return validateThresholdWithDisableSentinel("max_uncertain_import_count", value)
		},
	},
}

var stringThresholdValidators = []stringThresholdValidator{
	{
		value:    func(v *Values) string { return v.LockfileDriftPolicy },
		override: func(o *Overrides) *string { return o.LockfileDriftPolicy },
		validate: validateLockfileDriftPolicy,
	},
}

func (o *Overrides) SetLicenseDenyList(values []string) {
	o.LicenseDenyList = append(make([]string, 0, len(values)), values...)
	o.licenseDenyListSet = true
}

func (o *Overrides) HasLicenseDenyListOverride() bool {
	return o.licenseDenyListSet
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
		LicenseDenyList:                   make([]string, 0),
		LicenseFailOnDeny:                 DefaultLicenseFailOnDeny,
		LicenseIncludeRegistryProvenance:  DefaultLicenseIncludeRegistryProvenance,
	}
}

func (v *Values) Validate() error {
	if err := validateValueInts(v); err != nil {
		return err
	}
	if err := report.ValidateRemovalCandidateWeightSet(RemovalCandidateWeights(*v)); err != nil {
		return err
	}
	if err := validateValueStrings(v); err != nil {
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
	if o.licenseDenyListSet || len(o.LicenseDenyList) > 0 {
		resolved.LicenseDenyList = append(make([]string, 0, len(o.LicenseDenyList)), o.LicenseDenyList...)
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
	if err := validateOverrideInts(o); err != nil {
		return err
	}
	if err := validateOptionalWeights(o); err != nil {
		return err
	}
	if err := validateOverrideStrings(o); err != nil {
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

func validateValueInts(values *Values) error {
	for _, field := range intThresholdValidators {
		if err := field.validate(field.value(values)); err != nil {
			return err
		}
	}
	return nil
}

func validateOverrideInts(overrides *Overrides) error {
	for _, field := range intThresholdValidators {
		value := field.override(overrides)
		if value == nil {
			continue
		}
		if err := field.validate(*value); err != nil {
			return err
		}
	}
	return nil
}

func validateValueStrings(values *Values) error {
	for _, field := range stringThresholdValidators {
		if err := field.validate(field.value(values)); err != nil {
			return err
		}
	}
	return nil
}

func validateOverrideStrings(overrides *Overrides) error {
	for _, field := range stringThresholdValidators {
		value := field.override(overrides)
		if value == nil {
			continue
		}
		if err := field.validate(*value); err != nil {
			return err
		}
	}
	return nil
}

func validateOptionalWeights(overrides *Overrides) error {
	if overrides.RemovalCandidateWeightUsage == nil &&
		overrides.RemovalCandidateWeightImpact == nil &&
		overrides.RemovalCandidateWeightConfidence == nil {
		return nil
	}
	weights := report.DefaultRemovalCandidateWeights()
	if overrides.RemovalCandidateWeightUsage != nil {
		weights.Usage = *overrides.RemovalCandidateWeightUsage
	}
	if overrides.RemovalCandidateWeightImpact != nil {
		weights.Impact = *overrides.RemovalCandidateWeightImpact
	}
	if overrides.RemovalCandidateWeightConfidence != nil {
		weights.Confidence = *overrides.RemovalCandidateWeightConfidence
	}
	return report.ValidateRemovalCandidateWeightSet(weights)
}

func normalizeDenyList(values []string) []string {
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
