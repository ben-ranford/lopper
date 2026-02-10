package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/ben-ranford/lopper/internal/app"
	"github.com/ben-ranford/lopper/internal/report"
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

	fs := flag.NewFlagSet("analyse", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	repoPath := fs.String("repo", req.RepoPath, "repository path")
	top := fs.Int("top", 0, "top N dependencies")
	formatFlag := fs.String("format", string(req.Analyse.Format), "output format")
	failOnIncrease := fs.Int("fail-on-increase", 0, "fail if waste increases beyond threshold")
	languageFlag := fs.String("language", req.Analyse.Language, "language adapter")
	baselinePath := fs.String("baseline", req.Analyse.BaselinePath, "baseline report path")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return req, ErrHelpRequested
		}
		return req, err
	}

	if *top < 0 {
		return req, fmt.Errorf("--top must be >= 0")
	}
	if *failOnIncrease < 0 {
		return req, fmt.Errorf("--fail-on-increase must be >= 0")
	}

	format, err := report.ParseFormat(*formatFlag)
	if err != nil {
		return req, err
	}

	remaining := fs.Args()
	if len(remaining) > 1 {
		return req, fmt.Errorf("too many arguments for analyse")
	}

	dependency := ""
	if len(remaining) == 1 {
		dependency = strings.TrimSpace(remaining[0])
	}

	if dependency != "" && *top > 0 {
		return req, ErrConflictingTargets
	}
	if dependency == "" && *top == 0 {
		return req, fmt.Errorf("missing dependency name or --top")
	}

	req.Mode = app.ModeAnalyse
	req.RepoPath = *repoPath
	req.Analyse = app.AnalyseRequest{
		Dependency:     dependency,
		TopN:           *top,
		FailOnIncrease: *failOnIncrease,
		Format:         format,
		Language:       strings.TrimSpace(*languageFlag),
		BaselinePath:   strings.TrimSpace(*baselinePath),
	}

	return req, nil
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

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return req, ErrHelpRequested
		}
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
	case "--repo", "--top", "--format", "--fail-on-increase", "--language", "--baseline", "--snapshot", "--filter", "--sort", "--page-size":
		return true
	default:
		return false
	}
}
