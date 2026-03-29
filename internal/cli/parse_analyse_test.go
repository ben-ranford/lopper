package cli

import (
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/app"
	"github.com/ben-ranford/lopper/internal/notify"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/testutil"
	"github.com/ben-ranford/lopper/internal/thresholds"
)

func TestParseArgsAnalyseDependency(t *testing.T) {
	req := mustParseArgs(t, []string{"analyse", "lodash"})
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
	if req.Analyse.ScopeMode != app.ScopeModePackage {
		t.Fatalf("expected scope mode package, got %q", req.Analyse.ScopeMode)
	}
	if req.Analyse.SuggestOnly {
		t.Fatalf("expected suggest-only to be false by default")
	}
}

func TestParseArgsAnalyseScopeMode(t *testing.T) {
	req, err := ParseArgs([]string{"analyse", "--top", "5", "--scope-mode", "changed-packages"})
	if err != nil {
		t.Fatalf(unexpectedErrFmt, err)
	}
	if req.Analyse.ScopeMode != app.ScopeModeChangedPackages {
		t.Fatalf("expected changed-packages scope mode, got %q", req.Analyse.ScopeMode)
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
		{name: "pr_comment", format: "pr-comment", want: report.FormatPRComment},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := mustParseArgs(t, []string{"analyse", "--top", "5", formatFlagName, tc.format})
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
	req := mustParseArgs(t, []string{"analyse", "lodash", languageFlagName, "js-ts"})
	if req.Analyse.Language != "js-ts" {
		t.Fatalf("expected language js-ts, got %q", req.Analyse.Language)
	}
}

func TestParseArgsAnalyseLanguages(t *testing.T) {
	cases := []string{"all", "jvm", "kotlin-android", "rust", "ruby", "elixir"}
	for _, language := range cases {
		t.Run(language, func(t *testing.T) {
			req := mustParseArgs(t, []string{"analyse", "--top", "10", languageFlagName, language})
			if req.Analyse.Language != language {
				t.Fatalf("expected language %q, got %q", language, req.Analyse.Language)
			}
		})
	}
}

func TestParseArgsAnalyseBaselineSnapshotFlags(t *testing.T) {
	req := mustParseArgs(t, []string{
		"analyse", "--top", "5",
		"--baseline-store", ".artifacts/baselines",
		"--baseline-key", "commit:abc123",
		"--save-baseline",
		"--baseline-label", "release-candidate",
	})
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
			req := mustParseArgs(t, tc.args)
			if got := tc.got(req); got != tc.want {
				t.Fatalf("expected %q, got %q", tc.want, got)
			}
		})
	}
}

