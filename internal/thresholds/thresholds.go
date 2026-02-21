package thresholds

import "fmt"

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

func (v Values) Validate() error {
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

func (o Overrides) Apply(base Values) Values {
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

func (o Overrides) Validate() error {
	if o.FailOnIncreasePercent != nil {
		if err := validateFailOnIncrease(*o.FailOnIncreasePercent); err != nil {
			return err
		}
	}
	if o.LowConfidenceWarningPercent != nil {
		if err := validatePercentageRange("low_confidence_warning_percent", *o.LowConfidenceWarningPercent); err != nil {
			return err
		}
	}
	if o.MinUsagePercentForRecommendations != nil {
		if err := validatePercentageRange("min_usage_percent_for_recommendations", *o.MinUsagePercentForRecommendations); err != nil {
			return err
		}
	}
	if o.RemovalCandidateWeightUsage != nil {
		if err := validateWeight("removal_candidate_weight_usage", *o.RemovalCandidateWeightUsage); err != nil {
			return err
		}
	}
	if o.RemovalCandidateWeightImpact != nil {
		if err := validateWeight("removal_candidate_weight_impact", *o.RemovalCandidateWeightImpact); err != nil {
			return err
		}
	}
	if o.RemovalCandidateWeightConfidence != nil {
		if err := validateWeight("removal_candidate_weight_confidence", *o.RemovalCandidateWeightConfidence); err != nil {
			return err
		}
	}
	if o.RemovalCandidateWeightUsage != nil || o.RemovalCandidateWeightImpact != nil || o.RemovalCandidateWeightConfidence != nil {
		values := o.Apply(Defaults())
		if !hasPositiveWeight(values.RemovalCandidateWeightUsage, values.RemovalCandidateWeightImpact, values.RemovalCandidateWeightConfidence) {
			return fmt.Errorf("invalid removal candidate weights: at least one weight must be greater than 0")
		}
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
