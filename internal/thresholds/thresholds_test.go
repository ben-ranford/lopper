package thresholds

import (
	"math"
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
	usageWeight := 0.7
	resolved := (&Overrides{LowConfidenceWarningPercent: &lowConfidence, RemovalCandidateWeightUsage: &usageWeight}).Apply(Defaults())
	if resolved.LowConfidenceWarningPercent != 25 {
		t.Fatalf("expected low confidence threshold 25, got %d", resolved.LowConfidenceWarningPercent)
	}
	if resolved.RemovalCandidateWeightUsage != 0.7 {
		t.Fatalf("expected usage weight 0.7, got %f", resolved.RemovalCandidateWeightUsage)
	}
	if resolved.FailOnIncreasePercent != Defaults().FailOnIncreasePercent {
		t.Fatalf("expected fail-on-increase threshold default %d, got %d", Defaults().FailOnIncreasePercent, resolved.FailOnIncreasePercent)
	}
}

func TestValuesValidateErrors(t *testing.T) {
	tests := []Values{
		{FailOnIncreasePercent: -1, LowConfidenceWarningPercent: 40, MinUsagePercentForRecommendations: 40, RemovalCandidateWeightUsage: 0.5, RemovalCandidateWeightImpact: 0.3, RemovalCandidateWeightConfidence: 0.2},
		{FailOnIncreasePercent: 0, LowConfidenceWarningPercent: 101, MinUsagePercentForRecommendations: 40, RemovalCandidateWeightUsage: 0.5, RemovalCandidateWeightImpact: 0.3, RemovalCandidateWeightConfidence: 0.2},
		{FailOnIncreasePercent: 0, LowConfidenceWarningPercent: 40, MinUsagePercentForRecommendations: -2, RemovalCandidateWeightUsage: 0.5, RemovalCandidateWeightImpact: 0.3, RemovalCandidateWeightConfidence: 0.2},
		{FailOnIncreasePercent: 0, LowConfidenceWarningPercent: 40, MinUsagePercentForRecommendations: 40, RemovalCandidateWeightUsage: -0.1, RemovalCandidateWeightImpact: 0.3, RemovalCandidateWeightConfidence: 0.2},
		{FailOnIncreasePercent: 0, LowConfidenceWarningPercent: 40, MinUsagePercentForRecommendations: 40, RemovalCandidateWeightUsage: 0, RemovalCandidateWeightImpact: 0, RemovalCandidateWeightConfidence: 0},
	}
	for _, tc := range tests {
		if tc.Validate() == nil {
			t.Fatalf("expected validation error for %+v", tc)
		}
	}
}

func TestOverridesValidateErrors(t *testing.T) {
	fail := -1
	low := 200
	min := -5
	weight := -1.0
	nan := math.NaN()
	inf := math.Inf(1)
	zeroWeight := 0.0
	tests := []struct {
		name      string
		overrides Overrides
		want      string
	}{
		{
			name: "invalid lockfile drift policy",
			overrides: func() Overrides {
				policy := "invalid"
				return Overrides{LockfileDriftPolicy: &policy}
			}(),
			want: "lockfile_drift_policy",
		},
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
		{
			name:      "invalid score weight",
			overrides: Overrides{RemovalCandidateWeightUsage: &weight},
			want:      "removal_candidate_weight_usage",
		},
		{
			name:      "nan score weight",
			overrides: Overrides{RemovalCandidateWeightUsage: &nan},
			want:      "must be finite",
		},
		{
			name:      "infinite score weight",
			overrides: Overrides{RemovalCandidateWeightImpact: &inf},
			want:      "must be finite",
		},
		{
			name: "all score weights zero",
			overrides: Overrides{
				RemovalCandidateWeightUsage:      &zeroWeight,
				RemovalCandidateWeightImpact:     &zeroWeight,
				RemovalCandidateWeightConfidence: &zeroWeight,
			},
			want: "at least one weight",
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
	usageWeight := 0.6
	impactWeight := 0.3
	confidenceWeight := 0.1
	lockfileDriftPolicy := "fail"
	base := Defaults()
	got := (&Overrides{FailOnIncreasePercent: &fail, LowConfidenceWarningPercent: &low, MinUsagePercentForRecommendations: &min, RemovalCandidateWeightUsage: &usageWeight, RemovalCandidateWeightImpact: &impactWeight, RemovalCandidateWeightConfidence: &confidenceWeight, LockfileDriftPolicy: &lockfileDriftPolicy}).Apply(base)
	if got.FailOnIncreasePercent != 4 || got.LowConfidenceWarningPercent != 22 || got.MinUsagePercentForRecommendations != 60 {
		t.Fatalf("unexpected resolved thresholds: %+v", got)
	}
	if got.RemovalCandidateWeightUsage != 0.6 || got.RemovalCandidateWeightImpact != 0.3 || got.RemovalCandidateWeightConfidence != 0.1 {
		t.Fatalf("unexpected resolved score weights: %+v", got)
	}
	if got.LockfileDriftPolicy != "fail" {
		t.Fatalf("unexpected lockfile drift policy: %+v", got)
	}
}
