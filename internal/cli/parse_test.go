package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/app"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/testutil"
	"github.com/ben-ranford/lopper/internal/thresholds"
)

const (
	unexpectedErrFmt     = "unexpected error: %v"
	modeMismatchFmt      = "expected mode %q, got %q"
	languageFlagName     = "--language"
	suggestOnlyFlag      = "--suggest-only"
	failAliasFlag        = "--fail-on-increase"
	thresholdFailFlag    = "--threshold-fail-on-increase"
	thresholdLowWarnFlag = "--threshold-low-confidence-warning"
	scoreWeightFlag      = "--score-weight-usage"
	parseConfigFileName  = ".lopper.yml"
)

func TestParseArgsDefault(t *testing.T) {
	req, err := ParseArgs(nil)
	if err != nil {
		t.Fatalf(unexpectedErrFmt, err)
	}
	if req.Mode != app.ModeTUI {
		t.Fatalf(modeMismatchFmt, app.ModeTUI, req.Mode)
	}
}

func TestParseArgsAnalyseDependency(t *testing.T) {
	req, err := ParseArgs([]string{"analyse", "lodash"})
	if err != nil {
		t.Fatalf(unexpectedErrFmt, err)
	}
	if req.Mode != app.ModeAnalyse {
		t.Fatalf(modeMismatchFmt, app.ModeAnalyse, req.Mode)
	}
	if req.Analyse.Dependency != "lodash" {
		t.Fatalf("expected dependency lodash, got %q", req.Analyse.Dependency)
	}
	if req.Analyse.Format != report.FormatTable {
		t.Fatalf("expected format %q, got %q", report.FormatTable, req.Analyse.Format)
	}
	if req.Analyse.Language != "auto" {
		t.Fatalf("expected language auto, got %q", req.Analyse.Language)
	}
	if req.Analyse.RuntimeProfile != "node-import" {
		t.Fatalf("expected runtime profile node-import, got %q", req.Analyse.RuntimeProfile)
	}
	if req.Analyse.SuggestOnly {
		t.Fatalf("expected suggest-only to be false by default")
	}
}

func TestParseArgsAnalyseTop(t *testing.T) {
	cases := []struct {
		name   string
		format string
		want   report.Format
	}{
		{name: "json", format: "json", want: report.FormatJSON},
		{name: "sarif", format: "sarif", want: report.FormatSARIF},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := ParseArgs([]string{"analyse", "--top", "5", "--format", tc.format})
			if err != nil {
				t.Fatalf(unexpectedErrFmt, err)
			}
			if req.Analyse.TopN != 5 {
				t.Fatalf("expected top 5, got %d", req.Analyse.TopN)
			}
			if req.Analyse.Format != tc.want {
				t.Fatalf("expected format %q, got %q", tc.want, req.Analyse.Format)
			}
		})
	}
}

func TestParseArgsAnalyseLanguage(t *testing.T) {
	req, err := ParseArgs([]string{"analyse", "lodash", languageFlagName, "js-ts"})
	if err != nil {
		t.Fatalf(unexpectedErrFmt, err)
	}
	if req.Analyse.Language != "js-ts" {
		t.Fatalf("expected language js-ts, got %q", req.Analyse.Language)
	}
}

func TestParseArgsAnalyseLanguages(t *testing.T) {
	cases := []string{"all", "jvm", "rust"}
	for _, language := range cases {
		t.Run(language, func(t *testing.T) {
			req, err := ParseArgs([]string{"analyse", "--top", "10", languageFlagName, language})
			if err != nil {
				t.Fatalf(unexpectedErrFmt, err)
			}
			if req.Analyse.Language != language {
				t.Fatalf("expected language %q, got %q", language, req.Analyse.Language)
			}
		})
	}
}

