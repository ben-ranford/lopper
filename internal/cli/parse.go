package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/ben-ranford/lopper/internal/app"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/thresholds"
)

var (
	ErrHelpRequested      = errors.New("help requested")
	ErrConflictingTargets = errors.New("cannot use both dependency and --top")
)

func ParseArgs(args []string) (app.Request, error) {
	req := app.DefaultRequest()
	if len(args) == 0 {
		return req, nil
	}

	if isHelpArg(args[0]) {
		return req, ErrHelpRequested
	}

	switch args[0] {
	case "tui":
		return parseTUI(args[1:], req)
	case "analyse":
		return parseAnalyse(args[1:], req)
	default:
		return req, fmt.Errorf("unknown command: %s", args[0])
	}
}

func parseAnalyse(args []string, req app.Request) (app.Request, error) {
	args = normalizeArgs(args)

	fs, flags := newAnalyseFlagSet(req)
	if err := parseFlagSet(fs, args); err != nil {
		return req, err
	}

	dependency, err := validateAnalyseTarget(fs.Args(), *flags.top)
	if err != nil {
		return req, err
	}
	if err := validateSuggestOnlyTarget(*flags.suggestOnly, dependency, *flags.top); err != nil {
		return req, err
	}

	format, err := report.ParseFormat(*flags.formatFlag)
	if err != nil {
		return req, err
	}

	visited := visitedFlags(fs)
	resolvedThresholds, resolvedScope, policySources, resolvedConfigPath, err := resolveAnalyseThresholds(fs, flags)
	if err != nil {
		return req, err
	}

	req.Mode = app.ModeAnalyse
	req.RepoPath = strings.TrimSpace(*flags.repoPath)
	req.Analyse = app.AnalyseRequest{
		Dependency:         dependency,
		TopN:               *flags.top,
		SuggestOnly:        *flags.suggestOnly,
		Format:             format,
		Language:           strings.TrimSpace(*flags.languageFlag),
		CacheEnabled:       *flags.cacheEnabled,
		CachePath:          strings.TrimSpace(*flags.cachePath),
		CacheReadOnly:      *flags.cacheReadOnly,
		RuntimeProfile:     strings.TrimSpace(*flags.runtimeProfile),
		BaselinePath:       strings.TrimSpace(*flags.baselinePath),
		BaselineStorePath:  strings.TrimSpace(*flags.baselineStorePath),
		BaselineKey:        strings.TrimSpace(*flags.baselineKey),
		BaselineLabel:      strings.TrimSpace(*flags.baselineLabel),
		SaveBaseline:       *flags.saveBaseline,
		RuntimeTracePath:   strings.TrimSpace(*flags.runtimeTracePath),
		RuntimeTestCommand: strings.TrimSpace(*flags.runtimeTestCommand),
		IncludePatterns:    resolveScopePatterns(visited, "include", flags.includePatterns.Values(), resolvedScope.Include),
		ExcludePatterns:    resolveScopePatterns(visited, "exclude", flags.excludePatterns.Values(), resolvedScope.Exclude),
		ConfigPath:         resolvedConfigPath,
		PolicySources:      policySources,
		Thresholds:         resolvedThresholds,
	}

	return req, nil
}

type analyseFlagValues struct {
	repoPath                      *string
	top                           *int
	suggestOnly                   *bool
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
	includePatterns               *patternListFlag
	excludePatterns               *patternListFlag
	lockfileDriftPolicy           *string
}

func newAnalyseFlagSet(req app.Request) (*flag.FlagSet, analyseFlagValues) {
	fs := flag.NewFlagSet("analyse", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	includePatterns := newPatternListFlag(req.Analyse.IncludePatterns)
	excludePatterns := newPatternListFlag(req.Analyse.ExcludePatterns)

	values := analyseFlagValues{
		repoPath:                      fs.String("repo", req.RepoPath, "repository path"),
		top:                           fs.Int("top", 0, "top N dependencies"),
		suggestOnly:                   fs.Bool("suggest-only", false, "generate codemod patch previews without mutating source files"),
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
		includePatterns:               includePatterns,
		excludePatterns:               excludePatterns,
		lockfileDriftPolicy:           fs.String("lockfile-drift-policy", req.Analyse.Thresholds.LockfileDriftPolicy, "lockfile drift policy (off, warn, fail)"),
	}
	fs.Var(includePatterns, "include", "comma-separated include path globs (repeatable)")
	fs.Var(excludePatterns, "exclude", "comma-separated exclude path globs (repeatable)")

	return fs, values
}

func parseFlagSet(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return ErrHelpRequested
		}
		return err
	}
	return nil
}

