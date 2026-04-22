package cli

import (
	"flag"
	"io"
	"strings"

	"github.com/ben-ranford/lopper/internal/app"
)

type analyseFlagValues struct {
	repoPath                      *string
	top                           *int
	suggestOnly                   *bool
	applyCodemod                  *bool
	applyCodemodConfirm           *bool
	allowDirty                    *bool
	scopeMode                     *string
	formatFlag                    *string
	cacheEnabled                  *bool
	cachePath                     *string
	cacheReadOnly                 *bool
	legacyFailOnIncrease          *int
	thresholdFailOnIncrease       *int
	thresholdLowConfidenceWarning *int
	thresholdMinUsagePercent      *int
	thresholdMaxUncertainImports  *int
	scoreWeightUsage              *float64
	scoreWeightImpact             *float64
	scoreWeightConfidence         *float64
	licenseDeny                   *string
	licenseFailOnDeny             *bool
	licenseIncludeRegistryProv    *bool
	languageFlag                  *string
	runtimeProfile                *string
	baselinePath                  *string
	baselineStorePath             *string
	baselineKey                   *string
	baselineLabel                 *string
	saveBaseline                  *bool
	runtimeTracePath              *string
	runtimeTestCommand            *string
	configPath                    *string
	enableFeatures                *patternListFlag
	disableFeatures               *patternListFlag
	includePatterns               *patternListFlag
	excludePatterns               *patternListFlag
	lockfileDriftPolicy           *string
	notifyOn                      *string
	notifySlack                   *string
	notifyTeams                   *string
}

