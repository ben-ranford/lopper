package thresholds

import "fmt"

const (
	DefaultFailOnIncreasePercent             = 0
	DefaultLowConfidenceWarningPercent       = 40
	DefaultMinUsagePercentForRecommendations = 40
)

type Values struct {
	FailOnIncreasePercent             int
	LowConfidenceWarningPercent       int
	MinUsagePercentForRecommendations int
}

type Overrides struct {
	FailOnIncreasePercent             *int
	LowConfidenceWarningPercent       *int
	MinUsagePercentForRecommendations *int
}

func Defaults() Values {
	return Values{
		FailOnIncreasePercent:             DefaultFailOnIncreasePercent,
		LowConfidenceWarningPercent:       DefaultLowConfidenceWarningPercent,
		MinUsagePercentForRecommendations: DefaultMinUsagePercentForRecommendations,
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
