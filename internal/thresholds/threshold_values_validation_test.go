package thresholds

import (
	"slices"
	"strings"
	"testing"
)

func TestThresholdValueValidationRegressionCases(t *testing.T) {
	cases := []struct {
		name string
		edit func(*Values)
		want string
	}{
		{
			name: "fail increase below disabled sentinel",
			edit: func(values *Values) { values.FailOnIncreasePercent = -2 },
			want: "fail_on_increase_percent",
		},
		{
			name: "low confidence below range",
			edit: func(values *Values) { values.LowConfidenceWarningPercent = -1 },
			want: "low_confidence_warning_percent",
		},
		{
			name: "min usage above range",
			edit: func(values *Values) { values.MinUsagePercentForRecommendations = 101 },
			want: "min_usage_percent_for_recommendations",
		},
		{
			name: "max uncertain below disabled sentinel",
			edit: func(values *Values) { values.MaxUncertainImportCount = -2 },
			want: "max_uncertain_import_count",
		},
		{
			name: "all weights zero",
			edit: func(values *Values) {
				values.RemovalCandidateWeightUsage = 0
				values.RemovalCandidateWeightImpact = 0
				values.RemovalCandidateWeightConfidence = 0
			},
			want: "at least one weight",
		},
		{
			name: "bad lockfile drift policy",
			edit: func(values *Values) { values.LockfileDriftPolicy = "broken" },
			want: "lockfile_drift_policy",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			values := Defaults()
			tc.edit(&values)
			err := values.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected validation error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestThresholdOverridesValidationRegressionCases(t *testing.T) {
	intValue := func(value int) *int { return &value }
	floatValue := func(value float64) *float64 { return &value }
	stringValue := func(value string) *string { return &value }

	cases := []struct {
		name      string
		overrides Overrides
		want      string
	}{
		{
			name: "fail increase below disabled sentinel",
			overrides: Overrides{
				FailOnIncreasePercent: intValue(-2),
			},
			want: "fail_on_increase_percent",
		},
		{
			name: "low confidence above range",
			overrides: Overrides{
				LowConfidenceWarningPercent: intValue(101),
			},
			want: "low_confidence_warning_percent",
		},
		{
			name: "all override weights zero",
			overrides: Overrides{
				RemovalCandidateWeightUsage:      floatValue(0),
				RemovalCandidateWeightImpact:     floatValue(0),
				RemovalCandidateWeightConfidence: floatValue(0),
			},
			want: "at least one weight",
		},
		{
			name: "bad lockfile drift policy",
			overrides: Overrides{
				LockfileDriftPolicy: stringValue("broken"),
			},
			want: "lockfile_drift_policy",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.overrides.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected validation error containing %q, got %v", tc.want, err)
			}
		})
	}

	overrides := Overrides{RemovalCandidateWeightUsage: floatValue(0)}
	if err := overrides.Validate(); err != nil {
		t.Fatalf("expected partial weight override to validate against default companion weights: %v", err)
	}

	overrides = Overrides{}
	overrides.SetLicenseDenyList([]string{"gpl-3.0-only", " ", "GPL-3.0-only", "MIT"})
	if err := overrides.Validate(); err != nil {
		t.Fatalf("validate overrides deny list: %v", err)
	}
	if !slices.Equal(overrides.LicenseDenyList, []string{"GPL-3.0-ONLY", "MIT"}) {
		t.Fatalf("unexpected override deny list normalization: %#v", overrides.LicenseDenyList)
	}
}

func TestValuesValidateAdditionalBranches(t *testing.T) {
	values := Defaults()
	values.LicenseDenyList = []string{"gpl-3.0-only", "  ", "GPL-3.0-only", "Apache-2.0"}
	if err := values.Validate(); err != nil {
		t.Fatalf("validate values with deny list: %v", err)
	}
	if !slices.Equal(values.LicenseDenyList, []string{"APACHE-2.0", "GPL-3.0-ONLY"}) {
		t.Fatalf("unexpected deny list normalization: %#v", values.LicenseDenyList)
	}

	values = Defaults()
	values.LockfileDriftPolicy = "broken"
	err := values.Validate()
	if err == nil || !strings.Contains(err.Error(), "lockfile_drift_policy") {
		t.Fatalf("expected invalid lockfile drift policy error, got %v", err)
	}
}
