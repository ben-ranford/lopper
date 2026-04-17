package elixir

import (
	"fmt"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
)

func buildRequestedDependencies(req language.Request, scan scanResult) ([]report.DependencyReport, []string) {
	buildReport := func(dep string) (report.DependencyReport, []string) {
		stats := shared.BuildDependencyStats(dep, scan.files, normalizeDependencyID)
		warnings := []string(nil)
		if !stats.HasImports {
			warnings = []string{fmt.Sprintf("no imports found for dependency %q", dep)}
		}
		return report.DependencyReport{
			Language:             "elixir",
			Name:                 dep,
			UsedExportsCount:     stats.UsedCount,
			TotalExportsCount:    stats.TotalCount,
			UsedPercent:          stats.UsedPercent,
			TopUsedSymbols:       stats.TopSymbols,
			UsedImports:          stats.UsedImports,
			UnusedImports:        stats.UnusedImports,
			EstimatedUnusedBytes: 0,
		}, warnings
	}
	topBuilder := func(topN int, _ scanResult, weights report.RemovalCandidateWeights) ([]report.DependencyReport, []string) {
		set := make(map[string]struct{})
		for dep := range scan.declared {
			set[dep] = struct{}{}
		}
		for _, dep := range shared.ListDependencies(scan.files, normalizeDependencyID) {
			set[dep] = struct{}{}
		}
		return shared.BuildTopReports(topN, shared.SortedKeys(set), buildReport, weights)
	}
	buildDependency := func(dep string, _ scanResult) (report.DependencyReport, []string) {
		return buildReport(dep)
	}
	return shared.BuildRequestedDependenciesWithWeights(req, scan, normalizeDependencyID, buildDependency, resolveWeights, topBuilder)
}

func resolveWeights(value *report.RemovalCandidateWeights) report.RemovalCandidateWeights {
	if value == nil {
		return report.DefaultRemovalCandidateWeights()
	}
	return report.NormalizeRemovalCandidateWeights(*value)
}
