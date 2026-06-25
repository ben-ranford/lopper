package app

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ben-ranford/lopper/internal/advisory"
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
	if err != nil && isBootstrapableMissingBaseline(req, err) {
		return reportData, nil
	}
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

func isBootstrapableMissingBaseline(req AnalyseRequest, err error) bool {
	if !req.SaveBaseline {
		return false
	}
	if strings.TrimSpace(req.BaselinePath) != "" {
		return false
	}
	if strings.TrimSpace(req.BaselineStorePath) == "" {
		return false
	}
	if !errors.Is(err, os.ErrNotExist) {
		return false
	}
	return true
}

func resolveBaselineComparisonPaths(repoPath string, req AnalyseRequest) (string, string, string, bool, error) {
	if strings.TrimSpace(req.BaselinePath) != "" {
		return strings.TrimSpace(req.BaselinePath), "", resolveCurrentBaselineKey(repoPath), true, nil
	}

	return resolveBaselineStoreComparisonPaths(repoPath, baselineKeyRequestFromAnalyse(req), report.BaselineSnapshotPath)
}

func (a *App) saveBaselineIfNeeded(reportData report.Report, repoPath string, req AnalyseRequest, now time.Time) (report.Report, error) {
	return saveImmutableBaselineSnapshot(reportData, immutableBaselineSaveConfig[report.Report]{
		enabled:       req.SaveBaseline,
		repoPath:      repoPath,
		req:           baselineKeyRequestFromAnalyse(req),
		keyName:       "baseline",
		now:           now,
		save:          report.SaveSnapshot,
		appendWarning: appendBaselineSaveWarning,
	})
}

func resolveSaveBaselineKey(repoPath string, req AnalyseRequest) (string, error) {
	return resolveBaselineSaveKey(repoPath, baselineKeyRequestFromAnalyse(req), "baseline")
}

type baselineKeyRequest struct {
	storePath string
	key       string
	label     string
}

func baselineKeyRequestFromAnalyse(req AnalyseRequest) baselineKeyRequest {
	return baselineKeyRequest{
		storePath: req.BaselineStorePath,
		key:       req.BaselineKey,
		label:     req.BaselineLabel,
	}
}

func baselineKeyRequestFromDashboard(resolved resolvedDashboardRequest) baselineKeyRequest {
	return baselineKeyRequest{
		storePath: resolved.baselineStorePath,
		key:       resolved.baselineKey,
		label:     resolved.baselineLabel,
	}
}

func resolveBaselineStoreComparisonPaths(repoPath string, req baselineKeyRequest, snapshotPath func(string, string) string) (string, string, string, bool, error) {
	storePath := strings.TrimSpace(req.storePath)
	if storePath == "" {
		return "", "", "", false, nil
	}

	baselineKey := strings.TrimSpace(req.key)
	if baselineKey == "" {
		baselineKey = resolveCurrentBaselineKey(repoPath)
	}
	if baselineKey == "" {
		return "", "", "", false, fmt.Errorf("baseline key is required when using --baseline-store")
	}

	return snapshotPath(storePath, baselineKey), baselineKey, resolveCurrentBaselineKey(repoPath), true, nil
}

func resolveBaselineSaveTarget(repoPath string, req baselineKeyRequest, keyName string) (string, string, error) {
	storePath := strings.TrimSpace(req.storePath)
	if storePath == "" {
		return "", "", fmt.Errorf("--save-baseline requires --baseline-store")
	}
	saveKey, err := resolveBaselineSaveKey(repoPath, req, keyName)
	if err != nil {
		return "", "", err
	}
	return storePath, saveKey, nil
}

func resolveBaselineSaveKey(repoPath string, req baselineKeyRequest, keyName string) (string, error) {
	if label := strings.TrimSpace(req.label); label != "" {
		return "label:" + label, nil
	}
	if key := strings.TrimSpace(req.key); key != "" {
		return key, nil
	}

	key := resolveCurrentBaselineKey(repoPath)
	if key == "" {
		return "", fmt.Errorf("unable to resolve git commit for %s key; pass --baseline-label or --baseline-key", keyName)
	}
	return key, nil
}

type immutableBaselineSaveConfig[T any] struct {
	enabled       bool
	repoPath      string
	req           baselineKeyRequest
	keyName       string
	now           time.Time
	save          func(string, string, T, time.Time) (string, error)
	appendWarning func(T, string) T
}

func saveImmutableBaselineSnapshot[T any](reportData T, cfg immutableBaselineSaveConfig[T]) (T, error) {
	if !cfg.enabled {
		return reportData, nil
	}

	storePath, saveKey, err := resolveBaselineSaveTarget(cfg.repoPath, cfg.req, cfg.keyName)
	if err != nil {
		return reportData, err
	}
	savedPath, err := cfg.save(storePath, saveKey, reportData, cfg.now)
	if err != nil {
		return reportData, err
	}
	return cfg.appendWarning(reportData, savedPath), nil
}

func appendBaselineSaveWarning(reportData report.Report, savedPath string) report.Report {
	reportData.Warnings = append(reportData.Warnings, "saved immutable baseline snapshot: "+savedPath)
	return reportData
}

func applyAdvisoriesIfNeeded(reportData report.Report, req AnalyseRequest) (report.Report, error) {
	if strings.TrimSpace(req.AdvisorySourcePath) == "" {
		return reportData, nil
	}
	advisories, err := advisory.Load(req.AdvisorySourcePath)
	if err != nil {
		return reportData, err
	}
	report.AnnotateVulnerabilities(&reportData, advisories)
	reportData.Summary = report.ComputeSummary(reportData.Dependencies)
	return reportData, nil
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

func validateReachableVulnerabilityThreshold(reportData report.Report, threshold string) error {
	if !report.ValidVulnerabilityPriorityThreshold(threshold) {
		return fmt.Errorf("invalid reachable vulnerability priority threshold: %s", threshold)
	}
	if !hasReachableVulnerabilityAtOrAbove(reportData, threshold) {
		return nil
	}
	return ErrReachableVulnerabilities
}

func hasReachableVulnerabilityAtOrAbove(reportData report.Report, threshold string) bool {
	if strings.TrimSpace(threshold) == "" || report.NormalizeVulnerabilityPriorityThreshold(threshold) == report.VulnerabilityPriorityOff {
		return false
	}
	if reportData.BaselineComparison != nil {
		for _, finding := range reportData.BaselineComparison.NewReachableVulnerabilities {
			if report.VulnerabilityPriorityMeetsThreshold(finding.Priority, threshold) {
				return true
			}
		}
		return false
	}
	for _, dep := range reportData.Dependencies {
		for _, finding := range dep.Vulnerabilities {
			if finding.Reachable && report.VulnerabilityPriorityMeetsThreshold(finding.Priority, threshold) {
				return true
			}
		}
	}
	return false
}