func validateAnalyseTarget(remaining []string, top int) (string, error) {
	if top < 0 {
		return "", fmt.Errorf("--top must be >= 0")
	}
	if len(remaining) > 1 {
		return "", fmt.Errorf("too many arguments for analyse")
	}

	dependency := ""
	if len(remaining) == 1 {
		dependency = strings.TrimSpace(remaining[0])
	}
	if dependency != "" && top > 0 {
		return "", ErrConflictingTargets
	}
	if dependency == "" && top == 0 {
		return "", fmt.Errorf("missing dependency name or --top")
	}
	return dependency, nil
}

func validateSuggestOnlyTarget(suggestOnly bool, dependency string, top int) error {
	if !suggestOnly {
		return nil
	}
	if top > 0 {
		return fmt.Errorf("--suggest-only requires a specific dependency target")
	}
	if strings.TrimSpace(dependency) == "" {
		return fmt.Errorf("--suggest-only requires a dependency argument")
	}
	return nil
}

func resolveAnalyseThresholds(fs *flag.FlagSet, values analyseFlagValues) (thresholds.Values, thresholds.PathScope, []string, string, error) {
	loadResult, err := thresholds.LoadWithPolicy(strings.TrimSpace(*values.repoPath), strings.TrimSpace(*values.configPath))
	if err != nil {
		return thresholds.Values{}, thresholds.PathScope{}, nil, "", err
	}

	resolvedThresholds := loadResult.Resolved
	cliOverrides, err := cliThresholdOverrides(visitedFlags(fs), values)
	if err != nil {
		return thresholds.Values{}, thresholds.PathScope{}, nil, "", err
	}
	resolvedThresholds = cliOverrides.Apply(resolvedThresholds)
	if err := resolvedThresholds.Validate(); err != nil {
		return thresholds.Values{}, thresholds.PathScope{}, nil, "", err
	}
	policySources := append([]string{}, loadResult.PolicySources...)
	if hasThresholdOverrides(cliOverrides) {
		policySources = append([]string{"cli"}, policySources...)
	}
	return resolvedThresholds, loadResult.Scope, policySources, loadResult.ConfigPath, nil
}

func cliThresholdOverrides(visited map[string]bool, values analyseFlagValues) (thresholds.Overrides, error) {
	overrides := thresholds.Overrides{}
	if visited["fail-on-increase"] {
		overrides.FailOnIncreasePercent = values.legacyFailOnIncrease
	}
	if visited["threshold-fail-on-increase"] {
		if overrides.FailOnIncreasePercent != nil && *overrides.FailOnIncreasePercent != *values.thresholdFailOnIncrease {
			return thresholds.Overrides{}, fmt.Errorf("--fail-on-increase and --threshold-fail-on-increase must match when both are provided")
		}
		overrides.FailOnIncreasePercent = values.thresholdFailOnIncrease
	}
	if visited["threshold-low-confidence-warning"] {
		overrides.LowConfidenceWarningPercent = values.thresholdLowConfidenceWarning
	}
	if visited["threshold-min-usage-percent"] {
		overrides.MinUsagePercentForRecommendations = values.thresholdMinUsagePercent
	}
	if visited["threshold-max-uncertain-imports"] {
		overrides.MaxUncertainImportCount = values.thresholdMaxUncertainImports
	}
	if visited["score-weight-usage"] {
		overrides.RemovalCandidateWeightUsage = values.scoreWeightUsage
	}
	if visited["score-weight-impact"] {
		overrides.RemovalCandidateWeightImpact = values.scoreWeightImpact
	}
	if visited["score-weight-confidence"] {
		overrides.RemovalCandidateWeightConfidence = values.scoreWeightConfidence
	}
	if visited["lockfile-drift-policy"] {
		overrides.LockfileDriftPolicy = values.lockfileDriftPolicy
	}
	return overrides, nil
}

func hasThresholdOverrides(overrides thresholds.Overrides) bool {
	return overrides.FailOnIncreasePercent != nil ||
		overrides.LowConfidenceWarningPercent != nil ||
		overrides.MinUsagePercentForRecommendations != nil ||
		overrides.MaxUncertainImportCount != nil ||
		overrides.RemovalCandidateWeightUsage != nil ||
		overrides.RemovalCandidateWeightImpact != nil ||
		overrides.RemovalCandidateWeightConfidence != nil ||
		overrides.LockfileDriftPolicy != nil
}

func resolveScopePatterns(visited map[string]bool, flagName string, cliValues []string, configValues []string) []string {
	if visited[flagName] {
		if len(cliValues) == 0 {
			return nil
		}
		return append([]string{}, cliValues...)
	}
	if len(configValues) == 0 {
		return nil
	}
	return append([]string{}, configValues...)
}

type patternListFlag struct {
	patterns []string
}

