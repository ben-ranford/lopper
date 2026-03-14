package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ben-ranford/lopper/internal/analysis"
	"github.com/ben-ranford/lopper/internal/notify"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/runtime"
	"github.com/ben-ranford/lopper/internal/workspace"
)

type preparedAnalyseExecution struct {
	request                 analysis.Request
	lockfileWarnings        []string
	runtimeWarnings         []string
	effectiveThresholds     report.EffectiveThresholds
	removalCandidateWeights report.RemovalCandidateWeights
	licensePolicy           report.LicensePolicy
	policySources           []string
}

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

func prepareAnalyseExecution(ctx context.Context, req Request) (preparedAnalyseExecution, error) {
	lockfileWarnings, err := evaluateLockfileDriftPolicy(ctx, req.RepoPath, req.Analyse.Thresholds.LockfileDriftPolicy)
	if err != nil {
		return preparedAnalyseExecution{}, err
	}
	if err := validateCodemodApplyPreconditions(ctx, req.RepoPath, req.Analyse); err != nil {
		return preparedAnalyseExecution{}, err
	}

	lowConfidence := req.Analyse.Thresholds.LowConfidenceWarningPercent
	minUsage := req.Analyse.Thresholds.MinUsagePercentForRecommendations
	weights := report.RemovalCandidateWeights{
		Usage:      req.Analyse.Thresholds.RemovalCandidateWeightUsage,
		Impact:     req.Analyse.Thresholds.RemovalCandidateWeightImpact,
		Confidence: req.Analyse.Thresholds.RemovalCandidateWeightConfidence,
	}
	runtimeWarnings, runtimeTracePath := prepareRuntimeTrace(ctx, req)
	effectiveThresholds := report.EffectiveThresholds{
		FailOnIncreasePercent:             req.Analyse.Thresholds.FailOnIncreasePercent,
		LowConfidenceWarningPercent:       req.Analyse.Thresholds.LowConfidenceWarningPercent,
		MinUsagePercentForRecommendations: req.Analyse.Thresholds.MinUsagePercentForRecommendations,
		MaxUncertainImportCount:           req.Analyse.Thresholds.MaxUncertainImportCount,
	}

	return preparedAnalyseExecution{
		request: analysis.Request{
			RepoPath:                          req.RepoPath,
			Dependency:                        req.Analyse.Dependency,
			TopN:                              req.Analyse.TopN,
			ScopeMode:                         req.Analyse.ScopeMode,
			SuggestOnly:                       req.Analyse.SuggestOnly || req.Analyse.ApplyCodemod,
			Language:                          req.Analyse.Language,
			ConfigPath:                        req.Analyse.ConfigPath,
			RuntimeProfile:                    req.Analyse.RuntimeProfile,
			RuntimeTracePath:                  runtimeTracePath,
			IncludePatterns:                   req.Analyse.IncludePatterns,
			ExcludePatterns:                   req.Analyse.ExcludePatterns,
			LowConfidenceWarningPercent:       &lowConfidence,
			MinUsagePercentForRecommendations: &minUsage,
			RemovalCandidateWeights:           &weights,
			LicenseDenyList:                   append([]string{}, req.Analyse.Thresholds.LicenseDenyList...),
			IncludeRegistryProvenance:         req.Analyse.Thresholds.LicenseIncludeRegistryProvenance,
			Cache: &analysis.CacheOptions{
				Enabled:  req.Analyse.CacheEnabled,
				Path:     req.Analyse.CachePath,
				ReadOnly: req.Analyse.CacheReadOnly,
			},
		},
		lockfileWarnings:        lockfileWarnings,
		runtimeWarnings:         runtimeWarnings,
		effectiveThresholds:     effectiveThresholds,
		removalCandidateWeights: weights,
		licensePolicy: report.LicensePolicy{
			Deny:                      report.SortedDenyList(req.Analyse.Thresholds.LicenseDenyList),
			FailOnDenied:              req.Analyse.Thresholds.LicenseFailOnDeny,
			IncludeRegistryProvenance: req.Analyse.Thresholds.LicenseIncludeRegistryProvenance,
		},
		policySources: append([]string{}, req.Analyse.PolicySources...),
	}, nil
}

func (a *App) invokeAnalyse(ctx context.Context, prepared preparedAnalyseExecution) (report.Report, error) {
	return a.Analyzer.Analyse(ctx, prepared.request)
}

