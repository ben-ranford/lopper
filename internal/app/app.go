package app

import (
	"context"
	"errors"
	"io"

	"github.com/ben-ranford/lopper/internal/analysis"
	"github.com/ben-ranford/lopper/internal/notify"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/ui"
)

var (
	ErrUnknownMode                  = errors.New("unknown mode")
	ErrFailOnIncrease               = errors.New("dependency waste increased beyond threshold")
	ErrBaselineRequired             = errors.New("baseline report is required for fail-on-increase")
	ErrLockfileDrift                = errors.New("lockfile drift detected")
	ErrUncertaintyThresholdExceeded = errors.New("uncertain dynamic import/require usage exceeded threshold")
	ErrDeniedLicenses               = errors.New("denied licenses detected")
	ErrDirtyWorktree                = errors.New("codemod apply requires a clean git worktree")
	ErrCodemodApplyFailed           = errors.New("codemod apply failed")
)

type App struct {
	Analyzer  analysis.Analyser
	Formatter *report.Formatter
	TUI       ui.TUI
	Notify    *notify.Dispatcher
}

func New(out io.Writer, in io.Reader) *App {
	analyzer := analysis.NewService()
	formatter := report.NewFormatter()

	return &App{
		Analyzer:  analyzer,
		Formatter: formatter,
		TUI:       ui.NewSummary(out, in, analyzer, formatter),
		Notify:    notify.NewDefaultDispatcher(),
	}
}

func (a *App) Execute(ctx context.Context, req Request) (string, error) {
	switch req.Mode {
	case ModeTUI:
		return a.executeTUI(ctx, req)
	case ModeAnalyse:
		return a.executeAnalyse(ctx, req)
	case ModeDashboard:
		return a.executeDashboard(ctx, req)
	default:
		return "", ErrUnknownMode
	}
}

func (a *App) executeTUI(ctx context.Context, req Request) (string, error) {
	opts := ui.Options{
		RepoPath: req.RepoPath,
		Language: req.TUI.Language,
		TopN:     req.TUI.TopN,
		Filter:   req.TUI.Filter,
		Sort:     req.TUI.Sort,
		PageSize: req.TUI.PageSize,
	}
	if req.TUI.SnapshotPath != "" {
		return "", a.TUI.Snapshot(ctx, opts, req.TUI.SnapshotPath)
	}
	return "", a.TUI.Start(ctx, opts)
}
