package app

import (
	"context"
	"errors"

	"github.com/ben-ranford/lopper/internal/notify"
	"github.com/ben-ranford/lopper/internal/report"
)

func (a *App) appendNotificationWarnings(ctx context.Context, cfg notify.Config, reportData *report.Report, outcome notify.Outcome) {
	if reportData == nil {
		return
	}
	if !cfg.HasTargets() {
		return
	}

	notifyWarnings := a.Notify.Dispatch(ctx, cfg, *reportData, outcome)
	reportData.Warnings = append(reportData.Warnings, notifyWarnings...)
}

func buildNotificationOutcome(reportData report.Report, runErr error) notify.Outcome {
	outcome := notify.Outcome{
		WasteIncreasePercent: reportData.WasteIncreasePercent,
	}
	if runErr == nil {
		return outcome
	}

	if errors.Is(runErr, ErrFailOnIncrease) || errors.Is(runErr, ErrDeniedLicenses) || errors.Is(runErr, ErrUncertaintyThresholdExceeded) {
		outcome.Breach = true
	}

	return outcome
}
