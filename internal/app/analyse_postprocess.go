package app

import (
	"fmt"
	"strings"
	"time"

	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/workspace"
)

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
	if threshold < 0 {
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
	if threshold < 0 {
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
