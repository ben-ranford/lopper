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

	executionPlan := planDashboardExecution(req.Dashboard, resolved.repos)
	analyses := a.executeDashboardAnalysisPlan(ctx, executionPlan)
	now := time.Now()
	reportData := dashboard.Aggregate(now, analyses)

	reportData, err = a.applyDashboardBaselineIfNeeded(reportData, req.RepoPath, resolved)
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
	return persistDashboardOutput(formatted, resolved.outputPath)
}

func (a *App) applyDashboardBaselineIfNeeded(reportData dashboard.Report, repoPath string, resolved resolvedDashboardRequest) (dashboard.Report, error) {
	baselinePath, baselineKey, currentKey, shouldApply, err := resolveDashboardBaselinePaths(repoPath, resolved)
	if err != nil {
		return reportData, err
	}
	if !shouldApply {
		return reportData, nil
	}

	baseline, loadedKey, err := dashboard.LoadWithKey(baselinePath)
	if err != nil {
		if resolved.saveBaseline && errors.Is(err, os.ErrNotExist) {
			return reportData, nil
		}
		return reportData, err
	}
	if strings.TrimSpace(baselineKey) == "" {
		baselineKey = loadedKey
	}
	return dashboard.ApplyBaselineWithKeys(reportData, baseline, baselineKey, currentKey)
}

func (a *App) saveDashboardBaselineIfNeeded(reportData dashboard.Report, repoPath string, resolved resolvedDashboardRequest, now time.Time) (dashboard.Report, error) {
	if !resolved.saveBaseline {
		return reportData, nil
	}

	storePath, saveKey, err := resolveBaselineSaveTarget(repoPath, baselineKeyRequestFromDashboard(resolved), "dashboard baseline")
	if err != nil {
		return reportData, err
	}
	savedPath, err := dashboard.SaveSnapshot(storePath, saveKey, reportData, now)
	if err != nil {
		return reportData, err
	}
	reportData.SourceWarnings = append(reportData.SourceWarnings, "saved immutable dashboard baseline snapshot: "+savedPath)
	return reportData, nil
}

func resolveDashboardBaselinePaths(repoPath string, resolved resolvedDashboardRequest) (string, string, string, bool, error) {
	return resolveBaselineStoreComparisonPaths(repoPath, baselineKeyRequestFromDashboard(resolved), dashboard.BaselineSnapshotPath)
}
