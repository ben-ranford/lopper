package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ben-ranford/lopper/internal/dashboard"
)

func (a *App) executeDashboard(ctx context.Context, req Request) (string, error) {
	if a.Analyzer == nil {
		return "", fmt.Errorf("dashboard analyzer is not configured")
	}

	resolved, err := resolveDashboardRequest(req.Dashboard)
	if err != nil {
		return "", err
	}
	if err := validateDashboardFeatures(req.Dashboard, resolved); err != nil {
		return "", err
	}

	executionPlan := a.prepareDashboardExecutionPlan(ctx, req.Dashboard, resolved.repos)
	analyses := a.executeDashboardAnalysisPlan(ctx, executionPlan)
	now := time.Now()
	includeRemediationQueue := req.Dashboard.Features.Enabled(DashboardRemediationQueuePreviewFeature) ||
		req.Dashboard.Features.Enabled(DashboardRemediationRoutingSummariesPreviewFeature)
	var remediationRouter func(dashboard.RepoInput, []dashboard.RemediationItem) []dashboard.RemediationItem
	if req.Dashboard.Features.Enabled(DashboardRemediationRoutingSummariesPreviewFeature) {
		remediationRouter = dashboardRemediationRouter(resolved.routing)
	}
	reportData := dashboard.AggregateWithOptions(now, analyses, dashboard.AggregateOptions{
		IncludeRemediationQueue:    includeRemediationQueue,
		IncludePortfolioComponents: resolved.format == dashboard.FormatCycloneDXJSON,
		RemediationRouter:          remediationRouter,
	})

	reportData, err = a.applyDashboardBaselineIfNeeded(reportData, req.RepoPath, resolved, includeRemediationQueue)
	if err != nil {
		return "", err
	}
	reportData, err = a.saveDashboardBaselineIfNeeded(reportData, req.RepoPath, resolved, now)
	if err != nil {
		return "", err
	}

	formatted, err := dashboard.FormatReport(reportData, resolved.format)
	if err != nil {
		return "", err
	}
	return persistDashboardOutput(formatted, resolved.outputPath, dashboardOutputTrustedRoots(executionPlan)...)
}

func dashboardRemediationRouter(base dashboard.RoutingOptions) func(dashboard.RepoInput, []dashboard.RemediationItem) []dashboard.RemediationItem {
	return func(input dashboard.RepoInput, items []dashboard.RemediationItem) []dashboard.RemediationItem {
		routing := base
		if repoPath := strings.TrimSpace(input.Path); repoPath != "" {
			routing.Codeowners = dashboard.LoadCodeowners(repoPath)
		}
		return dashboard.ApplyRouting(items, routing)
	}
}

func validateDashboardFeatures(req DashboardRequest, resolved resolvedDashboardRequest) error {
	switch resolved.format {
	case dashboard.FormatSlackSummary, dashboard.FormatTeamsSummary:
		if req.Features.Enabled(DashboardRemediationRoutingSummariesPreviewFeature) {
			return nil
		}
		return fmt.Errorf("dashboard format %q requires --enable-feature %s", resolved.format, DashboardRemediationRoutingSummariesPreviewFeature)
	case dashboard.FormatCycloneDXJSON:
		if req.Features.Enabled(DashboardCycloneDXPortfolioPreviewFeature) {
			return nil
		}
		return fmt.Errorf("dashboard format %q requires --enable-feature %s", resolved.format, DashboardCycloneDXPortfolioPreviewFeature)
	default:
		return nil
	}
}

func (a *App) applyDashboardBaselineIfNeeded(reportData dashboard.Report, repoPath string, resolved resolvedDashboardRequest, includeRemediationQueue bool) (dashboard.Report, error) {
	_, baselineKey, currentKey, shouldApply, err := resolveDashboardBaselinePaths(repoPath, resolved)
	if err != nil {
		return reportData, err
	}
	if !shouldApply {
		return reportData, nil
	}

	baseline, loadedKey, _, err := dashboard.LoadSnapshot(resolved.baselineStorePath, baselineKey)
	if err != nil {
		if resolved.saveBaseline && errors.Is(err, os.ErrNotExist) {
			return reportData, nil
		}
		return reportData, err
	}
	if strings.TrimSpace(baselineKey) == "" {
		baselineKey = loadedKey
	}
	if !includeRemediationQueue {
		baseline.RemediationItems = nil
	}
	return dashboard.ApplyBaselineWithKeys(reportData, baseline, baselineKey, currentKey)
}

func (a *App) saveDashboardBaselineIfNeeded(reportData dashboard.Report, repoPath string, resolved resolvedDashboardRequest, now time.Time) (dashboard.Report, error) {
	return saveImmutableBaselineSnapshot(reportData, immutableBaselineSaveConfig[dashboard.Report]{
		enabled:       resolved.saveBaseline,
		repoPath:      repoPath,
		req:           baselineKeyRequestFromDashboard(resolved),
		keyName:       "dashboard baseline",
		now:           now,
		save:          dashboard.SaveSnapshot,
		appendWarning: appendDashboardBaselineSaveWarning,
	})
}

func resolveDashboardBaselinePaths(repoPath string, resolved resolvedDashboardRequest) (string, string, string, bool, error) {
	return resolveBaselineStoreComparisonPaths(repoPath, baselineKeyRequestFromDashboard(resolved), dashboard.ResolveBaselineSnapshotPath)
}

func appendDashboardBaselineSaveWarning(reportData dashboard.Report, savedPath string) dashboard.Report {
	reportData.SourceWarnings = append(reportData.SourceWarnings, "saved immutable dashboard baseline snapshot: "+savedPath)
	return reportData
}

func dashboardOutputTrustedRoots(plan dashboardExecutionPlan) []string {
	roots := make([]string, 0, len(plan.initialResults))
	seen := make(map[string]struct{}, len(plan.initialResults))
	for _, result := range plan.initialResults {
		path := strings.TrimSpace(result.Input.Path)
		if path == "" {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		roots = append(roots, path)
	}
	return roots
}
