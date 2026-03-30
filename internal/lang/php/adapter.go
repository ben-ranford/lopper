package php

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/thresholds"
	"github.com/ben-ranford/lopper/internal/workspace"
)

type Adapter struct {
	language.AdapterLifecycle
}

const (
	composerJSONName = "composer.json"
	composerLockName = "composer.lock"
	maxDetectFiles   = 1024
	maxScanFiles     = 2048
)

func NewAdapter() *Adapter {
	adapter := &Adapter{}
	adapter.AdapterLifecycle = language.NewAdapterLifecycle("php", []string{"php8", "php7"}, adapter.DetectWithConfidence)
	return adapter
}

func (a *Adapter) DetectWithConfidence(ctx context.Context, repoPath string) (language.Detection, error) {
	repoPath = shared.DefaultRepoPath(repoPath)
	detection := language.Detection{}
	roots := make(map[string]struct{})

	if err := applyPHPRootSignals(repoPath, &detection, roots); err != nil {
		return language.Detection{}, err
	}

	visited := 0
	err := filepath.WalkDir(repoPath, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if ctx != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		return walkPHPDetectionEntry(path, entry, roots, &detection, &visited, maxDetectFiles)
	})
	if err != nil && !errors.Is(err, fs.SkipAll) {
		return language.Detection{}, err
	}

	return shared.FinalizeDetection(repoPath, detection, roots), nil
}

