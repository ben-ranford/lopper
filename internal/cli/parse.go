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

	resolvedThresholds, resolvedConfigPath, err := resolveAnalyseThresholds(fs, flags)
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
		ConfigPath:         resolvedConfigPath,
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
}

func newAnalyseFlagSet(req app.Request) (*flag.FlagSet, analyseFlagValues) {
	fs := flag.NewFlagSet("analyse", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

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
	}

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

func resolveAnalyseThresholds(fs *flag.FlagSet, values analyseFlagValues) (thresholds.Values, string, error) {
	configOverrides, resolvedConfigPath, err := thresholds.Load(strings.TrimSpace(*values.repoPath), strings.TrimSpace(*values.configPath))
	if err != nil {
		return thresholds.Values{}, "", err
	}

	resolvedThresholds := configOverrides.Apply(thresholds.Defaults())
	cliOverrides, err := cliThresholdOverrides(visitedFlags(fs), values)
	if err != nil {
		return thresholds.Values{}, "", err
	}
	resolvedThresholds = cliOverrides.Apply(resolvedThresholds)
	if err := resolvedThresholds.Validate(); err != nil {
		return thresholds.Values{}, "", err
	}
	return resolvedThresholds, resolvedConfigPath, nil
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
	if visited["score-weight-usage"] {
		overrides.RemovalCandidateWeightUsage = values.scoreWeightUsage
	}
	if visited["score-weight-impact"] {
		overrides.RemovalCandidateWeightImpact = values.scoreWeightImpact
	}
	if visited["score-weight-confidence"] {
		overrides.RemovalCandidateWeightConfidence = values.scoreWeightConfidence
	}
	return overrides, nil
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
	case "--repo", "--top", "--format", "--cache-path", "--fail-on-increase", "--threshold-fail-on-increase", "--threshold-low-confidence-warning", "--threshold-min-usage-percent", "--score-weight-usage", "--score-weight-impact", "--score-weight-confidence", "--language", "--runtime-profile", "--baseline", "--baseline-store", "--baseline-key", "--baseline-label", "--runtime-trace", "--runtime-test-command", "--config", "--snapshot", "--filter", "--sort", "--page-size":
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
