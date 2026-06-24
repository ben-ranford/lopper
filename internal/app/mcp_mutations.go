package app

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/ben-ranford/lopper/internal/analysis"
	"github.com/ben-ranford/lopper/internal/dashboard"
	"github.com/ben-ranford/lopper/internal/mcp"
	"github.com/ben-ranford/lopper/internal/notify"
	"github.com/ben-ranford/lopper/internal/report"
)

const (
	baselineSaveWarningPrefix          = "saved immutable baseline snapshot: "
	dashboardBaselineSaveWarningPrefix = "saved immutable dashboard baseline snapshot: "
)

type appMCPMutationRunner struct {
	app *App
}

func (a *App) mcpMutationRunner() mcp.MutationRunner {
	if a == nil {
		return nil
	}
	a.ensureMCPMutationDefaults()
	return &appMCPMutationRunner{app: a}
}

func (a *App) ensureMCPMutationDefaults() {
	if a.Analyzer == nil {
		service := analysis.NewService()
		a.Analyzer = service
		if a.Languages == nil {
			a.Languages = service.Registry
		}
	}
	if a.Formatter == nil {
		a.Formatter = report.NewFormatter()
	}
	if a.Notify == nil {
		a.Notify = notify.NewDefaultDispatcher()
	}
}

func (r *appMCPMutationRunner) ApplyCodemod(ctx context.Context, req mcp.AnalysisMutationRequest) (report.Report, error) {
	appReq := newMCPAnalyseRequest(req)
	appReq.Analyse.ApplyCodemod = true
	appReq.Analyse.AllowDirty = req.AllowDirty
	return r.runAnalyseReport(ctx, appReq)
}

func (r *appMCPMutationRunner) SaveBaseline(ctx context.Context, req mcp.AnalysisMutationRequest) (report.Report, string, error) {
	appReq := newMCPAnalyseRequest(req)
	appReq.Analyse.SaveBaseline = true
	appReq.Analyse.BaselineStorePath = req.BaselineStorePath
	if strings.TrimSpace(req.BaselineLabel) == "" {
		appReq.Analyse.BaselineKey = req.BaselineKey
	}
	appReq.Analyse.BaselineLabel = req.BaselineLabel

	reportData, err := r.runAnalyseReport(ctx, appReq)
	return reportData, savedSnapshotPath(reportData.Warnings, baselineSaveWarningPrefix), err
}

func (r *appMCPMutationRunner) SaveDashboardBaseline(ctx context.Context, req mcp.DashboardMutationRequest) (dashboard.Report, string, error) {
	appReq := DefaultRequest()
	appReq.Mode = ModeDashboard
	appReq.RepoPath = req.RepoPath
	appReq.Dashboard.Repos = mapMCPDashboardRepos(req.Repos)
	appReq.Dashboard.ConfigPath = req.ConfigPath
	appReq.Dashboard.Format = string(dashboard.FormatJSON)
	appReq.Dashboard.TopN = req.TopN
	appReq.Dashboard.DefaultLanguage = req.DefaultLanguage
	appReq.Dashboard.BaselineStorePath = req.BaselineStorePath
	if strings.TrimSpace(req.BaselineLabel) == "" {
		appReq.Dashboard.BaselineKey = req.BaselineKey
	}
	appReq.Dashboard.BaselineLabel = req.BaselineLabel
	appReq.Dashboard.SaveBaseline = true
	appReq.Dashboard.Features = req.Features

	reportData, err := r.runDashboardReport(ctx, appReq)
	return reportData, savedSnapshotPath(reportData.SourceWarnings, dashboardBaselineSaveWarningPrefix), err
}

func newMCPAnalyseRequest(req mcp.AnalysisMutationRequest) Request {
	appReq := DefaultRequest()
	appReq.Mode = ModeAnalyse
	appReq.RepoPath = req.RepoPath
	appReq.Analyse.Dependency = req.Dependency
	appReq.Analyse.TopN = req.TopN
	appReq.Analyse.ScopeMode = req.ScopeMode
	appReq.Analyse.Format = report.FormatJSON
	appReq.Analyse.Language = req.Language
	appReq.Analyse.CacheEnabled = req.CacheEnabled
	appReq.Analyse.CachePath = req.CachePath
	appReq.Analyse.CacheReadOnly = req.CacheReadOnly
	appReq.Analyse.RuntimeProfile = req.RuntimeProfile
	appReq.Analyse.RuntimeTracePath = req.RuntimeTracePath
	appReq.Analyse.IncludePatterns = append([]string{}, req.IncludePatterns...)
	appReq.Analyse.ExcludePatterns = append([]string{}, req.ExcludePatterns...)
	appReq.Analyse.ConfigPath = req.ConfigPath
	appReq.Analyse.PolicySources = append([]string{}, req.PolicySources...)
	appReq.Analyse.PolicyTrace = append([]report.PolicyMergeTrace{}, req.PolicyTrace...)
	appReq.Analyse.Features = req.Features
	appReq.Analyse.Thresholds = req.Thresholds
	return appReq
}

func (r *appMCPMutationRunner) runAnalyseReport(ctx context.Context, req Request) (report.Report, error) {
	output, err := r.app.executeAnalyse(ctx, req)
	return decodeMCPCommandReport[report.Report](output, err)
}

func (r *appMCPMutationRunner) runDashboardReport(ctx context.Context, req Request) (dashboard.Report, error) {
	output, err := r.app.executeDashboard(ctx, req)
	return decodeMCPCommandReport[dashboard.Report](output, err)
}

func decodeMCPCommandReport[T any](output string, runErr error) (T, error) {
	var v T
	if output != "" {
		if decodeErr := json.Unmarshal([]byte(output), &v); decodeErr != nil && runErr == nil {
			var zero T
			return zero, decodeErr
		}
	}
	return v, runErr
}

func mapMCPDashboardRepos(repos []mcp.DashboardRepoInput) []DashboardRepo {
	mapped := make([]DashboardRepo, 0, len(repos))
	for _, repo := range repos {
		mapped = append(mapped, DashboardRepo{
			Name:     repo.Name,
			Path:     repo.Path,
			Language: repo.Language,
		})
	}
	return mapped
}

func savedSnapshotPath(warnings []string, prefix string) string {
	for _, warning := range warnings {
		if path := strings.TrimSpace(strings.TrimPrefix(warning, prefix)); path != warning {
			return path
		}
	}
	return ""
}