func TestParseArgsAnalyseBaselineSnapshotFlags(t *testing.T) {
	req, err := ParseArgs([]string{
		"analyse", "--top", "5",
		"--baseline-store", ".artifacts/baselines",
		"--baseline-key", "commit:abc123",
		"--save-baseline",
		"--baseline-label", "release-candidate",
	})
	if err != nil {
		t.Fatalf(unexpectedErrFmt, err)
	}
	if req.Analyse.BaselineStorePath != ".artifacts/baselines" {
		t.Fatalf("expected baseline store path, got %q", req.Analyse.BaselineStorePath)
	}
	if req.Analyse.BaselineKey != "commit:abc123" {
		t.Fatalf("expected baseline key, got %q", req.Analyse.BaselineKey)
	}
	if !req.Analyse.SaveBaseline {
		t.Fatalf("expected save baseline true")
	}
	if req.Analyse.BaselineLabel != "release-candidate" {
		t.Fatalf("expected baseline label, got %q", req.Analyse.BaselineLabel)
	}
}

func TestParseArgsAnalyseStringFlags(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
		got  func(app.Request) string
	}{
		{
			name: "baseline_path",
			args: []string{"analyse", "lodash", "--baseline", "baseline.json"},
			want: "baseline.json",
			got: func(req app.Request) string {
				return req.Analyse.BaselinePath
			},
		},
		{
			name: "runtime_trace_path",
			args: []string{"analyse", "lodash", "--runtime-trace", "trace.ndjson"},
			want: "trace.ndjson",
			got: func(req app.Request) string {
				return req.Analyse.RuntimeTracePath
			},
		},
		{
			name: "runtime_profile",
			args: []string{"analyse", "lodash", "--runtime-profile", "browser-require"},
			want: "browser-require",
			got: func(req app.Request) string {
				return req.Analyse.RuntimeProfile
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := ParseArgs(tc.args)
			if err != nil {
				t.Fatalf(unexpectedErrFmt, err)
			}
			if got := tc.got(req); got != tc.want {
				t.Fatalf("expected %q, got %q", tc.want, got)
			}
		})
	}
}

func TestParseArgsAnalyseCacheFlags(t *testing.T) {
	req, err := ParseArgs([]string{
		"analyse",
		"lodash",
		"--cache=false",
		"--cache-path", "/tmp/lopper-cache",
		"--cache-readonly",
	})
	if err != nil {
		t.Fatalf(unexpectedErrFmt, err)
	}
	if req.Analyse.CacheEnabled {
		t.Fatalf("expected cache to be disabled")
	}
	if req.Analyse.CachePath != "/tmp/lopper-cache" {
		t.Fatalf("expected cache path /tmp/lopper-cache, got %q", req.Analyse.CachePath)
	}
	if !req.Analyse.CacheReadOnly {
		t.Fatalf("expected cache readonly mode enabled")
	}
}

func TestParseArgsAnalyseRuntimeTestCommand(t *testing.T) {
	req, err := ParseArgs([]string{"analyse", "--top", "5", "--runtime-test-command", "npm test"})
	if err != nil {
		t.Fatalf(unexpectedErrFmt, err)
	}
	if req.Analyse.RuntimeTestCommand != "npm test" {
		t.Fatalf("expected runtime test command, got %q", req.Analyse.RuntimeTestCommand)
	}
}

func TestParseArgsAnalyseSuggestOnly(t *testing.T) {
	req, err := ParseArgs([]string{"analyse", "lodash", suggestOnlyFlag})
	if err != nil {
		t.Fatalf(unexpectedErrFmt, err)
	}
	if !req.Analyse.SuggestOnly {
		t.Fatalf("expected suggest-only to be enabled")
	}
}

func TestParseArgsTUIFlags(t *testing.T) {
	req, err := ParseArgs([]string{"tui", "--top", "15", "--filter", "lod", "--sort", "name", "--page-size", "5", "--snapshot", "out.txt"})
	if err != nil {
		t.Fatalf(unexpectedErrFmt, err)
	}
	if req.Mode != app.ModeTUI {
		t.Fatalf(modeMismatchFmt, app.ModeTUI, req.Mode)
	}
	if req.TUI.TopN != 15 {
		t.Fatalf("expected top 15, got %d", req.TUI.TopN)
	}
	if req.TUI.Filter != "lod" {
		t.Fatalf("expected filter lod, got %q", req.TUI.Filter)
	}
	if req.TUI.Sort != "name" {
		t.Fatalf("expected sort name, got %q", req.TUI.Sort)
	}
	if req.TUI.PageSize != 5 {
		t.Fatalf("expected page size 5, got %d", req.TUI.PageSize)
	}
	if req.TUI.SnapshotPath != "out.txt" {
		t.Fatalf("expected snapshot out.txt, got %q", req.TUI.SnapshotPath)
	}
}

