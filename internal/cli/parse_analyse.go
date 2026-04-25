package cli

import (
	"flag"
	"fmt"
	"strings"

	"github.com/ben-ranford/lopper/internal/app"
	"github.com/ben-ranford/lopper/internal/featureflags"
	"github.com/ben-ranford/lopper/internal/notify"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/runtime"
	"github.com/ben-ranford/lopper/internal/thresholds"
)

type analyseParseState struct {
	dependency    string
	format        report.Format
	scopeMode     string
	visited       map[string]bool
	thresholds    thresholds.Values
	scope         thresholds.PathScope
	policySources []string
	configPath    string
	features      featureflags.Set
	notifications notify.Config
}

func parseAnalyse(args []string, req app.Request) (app.Request, error) {
	normalizedArgs, err := normalizeArgs(args)
	if err != nil {
		return req, err
	}
	args = normalizedArgs

	fs, flags := newAnalyseFlagSet(req)
	if err := parseFlagSet(fs, args); err != nil {
		return req, err
	}

	state, err := parseAnalyseState(fs, flags)
	if err != nil {
		return req, err
	}

	return buildAnalyseRequest(req, flags, state), nil
}

func parseAnalyseState(fs *flag.FlagSet, flags analyseFlagValues) (analyseParseState, error) {
	dependency, err := validateAnalyseTarget(fs.Args(), *flags.top)
	if err != nil {
		return analyseParseState{}, err
	}
	if err := validateSuggestOnlyTarget(*flags.suggestOnly, dependency, *flags.top); err != nil {
		return analyseParseState{}, err
	}
	if err := validateCodemodApplyFlags(*flags.suggestOnly, *flags.applyCodemod, *flags.applyCodemodConfirm, *flags.allowDirty, dependency, *flags.top); err != nil {
		return analyseParseState{}, err
	}
	if err := runtime.ValidateRuntimeCommand(*flags.runtimeTestCommand); err != nil {
		return analyseParseState{}, err
	}

	format, err := report.ParseFormat(*flags.formatFlag)
	if err != nil {
		return analyseParseState{}, err
	}
	scopeMode, err := parseScopeMode(*flags.scopeMode)
	if err != nil {
		return analyseParseState{}, err
	}

	visited := visitedFlags(fs)
	resolvedThresholds, resolvedScope, policySources, configFeatures, resolvedConfigPath, err := resolveAnalyseThresholds(flags, visited)
	if err != nil {
		return analyseParseState{}, err
	}
	resolvedFeatures, err := resolveAnalyseFeatures(visited, flags, configFeatures)
	if err != nil {
		return analyseParseState{}, err
	}
	resolvedNotifications, err := resolveAnalyseNotifications(visited, flags, resolvedConfigPath)
	if err != nil {
		return analyseParseState{}, err
	}

	return analyseParseState{
		dependency:    dependency,
		format:        format,
		scopeMode:     scopeMode,
		visited:       visited,
		thresholds:    resolvedThresholds,
		scope:         resolvedScope,
		policySources: policySources,
		configPath:    resolvedConfigPath,
		features:      resolvedFeatures,
		notifications: resolvedNotifications,
	}, nil
}

func buildAnalyseRequest(req app.Request, flags analyseFlagValues, state analyseParseState) app.Request {
	req.Mode = app.ModeAnalyse
	req.RepoPath = strings.TrimSpace(*flags.repoPath)
	req.Analyse = app.AnalyseRequest{
		Dependency:         state.dependency,
		TopN:               *flags.top,
		ScopeMode:          state.scopeMode,
		SuggestOnly:        *flags.suggestOnly,
		ApplyCodemod:       *flags.applyCodemod,
		AllowDirty:         *flags.allowDirty,
		Format:             state.format,
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
		IncludePatterns:    resolveScopePatterns(state.visited, "include", flags.includePatterns.Values(), state.scope.Include),
		ExcludePatterns:    resolveScopePatterns(state.visited, "exclude", flags.excludePatterns.Values(), state.scope.Exclude),
		ConfigPath:         state.configPath,
		PolicySources:      state.policySources,
		Features:           state.features,
		Thresholds:         state.thresholds,
		Notifications:      state.notifications,
	}

	return req
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

func validateCodemodApplyFlags(suggestOnly bool, applyCodemod bool, applyConfirm bool, allowDirty bool, dependency string, top int) error {
	if suggestOnly && applyCodemod {
		return fmt.Errorf("--suggest-only and --apply-codemod cannot be combined")
	}
	if !applyCodemod {
		if applyConfirm {
			return fmt.Errorf("--apply-codemod-confirm requires --apply-codemod")
		}
		if allowDirty {
			return fmt.Errorf("--allow-dirty requires --apply-codemod")
		}
		return nil
	}
	if top > 0 {
		return fmt.Errorf("--apply-codemod requires a specific dependency target")
	}
	if strings.TrimSpace(dependency) == "" {
		return fmt.Errorf("--apply-codemod requires a dependency argument")
	}
	if !applyConfirm {
		return fmt.Errorf("--apply-codemod requires --apply-codemod-confirm")
	}
	return nil
}