func decorateAnalyseReport(reportData *report.Report, prepared preparedAnalyseExecution) {
	if reportData == nil {
		return
	}

	effectiveThresholds := prepared.effectiveThresholds
	licensePolicy := prepared.licensePolicy
	licensePolicy.Deny = append([]string{}, prepared.licensePolicy.Deny...)
	reportData.EffectiveThresholds = &effectiveThresholds
	reportData.EffectivePolicy = &report.EffectivePolicy{
		Sources:                 append([]string{}, prepared.policySources...),
		Thresholds:              effectiveThresholds,
		RemovalCandidateWeights: prepared.removalCandidateWeights,
		License:                 licensePolicy,
	}
	reportData.Warnings = append(reportData.Warnings, prepared.runtimeWarnings...)
	reportData.Warnings = append(reportData.Warnings, prepared.lockfileWarnings...)
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

func prepareRuntimeTrace(ctx context.Context, req Request) ([]string, string) {
	runtimeTracePath := strings.TrimSpace(req.Analyse.RuntimeTracePath)
	runtimeCommand := strings.TrimSpace(req.Analyse.RuntimeTestCommand)
	if runtimeCommand == "" {
		return nil, runtimeTracePath
	}

	warnings := make([]string, 0, 1)
	repoPath, normalizeErr := workspace.NormalizeRepoPath(req.RepoPath)
	if normalizeErr != nil {
		repoPath = strings.TrimSpace(req.RepoPath)
		if repoPath == "" {
			repoPath = req.RepoPath
		}
		warnings = append(warnings, "runtime trace setup: using raw repo path due to normalization error: "+normalizeErr.Error())
	}
	if runtimeTracePath == "" {
		runtimeTracePath = runtime.DefaultTracePath(repoPath)
	}
	if err := runtime.Capture(ctx, runtime.CaptureRequest{
		RepoPath:  repoPath,
		TracePath: runtimeTracePath,
		Command:   runtimeCommand,
	}); err != nil {
		if strings.TrimSpace(req.Analyse.RuntimeTracePath) == "" {
			return append(warnings, "runtime trace command failed; continuing with static analysis: "+err.Error()), ""
		}
		return append(warnings, "runtime trace command failed; continuing with static analysis: "+err.Error()), runtimeTracePath
	}

	return warnings, runtimeTracePath
}

func (a *App) applyBaselineIfNeeded(reportData report.Report, repoPath string, req AnalyseRequest) (report.Report, error) {
	baselinePath, baselineKey, currentKey, shouldApply, err := resolveBaselineComparisonPaths(repoPath, req)
	if err != nil {
		return reportData, err
	}
	if !shouldApply {
		return reportData, nil
	}

	baseline, loadedKey, err := report.LoadWithKey(baselinePath)
	if err != nil {
		return reportData, err
	}
	if strings.TrimSpace(baselineKey) == "" {
		baselineKey = loadedKey
	}
	reportData, err = report.ApplyBaselineWithKeys(reportData, baseline, baselineKey, currentKey)
	if err != nil {
		return reportData, err
	}

	return reportData, nil
}

func resolveBaselineComparisonPaths(repoPath string, req AnalyseRequest) (string, string, string, bool, error) {
	if strings.TrimSpace(req.BaselinePath) != "" {
		return strings.TrimSpace(req.BaselinePath), "", resolveCurrentBaselineKey(repoPath), true, nil
	}

	storePath := strings.TrimSpace(req.BaselineStorePath)
	if storePath == "" {
		return "", "", "", false, nil
	}

	baselineKey := strings.TrimSpace(req.BaselineKey)
	if baselineKey == "" {
		baselineKey = resolveCurrentBaselineKey(repoPath)
	}
	if baselineKey == "" {
		return "", "", "", false, fmt.Errorf("baseline key is required when using --baseline-store")
	}

	baselinePath := report.BaselineSnapshotPath(storePath, baselineKey)
	currentKey := resolveCurrentBaselineKey(repoPath)
	return baselinePath, baselineKey, currentKey, true, nil
}

func (a *App) saveBaselineIfNeeded(reportData report.Report, repoPath string, req AnalyseRequest, now time.Time) (report.Report, error) {
	if !req.SaveBaseline {
		return reportData, nil
	}

	storePath := strings.TrimSpace(req.BaselineStorePath)
	if storePath == "" {
		return reportData, fmt.Errorf("--save-baseline requires --baseline-store")
	}
	saveKey, err := resolveSaveBaselineKey(repoPath, req)
	if err != nil {
		return reportData, err
	}
	savedPath, err := report.SaveSnapshot(storePath, saveKey, reportData, now)
	if err != nil {
		return reportData, err
	}
	reportData.Warnings = append(reportData.Warnings, "saved immutable baseline snapshot: "+savedPath)

	return reportData, nil
}

func resolveSaveBaselineKey(repoPath string, req AnalyseRequest) (string, error) {
	if label := strings.TrimSpace(req.BaselineLabel); label != "" {
		return "label:" + label, nil
	}
	if key := strings.TrimSpace(req.BaselineKey); key != "" {
		return key, nil
	}

	key := resolveCurrentBaselineKey(repoPath)
	if key == "" {
		return "", fmt.Errorf("unable to resolve git commit for baseline key; pass --baseline-label or --baseline-key")
	}

	return key, nil
}

func resolveCurrentBaselineKey(repoPath string) string {
	sha, err := workspace.CurrentCommitSHA(repoPath)
	if err != nil || strings.TrimSpace(sha) == "" {
		return ""
	}

	return "commit:" + sha
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

func validateUncertaintyThreshold(reportData report.Report, threshold int) error {
	if threshold <= 0 {
		return nil
	}

	uncertainImports := 0
	if reportData.UsageUncertainty != nil {
		uncertainImports = reportData.UsageUncertainty.UncertainImportUses
	}
	if uncertainImports > threshold {
		return ErrUncertaintyThresholdExceeded
	}

	return nil
}

func validateDeniedLicenses(reportData report.Report, failOnDeny bool) error {
	if !failOnDeny {
		return nil
	}
	if reportData.BaselineComparison != nil {
		if len(reportData.BaselineComparison.NewDeniedLicenses) > 0 {
			return ErrDeniedLicenses
		}
		return nil
	}
	if report.CountDeniedLicenses(reportData.Dependencies) > 0 {
		return ErrDeniedLicenses
	}

	return nil
}

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
