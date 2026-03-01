package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/ben-ranford/lopper/internal/analysis"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/runtime"
	"github.com/ben-ranford/lopper/internal/ui"
	"github.com/ben-ranford/lopper/internal/workspace"
)

var (
	ErrUnknownMode                  = errors.New("unknown mode")
	ErrFailOnIncrease               = errors.New("dependency waste increased beyond threshold")
	ErrBaselineRequired             = errors.New("baseline report is required for fail-on-increase")
	ErrLockfileDrift                = errors.New("lockfile drift detected")
	ErrUncertaintyThresholdExceeded = errors.New("uncertain dynamic import/require usage exceeded threshold")
	ErrDeniedLicenses               = errors.New("denied licenses detected")
)

type App struct {
	Analyzer  analysis.Analyser
	Formatter *report.Formatter
	TUI       ui.TUI
}

func New(out io.Writer, in io.Reader) *App {
	analyzer := analysis.NewService()
	formatter := report.NewFormatter()

	return &App{
		Analyzer:  analyzer,
		Formatter: formatter,
		TUI:       ui.NewSummary(out, in, analyzer, formatter),
	}
}

func (a *App) Execute(ctx context.Context, req Request) (string, error) {
	switch req.Mode {
	case ModeTUI:
		return a.executeTUI(ctx, req)
	case ModeAnalyse:
		return a.executeAnalyse(ctx, req)
	default:
		return "", ErrUnknownMode
	}
}

func (a *App) executeTUI(ctx context.Context, req Request) (string, error) {
	opts := ui.Options{
		RepoPath: req.RepoPath,
		Language: req.TUI.Language,
		TopN:     req.TUI.TopN,
		Filter:   req.TUI.Filter,
		Sort:     req.TUI.Sort,
		PageSize: req.TUI.PageSize,
	}
	if req.TUI.SnapshotPath != "" {
		return "", a.TUI.Snapshot(ctx, opts, req.TUI.SnapshotPath)
	}
	return "", a.TUI.Start(ctx, opts)
}

func (a *App) executeAnalyse(ctx context.Context, req Request) (string, error) {
	lockfileWarnings, err := evaluateLockfileDriftPolicy(ctx, req.RepoPath, req.Analyse.Thresholds.LockfileDriftPolicy)
	if err != nil {
		return "", err
	}

	lowConfidence := req.Analyse.Thresholds.LowConfidenceWarningPercent
	minUsage := req.Analyse.Thresholds.MinUsagePercentForRecommendations
	weights := report.RemovalCandidateWeights{
		Usage:      req.Analyse.Thresholds.RemovalCandidateWeightUsage,
		Impact:     req.Analyse.Thresholds.RemovalCandidateWeightImpact,
		Confidence: req.Analyse.Thresholds.RemovalCandidateWeightConfidence,
	}
	runtimeWarnings, runtimeTracePath := prepareRuntimeTrace(ctx, req)

	reportData, err := a.Analyzer.Analyse(ctx, analysis.Request{
		RepoPath:                          req.RepoPath,
		Dependency:                        req.Analyse.Dependency,
		TopN:                              req.Analyse.TopN,
		ScopeMode:                         req.Analyse.ScopeMode,
		SuggestOnly:                       req.Analyse.SuggestOnly,
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
		LicenseFailOnDeny:                 req.Analyse.Thresholds.LicenseFailOnDeny,
		IncludeRegistryProvenance:         req.Analyse.Thresholds.LicenseIncludeRegistryProvenance,
		Cache: &analysis.CacheOptions{
			Enabled:  req.Analyse.CacheEnabled,
			Path:     req.Analyse.CachePath,
			ReadOnly: req.Analyse.CacheReadOnly,
		},
	})
	if err != nil {
		return "", err
	}
	reportData.EffectiveThresholds = &report.EffectiveThresholds{
		FailOnIncreasePercent:             req.Analyse.Thresholds.FailOnIncreasePercent,
		LowConfidenceWarningPercent:       req.Analyse.Thresholds.LowConfidenceWarningPercent,
		MinUsagePercentForRecommendations: req.Analyse.Thresholds.MinUsagePercentForRecommendations,
		MaxUncertainImportCount:           req.Analyse.Thresholds.MaxUncertainImportCount,
	}
	reportData.EffectivePolicy = &report.EffectivePolicy{
		Sources: req.Analyse.PolicySources,
		Thresholds: report.EffectiveThresholds{
			FailOnIncreasePercent:             req.Analyse.Thresholds.FailOnIncreasePercent,
			LowConfidenceWarningPercent:       req.Analyse.Thresholds.LowConfidenceWarningPercent,
			MinUsagePercentForRecommendations: req.Analyse.Thresholds.MinUsagePercentForRecommendations,
			MaxUncertainImportCount:           req.Analyse.Thresholds.MaxUncertainImportCount,
		},
		RemovalCandidateWeights: report.RemovalCandidateWeights{
			Usage:      req.Analyse.Thresholds.RemovalCandidateWeightUsage,
			Impact:     req.Analyse.Thresholds.RemovalCandidateWeightImpact,
			Confidence: req.Analyse.Thresholds.RemovalCandidateWeightConfidence,
		},
		License: report.LicensePolicy{
			Deny:                      report.SortedDenyList(req.Analyse.Thresholds.LicenseDenyList),
			FailOnDenied:              req.Analyse.Thresholds.LicenseFailOnDeny,
			IncludeRegistryProvenance: req.Analyse.Thresholds.LicenseIncludeRegistryProvenance,
		},
	}
	reportData.Warnings = append(reportData.Warnings, runtimeWarnings...)
	reportData.Warnings = append(reportData.Warnings, lockfileWarnings...)

	reportData, err = a.applyBaselineIfNeeded(reportData, req.RepoPath, req.Analyse)
	if err != nil {
		formatted, formatErr := a.Formatter.Format(reportData, req.Analyse.Format)
		if formatErr != nil {
			return "", err
		}
		return formatted, err
	}
	if err := validateFailOnIncrease(reportData, req.Analyse.Thresholds.FailOnIncreasePercent); err != nil {
		formatted, formatErr := a.Formatter.Format(reportData, req.Analyse.Format)
		if formatErr != nil {
			return "", err
		}
		return formatted, err
	}
	if err := validateUncertaintyThreshold(reportData, req.Analyse.Thresholds.MaxUncertainImportCount); err != nil {
		formatted, formatErr := a.Formatter.Format(reportData, req.Analyse.Format)
		if formatErr != nil {
			return "", err
		}
		return formatted, err
	}
	if err := validateDeniedLicenses(reportData, req.Analyse.Thresholds.LicenseFailOnDeny); err != nil {
		formatted, formatErr := a.Formatter.Format(reportData, req.Analyse.Format)
		if formatErr != nil {
			return "", err
		}
		return formatted, err
	}
	reportData, err = a.saveBaselineIfNeeded(reportData, req.RepoPath, req.Analyse, time.Now())
	if err != nil {
		formatted, formatErr := a.Formatter.Format(reportData, req.Analyse.Format)
		if formatErr != nil {
			return "", err
		}
		return formatted, err
	}
	formatted, err := a.Formatter.Format(reportData, req.Analyse.Format)
	if err != nil {
		return "", err
	}
	return formatted, nil
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
	if reportData.BaselineComparison != nil && len(reportData.BaselineComparison.NewDeniedLicenses) > 0 {
		return ErrDeniedLicenses
	}
	if report.CountDeniedLicenses(reportData.Dependencies) > 0 {
		return ErrDeniedLicenses
	}
	return nil
}
