package cli

import (
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/ben-ranford/lopper/internal/app"
	"github.com/ben-ranford/lopper/internal/featureflags"
	"github.com/ben-ranford/lopper/internal/thresholds"
)

const (
	snapshotOutputPathUsage = "snapshot output path"
	staveTUIFeatureName     = "stave-tui-preview"
)

func parseTUI(args []string, req app.Request) (app.Request, error) {
	normalizedArgs, err := normalizeArgs(args)
	if err != nil {
		return req, err
	}
	args = normalizedArgs

	fs := flag.NewFlagSet("tui", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	repoPath := fs.String("repo", req.RepoPath, "repository path")
	configPath := fs.String("config", "", "config file path (relative to repository)")
	languageFlag := fs.String("language", req.TUI.Language, "language adapter")
	top := fs.Int("top", req.TUI.TopN, "top N dependencies")
	filter := fs.String("filter", req.TUI.Filter, "filter dependencies")
	sortMode := fs.String("sort", req.TUI.Sort, "sort mode")
	pageSize := fs.Int("page-size", req.TUI.PageSize, "page size")
	snapshot := fs.String("snapshot", req.TUI.SnapshotPath, snapshotOutputPathUsage)
	outputFlag := fs.String("output", "", snapshotOutputPathUsage)
	outputShortFlag := fs.String("o", "", snapshotOutputPathUsage)
	baselinePath := fs.String("baseline", req.TUI.BaselinePath, "baseline report path")
	baselineStorePath := fs.String("baseline-store", req.TUI.BaselineStorePath, "baseline snapshot directory")
	baselineKey := fs.String("baseline-key", req.TUI.BaselineKey, "baseline snapshot key for comparison")
	enableFeatures := newPatternListFlag(nil)
	disableFeatures := newPatternListFlag(nil)
	fs.Var(enableFeatures, "enable-feature", "comma-separated feature flag names to enable (repeatable)")
	fs.Var(disableFeatures, "disable-feature", "comma-separated feature flag names to disable (repeatable)")

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
	outputPath, err := resolveOutputPath(*outputFlag, *outputShortFlag)
	if err != nil {
		return req, err
	}
	snapshotPath, err := resolveTUISnapshotPath(*snapshot, outputPath)
	if err != nil {
		return req, err
	}

	req.Mode = app.ModeTUI
	req.RepoPath = *repoPath
	req.TUI = app.TUIRequest{
		Language:          strings.TrimSpace(*languageFlag),
		SnapshotPath:      snapshotPath,
		Filter:            strings.TrimSpace(*filter),
		Sort:              strings.TrimSpace(*sortMode),
		TopN:              *top,
		PageSize:          *pageSize,
		BaselinePath:      strings.TrimSpace(*baselinePath),
		BaselineStorePath: strings.TrimSpace(*baselineStorePath),
		BaselineKey:       strings.TrimSpace(*baselineKey),
	}
	features, useStave, err := resolveTUIFeatures(*repoPath, *configPath, enableFeatures.Values(), disableFeatures.Values())
	if err != nil {
		return req, err
	}
	req.TUI.Features = features
	req.TUI.UseStavePreview = useStave

	return req, nil
}

func resolveTUIFeatures(repoPath, configPath string, enable, disable []string) (featureflags.Set, bool, error) {
	config, err := thresholds.LoadWithPolicy(strings.TrimSpace(repoPath), configPath)
	if err != nil {
		return featureflags.Set{}, false, err
	}
	channel, lock, err := resolveFeatureBuildContext()
	if err != nil {
		return featureflags.Set{}, false, err
	}
	features, err := featureRegistryProvider().ResolveLayers(featureflags.ResolveOptions{
		Channel: channel,
		Lock:    lock,
		Enable:  config.Features.Enable,
		Disable: config.Features.Disable,
	}, featureflags.Overrides{Enable: enable, Disable: disable})
	if err != nil {
		return featureflags.Set{}, false, err
	}
	explicit := featureExplicitlyEnabled(enable, staveTUIFeatureName) || featureExplicitlyEnabled(config.Features.Enable, staveTUIFeatureName)
	return features, explicit && features.Enabled(staveTUIFeatureName), nil
}

func featureExplicitlyEnabled(refs []string, canonical string) bool {
	registry := featureRegistryProvider()
	for _, ref := range refs {
		resolved, ok := registry.LookupReference(ref)
		if ok && resolved.Flag.Name == canonical {
			return true
		}
	}
	return false
}

func resolveTUISnapshotPath(snapshotPath, outputPath string) (string, error) {
	return resolveMatchingPath(snapshotPath, outputPath, "--snapshot", "--output")
}
