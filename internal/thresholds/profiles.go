package thresholds

import (
	"fmt"
	"strings"
)

const ProfilesPreviewFeature = "threshold-profiles"

type Profile struct {
	Name        string
	Description string
	values      profileValues
}

type profileValues struct {
	FailOnIncreasePercent             int
	LowConfidenceWarningPercent       int
	MinUsagePercentForRecommendations int
	RemovalCandidateWeightUsage       float64
	RemovalCandidateWeightImpact      float64
	RemovalCandidateWeightConfidence  float64
}

var builtInProfiles = []Profile{
	{
		Name:        "strict",
		Description: "Faster regression detection with more warnings.",
		values: profileValues{
			FailOnIncreasePercent:             1,
			LowConfidenceWarningPercent:       55,
			MinUsagePercentForRecommendations: 60,
			RemovalCandidateWeightUsage:       0.60,
			RemovalCandidateWeightImpact:      0.25,
			RemovalCandidateWeightConfidence:  0.15,
		},
	},
	{
		Name:        "balanced",
		Description: "Stable signal without over-triggering.",
		values: profileValues{
			FailOnIncreasePercent:             2,
			LowConfidenceWarningPercent:       40,
			MinUsagePercentForRecommendations: 40,
			RemovalCandidateWeightUsage:       0.50,
			RemovalCandidateWeightImpact:      0.30,
			RemovalCandidateWeightConfidence:  0.20,
		},
	},
	{
		Name:        "noise-reduction",
		Description: "Fewer warnings and recommendations for noisy repositories.",
		values: profileValues{
			FailOnIncreasePercent:             5,
			LowConfidenceWarningPercent:       25,
			MinUsagePercentForRecommendations: 25,
			RemovalCandidateWeightUsage:       0.35,
			RemovalCandidateWeightImpact:      0.25,
			RemovalCandidateWeightConfidence:  0.40,
		},
	},
}

func BuiltInProfiles() []Profile {
	profiles := make([]Profile, len(builtInProfiles))
	copy(profiles, builtInProfiles)
	return profiles
}

func ProfileNames() []string {
	names := make([]string, 0, len(builtInProfiles))
	for _, profile := range builtInProfiles {
		names = append(names, profile.Name)
	}
	return names
}

func LookupProfile(name string) (Profile, bool) {
	normalized := normalizeProfileName(name)
	for _, profile := range builtInProfiles {
		if profile.Name == normalized {
			return profile, true
		}
	}
	return Profile{}, false
}

func ProfileConfigYAML(name string) (string, error) {
	profile, ok := LookupProfile(name)
	if !ok {
		return "", fmt.Errorf("unknown threshold profile %q (available: %s)", strings.TrimSpace(name), strings.Join(ProfileNames(), ", "))
	}
	return (&profile).ConfigYAML(), nil
}

func (p *Profile) Values() Values {
	values := Defaults()
	values.FailOnIncreasePercent = p.values.FailOnIncreasePercent
	values.LowConfidenceWarningPercent = p.values.LowConfidenceWarningPercent
	values.MinUsagePercentForRecommendations = p.values.MinUsagePercentForRecommendations
	values.RemovalCandidateWeightUsage = p.values.RemovalCandidateWeightUsage
	values.RemovalCandidateWeightImpact = p.values.RemovalCandidateWeightImpact
	values.RemovalCandidateWeightConfidence = p.values.RemovalCandidateWeightConfidence
	return values
}

func (p *Profile) ConfigYAML() string {
	var b strings.Builder
	b.WriteString("thresholds:\n")
	appendProfileInt(&b, "fail_on_increase_percent", p.values.FailOnIncreasePercent)
	appendProfileInt(&b, "low_confidence_warning_percent", p.values.LowConfidenceWarningPercent)
	appendProfileInt(&b, "min_usage_percent_for_recommendations", p.values.MinUsagePercentForRecommendations)
	appendProfileFloat(&b, "removal_candidate_weight_usage", p.values.RemovalCandidateWeightUsage)
	appendProfileFloat(&b, "removal_candidate_weight_impact", p.values.RemovalCandidateWeightImpact)
	appendProfileFloat(&b, "removal_candidate_weight_confidence", p.values.RemovalCandidateWeightConfidence)
	return b.String()
}

func appendProfileInt(b *strings.Builder, name string, value int) {
	fmt.Fprintf(b, "  %s: %d\n", name, value)
}

func appendProfileFloat(b *strings.Builder, name string, value float64) {
	fmt.Fprintf(b, "  %s: %.2f\n", name, value)
}

func normalizeProfileName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}
