package cli

import (
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/ben-ranford/lopper/internal/app"
)

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
