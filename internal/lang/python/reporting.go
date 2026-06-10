package python

import (
	"fmt"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
)

func buildRequestedPythonDependencies(req language.Request, scan scanResult) ([]report.DependencyReport, []string) {
	return shared.BuildRequestedDependenciesWithWeights(req, scan, normalizeDependencyID, buildDependencyReport, buildTopPythonDependencies)
}

func buildTopPythonDependencies(topN int, scan scanResult, weights report.RemovalCandidateWeights) ([]report.DependencyReport, []string) {
	dependencies := sortedDependencyUnion(scan.DeclaredDependencies, scan.ImportedDependencies)
	reportBuilder := func(dependency string) (report.DependencyReport, []string) {
		return buildDependencyReport(dependency, scan)
	}
	return shared.BuildTopReports(topN, dependencies, reportBuilder, weights)
}

func buildDependencyReport(dependency string, scan scanResult) (report.DependencyReport, []string) {
	stats := shared.BuildDependencyStats(dependency, pythonFileUsages(scan), normalizeDependencyID)
	warnings := make([]string, 0)
	if !stats.HasImports {
		warnings = append(warnings, fmt.Sprintf("no imports found for dependency %q", dependency))
	}

	dep := report.DependencyReport{
		Language:             "python",
		Name:                 dependency,
		UsedExportsCount:     stats.UsedCount,
		TotalExportsCount:    stats.TotalCount,
		UsedPercent:          stats.UsedPercent,
		EstimatedUnusedBytes: 0,
		TopUsedSymbols:       stats.TopSymbols,
		UsedImports:          stats.UsedImports,
		UnusedImports:        stats.UnusedImports,
	}
	if stats.WildcardImports > 0 {
		dep.RiskCues = append(dep.RiskCues, report.RiskCue{
			Code:     "wildcard-import",
			Severity: "medium",
			Message:  fmt.Sprintf("found %d wildcard import(s) for this dependency", stats.WildcardImports),
		})
	}
	dep.Recommendations = buildRecommendations(dep)
	return dep, warnings
}

func buildRecommendations(dep report.DependencyReport) []report.Recommendation {
	recs := make([]report.Recommendation, 0, 2)
	if len(dep.UsedImports) == 0 && len(dep.UnusedImports) > 0 {
		recs = append(recs, report.Recommendation{
			Code:      "remove-unused-dependency",
			Priority:  "high",
			Message:   fmt.Sprintf("No used imports were detected for %q; consider removing it.", dep.Name),
			Rationale: "Unused dependencies increase attack and maintenance surface.",
		})
	}
	if shared.HasWildcardImport(dep.UsedImports) || shared.HasWildcardImport(dep.UnusedImports) {
		recs = append(recs, report.Recommendation{
			Code:      "avoid-star-imports",
			Priority:  "medium",
			Message:   "Wildcard imports were detected; prefer explicit symbol imports.",
			Rationale: "Explicit imports improve readability and analysis precision.",
		})
	}
	return recs
}
