package php

import (
	"fmt"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/thresholds"
)

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