func TestParseArgsAnalyseThresholdDefaults(t *testing.T) {
	req, err := ParseArgs([]string{"analyse", "--top", "3"})
	if err != nil {
		t.Fatalf(unexpectedErrFmt, err)
	}
	if req.Analyse.Thresholds != thresholds.Defaults() {
		t.Fatalf("expected default thresholds %+v, got %+v", thresholds.Defaults(), req.Analyse.Thresholds)
	}
}

func TestParseArgsAnalyseThresholdFlags(t *testing.T) {
	req, err := ParseArgs([]string{
		"analyse",
		"--top", "4",
		thresholdFailFlag, "2",
		thresholdLowWarnFlag, "31",
		"--threshold-min-usage-percent", "45",
		scoreWeightFlag, "0.7",
		"--score-weight-impact", "0.2",
		"--score-weight-confidence", "0.1",
	})
	if err != nil {
		t.Fatalf(unexpectedErrFmt, err)
	}
	if req.Analyse.Thresholds.FailOnIncreasePercent != 2 {
		t.Fatalf("expected fail threshold 2, got %d", req.Analyse.Thresholds.FailOnIncreasePercent)
	}
	if req.Analyse.Thresholds.LowConfidenceWarningPercent != 31 {
		t.Fatalf("expected low-confidence threshold 31, got %d", req.Analyse.Thresholds.LowConfidenceWarningPercent)
	}
	if req.Analyse.Thresholds.MinUsagePercentForRecommendations != 45 {
		t.Fatalf("expected min-usage threshold 45, got %d", req.Analyse.Thresholds.MinUsagePercentForRecommendations)
	}
	if req.Analyse.Thresholds.RemovalCandidateWeightUsage != 0.7 || req.Analyse.Thresholds.RemovalCandidateWeightImpact != 0.2 || req.Analyse.Thresholds.RemovalCandidateWeightConfidence != 0.1 {
		t.Fatalf("expected score weights 0.7/0.2/0.1, got %+v", req.Analyse.Thresholds)
	}
}

func TestParseArgsAnalyseLegacyFailOnIncreaseAlias(t *testing.T) {
	req, err := ParseArgs([]string{"analyse", "--top", "2", failAliasFlag, "9"})
	if err != nil {
		t.Fatalf(unexpectedErrFmt, err)
	}
	if req.Analyse.Thresholds.FailOnIncreasePercent != 9 {
		t.Fatalf("expected alias threshold 9, got %d", req.Analyse.Thresholds.FailOnIncreasePercent)
	}
}

func TestParseArgsAnalyseThresholdAliasesConflict(t *testing.T) {
	_, err := ParseArgs([]string{
		"analyse", "--top", "2",
		failAliasFlag, "1",
		thresholdFailFlag, "2",
	})
	if err == nil {
		t.Fatalf("expected conflict error when fail-on-increase flags disagree")
	}
	if !strings.Contains(err.Error(), "must match") {
		t.Fatalf("unexpected conflict error: %v", err)
	}
}

func TestParseArgsAnalyseConfigPrecedence(t *testing.T) {
	repo := t.TempDir()
	config := strings.Join([]string{"thresholds:", " fail_on_increase_percent: 4", " low_confidence_warning_percent: 27", " min_usage_percent_for_recommendations: 52", " removal_candidate_weight_usage: 0.2", " removal_candidate_weight_impact: 0.5", " removal_candidate_weight_confidence: 0.3", ""}, "\n")
	testutil.MustWriteFile(t, filepath.Join(repo, parseConfigFileName), config)

	req, err := ParseArgs([]string{
		"analyse", "--top", "10",
		"--repo", repo,
		"--threshold-min-usage-percent", "60",
		"--score-weight-confidence", "0.6",
	})
	if err != nil {
		t.Fatalf(unexpectedErrFmt, err)
	}
	if req.Analyse.Thresholds.FailOnIncreasePercent != 4 {
		t.Fatalf("expected config fail threshold 4, got %d", req.Analyse.Thresholds.FailOnIncreasePercent)
	}
	if req.Analyse.Thresholds.LowConfidenceWarningPercent != 27 {
		t.Fatalf("expected config low-confidence threshold 27, got %d", req.Analyse.Thresholds.LowConfidenceWarningPercent)
	}
	if req.Analyse.Thresholds.MinUsagePercentForRecommendations != 60 {
		t.Fatalf("expected CLI min-usage threshold 60, got %d", req.Analyse.Thresholds.MinUsagePercentForRecommendations)
	}
	if req.Analyse.Thresholds.RemovalCandidateWeightUsage != 0.2 {
		t.Fatalf("expected config usage weight 0.2, got %f", req.Analyse.Thresholds.RemovalCandidateWeightUsage)
	}
	if req.Analyse.Thresholds.RemovalCandidateWeightImpact != 0.5 {
		t.Fatalf("expected config impact weight 0.5, got %f", req.Analyse.Thresholds.RemovalCandidateWeightImpact)
	}
	if req.Analyse.Thresholds.RemovalCandidateWeightConfidence != 0.6 {
		t.Fatalf("expected CLI confidence weight 0.6, got %f", req.Analyse.Thresholds.RemovalCandidateWeightConfidence)
	}
}

