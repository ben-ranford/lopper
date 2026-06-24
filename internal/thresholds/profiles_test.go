package thresholds

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestBuiltInProfilesMatchDocumentedPresets(t *testing.T) {
	want := map[string]Values{
		"strict":          profileValuesForTest(1, 55, 60, 0.60, 0.25, 0.15),
		"balanced":        profileValuesForTest(2, 40, 40, 0.50, 0.30, 0.20),
		"noise-reduction": profileValuesForTest(5, 25, 25, 0.35, 0.25, 0.40),
	}

	for _, profile := range BuiltInProfiles() {
		t.Run(profile.Name, func(t *testing.T) {
			got := profile.Values()
			if err := got.Validate(); err != nil {
				t.Fatalf("profile values must validate: %v", err)
			}
			if !reflect.DeepEqual(got, want[profile.Name]) {
				t.Fatalf("profile %s values drifted from documented preset:\n got: %+v\nwant: %+v", profile.Name, got, want[profile.Name])
			}
		})
	}
}

func TestProfileConfigYAMLIsAcceptedByLoader(t *testing.T) {
	for _, profile := range BuiltInProfiles() {
		t.Run(profile.Name, func(t *testing.T) {
			config, err := ProfileConfigYAML(profile.Name)
			if err != nil {
				t.Fatalf("render profile config: %v", err)
			}
			repo := t.TempDir()
			testutil.MustWriteFile(t, filepath.Join(repo, lopperYMLName), config)

			overrides, _, err := Load(repo, "")
			if err != nil {
				t.Fatalf("load generated profile config: %v", err)
			}
			if got := overrides.Apply(Defaults()); !reflect.DeepEqual(got, profile.Values()) {
				t.Fatalf("generated profile config resolved incorrectly:\n got: %+v\nwant: %+v", got, profile.Values())
			}
		})
	}
}

func TestProfileLookupAndConfigErrors(t *testing.T) {
	if profile, ok := LookupProfile(" STRICT "); !ok || profile.Name != "strict" {
		t.Fatalf("expected case-insensitive profile lookup, got %#v ok=%v", profile, ok)
	}
	if _, ok := LookupProfile("missing"); ok {
		t.Fatalf("did not expect missing profile lookup to succeed")
	}
	_, err := ProfileConfigYAML("missing")
	if err == nil || !strings.Contains(err.Error(), "available: strict, balanced, noise-reduction") {
		t.Fatalf("expected unknown profile error with names, got %v", err)
	}
}

func profileValuesForTest(failOnIncrease, lowConfidence, minUsage int, usage, impact, confidence float64) Values {
	values := Defaults()
	values.FailOnIncreasePercent = failOnIncrease
	values.LowConfidenceWarningPercent = lowConfidence
	values.MinUsagePercentForRecommendations = minUsage
	values.RemovalCandidateWeightUsage = usage
	values.RemovalCandidateWeightImpact = impact
	values.RemovalCandidateWeightConfidence = confidence
	return values
}
