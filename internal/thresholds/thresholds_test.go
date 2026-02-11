package thresholds

import (
	"strings"
	"testing"
)

func TestDefaultsValidate(t *testing.T) {
	values := Defaults()
	if err := values.Validate(); err != nil {
		t.Fatalf("validate defaults: %v", err)
	}
}

func TestOverridesApply(t *testing.T) {
	lowConfidence := 25
	resolved := Overrides{LowConfidenceWarningPercent: &lowConfidence}.Apply(Defaults())
	if resolved.LowConfidenceWarningPercent != 25 {
		t.Fatalf("expected low confidence threshold 25, got %d", resolved.LowConfidenceWarningPercent)
	}
	if resolved.FailOnIncreasePercent != Defaults().FailOnIncreasePercent {
		t.Fatalf("expected fail-on-increase threshold default %d, got %d", Defaults().FailOnIncreasePercent, resolved.FailOnIncreasePercent)
	}
}

func TestValuesValidateErrors(t *testing.T) {
	tests := []Values{
		{FailOnIncreasePercent: -1, LowConfidenceWarningPercent: 40, MinUsagePercentForRecommendations: 40},
		{FailOnIncreasePercent: 0, LowConfidenceWarningPercent: 101, MinUsagePercentForRecommendations: 40},
		{FailOnIncreasePercent: 0, LowConfidenceWarningPercent: 40, MinUsagePercentForRecommendations: -2},
	}
	for _, tc := range tests {
		if err := tc.Validate(); err == nil {
			t.Fatalf("expected validation error for %+v", tc)
		}
	}
}

func TestOverridesValidateErrors(t *testing.T) {
	fail := -1
	low := 200
	min := -5
	tests := []struct {
		name      string
		overrides Overrides
		want      string
	}{
		{
			name:      "invalid fail_on_increase",
			overrides: Overrides{FailOnIncreasePercent: &fail},
			want:      "fail_on_increase_percent",
		},
		{
			name:      "invalid low confidence",
			overrides: Overrides{LowConfidenceWarningPercent: &low},
			want:      "low_confidence_warning_percent",
		},
		{
			name:      "invalid min usage",
			overrides: Overrides{MinUsagePercentForRecommendations: &min},
			want:      "min_usage_percent_for_recommendations",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.overrides.Validate()
			if err == nil {
				t.Fatalf("expected validation error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestOverridesApplyAllFields(t *testing.T) {
	fail := 4
	low := 22
	min := 60
	base := Defaults()
	got := Overrides{
		FailOnIncreasePercent:             &fail,
		LowConfidenceWarningPercent:       &low,
		MinUsagePercentForRecommendations: &min,
	}.Apply(base)
	if got.FailOnIncreasePercent != 4 || got.LowConfidenceWarningPercent != 22 || got.MinUsagePercentForRecommendations != 60 {
		t.Fatalf("unexpected resolved thresholds: %+v", got)
	}
}