func newAnalyseFlagSet(req app.Request) (*flag.FlagSet, analyseFlagValues) {
	fs := flag.NewFlagSet("analyse", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	includePatterns := newPatternListFlag(req.Analyse.IncludePatterns)
	excludePatterns := newPatternListFlag(req.Analyse.ExcludePatterns)
	enableFeatures := newPatternListFlag(nil)
	disableFeatures := newPatternListFlag(nil)

	values := analyseFlagValues{
		repoPath:                      fs.String("repo", req.RepoPath, "repository path"),
		top:                           fs.Int("top", 0, "top N dependencies"),
		suggestOnly:                   fs.Bool("suggest-only", false, "generate codemod patch previews without mutating source files"),
		applyCodemod:                  fs.Bool("apply-codemod", req.Analyse.ApplyCodemod, "apply deterministic codemod patch previews for safe JS/TS subpath migrations"),
		applyCodemodConfirm:           fs.Bool("apply-codemod-confirm", false, "confirm codemod apply mode will mutate source files"),
		allowDirty:                    fs.Bool("allow-dirty", req.Analyse.AllowDirty, "allow codemod apply mode to run in a dirty git worktree"),
		scopeMode:                     fs.String("scope-mode", req.Analyse.ScopeMode, "analysis scope mode"),
		formatFlag:                    fs.String("format", string(req.Analyse.Format), "output format"),
		cacheEnabled:                  fs.Bool("cache", req.Analyse.CacheEnabled, "enable incremental analysis cache"),
		cachePath:                     fs.String("cache-path", req.Analyse.CachePath, "analysis cache directory path"),
		cacheReadOnly:                 fs.Bool("cache-readonly", req.Analyse.CacheReadOnly, "read cache without writing new entries"),
		legacyFailOnIncrease:          fs.Int("fail-on-increase", req.Analyse.Thresholds.FailOnIncreasePercent, "fail if waste increases beyond threshold"),
		thresholdFailOnIncrease:       fs.Int("threshold-fail-on-increase", req.Analyse.Thresholds.FailOnIncreasePercent, "waste increase threshold for CI failure"),
		thresholdLowConfidenceWarning: fs.Int("threshold-low-confidence-warning", req.Analyse.Thresholds.LowConfidenceWarningPercent, "low-confidence warning threshold"),
		thresholdMinUsagePercent:      fs.Int("threshold-min-usage-percent", req.Analyse.Thresholds.MinUsagePercentForRecommendations, "minimum usage percent threshold for recommendation generation"),
		thresholdMaxUncertainImports:  fs.Int("threshold-max-uncertain-imports", req.Analyse.Thresholds.MaxUncertainImportCount, "fail when uncertain dynamic import/require count exceeds threshold"),
		scoreWeightUsage:              fs.Float64("score-weight-usage", req.Analyse.Thresholds.RemovalCandidateWeightUsage, "relative weight for removal-candidate usage signal"),
		scoreWeightImpact:             fs.Float64("score-weight-impact", req.Analyse.Thresholds.RemovalCandidateWeightImpact, "relative weight for removal-candidate impact signal"),
		scoreWeightConfidence:         fs.Float64("score-weight-confidence", req.Analyse.Thresholds.RemovalCandidateWeightConfidence, "relative weight for removal-candidate confidence signal"),
		licenseDeny:                   fs.String("license-deny", strings.Join(req.Analyse.Thresholds.LicenseDenyList, ","), "comma-separated SPDX identifiers to deny"),
		licenseFailOnDeny:             fs.Bool("license-fail-on-deny", req.Analyse.Thresholds.LicenseFailOnDeny, "fail when denied licenses are detected"),
		licenseIncludeRegistryProv:    fs.Bool("license-provenance-registry", req.Analyse.Thresholds.LicenseIncludeRegistryProvenance, "opt-in registry provenance heuristics for JS/TS dependencies"),
		languageFlag:                  fs.String("language", req.Analyse.Language, "language adapter"),
		runtimeProfile:                fs.String("runtime-profile", req.Analyse.RuntimeProfile, "conditional exports runtime profile"),
		baselinePath:                  fs.String("baseline", req.Analyse.BaselinePath, "baseline report path"),
		baselineStorePath:             fs.String("baseline-store", req.Analyse.BaselineStorePath, "baseline snapshot directory"),
		baselineKey:                   fs.String("baseline-key", req.Analyse.BaselineKey, "baseline snapshot key for comparison"),
		baselineLabel:                 fs.String("baseline-label", req.Analyse.BaselineLabel, "label to use when saving a baseline snapshot"),
		saveBaseline:                  fs.Bool("save-baseline", req.Analyse.SaveBaseline, "save current run as immutable baseline snapshot"),
		runtimeTracePath:              fs.String("runtime-trace", req.Analyse.RuntimeTracePath, "runtime trace file path"),
		runtimeTestCommand:            fs.String("runtime-test-command", req.Analyse.RuntimeTestCommand, "optional command to execute tests with runtime tracing"),
		configPath:                    fs.String("config", req.Analyse.ConfigPath, "config file path"),
		enableFeatures:                enableFeatures,
		disableFeatures:               disableFeatures,
		includePatterns:               includePatterns,
		excludePatterns:               excludePatterns,
		lockfileDriftPolicy:           fs.String("lockfile-drift-policy", req.Analyse.Thresholds.LockfileDriftPolicy, "lockfile drift policy (off, warn, fail)"),
		notifyOn:                      fs.String("notify-on", string(req.Analyse.Notifications.Slack.Trigger), "notification trigger"),
		notifySlack:                   fs.String("notify-slack", req.Analyse.Notifications.Slack.WebhookURL, "Slack webhook URL"),
		notifyTeams:                   fs.String("notify-teams", req.Analyse.Notifications.Teams.WebhookURL, "Teams webhook URL"),
	}
	fs.Var(enableFeatures, "enable-feature", "comma-separated feature flag names to enable (repeatable)")
	fs.Var(disableFeatures, "disable-feature", "comma-separated feature flag names to disable (repeatable)")
	fs.Var(includePatterns, "include", "comma-separated include path globs (repeatable)")
	fs.Var(excludePatterns, "exclude", "comma-separated exclude path globs (repeatable)")

	return fs, values
}