func TestParseArgsAnalysePolicyPackSources(t *testing.T) {
	repo := t.TempDir()
	orgPolicy := `thresholds:
  low_confidence_warning_percent: 22
  removal_candidate_weight_usage: 0.4
  removal_candidate_weight_impact: 0.4
  removal_candidate_weight_confidence: 0.2
`
	repoPolicy := `policy:
  packs:
    - packs/org.yml
thresholds:
  fail_on_increase_percent: 5
`
	testutil.MustWriteFile(t, filepath.Join(repo, "packs", "org.yml"), orgPolicy)
	testutil.MustWriteFile(t, filepath.Join(repo, parseConfigFileName), repoPolicy)

	req, err := ParseArgs([]string{"analyse", "--top", "3", "--repo", repo})
	if err != nil {
		t.Fatalf("parse args with policy pack: %v", err)
	}
	if len(req.Analyse.PolicySources) != 3 {
		t.Fatalf("expected repo, pack, defaults policy sources; got %#v", req.Analyse.PolicySources)
	}
	if !strings.HasSuffix(req.Analyse.PolicySources[0], parseConfigFileName) {
		t.Fatalf("expected repo config source first, got %#v", req.Analyse.PolicySources)
	}
	if !strings.HasSuffix(req.Analyse.PolicySources[1], filepath.Join("packs", "org.yml")) {
		t.Fatalf("expected pack source second, got %#v", req.Analyse.PolicySources)
	}
	if req.Analyse.PolicySources[2] != "defaults" {
		t.Fatalf("expected defaults source last, got %#v", req.Analyse.PolicySources)
	}
	if req.Analyse.Thresholds.FailOnIncreasePercent != 5 {
		t.Fatalf("expected fail-on-increase from repo config")
	}
	if req.Analyse.Thresholds.LowConfidenceWarningPercent != 22 {
		t.Fatalf("expected low-confidence from policy pack")
	}
}

func TestParseArgsAnalysePolicySourcesIncludeCLI(t *testing.T) {
	req, err := ParseArgs([]string{"analyse", "--top", "1", thresholdLowWarnFlag, "23"})
	if err != nil {
		t.Fatalf("parse args: %v", err)
	}
	if len(req.Analyse.PolicySources) == 0 || req.Analyse.PolicySources[0] != "cli" {
		t.Fatalf("expected cli source precedence, got %#v", req.Analyse.PolicySources)
	}
}

func TestParseArgsAnalyseRejectsInvalidThreshold(t *testing.T) {
	_, err := ParseArgs([]string{"analyse", "--top", "2", thresholdLowWarnFlag, "101"})
	if err == nil {
		t.Fatalf("expected range validation error")
	}
	if !strings.Contains(err.Error(), "between 0 and 100") {
		t.Fatalf("unexpected validation error: %v", err)
	}
}

func TestParseArgsAnalyseRejectsInvalidScoreWeight(t *testing.T) {
	_, err := ParseArgs([]string{"analyse", "--top", "2", scoreWeightFlag, "-1"})
	if err == nil {
		t.Fatalf("expected score weight validation error")
	}
	if !strings.Contains(err.Error(), "removal_candidate_weight_usage") {
		t.Fatalf("unexpected validation error: %v", err)
	}
}

