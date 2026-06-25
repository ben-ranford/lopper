package app

import (
	"context"
	"strings"

	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/ui"
)

type appTUIActionRunner struct {
	app *App
}

func (a *App) tuiActionRunner() ui.ActionRunner {
	if a == nil {
		return nil
	}
	a.ensureMCPMutationDefaults()
	return &appTUIActionRunner{app: a}
}

func (r *appTUIActionRunner) ApplyCodemod(ctx context.Context, req ui.CodemodApplyRequest) (report.Report, error) {
	appReq := newTUIAnalyseRequest(req.RepoPath, req.TopN, req.Language)
	appReq.Analyse.Dependency = strings.TrimSpace(req.Dependency)
	appReq.Analyse.ApplyCodemod = true
	appReq.Analyse.AllowDirty = req.AllowDirty
	return r.runAnalyseReport(ctx, appReq)
}

func (r *appTUIActionRunner) SaveBaseline(ctx context.Context, req ui.BaselineSaveRequest) (report.Report, string, error) {
	appReq := newTUIAnalyseRequest(req.RepoPath, req.TopN, req.Language)
	appReq.Analyse.SaveBaseline = true
	appReq.Analyse.BaselineStorePath = strings.TrimSpace(req.BaselineStorePath)
	if strings.TrimSpace(req.BaselineLabel) == "" {
		appReq.Analyse.BaselineKey = strings.TrimSpace(req.BaselineKey)
	}
	appReq.Analyse.BaselineLabel = strings.TrimSpace(req.BaselineLabel)

	reportData, err := r.runAnalyseReport(ctx, appReq)
	return reportData, savedSnapshotPath(reportData.Warnings, baselineSaveWarningPrefix), err
}

func newTUIAnalyseRequest(repoPath string, topN int, language string) Request {
	appReq := DefaultRequest()
	appReq.Mode = ModeAnalyse
	appReq.RepoPath = repoPath
	appReq.Analyse.TopN = topN
	appReq.Analyse.Format = report.FormatJSON
	if trimmedLanguage := strings.TrimSpace(language); trimmedLanguage != "" {
		appReq.Analyse.Language = trimmedLanguage
	}
	return appReq
}

func (r *appTUIActionRunner) runAnalyseReport(ctx context.Context, req Request) (report.Report, error) {
	output, err := r.app.executeAnalyse(ctx, req)
	return decodeMCPCommandReport[report.Report](output, err)
}