func applyPHPRootSignals(repoPath string, detection *language.Detection, roots map[string]struct{}) error {
	signals := []struct {
		name       string
		confidence int
	}{
		{name: composerJSONName, confidence: 60},
		{name: composerLockName, confidence: 30},
	}
	for _, signal := range signals {
		candidate := filepath.Join(repoPath, signal.name)
		if _, err := os.Stat(candidate); err == nil {
			detection.Matched = true
			detection.Confidence += signal.confidence
			roots[repoPath] = struct{}{}
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func walkPHPDetectionEntry(path string, entry fs.DirEntry, roots map[string]struct{}, detection *language.Detection, visited *int, maxFiles int) error {
	if entry.IsDir() {
		if shouldSkipDir(entry.Name()) {
			return filepath.SkipDir
		}
		return nil
	}

	*visited++
	if *visited > maxFiles {
		return fs.SkipAll
	}

	switch strings.ToLower(entry.Name()) {
	case composerJSONName, composerLockName:
		detection.Matched = true
		detection.Confidence += 12
		roots[filepath.Dir(path)] = struct{}{}
	}

	if strings.EqualFold(filepath.Ext(path), ".php") {
		detection.Matched = true
		detection.Confidence += 2
	}
	return nil
}

func (a *Adapter) Analyse(ctx context.Context, req language.Request) (report.Report, error) {
	repoPath, err := workspace.NormalizeRepoPath(req.RepoPath)
	if err != nil {
		return report.Report{}, err
	}

	result := report.Report{
		GeneratedAt: a.Clock(),
		RepoPath:    repoPath,
	}

	composerData, composerWarnings, err := loadComposerData(repoPath)
	if err != nil {
		return report.Report{}, err
	}
	result.Warnings = append(result.Warnings, composerWarnings...)

	scan, err := scanRepo(ctx, repoPath, composerData)
	if err != nil {
		return report.Report{}, err
	}
	result.Warnings = append(result.Warnings, scan.Warnings...)

	dependencies, warnings := buildRequestedPHPDependencies(req, scan)
	result.Dependencies = dependencies
	result.Warnings = append(result.Warnings, warnings...)
	result.Summary = report.ComputeSummary(result.Dependencies)
	return result, nil
}

func buildRequestedPHPDependencies(req language.Request, scan scanResult) ([]report.DependencyReport, []string) {
	switch {
	case req.Dependency != "":
		dependency := normalizeDependencyID(req.Dependency)
		depReport, warnings := buildDependencyReport(dependency, scan, resolveMinUsageRecommendationThreshold(req.MinUsagePercentForRecommendations))
		return []report.DependencyReport{depReport}, warnings
	case req.TopN > 0:
		return buildTopPHPDependencies(req.TopN, scan, resolveMinUsageRecommendationThreshold(req.MinUsagePercentForRecommendations), resolveRemovalCandidateWeights(req.RemovalCandidateWeights))
	default:
		return nil, []string{"no dependency or top-N target provided"}
	}
}

func resolveMinUsageRecommendationThreshold(threshold *int) int {
	if threshold != nil {
		return *threshold
	}
	return thresholds.Defaults().MinUsagePercentForRecommendations
}

func buildTopPHPDependencies(topN int, scan scanResult, minUsagePercent int, weights report.RemovalCandidateWeights) ([]report.DependencyReport, []string) {
	dependencies := allDependencies(scan)
	if len(dependencies) == 0 {
		return nil, []string{"no dependency data available for top-N ranking"}
	}

	reports := make([]report.DependencyReport, 0, len(dependencies))
	warnings := make([]string, 0)
	for _, dependency := range dependencies {
		depReport, depWarnings := buildDependencyReport(dependency, scan, minUsagePercent)
		reports = append(reports, depReport)
		warnings = append(warnings, depWarnings...)
	}
	shared.SortReportsByWaste(reports, weights)
	if topN > 0 && topN < len(reports) {
		reports = reports[:topN]
	}
	return reports, warnings
}

func allDependencies(scan scanResult) []string {
	set := make(map[string]struct{})
	for dep := range scan.DeclaredDependencies {
		set[dep] = struct{}{}
	}
	for _, dep := range shared.ListDependencies(phpFileUsages(scan), normalizeDependencyID) {
		set[dep] = struct{}{}
	}
	return shared.SortedKeys(set)
}

func buildDependencyReport(dependency string, scan scanResult, minUsagePercent int) (report.DependencyReport, []string) {
	stats := shared.BuildDependencyStats(dependency, phpFileUsages(scan), normalizeDependencyID)
	warnings := make([]string, 0)
	if !stats.HasImports {
		warnings = append(warnings, fmt.Sprintf("no imports found for dependency %q", dependency))
	}

	dep := report.DependencyReport{
		Language:             "php",
		Name:                 dependency,
		UsedExportsCount:     stats.UsedCount,
		TotalExportsCount:    stats.TotalCount,
		UsedPercent:          stats.UsedPercent,
		EstimatedUnusedBytes: 0,
		TopUsedSymbols:       stats.TopSymbols,
		UsedImports:          stats.UsedImports,
		UnusedImports:        stats.UnusedImports,
	}
	if grouped := scan.GroupedImportsByDependency[dependency]; grouped > 0 {
		dep.RiskCues = append(dep.RiskCues, report.RiskCue{
			Code:     "grouped-use-import",
			Severity: "medium",
			Message:  fmt.Sprintf("found %d grouped PHP use import(s) for this dependency", grouped),
		})
	}
	if dynamic := scan.DynamicUsageByDependency[dependency]; dynamic > 0 {
		dep.RiskCues = append(dep.RiskCues, report.RiskCue{
			Code:     "dynamic-loading",
			Severity: "high",
			Message:  fmt.Sprintf("found %d file(s) with dynamic/reflection usage that may hide dependency references", dynamic),
		})
	}
	dep.Recommendations = buildRecommendations(dep, minUsagePercent)
	return dep, warnings
}

func buildRecommendations(dep report.DependencyReport, minUsagePercent int) []report.Recommendation {
	recs := make([]report.Recommendation, 0, 3)
	if len(dep.UsedImports) == 0 && len(dep.UnusedImports) > 0 {
		recs = append(recs, report.Recommendation{
			Code:      "remove-unused-dependency",
			Priority:  "high",
			Message:   fmt.Sprintf("No used imports were detected for %q; consider removing it.", dep.Name),
			Rationale: "Unused dependencies increase risk and maintenance surface.",
		})
	}
	if hasRiskCue(dep.RiskCues, "grouped-use-import") {
		recs = append(recs, report.Recommendation{
			Code:      "prefer-explicit-imports",
			Priority:  "medium",
			Message:   "Grouped use imports were detected; prefer explicit imports for clearer attribution.",
			Rationale: "Explicit imports improve readability and reduce ambiguity in static analysis.",
		})
	}
	if hasRiskCue(dep.RiskCues, "dynamic-loading") {
		recs = append(recs, report.Recommendation{
			Code:      "review-dynamic-loading",
			Priority:  "high",
			Message:   "Dynamic loading/reflection patterns were detected; manually review runtime dependency usage.",
			Rationale: "Static analysis can under-report usage when class names are resolved dynamically.",
		})
	}
	if dep.TotalExportsCount > 0 && dep.UsedPercent < float64(minUsagePercent) {
		recs = append(recs, report.Recommendation{
			Code:      "low-usage-dependency",
			Priority:  "medium",
			Message:   fmt.Sprintf("Dependency %q has low observed usage (%.1f%%).", dep.Name, dep.UsedPercent),
			Rationale: "Low-usage dependencies are candidates for removal or replacement.",
		})
	}
	return recs
}

func resolveRemovalCandidateWeights(value *report.RemovalCandidateWeights) report.RemovalCandidateWeights {
	if value == nil {
		return report.DefaultRemovalCandidateWeights()
	}
	return report.NormalizeRemovalCandidateWeights(*value)
}

func hasRiskCue(cues []report.RiskCue, code string) bool {
	for _, cue := range cues {
		if cue.Code == code {
			return true
		}
	}
	return false
}

func normalizeDependencyID(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	return strings.ReplaceAll(value, "_", "-")
}

func normalizePackagePart(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	value = strings.ReplaceAll(value, "_", "-")
	value = strings.ReplaceAll(value, ".", "-")
	parts := make([]rune, 0, len(value)+4)
	for i, r := range value {
		if i > 0 && r >= 'A' && r <= 'Z' && parts[len(parts)-1] != '-' {
			parts = append(parts, '-')
		}
		parts = append(parts, r)
	}
	cleaned := strings.ToLower(string(parts))
	cleaned = strings.Trim(cleaned, "-")
	cleaned = regexp.MustCompile(`-+`).ReplaceAllString(cleaned, "-")
	return cleaned
}

func shouldSkipDir(name string) bool {
	switch name {
	case ".git", ".idea", "node_modules", "vendor", "dist", "build", ".next", ".turbo", "coverage", "tmp", "cache":
		return true
	default:
		return false
	}
}
