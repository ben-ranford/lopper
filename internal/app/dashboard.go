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

	storePath := strings.TrimSpace(resolved.baselineStorePath)
	if storePath == "" {
		return reportData, fmt.Errorf("--save-baseline requires --baseline-store")
	}
	saveKey, err := resolveDashboardSaveBaselineKey(repoPath, resolved)
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
	storePath := strings.TrimSpace(resolved.baselineStorePath)
	if storePath == "" {
		return "", "", "", false, nil
	}

	baselineKey := strings.TrimSpace(resolved.baselineKey)
	if baselineKey == "" {
		baselineKey = resolveCurrentBaselineKey(repoPath)
	}
	if baselineKey == "" {
		return "", "", "", false, fmt.Errorf("baseline key is required when using --baseline-store")
	}

	return dashboard.BaselineSnapshotPath(storePath, baselineKey), baselineKey, resolveCurrentBaselineKey(repoPath), true, nil
}

func resolveDashboardSaveBaselineKey(repoPath string, resolved resolvedDashboardRequest) (string, error) {
	if label := strings.TrimSpace(resolved.baselineLabel); label != "" {
		return "label:" + label, nil
	}
	if key := strings.TrimSpace(resolved.baselineKey); key != "" {
		return key, nil
	}
	key := resolveCurrentBaselineKey(repoPath)
	if key == "" {
		return "", fmt.Errorf("unable to resolve git commit for dashboard baseline key; pass --baseline-label or --baseline-key")
	}
	return key, nil
}
