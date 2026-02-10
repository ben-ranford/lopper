package app

import (
	"context"
	"errors"
	"io"

	"github.com/ben-ranford/lopper/internal/analysis"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/ui"
)

var (
	ErrUnknownMode      = errors.New("unknown mode")
	ErrFailOnIncrease   = errors.New("dependency waste increased beyond threshold")
	ErrBaselineRequired = errors.New("baseline report is required for fail-on-increase")
)

type App struct {
	Analyzer  analysis.Analyzer
	Formatter report.Formatter
	TUI       ui.TUI
}

func New(out io.Writer, in io.Reader) *App {
	analyzer := analysis.NewService()
	formatter := report.NewFormatter()

	return &App{
		Analyzer:  analyzer,
		Formatter: formatter,
		TUI:       ui.NewSummary(out, in, analyzer, formatter),
	}
}

func (a *App) Execute(ctx context.Context, req Request) (string, error) {
	switch req.Mode {
	case ModeTUI:
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
	case ModeAnalyse:
		reportData, err := a.Analyzer.Analyse(ctx, analysis.Request{
			RepoPath:   req.RepoPath,
			Dependency: req.Analyse.Dependency,
			TopN:       req.Analyse.TopN,
			Language:   req.Analyse.Language,
		})
		if err != nil {
			return "", err
		}

		formatted, err := a.Formatter.Format(reportData, req.Analyse.Format)
		if err != nil {
			return "", err
		}

		if req.Analyse.BaselinePath != "" {
			baseline, err := report.Load(req.Analyse.BaselinePath)
			if err != nil {
				return formatted, err
			}
			reportData, err = report.ApplyBaseline(reportData, baseline)
			if err != nil {
				return formatted, err
			}
			formatted, err = a.Formatter.Format(reportData, req.Analyse.Format)
			if err != nil {
				return "", err
			}
		}

		if req.Analyse.FailOnIncrease > 0 {
			if reportData.WasteIncreasePercent == nil {
				return formatted, ErrBaselineRequired
			}
			if *reportData.WasteIncreasePercent > float64(req.Analyse.FailOnIncrease) {
				return formatted, ErrFailOnIncrease
			}
		}

		return formatted, nil
	default:
		return "", ErrUnknownMode
	}
}
