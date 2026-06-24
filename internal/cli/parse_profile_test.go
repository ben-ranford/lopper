package cli

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/app"
	"github.com/ben-ranford/lopper/internal/featureflags"
	"github.com/ben-ranford/lopper/internal/testutil"
	"github.com/ben-ranford/lopper/internal/thresholds"
)

func TestParseArgsProfileApply(t *testing.T) {
	withFeatureRegistry(t, featureflags.ChannelRelease, nil)
	req := mustParseArgs(t, []string{"profile", "apply", "strict", "--output", ".lopper.yml", "-o", ".lopper.yml", "--force", "--enable-feature", thresholds.ProfilesPreviewFeature})
	if req.Mode != app.ModeProfile {
		t.Fatalf(modeMismatchFmt, app.ModeProfile, req.Mode)
	}
	if req.Profile.Name != "strict" {
		t.Fatalf("expected strict profile, got %q", req.Profile.Name)
	}
	if req.Profile.OutputPath != ".lopper.yml" {
		t.Fatalf("expected output path, got %q", req.Profile.OutputPath)
	}
	if !req.Profile.Force {
		t.Fatalf("expected force to be enabled")
	}
	if !req.Profile.Features.Enabled(thresholds.ProfilesPreviewFeature) {
		t.Fatalf("expected profile preview feature enabled")
	}
}

func TestParseArgsProfileApplyEveryNamedProfile(t *testing.T) {
	withFeatureRegistry(t, featureflags.ChannelRolling, nil)
	for _, profile := range thresholds.BuiltInProfiles() {
		t.Run(profile.Name, func(t *testing.T) {
			req := mustParseArgs(t, []string{"profile", "apply", profile.Name})
			if req.Profile.Name != profile.Name {
				t.Fatalf("expected profile %q, got %q", profile.Name, req.Profile.Name)
			}
		})
	}
}

func TestParseArgsProfileApplyErrors(t *testing.T) {
	withFeatureRegistry(t, featureflags.ChannelRelease, nil)

	cases := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing subcommand", args: []string{"profile"}, want: "help requested"},
		{name: "unknown subcommand", args: []string{"profile", "show"}, want: "unknown profile command"},
		{name: "missing name", args: []string{"profile", "apply"}, want: "requires a profile name"},
		{name: "unknown name", args: []string{"profile", "apply", "loud"}, want: "unknown threshold profile"},
		{name: "extra arg", args: []string{"profile", "apply", "strict", "extra"}, want: "too many arguments"},
		{name: "output conflict", args: []string{"profile", "apply", "strict", "--output", "one.yml", "-o", "two.yml"}, want: "--output and -o must match"},
		{name: "unknown feature", args: []string{"profile", "apply", "strict", "--enable-feature", "missing"}, want: "unknown feature"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseArgs(tc.args)
			if tc.want == "help requested" {
				if !errors.Is(err, ErrHelpRequested) {
					t.Fatalf("expected help request, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestParseArgsAnalyseGeneratedProfileConfigPreservesCLIPrecedence(t *testing.T) {
	repo := t.TempDir()
	config, err := thresholds.ProfileConfigYAML("balanced")
	if err != nil {
		t.Fatalf("render profile config: %v", err)
	}
	testutil.MustWriteFile(t, filepath.Join(repo, parseConfigFileName), config)

	req := mustParseArgs(t, []string{
		"analyse", "--top", "10",
		repoFlagName, repo,
		"--threshold-min-usage-percent", "60",
		"--score-weight-confidence", "0.60",
	})

	if req.Analyse.Thresholds.FailOnIncreasePercent != 2 {
		t.Fatalf("expected generated config fail threshold, got %d", req.Analyse.Thresholds.FailOnIncreasePercent)
	}
	if req.Analyse.Thresholds.LowConfidenceWarningPercent != 40 {
		t.Fatalf("expected generated config low confidence threshold, got %d", req.Analyse.Thresholds.LowConfidenceWarningPercent)
	}
	if req.Analyse.Thresholds.MinUsagePercentForRecommendations != 60 {
		t.Fatalf("expected CLI min usage override, got %d", req.Analyse.Thresholds.MinUsagePercentForRecommendations)
	}
	if req.Analyse.Thresholds.RemovalCandidateWeightUsage != 0.50 || req.Analyse.Thresholds.RemovalCandidateWeightImpact != 0.30 {
		t.Fatalf("expected generated profile usage/impact weights, got %+v", req.Analyse.Thresholds)
	}
	if req.Analyse.Thresholds.RemovalCandidateWeightConfidence != 0.60 {
		t.Fatalf("expected CLI confidence weight override, got %f", req.Analyse.Thresholds.RemovalCandidateWeightConfidence)
	}
}
