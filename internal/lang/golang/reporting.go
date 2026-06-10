package golang

import (
	"fmt"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
)

func buildRequestedGoDependencies(req language.Request, scan scanResult) ([]report.DependencyReport, []string) {
	return shared.BuildRequestedDependenciesWithWeights(req, scan, normalizeDependencyID, buildDependencyReport, buildTopGoDependencies)
}

func buildTopGoDependencies(topN int, scan scanResult, weights report.RemovalCandidateWeights) ([]report.DependencyReport, []string) {
	importRecords := func(file fileScan) []shared.ImportRecord { return file.Imports }
	usageRecords := func(file fileScan) map[string]int { return file.Usage }
	fileUsages := shared.MapFileUsages(scan.Files, importRecords, usageRecords)
	dependencies := shared.ListDependencies(fileUsages, normalizeDependencyID)
	return buildTopGoReports(topN, dependencies, scan, weights)
}

func buildTopGoReports(topN int, dependencies []string, scan scanResult, weights report.RemovalCandidateWeights) ([]report.DependencyReport, []string) {
	builder := func(dependency string) (report.DependencyReport, []string) {
		return buildDependencyReport(dependency, scan)
	}
	return shared.BuildTopReports(topN, dependencies, builder, weights)
}

func buildDependencyReport(dependency string, scan scanResult) (report.DependencyReport, []string) {
	stats := shared.BuildDependencyStats(dependency, goFileUsages(scan), normalizeDependencyID)
	dep := report.DependencyReport{Language: "go", Name: dependency}
	dep.UsedExportsCount = stats.UsedCount
	dep.TotalExportsCount = stats.TotalCount
	dep.UsedPercent = stats.UsedPercent
	dep.EstimatedUnusedBytes = 0
	dep.TopUsedSymbols = stats.TopSymbols
	dep.UsedImports = stats.UsedImports
	dep.UnusedImports = stats.UnusedImports
	dep.Provenance = buildGoDependencyProvenance(scan.DependencyProvenanceByDep[dependency])

	warnings := dependencyWarnings(dependency, stats.HasImports)
	if stats.WildcardImports > 0 {
		dep.RiskCues = append(dep.RiskCues, report.RiskCue{
			Code:     "dot-import",
			Severity: "medium",
			Message:  "dot imports were detected; they can obscure symbol provenance",
		})
	}
	if scan.BlankImportsByDependency[dependency] > 0 {
		dep.RiskCues = append(dep.RiskCues, report.RiskCue{
			Code:     "side-effect-import",
			Severity: "medium",
			Message:  "blank imports were detected; init side effects can hide coupling and startup overhead",
		})
	}
	if scan.UndeclaredImportsByDependency[dependency] > 0 {
		dep.RiskCues = append(dep.RiskCues, report.RiskCue{
			Code:     "undeclared-module-path",
			Severity: "low",
			Message:  "imports resolved to this module but it is not explicitly declared in go.mod",
		})
	}
	dep.Recommendations = buildRecommendations(dep, scan.UndeclaredImportsByDependency[dependency] > 0)
	return dep, warnings
}

func buildGoDependencyProvenance(info goDependencyProvenance) *report.DependencyProvenance {
	if !info.Declared && !info.Replacement && !info.Vendored {
		return nil
	}
	signals := make([]string, 0, 3)
	source := "go.mod"
	confidence := "medium"
	if info.Declared {
		signals = append(signals, goModName)
		confidence = "high"
	}
	if info.Replacement {
		signals = append(signals, "replace")
		source = "go.mod-replace"
		confidence = "high"
	}
	if info.Vendored {
		signals = append(signals, vendorModulesTxtName)
		if info.Declared || info.Replacement {
			source = "go.mod+vendor"
			confidence = "high"
		} else {
			source = "vendor/modules.txt"
			confidence = "medium"
		}
	}
	return &report.DependencyProvenance{
		Source:     source,
		Confidence: confidence,
		Signals:    uniqueStrings(signals),
	}
}

func buildRecommendations(dep report.DependencyReport, hasUndeclaredImports bool) []report.Recommendation {
	recs := make([]report.Recommendation, 0, 3)
	recs = appendUnusedDependencyRecommendation(recs, dep)
	recs = appendDotImportRecommendation(recs, dep)
	if hasUndeclaredImports {
		recs = append(recs, report.Recommendation{
			Code:      "declare-go-module-requirement",
			Priority:  "medium",
			Message:   fmt.Sprintf("Imports for %q were detected without a matching go.mod requirement.", dep.Name),
			Rationale: "Explicit requirements improve reproducibility and make dependency intent clear.",
		})
	}
	return recs
}

func dependencyWarnings(dependency string, hasImports bool) []string {
	if hasImports {
		return nil
	}
	return []string{fmt.Sprintf("no imports found for dependency %q", dependency)}
}

func appendUnusedDependencyRecommendation(recs []report.Recommendation, dep report.DependencyReport) []report.Recommendation {
	if len(dep.UsedImports) != 0 || len(dep.UnusedImports) == 0 {
		return recs
	}
	return append(recs, report.Recommendation{
		Code:      "remove-unused-dependency",
		Priority:  "high",
		Message:   fmt.Sprintf("No used imports were detected for %q; consider removing it.", dep.Name),
		Rationale: "Unused dependencies increase attack and maintenance surface.",
	})
}

func appendDotImportRecommendation(recs []report.Recommendation, dep report.DependencyReport) []report.Recommendation {
	if !shared.HasWildcardImport(dep.UsedImports) && !shared.HasWildcardImport(dep.UnusedImports) {
		return recs
	}
	return append(recs, report.Recommendation{
		Code:      "avoid-dot-imports",
		Priority:  "medium",
		Message:   "Dot imports were detected; prefer package-qualified usage for clarity.",
		Rationale: "Qualified imports preserve namespace clarity and improve static analysis precision.",
	})
}

func goFileUsages(scan scanResult) []shared.FileUsage {
	return shared.MapFileUsages(scan.Files, func(file fileScan) []shared.ImportRecord { return file.Imports }, func(file fileScan) map[string]int { return file.Usage })
}