func newPatternListFlag(initial []string) *patternListFlag {
	if len(initial) == 0 {
		return &patternListFlag{}
	}
	return &patternListFlag{
		patterns: append([]string{}, initial...),
	}
}

func (f *patternListFlag) String() string {
	return strings.Join(f.patterns, ",")
}

func (f *patternListFlag) Set(value string) error {
	f.patterns = mergePatterns(f.patterns, splitPatternList(value))
	return nil
}

func (f *patternListFlag) Values() []string {
	if len(f.patterns) == 0 {
		return nil
	}
	return append([]string{}, f.patterns...)
}

func mergePatterns(existing, next []string) []string {
	if len(next) == 0 {
		return existing
	}
	seen := make(map[string]struct{}, len(existing)+len(next))
	merged := make([]string, 0, len(existing)+len(next))
	for _, pattern := range existing {
		if _, ok := seen[pattern]; ok {
			continue
		}
		seen[pattern] = struct{}{}
		merged = append(merged, pattern)
	}
	for _, pattern := range next {
		if _, ok := seen[pattern]; ok {
			continue
		}
		seen[pattern] = struct{}{}
		merged = append(merged, pattern)
	}
	return merged
}

func splitPatternList(value string) []string {
	parts := strings.Split(value, ",")
	seen := make(map[string]struct{}, len(parts))
	patterns := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		if _, exists := seen[trimmed]; exists {
			continue
		}
		seen[trimmed] = struct{}{}
		patterns = append(patterns, trimmed)
	}
	if len(patterns) == 0 {
		return nil
	}
	return patterns
}

func parseTUI(args []string, req app.Request) (app.Request, error) {
	args = normalizeArgs(args)

	fs := flag.NewFlagSet("tui", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	repoPath := fs.String("repo", req.RepoPath, "repository path")
	languageFlag := fs.String("language", req.TUI.Language, "language adapter")
	top := fs.Int("top", req.TUI.TopN, "top N dependencies")
	filter := fs.String("filter", req.TUI.Filter, "filter dependencies")
	sortMode := fs.String("sort", req.TUI.Sort, "sort mode")
	pageSize := fs.Int("page-size", req.TUI.PageSize, "page size")
	snapshot := fs.String("snapshot", req.TUI.SnapshotPath, "snapshot output path")

	if err := parseFlagSet(fs, args); err != nil {
		return req, err
	}
	if fs.NArg() > 0 {
		return req, fmt.Errorf("unexpected arguments for tui")
	}
	if *top < 0 {
		return req, fmt.Errorf("--top must be >= 0")
	}
	if *pageSize < 0 {
		return req, fmt.Errorf("--page-size must be >= 0")
	}

	req.Mode = app.ModeTUI
	req.RepoPath = *repoPath
	req.TUI = app.TUIRequest{
		Language:     strings.TrimSpace(*languageFlag),
		SnapshotPath: strings.TrimSpace(*snapshot),
		Filter:       strings.TrimSpace(*filter),
		Sort:         strings.TrimSpace(*sortMode),
		TopN:         *top,
		PageSize:     *pageSize,
	}

	return req, nil
}

func isHelpArg(arg string) bool {
	switch arg {
	case "-h", "--help", "help":
		return true
	default:
		return false
	}
}

func normalizeArgs(args []string) []string {
	if len(args) == 0 {
		return args
	}

	flags := make([]string, 0, len(args))
	positionals := make([]string, 0, 1)

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positionals = append(positionals, args[i+1:]...)
			break
		}
		if strings.HasPrefix(arg, "-") {
			flags = append(flags, arg)
			if flagNeedsValue(arg) && i+1 < len(args) {
				flags = append(flags, args[i+1])
				i++
			}
			continue
		}
		positionals = append(positionals, arg)
	}

	return append(flags, positionals...)
}

func flagNeedsValue(arg string) bool {
	if strings.Contains(arg, "=") {
		return false
	}
	switch arg {
	case "--repo", "--top", "--format", "--cache-path", "--fail-on-increase", "--threshold-fail-on-increase", "--threshold-low-confidence-warning", "--threshold-min-usage-percent", "--threshold-max-uncertain-imports", "--score-weight-usage", "--score-weight-impact", "--score-weight-confidence", "--language", "--runtime-profile", "--baseline", "--baseline-store", "--baseline-key", "--baseline-label", "--runtime-trace", "--runtime-test-command", "--config", "--include", "--exclude", "--lockfile-drift-policy", "--snapshot", "--filter", "--sort", "--page-size":
		return true
	default:
		return false
	}
}

func visitedFlags(fs *flag.FlagSet) map[string]bool {
	visited := make(map[string]bool)
	fs.Visit(func(f *flag.Flag) {
		visited[f.Name] = true
	})
	return visited
}
