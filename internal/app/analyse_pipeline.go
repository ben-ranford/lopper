package app

import (
	"context"
	"time"

	"github.com/ben-ranford/lopper/internal/report"
)

type analyseReportStage func(context.Context, report.Report) (report.Report, error)

func (a *App) executeAnalyse(ctx context.Context, req Request) (string, error) {
	prepared, err := prepareAnalyseExecution(ctx, req)
	if err != nil {
		return "", err
	}

	reportData, err := a.invokeAnalyse(ctx, prepared)
	if err != nil {
		return "", err
	}

	decorateAnalyseReport(&reportData, prepared)
	reportData, err = a.runAnalysePostStages(ctx, req.RepoPath, req.Analyse, reportData)

	return a.completeAnalyseExecution(ctx, req.Analyse, reportData, err)
}

func (a *App) invokeAnalyse(ctx context.Context, prepared preparedAnalyseExecution) (report.Report, error) {
	return a.Analyzer.Analyse(ctx, prepared.request)
}

func (a *App) runAnalysePostStages(ctx context.Context, repoPath string, req AnalyseRequest, reportData report.Report) (report.Report, error) {
	now := time.Now()

	return runAnalyseStages(ctx, reportData, []analyseReportStage{
		func(_ context.Context, reportData report.Report) (report.Report, error) {
			return a.applyBaselineIfNeeded(reportData, repoPath, req)
		},
		analyseValidationStage(func(reportData report.Report) error {
			return validateFailOnIncrease(reportData, req.Thresholds.FailOnIncreasePercent)
		}),
		analyseValidationStage(func(reportData report.Report) error {
			return validateUncertaintyThreshold(reportData, req.Thresholds.MaxUncertainImportCount)
		}),
		analyseValidationStage(func(reportData report.Report) error {
			return validateDeniedLicenses(reportData, req.Thresholds.LicenseFailOnDeny)
		}),
		func(ctx context.Context, reportData report.Report) (report.Report, error) {
			return applyCodemodIfNeeded(ctx, reportData, repoPath, req, now)
		},
		func(_ context.Context, reportData report.Report) (report.Report, error) {
			return a.saveBaselineIfNeeded(reportData, repoPath, req, now)
		},
	})
}

func runAnalyseStages(ctx context.Context, reportData report.Report, stages []analyseReportStage) (report.Report, error) {
	var err error
	for _, stage := range stages {
		reportData, err = stage(ctx, reportData)
		if err != nil {
			return reportData, err
		}
	}

	return reportData, nil
}

func analyseValidationStage(validate func(report.Report) error) analyseReportStage {
	return func(_ context.Context, reportData report.Report) (report.Report, error) {
		return reportData, validate(reportData)
	}
}

func (a *App) completeAnalyseExecution(ctx context.Context, req AnalyseRequest, reportData report.Report, runErr error) (string, error) {
	a.appendNotificationWarnings(ctx, req.Notifications, &reportData, buildNotificationOutcome(reportData, runErr))
	if runErr != nil {
		return a.formatReportWithOriginalError(reportData, req.Format, runErr)
	}

	formatted, err := a.Formatter.Format(reportData, req.Format)
	if err != nil {
		return "", err
	}

	return formatted, nil
}

func (a *App) formatReportWithOriginalError(reportData report.Report, format report.Format, originalErr error) (string, error) {
	formatted, formatErr := a.Formatter.Format(reportData, format)
	if formatErr != nil {
		return "", originalErr
	}

	return formatted, originalErr
}