func TestParseArgsAnalyseCacheFlags(t *testing.T) {
	req := mustParseArgs(t, []string{
		"analyse",
		"lodash",
		"--cache=false",
		"--cache-path", "/tmp/lopper-cache",
		"--cache-readonly",
	})
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

func TestParseArgsAnalyseScopeFlags(t *testing.T) {
	req := mustParseArgs(t, []string{
		"analyse",
		"--top", "3",
		includeFlagName, scopeAnalyseGoGlobs,
		excludeFlagName, scopeExcludeTestGlob,
	})
	if got := strings.Join(req.Analyse.IncludePatterns, ","); got != scopeAnalyseGoGlobs {
		t.Fatalf("unexpected include patterns: %q", got)
	}
	if got := strings.Join(req.Analyse.ExcludePatterns, ","); got != scopeExcludeTestGlob {
		t.Fatalf("unexpected exclude patterns: %q", got)
	}
}

func TestParseArgsAnalyseScopeFlagsRepeatable(t *testing.T) {
	req := mustParseArgs(t, []string{
		"analyse",
		"--top", "3",
		includeFlagName, scopeGoGlob,
		includeFlagName, "internal/**/*.go,cmd/**/*.go",
		excludeFlagName, scopeExcludeTestGlob,
		excludeFlagName, scopeVendorGlob,
	})
	if got := strings.Join(req.Analyse.IncludePatterns, ","); got != scopeIncludeCombined {
		t.Fatalf("unexpected include patterns for repeatable flags: %q", got)
	}
	if got := strings.Join(req.Analyse.ExcludePatterns, ","); got != scopeExcludeTestGlob+","+scopeVendorGlob {
		t.Fatalf("unexpected exclude patterns for repeatable flags: %q", got)
	}
}

func TestParseArgsAnalyseRuntimeTestCommand(t *testing.T) {
	req := mustParseArgs(t, []string{"analyse", "--top", "5", "--runtime-test-command", "npm test"})
	if req.Analyse.RuntimeTestCommand != "npm test" {
		t.Fatalf("expected runtime test command, got %q", req.Analyse.RuntimeTestCommand)
	}
}

func TestParseArgsAnalyseSuggestOnly(t *testing.T) {
	req := mustParseArgs(t, []string{"analyse", "lodash", suggestOnlyFlag})
	if !req.Analyse.SuggestOnly {
		t.Fatalf("expected suggest-only to be enabled")
	}
}

func TestParseArgsAnalyseApplyCodemod(t *testing.T) {
	req := mustParseArgs(t, []string{"analyse", "lodash", applyCodemodFlag, applyCodemodConfirmFlag, allowDirtyFlag})
	if !req.Analyse.ApplyCodemod {
		t.Fatalf("expected apply-codemod to be enabled")
	}
	if !req.Analyse.AllowDirty {
		t.Fatalf("expected allow-dirty to be enabled")
	}
	if req.Analyse.SuggestOnly {
		t.Fatalf("expected suggest-only to remain disabled")
	}
}

func TestParseArgsAnalyseApplyCodemodValidation(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "missing_confirm",
			args: []string{"analyse", "lodash", applyCodemodFlag},
			want: "--apply-codemod requires --apply-codemod-confirm",
		},
		{
			name: "confirm_without_apply",
			args: []string{"analyse", "lodash", applyCodemodConfirmFlag},
			want: "--apply-codemod-confirm requires --apply-codemod",
		},
		{
			name: "allow_dirty_without_apply",
			args: []string{"analyse", "lodash", allowDirtyFlag},
			want: "--allow-dirty requires --apply-codemod",
		},
		{
			name: "conflicts_with_suggest_only",
			args: []string{"analyse", "lodash", suggestOnlyFlag, applyCodemodFlag, applyCodemodConfirmFlag},
			want: "--suggest-only and --apply-codemod cannot be combined",
		},
		{
			name: "top_not_supported",
			args: []string{"analyse", "--top", "5", applyCodemodFlag, applyCodemodConfirmFlag},
			want: "--apply-codemod requires a specific dependency target",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := expectParseArgsError(t, tc.args, "expected apply-codemod validation error")
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestParseArgsAnalyseThresholdDefaults(t *testing.T) {
	req := mustParseArgs(t, []string{"analyse", "--top", "3"})
	if !reflect.DeepEqual(req.Analyse.Thresholds, thresholds.Defaults()) {
		t.Fatalf("expected default thresholds %+v, got %+v", thresholds.Defaults(), req.Analyse.Thresholds)
	}
}

func TestParseArgsAnalyseThresholdFlags(t *testing.T) {
	req := mustParseArgs(t, []string{
		"analyse",
		"--top", "4",
		thresholdFailFlag, "2",
		thresholdLowWarnFlag, "31",
		"--threshold-min-usage-percent", "45",
		"--threshold-max-uncertain-imports", "3",
		scoreWeightFlag, "0.7",
		"--score-weight-impact", "0.2",
		"--score-weight-confidence", "0.1",
		"--license-deny", "gpl-3.0-only,agpl-3.0-only",
		"--license-fail-on-deny",
		"--license-provenance-registry",
		lockfileDriftPolicyFlagName, "fail",
	})
	if req.Analyse.Thresholds.FailOnIncreasePercent != 2 {
		t.Fatalf("expected fail threshold 2, got %d", req.Analyse.Thresholds.FailOnIncreasePercent)
	}
	if req.Analyse.Thresholds.LowConfidenceWarningPercent != 31 {
		t.Fatalf("expected low-confidence threshold 31, got %d", req.Analyse.Thresholds.LowConfidenceWarningPercent)
	}
	if req.Analyse.Thresholds.MinUsagePercentForRecommendations != 45 {
		t.Fatalf("expected min-usage threshold 45, got %d", req.Analyse.Thresholds.MinUsagePercentForRecommendations)
	}
	if req.Analyse.Thresholds.MaxUncertainImportCount != 3 {
		t.Fatalf("expected max uncertain import threshold 3, got %d", req.Analyse.Thresholds.MaxUncertainImportCount)
	}
	if req.Analyse.Thresholds.RemovalCandidateWeightUsage != 0.7 || req.Analyse.Thresholds.RemovalCandidateWeightImpact != 0.2 || req.Analyse.Thresholds.RemovalCandidateWeightConfidence != 0.1 {
		t.Fatalf("expected score weights 0.7/0.2/0.1, got %+v", req.Analyse.Thresholds)
	}
	if req.Analyse.Thresholds.LockfileDriftPolicy != "fail" {
		t.Fatalf("expected lockfile drift policy fail, got %q", req.Analyse.Thresholds.LockfileDriftPolicy)
	}
	if strings.Join(req.Analyse.Thresholds.LicenseDenyList, ",") != "AGPL-3.0-ONLY,GPL-3.0-ONLY" {
		t.Fatalf("unexpected license deny list: %#v", req.Analyse.Thresholds.LicenseDenyList)
	}
	if !req.Analyse.Thresholds.LicenseFailOnDeny {
		t.Fatalf("expected license fail on deny true")
	}
	if !req.Analyse.Thresholds.LicenseIncludeRegistryProvenance {
		t.Fatalf("expected license provenance registry true")
	}
}

func TestParseArgsAnalyseLegacyFailOnIncreaseAlias(t *testing.T) {
	req := mustParseArgs(t, []string{"analyse", "--top", "2", failAliasFlag, "9"})
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
	config := strings.Join([]string{"thresholds:", " fail_on_increase_percent: 4", " low_confidence_warning_percent: 27", " min_usage_percent_for_recommendations: 52", " removal_candidate_weight_usage: 0.2", " removal_candidate_weight_impact: 0.5", " removal_candidate_weight_confidence: 0.3", " lockfile_drift_policy: warn", ""}, "\n")
	testutil.MustWriteFile(t, filepath.Join(repo, parseConfigFileName), config)

	req := mustParseArgs(t, []string{
		"analyse", "--top", "10",
		repoFlagName, repo,
		"--threshold-min-usage-percent", "60",
		"--score-weight-confidence", "0.6",
		lockfileDriftPolicyFlagName, "fail",
	})
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
	if req.Analyse.Thresholds.LockfileDriftPolicy != "fail" {
		t.Fatalf("expected CLI lockfile drift policy fail, got %q", req.Analyse.Thresholds.LockfileDriftPolicy)
	}
}

func TestParseArgsAnalyseScopeConfigPrecedence(t *testing.T) {
	repo := t.TempDir()
	config := "scope:\n include:\n  - \"src/**/*.go\"\n exclude:\n  - \"**/*_test.go\"\n"
	testutil.MustWriteFile(t, filepath.Join(repo, parseConfigFileName), config)

	req := mustParseArgs(t, []string{
		"analyse", "--top", "5",
		repoFlagName, repo,
		excludeFlagName, scopeVendorGlob,
	})
	if got := strings.Join(req.Analyse.IncludePatterns, ","); got != scopeGoGlob {
		t.Fatalf("expected include patterns from config, got %q", got)
	}
	if got := strings.Join(req.Analyse.ExcludePatterns, ","); got != scopeVendorGlob {
		t.Fatalf("expected CLI exclude override, got %q", got)
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

	req := mustParseArgs(t, []string{"analyse", "--top", "3", repoFlagName, repo})
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
	req := mustParseArgs(t, []string{"analyse", "--top", "1", thresholdLowWarnFlag, "23"})
	if len(req.Analyse.PolicySources) == 0 || req.Analyse.PolicySources[0] != "cli" {
		t.Fatalf("expected cli source precedence, got %#v", req.Analyse.PolicySources)
	}
}

func TestParseArgsAnalyseNotificationPrecedence(t *testing.T) {
	repo := t.TempDir()
	config := `notifications:
  on: breach
  slack:
    webhook: https://hooks.slack.com/services/A/B/CONFIG
  teams:
    webhook: https://outlook.office.com/webhook/CONFIG
    on: improvement
`
	writeFile(t, filepath.Join(repo, ".lopper.yml"), config)

	t.Setenv(notify.EnvOn, "regression")
	t.Setenv(notify.EnvSlackWebhook, "https://hooks.slack.com/services/A/B/ENV")

	req, err := ParseArgs([]string{
		"analyse", "--top", "10",
		"--repo", repo,
		notifyOnFlag, "improvement",
		"--notify-teams", "https://outlook.office.com/webhook/CLI",
	})
	if err != nil {
		t.Fatalf(unexpectedErrFmt, err)
	}

	if req.Analyse.Notifications.Slack.Trigger != notify.TriggerImprovement {
		t.Fatalf("expected CLI notify-on to set slack trigger, got %q", req.Analyse.Notifications.Slack.Trigger)
	}
	if req.Analyse.Notifications.Teams.Trigger != notify.TriggerImprovement {
		t.Fatalf("expected CLI notify-on to set teams trigger, got %q", req.Analyse.Notifications.Teams.Trigger)
	}
	if !strings.Contains(req.Analyse.Notifications.Slack.WebhookURL, "/ENV") {
		t.Fatalf("expected env slack webhook override, got %q", req.Analyse.Notifications.Slack.WebhookURL)
	}
	if !strings.Contains(req.Analyse.Notifications.Teams.WebhookURL, "/CLI") {
		t.Fatalf("expected CLI teams webhook override, got %q", req.Analyse.Notifications.Teams.WebhookURL)
	}
}

func TestParseArgsAnalyseInvalidNotificationInputs(t *testing.T) {
	if _, err := ParseArgs([]string{"analyse", "--top", "1", notifyOnFlag, "bad"}); err == nil {
		t.Fatalf("expected invalid notify-on error")
	}

	_, err := ParseArgs([]string{"analyse", "--top", "1", "--notify-slack", "hooks.slack.com/services/A/B/SECRET"})
	if err == nil {
		t.Fatalf("expected invalid notify-slack URL error")
	}
	if strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("expected parse error to redact webhook secrets, got %q", err.Error())
	}
}

func TestResolveAnalyseNotificationsErrors(t *testing.T) {
	t.Run("invalid config overrides", func(t *testing.T) {
		repo := t.TempDir()
		configPath := filepath.Join(repo, parseConfigFileName)
		writeFile(t, configPath, "notifications:\n  slack:\n    on: definitely-not-valid\n")

		_, err := resolveAnalyseNotifications(map[string]bool{}, analyseFlagValues{}, configPath)
		if err == nil {
			t.Fatalf("expected config notification parse error")
		}
	})

	t.Run("invalid env overrides", func(t *testing.T) {
		t.Setenv(notify.EnvOn, "definitely-not-valid")

		_, err := resolveAnalyseNotifications(map[string]bool{}, analyseFlagValues{}, "")
		if err == nil {
			t.Fatalf("expected env notification parse error")
		}
	})
}

func TestValidateSuggestOnlyTargetRequiresDependency(t *testing.T) {
	if validateSuggestOnlyTarget(true, "   ", 0) == nil {
		t.Fatalf("expected suggest-only validation to require dependency")
	}
}

func TestParseArgsAnalyseRejectsInvalidThreshold(t *testing.T) {
	err := expectParseArgsError(t, []string{"analyse", "--top", "2", thresholdLowWarnFlag, "101"}, "expected range validation error")
	if !strings.Contains(err.Error(), "between 0 and 100") {
		t.Fatalf(unexpectedValidationErrFmt, err)
	}
}

func TestParseArgsAnalyseRejectsInvalidScoreWeight(t *testing.T) {
	err := expectParseArgsError(t, []string{"analyse", "--top", "2", scoreWeightFlag, "-1"}, "expected score weight validation error")
	if !strings.Contains(err.Error(), "removal_candidate_weight_usage") {
		t.Fatalf(unexpectedValidationErrFmt, err)
	}
}

func TestParseArgsAnalyseRejectsInvalidLockfileDriftPolicy(t *testing.T) {
	_, err := ParseArgs([]string{"analyse", "--top", "2", lockfileDriftPolicyFlagName, "bad"})
	if err == nil {
		t.Fatalf("expected lockfile drift policy validation error")
	}
	if !strings.Contains(err.Error(), "lockfile_drift_policy") {
		t.Fatalf(unexpectedValidationErrFmt, err)
	}
}

func TestParseArgsAnalyseInvalidCombinations(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantErr error
	}{
		{name: "missing_target", args: []string{"analyse"}},
		{name: "conflicting_targets", args: []string{"analyse", "lodash", "--top", "2"}, wantErr: ErrConflictingTargets},
		{name: "suggest_only_with_top", args: []string{"analyse", "--top", "2", suggestOnlyFlag}},
		{name: "suggest_only_without_dependency", args: []string{"analyse", suggestOnlyFlag}},
		{name: "negative_top", args: []string{"analyse", "--top", "-1"}},
		{name: "too_many_args", args: []string{"analyse", "a", "b"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseArgs(tc.args)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("expected error %v, got %v", tc.wantErr, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected parse error")
			}
		})
	}
	if _, err := ParseArgs([]string{"analyse", "--top", "2", "--scope-mode", "invalid"}); err == nil {
		t.Fatalf("expected invalid scope-mode error")
	}
}

func TestParseArgsVisitedFlagThresholdAliasMatch(t *testing.T) {
	req := mustParseArgs(t, []string{
		"analyse", "--top", "2",
		failAliasFlag, "3",
		thresholdFailFlag, "3",
	})
	if req.Analyse.Thresholds.FailOnIncreasePercent != 3 {
		t.Fatalf("expected resolved fail threshold 3, got %d", req.Analyse.Thresholds.FailOnIncreasePercent)
	}
}
