package app

import (
	"context"
	"fmt"
	"time"
)

func (a *App) executeDashboard(ctx context.Context, req Request) (string, error) {
	if a.Analyzer == nil {
		return "", fmt.Errorf("dashboard analyzer is not configured")
	}

	resolved, err := resolveDashboardRequest(req.Dashboard)
	if err != nil {
		return "", err
	}

	executionPlan := planDashboardExecution(req.Dashboard, resolved.repos)
	analyses := a.executeDashboardAnalysisPlan(ctx, executionPlan)

	materializationPlan := planDashboardMaterialization(resolved)
	return materializeDashboardReport(analyses, materializationPlan, time.Now())
}
