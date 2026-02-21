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
		return a.executeTUI(ctx, req)
	case ModeAnalyse:
		return a.executeAnalyse(ctx, req)
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

func (a *App) executeAnalyse(ctx context.Context, req Request) (string, error) {
	lowConfidence := req.Analyse.Thresholds.LowConfidenceWarningPercent
	minUsage := req.Analyse.Thresholds.MinUsagePercentForRecommendations

	reportData, err := a.Analyzer.Analyse(ctx, analysis.Request{
		RepoPath:                          req.RepoPath,
		Dependency:                        req.Analyse.Dependency,
		TopN:                              req.Analyse.TopN,
		Language:                          req.Analyse.Language,
		RuntimeProfile:                    req.Analyse.RuntimeProfile,
		RuntimeTracePath:                  req.Analyse.RuntimeTracePath,
		LowConfidenceWarningPercent:       &lowConfidence,
		MinUsagePercentForRecommendations: &minUsage,
	})
	if err != nil {
		return "", err
	}
	reportData.EffectiveThresholds = &report.EffectiveThresholds{
		FailOnIncreasePercent:             req.Analyse.Thresholds.FailOnIncreasePercent,
		LowConfidenceWarningPercent:       req.Analyse.Thresholds.LowConfidenceWarningPercent,
		MinUsagePercentForRecommendations: req.Analyse.Thresholds.MinUsagePercentForRecommendations,
	}

	reportData, formatted, err := a.applyBaselineIfNeeded(reportData, req.Analyse)
	if err != nil {
		return formatted, err
	}
	if err := validateFailOnIncrease(reportData, req.Analyse.Thresholds.FailOnIncreasePercent); err != nil {
		return formatted, err
	}
	return formatted, nil
}

func (a *App) applyBaselineIfNeeded(reportData report.Report, req AnalyseRequest) (report.Report, string, error) {
	formatted, err := a.Formatter.Format(reportData, req.Format)
	if err != nil {
		return report.Report{}, "", err
	}
	if req.BaselinePath == "" {
		return reportData, formatted, nil
	}

	baseline, err := report.Load(req.BaselinePath)
	if err != nil {
		return reportData, formatted, err
	}
	reportData, err = report.ApplyBaseline(reportData, baseline)
	if err != nil {
		return reportData, formatted, err
	}
	formatted, err = a.Formatter.Format(reportData, req.Format)
	if err != nil {
		return report.Report{}, "", err
	}
	return reportData, formatted, nil
}

func validateFailOnIncrease(reportData report.Report, threshold int) error {
	if threshold <= 0 {
		return nil
	}
	if reportData.WasteIncreasePercent == nil {
		return ErrBaselineRequired
	}
	if *reportData.WasteIncreasePercent > float64(threshold) {
		return ErrFailOnIncrease
	}
	return nil
}
