package thresholds

import (
	"fmt"
	"math"
)

const (
	DefaultFailOnIncreasePercent             = 0
	DefaultLowConfidenceWarningPercent       = 40
	DefaultMinUsagePercentForRecommendations = 40
	DefaultRemovalCandidateWeightUsage       = 0.50
	DefaultRemovalCandidateWeightImpact      = 0.30
	DefaultRemovalCandidateWeightConfidence  = 0.20
)

type Values struct {
	FailOnIncreasePercent             int
	LowConfidenceWarningPercent       int
	MinUsagePercentForRecommendations int
	RemovalCandidateWeightUsage       float64
	RemovalCandidateWeightImpact      float64
	RemovalCandidateWeightConfidence  float64
}

type Overrides struct {
	FailOnIncreasePercent             *int
	LowConfidenceWarningPercent       *int
	MinUsagePercentForRecommendations *int
	RemovalCandidateWeightUsage       *float64
	RemovalCandidateWeightImpact      *float64
	RemovalCandidateWeightConfidence  *float64
}

func Defaults() Values {
	return Values{
		FailOnIncreasePercent:             DefaultFailOnIncreasePercent,
		LowConfidenceWarningPercent:       DefaultLowConfidenceWarningPercent,
		MinUsagePercentForRecommendations: DefaultMinUsagePercentForRecommendations,
		RemovalCandidateWeightUsage:       DefaultRemovalCandidateWeightUsage,
		RemovalCandidateWeightImpact:      DefaultRemovalCandidateWeightImpact,
		RemovalCandidateWeightConfidence:  DefaultRemovalCandidateWeightConfidence,
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
	if err := validateWeight("removal_candidate_weight_usage", v.RemovalCandidateWeightUsage); err != nil {
		return err
	}
	if err := validateWeight("removal_candidate_weight_impact", v.RemovalCandidateWeightImpact); err != nil {
		return err
	}
	if err := validateWeight("removal_candidate_weight_confidence", v.RemovalCandidateWeightConfidence); err != nil {
		return err
	}
	if !hasPositiveWeight(v.RemovalCandidateWeightUsage, v.RemovalCandidateWeightImpact, v.RemovalCandidateWeightConfidence) {
		return fmt.Errorf("invalid removal candidate weights: at least one weight must be greater than 0")
	}
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
	if o.RemovalCandidateWeightUsage != nil {
		resolved.RemovalCandidateWeightUsage = *o.RemovalCandidateWeightUsage
	}
	if o.RemovalCandidateWeightImpact != nil {
		resolved.RemovalCandidateWeightImpact = *o.RemovalCandidateWeightImpact
	}
	if o.RemovalCandidateWeightConfidence != nil {
		resolved.RemovalCandidateWeightConfidence = *o.RemovalCandidateWeightConfidence
	}
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
	if err := validateOptionalWeights(o); err != nil {
		return err
	}
	return nil
}

func validateFailOnIncrease(value int) error {
	if value < 0 {
		return fmt.Errorf("invalid threshold fail_on_increase_percent: %d (must be >= 0)", value)
	}
	return nil
}

func validatePercentageRange(name string, value int) error {
	if value < 0 || value > 100 {
		return fmt.Errorf("invalid threshold %s: %d (must be between 0 and 100)", name, value)
	}
	return nil
}

func validateWeight(name string, value float64) error {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return fmt.Errorf("invalid threshold %s: %v (must be finite)", name, value)
	}
	if value < 0 {
		return fmt.Errorf("invalid threshold %s: %v (must be >= 0)", name, value)
	}
	return nil
}

func hasPositiveWeight(values ...float64) bool {
	for _, value := range values {
		if value > 0 {
			return true
		}
	}
	return false
}

func validateOptionalInt(value *int, validate func(int) error) error {
	if value == nil {
		return nil
	}
	return validate(*value)
}

func validateOptionalWeight(name string, value *float64) error {
	if value == nil {
		return nil
	}
	return validateWeight(name, *value)
}

func validateOptionalWeights(overrides *Overrides) error {
	weightChecks := []struct {
		name  string
		value *float64
	}{
		{name: "removal_candidate_weight_usage", value: overrides.RemovalCandidateWeightUsage},
		{name: "removal_candidate_weight_impact", value: overrides.RemovalCandidateWeightImpact},
		{name: "removal_candidate_weight_confidence", value: overrides.RemovalCandidateWeightConfidence},
	}
	anySet := false
	for _, check := range weightChecks {
		if check.value != nil {
			anySet = true
		}
		if err := validateOptionalWeight(check.name, check.value); err != nil {
			return err
		}
	}
	if !anySet {
		return nil
	}
	values := overrides.Apply(Defaults())
	if !hasPositiveWeight(values.RemovalCandidateWeightUsage, values.RemovalCandidateWeightImpact, values.RemovalCandidateWeightConfidence) {
		return fmt.Errorf("invalid removal candidate weights: at least one weight must be greater than 0")
	}
	return nil
}
