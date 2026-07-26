package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ben-ranford/lopper/internal/analysis"
	"github.com/ben-ranford/lopper/internal/report"
)

type analyseReportStage func(context.Context, report.Report) (report.Report, error)

func (a *App) executeAnalyse(ctx context.Context, req Request) (_ string, returnErr error) {
	if err := validateAnalyseFeatures(req.Analyse); err != nil {
		return "", err
	}
	repository, err := analysis.ResolveAuthorizedRepository(req.RepoPath, req.Analyse.repository, req.Analyse.cacheOptions)
	if err != nil {
		return "", err
	}
	req.Analyse.repository = repository
	repositoryView, ownsRepositoryView, err := openAnalyseRepositoryView(ctx, req)
	if err != nil {
		return "", err
	}
	if repositoryView != nil {
		if ownsRepositoryView {
			defer func() {
				returnErr = errors.Join(returnErr, repositoryView.Close())
			}()
		}
		req.Analyse.repositoryView = repositoryView
		captureAnalyseGitSensitiveState(ctx, repositoryView, &req.Analyse)
		req.Analyse.advisoryLoadPath, err = repositoryView.SnapshotPath(req.Analyse.AdvisorySourcePath)
		if err != nil {
			return "", err
		}
	}

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

	return a.completeAnalyseExecution(ctx, req.RepoPath, req.Analyse, reportData, err)
}

func captureAnalyseGitSensitiveState(ctx context.Context, repositoryView *analysis.RepositoryView, req *AnalyseRequest) {
	if req == nil {
		return
	}
	metadata := repositoryView.GitMetadata()
	if !req.currentBaselineKeyCaptured {
		if currentCommit := strings.TrimSpace(metadata.CurrentCommit); currentCommit != "" {
			req.currentBaselineKey = "commit:" + currentCommit
		}
		req.currentBaselineKeyCaptured = true
	}
	if !req.codemodPreconditionCaptured {
		if req.ApplyCodemod && !req.AllowDirty {
			req.codemodPrecondition = ensureCleanRepositoryViewForCodemod(repositoryView, false)
		}
		req.codemodPreconditionCaptured = true
	}
	if !req.lockfileDriftCaptured {
		req.lockfileWarnings, req.lockfileDriftErr = evaluateLockfileDriftPolicyWithRepositoryView(ctx, repositoryView, req.Thresholds.LockfileDriftPolicy, req.Features)
		req.lockfileDriftCaptured = true
	}
}

func openAnalyseRepositoryView(ctx context.Context, req Request) (*analysis.RepositoryView, bool, error) {
	if req.Analyse.repositoryView != nil {
		if err := analysis.ValidateRepositoryView(req.Analyse.repository, req.Analyse.repositoryView); err != nil {
			return nil, false, err
		}
		return req.Analyse.repositoryView, false, nil
	}
	view, err := analysis.OpenTrustedRepositoryWithGitMetadata(ctx, req.Analyse.repository, req.RepoPath, req.Analyse.cacheOptions)
	if err != nil {
		return nil, false, err
	}
	return view, true, nil
}

func (a *App) invokeAnalyse(ctx context.Context, prepared preparedAnalyseExecution) (report.Report, error) {
	return a.Analyzer.Analyse(ctx, prepared.request)
}

func (a *App) runAnalysePostStages(ctx context.Context, repoPath string, req AnalyseRequest, reportData report.Report) (report.Report, error) {
	now := time.Now()

	return runAnalyseStages(ctx, reportData, []analyseReportStage{
		func(_ context.Context, reportData report.Report) (report.Report, error) {
			return applyAdvisoriesIfNeeded(reportData, req)
		},
		func(_ context.Context, reportData report.Report) (report.Report, error) {
			return applyVulnerabilityExceptionsIfNeeded(reportData, req, now)
		},
		func(_ context.Context, reportData report.Report) (report.Report, error) {
			return a.applyBaselineIfNeededWithRepository(reportData, repoPath, req, req.repositoryView)
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
		analyseValidationStage(func(reportData report.Report) error {
			return validateReachableVulnerabilityThreshold(reportData, req.Thresholds.ReachableVulnerabilityPriority)
		}),
		func(ctx context.Context, reportData report.Report) (report.Report, error) {
			return applyCodemodIfNeededWithRepository(ctx, reportData, repoPath, req, now, req.repositoryView)
		},
		func(_ context.Context, reportData report.Report) (report.Report, error) {
			return a.saveBaselineIfNeededWithRepository(reportData, repoPath, req, now, req.repositoryView)
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

func (a *App) completeAnalyseExecution(ctx context.Context, repoPath string, req AnalyseRequest, reportData report.Report, runErr error) (string, error) {
	a.appendNotificationWarnings(ctx, req.Notifications, &reportData, buildNotificationOutcome(reportData, runErr))
	if err := validateAnalyseFeatures(req); err != nil {
		if runErr != nil {
			return "", runErr
		}
		return "", err
	}
	formatted, err := a.Formatter.Format(reportData, req.Format)
	if err != nil {
		if runErr != nil {
			return "", runErr
		}
		return "", err
	}

	output, err := persistAnalyseOutput(formatted, req.OutputPath, repoPath, req.repositoryView)
	if err != nil {
		return "", err
	}

	return output, runErr
}

func persistAnalyseOutput(formatted, outputPath, repoPath string, repositoryView *analysis.RepositoryView) (string, error) {
	trimmedOutputPath := strings.TrimSpace(outputPath)
	if repositoryView == nil || trimmedOutputPath == "" || trimmedOutputPath == "-" || !filepath.IsAbs(trimmedOutputPath) {
		return persistCommandOutput(formatted, outputPath, "analyse report", repoPath)
	}
	relativePath, inRepository := repositoryView.RepositoryRelativePath(trimmedOutputPath)
	if !inRepository {
		return persistCommandOutput(formatted, outputPath, "analyse report", repoPath)
	}
	if hasDirectoryStyleOutputPath(trimmedOutputPath) {
		return "", fmt.Errorf("output path must name a file: %s", trimmedOutputPath)
	}
	if err := rejectRepositoryOutputSymlink(repositoryView, relativePath); err != nil {
		return "", err
	}
	if err := repositoryView.WriteFile(relativePath, []byte(formatted), 0o600); err != nil {
		return "", err
	}
	return "analyse report written to " + trimmedOutputPath, nil
}

func rejectRepositoryOutputSymlink(repositoryView *analysis.RepositoryView, relativePath string) error {
	parent := filepath.Dir(filepath.Clean(relativePath))
	if parent == "." {
		return nil
	}
	current := ""
	for _, component := range strings.Split(parent, string(filepath.Separator)) {
		current = filepath.Join(current, component)
		info, err := repositoryView.Lstat(current)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("output root contains symlink: %s", current)
		}
	}
	return nil
}
