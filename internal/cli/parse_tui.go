package cli

import (
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/ben-ranford/lopper/internal/app"
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
	languageFlag := fs.String("language", req.TUI.Language, "language adapter")
	top := fs.Int("top", req.TUI.TopN, "top N dependencies")
	filter := fs.String("filter", req.TUI.Filter, "filter dependencies")
	sortMode := fs.String("sort", req.TUI.Sort, "sort mode")
	pageSize := fs.Int("page-size", req.TUI.PageSize, "page size")
	snapshot := fs.String("snapshot", req.TUI.SnapshotPath, "snapshot output path")
	outputFlag := fs.String("output", "", "snapshot output path")
	outputShortFlag := fs.String("o", "", "snapshot output path")
	baselinePath := fs.String("baseline", req.TUI.BaselinePath, "baseline report path")
	baselineStorePath := fs.String("baseline-store", req.TUI.BaselineStorePath, "baseline snapshot directory")
	baselineKey := fs.String("baseline-key", req.TUI.BaselineKey, "baseline snapshot key for comparison")

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

	return req, nil
}

func resolveTUISnapshotPath(snapshotPath, outputPath string) (string, error) {
	snapshotPath = strings.TrimSpace(snapshotPath)
	outputPath = strings.TrimSpace(outputPath)
	if snapshotPath != "" && outputPath != "" && snapshotPath != outputPath {
		return "", fmt.Errorf("--snapshot and --output must match when both are provided")
	}
	if snapshotPath != "" {
		return snapshotPath, nil
	}
	return outputPath, nil
}
