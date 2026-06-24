package app

import (
	"context"
	"errors"
	"io"

	"github.com/ben-ranford/lopper/internal/analysis"
	"github.com/ben-ranford/lopper/internal/featureflags"
	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/mcp"
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
	ErrMCPFeatureDisabled           = errors.New("mcp server feature is disabled")
	ErrProfileFeatureDisabled       = errors.New("threshold profile command feature is disabled; enable threshold-profiles-preview with --enable-feature")
)

type App struct {
	Analyzer  analysis.Analyser
	In        io.Reader
	Out       io.Writer
	Formatter *report.Formatter
	TUI       ui.TUI
	Notify    *notify.Dispatcher
	Features  *featureflags.Registry
	Languages *language.Registry
}

func New(out io.Writer, in io.Reader) *App {
	analyzer := analysis.NewService()
	formatter := report.NewFormatter()

	return &App{
		Analyzer:  analyzer,
		In:        in,
		Out:       out,
		Formatter: formatter,
		TUI:       ui.NewSummary(out, in, analyzer, formatter),
		Notify:    notify.NewDefaultDispatcher(),
		Features:  featureflags.DefaultRegistry(),
		Languages: analyzer.Registry,
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
	case ModeFeatures:
		return a.executeFeatures(req)
	case ModeProfile:
		return a.executeProfile(req)
	case ModeMCP:
		if !req.MCP.Features.Enabled(mcp.ServerPreviewFeature) {
			return "", ErrMCPFeatureDisabled
		}
		return "", mcp.Serve(ctx, a.In, a.Out, mcp.Options{
			Analyzer:         a.Analyzer,
			LanguageRegistry: a.Languages,
			FeatureRegistry:  a.Features,
			Features:         req.MCP.Features,
			MutationRunner:   a.mcpMutationRunner(),
		})
	default:
		return "", ErrUnknownMode
	}
}

func (a *App) executeTUI(ctx context.Context, req Request) (string, error) {
	opts := ui.Options{
		RepoPath:          req.RepoPath,
		Language:          req.TUI.Language,
		TopN:              req.TUI.TopN,
		Filter:            req.TUI.Filter,
		Sort:              req.TUI.Sort,
		PageSize:          req.TUI.PageSize,
		BaselinePath:      req.TUI.BaselinePath,
		BaselineStorePath: req.TUI.BaselineStorePath,
		BaselineKey:       req.TUI.BaselineKey,
	}
	if req.TUI.SnapshotPath != "" {
		return "", a.TUI.Snapshot(ctx, opts, req.TUI.SnapshotPath)
	}
	return "", a.TUI.Start(ctx, opts)
}
