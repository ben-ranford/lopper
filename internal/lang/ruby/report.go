package ruby

import (
	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
)

func buildRequestedRubyDependencies(req language.Request, scan scanResult) ([]report.DependencyReport, []string) {
	return shared.BuildRequestedDependenciesWithWeights(req, scan, normalizeDependencyID, buildDependencyReport, resolveRemovalCandidateWeights, buildTopRubyDependencies)
}

func buildTopRubyDependencies(topN int, scan scanResult, weights report.RemovalCandidateWeights) ([]report.DependencyReport, []string) {
	dependencies := sortedDependencyUnion(scan.DeclaredDependencies, scan.ImportedDependencies)
	buildReport := func(dependency string) (report.DependencyReport, []string) {
		return buildDependencyReport(dependency, scan)
	}
	return shared.BuildTopReports(topN, dependencies, buildReport, weights)
}

func buildDependencyReport(dependency string, scan scanResult) (report.DependencyReport, []string) {
	stats := collectRubyDependencyStats(dependency, scan.Files)
	return shapeRubyDependencyReport(dependency, stats, scan.DeclaredSources[dependency])
}

func collectRubyDependencyStats(dependency string, files []fileScan) shared.DependencyStats {
	importsOf := func(file fileScan) []shared.ImportRecord {
		return file.Imports
	}
	usageOf := func(file fileScan) map[string]int {
		return file.Usage
	}
	fileUsages := shared.MapFileUsages(files, importsOf, usageOf)
	return shared.BuildDependencyStats(dependency, fileUsages, normalizeDependencyID)
}

func resolveRemovalCandidateWeights(value *report.RemovalCandidateWeights) report.RemovalCandidateWeights {
	if value == nil {
		return report.DefaultRemovalCandidateWeights()
	}
	return report.NormalizeRemovalCandidateWeights(*value)
}

func sortedDependencyUnion(values ...map[string]struct{}) []string {
	set := make(map[string]struct{})
	for _, value := range values {
		for dependency := range value {
			set[dependency] = struct{}{}
		}
	}
	return shared.SortedKeys(set)
}