func TestNormalizeArgsAndFlagNeedsValue(t *testing.T) {
	args := normalizeArgs([]string{"lodash", "--top", "5", "--format=json", "--", "--literal"})
	if len(args) == 0 {
		t.Fatalf("expected normalized args")
	}
	if !flagNeedsValue(thresholdFailFlag) {
		t.Fatalf("expected threshold flag to require value")
	}
	if !flagNeedsValue(scoreWeightFlag) {
		t.Fatalf("expected score weight flag to require value")
	}
	if flagNeedsValue("--format=json") {
		t.Fatalf("expected equals-form flag not to require separate value")
	}
	if flagNeedsValue("--unknown-flag") {
		t.Fatalf("did not expect unknown flag to be treated as requiring value")
	}
}

func TestParseArgsErrorsAndHelp(t *testing.T) {
	if _, err := ParseArgs([]string{"help"}); err != ErrHelpRequested {
		t.Fatalf("expected top-level help request error, got %v", err)
	}
	if _, err := ParseArgs([]string{"analyse", "--help"}); err != ErrHelpRequested {
		t.Fatalf("expected analyse help request error, got %v", err)
	}
	if _, err := ParseArgs([]string{"tui", "--help"}); err != ErrHelpRequested {
		t.Fatalf("expected tui help request error, got %v", err)
	}
	if _, err := ParseArgs([]string{"unknown"}); err == nil {
		t.Fatalf("expected unknown command error")
	}
}

func TestParseArgsAnalyseInvalidCombinations(t *testing.T) {
	if _, err := ParseArgs([]string{"analyse"}); err == nil {
		t.Fatalf("expected missing target error")
	}
	if _, err := ParseArgs([]string{"analyse", "lodash", "--top", "2"}); err != ErrConflictingTargets {
		t.Fatalf("expected conflicting-targets error, got %v", err)
	}
	if _, err := ParseArgs([]string{"analyse", "--top", "2", suggestOnlyFlag}); err == nil {
		t.Fatalf("expected suggest-only with top target to fail")
	}
	if _, err := ParseArgs([]string{"analyse", suggestOnlyFlag}); err == nil {
		t.Fatalf("expected suggest-only without dependency to fail")
	}
	if _, err := ParseArgs([]string{"analyse", "--top", "-1"}); err == nil {
		t.Fatalf("expected negative top error")
	}
	if _, err := ParseArgs([]string{"analyse", "a", "b"}); err == nil {
		t.Fatalf("expected too-many-arguments error")
	}
}

func TestParseArgsTUIInvalidInputs(t *testing.T) {
	if _, err := ParseArgs([]string{"tui", "--top", "-1"}); err == nil {
		t.Fatalf("expected negative top error")
	}
	if _, err := ParseArgs([]string{"tui", "--page-size", "-1"}); err == nil {
		t.Fatalf("expected negative page-size error")
	}
	if _, err := ParseArgs([]string{"tui", "extra"}); err == nil {
		t.Fatalf("expected unexpected-args error")
	}
}

func TestParseArgsVisitedFlagThresholdAliasMatch(t *testing.T) {
	req, err := ParseArgs([]string{
		"analyse", "--top", "2",
		failAliasFlag, "3",
		thresholdFailFlag, "3",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.Analyse.Thresholds.FailOnIncreasePercent != 3 {
		t.Fatalf("expected resolved fail threshold 3, got %d", req.Analyse.Thresholds.FailOnIncreasePercent)
	}
}

func TestParseArgsFlagParseAndConfigLoadErrors(t *testing.T) {
	if _, err := ParseArgs([]string{"analyse", "--top"}); err == nil {
		t.Fatalf("expected analyse flag parse error for missing value")
	}
	if _, err := ParseArgs([]string{"analyse", "dep", "--format", "invalid"}); err == nil {
		t.Fatalf("expected analyse format parse error")
	}
	if _, err := ParseArgs([]string{"analyse", "--top", "1", "--config", "missing-config.yml"}); err == nil {
		t.Fatalf("expected thresholds config load error")
	}
	if _, err := ParseArgs([]string{"tui", "--top"}); err == nil {
		t.Fatalf("expected tui flag parse error for missing value")
	}
}
