package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

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

	var baseline report.Report
	var loadedKey string
	if strings.TrimSpace(baselineKey) != "" {
		baseline, loadedKey, _, err = report.LoadSnapshot(req.BaselineStorePath, baselineKey)
	} else {
		baseline, loadedKey, err = report.LoadWithKey(baselinePath)
	}
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

	return resolveBaselineStoreComparisonPaths(repoPath, baselineKeyRequestFromAnalyse(req), report.ResolveBaselineSnapshotPath)
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

func applyAdvisories(reportData report.Report, advisorySourceTrustRoot, advisorySourcePath string) (report.Report, error) {
	if strings.TrimSpace(advisorySourcePath) == "" {
		return reportData, nil
	}
	advisories, err := advisory.LoadWithinRoot(advisorySourceTrustRoot, advisorySourcePath)
	if err != nil {
		return reportData, err
	}
	report.AnnotateVulnerabilities(&reportData, advisories)
	reportData.Summary = report.ComputeSummary(reportData.Dependencies)
	return reportData, nil
}

func applyAdvisoriesIfNeeded(reportData report.Report, req AnalyseRequest) (report.Report, error) {
	return applyAdvisories(reportData, req.AdvisorySourceTrustRoot, req.AdvisorySourcePath)
}

func applyVulnerabilityExceptionsIfNeeded(reportData report.Report, req AnalyseRequest, now time.Time) (report.Report, error) {
	return applyVulnerabilityExceptionsToReport(reportData, req.VulnerabilityExceptions, now), nil
}

func applyVulnerabilityExceptionsToReport(reportData report.Report, exceptions []report.VulnerabilityException, now time.Time) report.Report {
	if len(exceptions) == 0 {
		return reportData
	}
	diagnostics := report.ApplyVulnerabilityExceptions(&reportData, exceptions, now)
	reportData.Summary = report.ComputeSummary(reportData.Dependencies)
	reportData.Warnings = appendVulnerabilityExceptionWarnings(reportData.Warnings, diagnostics)
	return reportData
}

func appendVulnerabilityExceptionWarnings(warnings, diagnostics []string) []string {
	for _, diagnostic := range diagnostics {
		warnings = append(warnings, "vulnerability exception: "+diagnostic)
	}
	return warnings
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
	if !reachableVulnerabilityThresholdEnabled(threshold) {
		return nil
	}
	if hasOversizedRubyGemspecCoverageGap(reportData) {
		return ErrReachableVulnerabilities
	}
	if !hasReachableVulnerabilityAtOrAbove(reportData, threshold) {
		return nil
	}
	return ErrReachableVulnerabilities
}

func hasReachableVulnerabilityAtOrAbove(reportData report.Report, threshold string) bool {
	if !reachableVulnerabilityThresholdEnabled(threshold) {
		return false
	}
	if reportData.BaselineComparison != nil {
		return baselineHasReachableVulnerabilityAtOrAbove(reportData.BaselineComparison.NewReachableVulnerabilities, threshold)
	}
	return dependencyHasReachableVulnerabilityAtOrAbove(reportData.Dependencies, threshold)
}

func reachableVulnerabilityThresholdEnabled(threshold string) bool {
	return strings.TrimSpace(threshold) != "" && report.NormalizeVulnerabilityPriorityThreshold(threshold) != report.VulnerabilityPriorityOff
}

func hasOversizedRubyGemspecCoverageGap(reportData report.Report) bool {
	if reportData.BaselineComparison != nil {
		return hasOversizedRubyGemspecCoverageGapList(reportData.BaselineComparison.NewCoverageGaps)
	}
	if hasOversizedRubyGemspecCoverageGapList(reportData.CoverageGaps) {
		return true
	}
	return hasOversizedRubyGemspecDeclarationWarning(reportData.Warnings)
}

func hasOversizedRubyGemspecCoverageGapList(gaps []report.CoverageGap) bool {
	for _, gap := range gaps {
		if strings.TrimSpace(gap.Code) != report.CoverageGapRubyOversizedGemspec {
			continue
		}
		path := strings.TrimSpace(gap.Path)
		if path == "" || strings.EqualFold(filepath.Ext(path), ".gemspec") {
			return true
		}
	}
	return false
}

func hasOversizedRubyGemspecDeclarationWarning(warnings []string) bool {
	for _, warning := range warnings {
		if path, found := oversizedRubyGemspecDeclarationWarningPath(warning); found && strings.EqualFold(filepath.Ext(path), ".gemspec") {
			return true
		}
	}
	return false
}

func oversizedRubyGemspecDeclarationWarningPath(warning string) (string, bool) {
	warning = strings.TrimSpace(warning)
	for {
		if path, found := oversizedRubyGemspecDeclarationWarningPathFromMessage(warning); found {
			return path, true
		}
		rest, found := cutPRReviewRevisionWarningPrefix(warning)
		if !found {
			return "", false
		}
		warning = rest
	}
}

func oversizedRubyGemspecDeclarationWarningPathFromMessage(warning string) (string, bool) {
	path, found := equalFoldCutPrefix(warning, "skipped ")
	if !found {
		return "", false
	}
	const delimiter = " because it exceeds "
	index := strings.LastIndex(path, delimiter)
	if index < 0 {
		return "", false
	}
	if !strings.HasSuffix(strings.TrimSpace(path[index+len(delimiter):]), " bytes") {
		return "", false
	}
	return strings.TrimSpace(path[:index]), true
}

func cutPRReviewRevisionWarningPrefix(warning string) (string, bool) {
	for _, prefix := range []string{"base ", "head "} {
		suffix, found := equalFoldCutPrefix(warning, prefix)
		if !found {
			continue
		}
		index := strings.Index(suffix, ": ")
		if index < 0 {
			return "", false
		}
		return strings.TrimSpace(suffix[index+2:]), true
	}
	return "", false
}

func equalFoldCutPrefix(value, prefix string) (string, bool) {
	if prefix == "" {
		return value, true
	}

	cut := 0
	for _, prefixChar := range prefix {
		if cut >= len(value) {
			return "", false
		}
		valueChar, size := utf8.DecodeRuneInString(value[cut:])
		if !strings.EqualFold(string(valueChar), string(prefixChar)) {
			return "", false
		}
		cut += size
	}
	return value[cut:], true
}

func baselineHasReachableVulnerabilityAtOrAbove(findings []report.VulnerabilityDelta, threshold string) bool {
	for _, finding := range findings {
		if vulnerabilityDeltaMeetsReachableThreshold(finding, threshold) {
			return true
		}
	}
	return false
}

func dependencyHasReachableVulnerabilityAtOrAbove(deps []report.DependencyReport, threshold string) bool {
	for _, dep := range deps {
		for _, finding := range dep.Vulnerabilities {
			if !finding.Reachable || report.FindingSuppressedByException(finding) {
				continue
			}
			if vulnerabilityFindingMeetsReachableThreshold(finding, threshold) {
				return true
			}
		}
	}
	return false
}

func vulnerabilityDeltaMeetsReachableThreshold(finding report.VulnerabilityDelta, threshold string) bool {
	return finding.VersionStatus == "unevaluable" || report.VulnerabilityPriorityMeetsThreshold(finding.Priority, threshold)
}

func vulnerabilityFindingMeetsReachableThreshold(finding report.VulnerabilityFinding, threshold string) bool {
	return finding.VersionStatus == "unevaluable" || report.VulnerabilityPriorityMeetsThreshold(finding.Priority, threshold)
}
