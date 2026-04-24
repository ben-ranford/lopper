package powershell

import (
	"fmt"
	"sort"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
)

func buildRequestedPowerShellDependencies(req language.Request, scan scanResult) ([]report.DependencyReport, []string) {
	return shared.BuildRequestedDependenciesWithWeights(req, scan, normalizeDependencyID, buildDependencyReport, resolveRemovalCandidateWeights, buildTopPowerShellDependencies)
}

func buildTopPowerShellDependencies(topN int, scan scanResult, weights report.RemovalCandidateWeights) ([]report.DependencyReport, []string) {
	dependencies := sortedDependencyUnion(scan.DeclaredDependencies, scan.ImportedDependencies)
	builder := func(dependency string) (report.DependencyReport, []string) {
		return buildDependencyReport(dependency, scan)
	}
	return shared.BuildTopReports(topN, dependencies, builder, weights)
}

func buildDependencyReport(dependency string, scan scanResult) (report.DependencyReport, []string) {
	importsOf := func(file fileScan) []shared.ImportRecord {
		records := make([]shared.ImportRecord, 0, len(file.Imports))
		for _, imported := range file.Imports {
			records = append(records, imported.Record)
		}
		return records
	}
	usageOf := func(file fileScan) map[string]int {
		return file.Usage
	}
	fileUsages := shared.MapFileUsages(scan.Files, importsOf, usageOf)
	stats := shared.BuildDependencyStats(dependency, fileUsages, normalizeDependencyID)
	dependencyReport := shared.BuildDependencyReportFromStats(dependency, adapterID, stats)
	dependencyReport.Provenance = buildPowerShellDependencyProvenance(scan.DeclaredSources[dependency])
	applyImportSourceAttribution(dependency, &dependencyReport, scan)
	dependencyReport.Recommendations = buildRecommendations(dependencyReport)

	if stats.HasImports {
		return dependencyReport, nil
	}
	return dependencyReport, []string{fmt.Sprintf("no static module usage found for dependency %q", dependency)}
}

func applyImportSourceAttribution(dependency string, dependencyReport *report.DependencyReport, scan scanResult) {
	if dependencyReport == nil {
		return
	}
	provenanceByImport := make(map[string]map[string]struct{})
	for _, file := range scan.Files {
		for _, imported := range file.Imports {
			if normalizeDependencyID(imported.Record.Dependency) != normalizeDependencyID(dependency) {
				continue
			}
			key := importKey(imported.Record.Module, imported.Record.Name)
			if provenanceByImport[key] == nil {
				provenanceByImport[key] = make(map[string]struct{})
			}
			provenanceByImport[key][imported.Source] = struct{}{}
		}
	}

	applyImportProvenance := func(imports []report.ImportUse) {
		for i := range imports {
			key := importKey(imports[i].Module, imports[i].Name)
			sources := shared.SortedKeys(provenanceByImport[key])
			if len(sources) == 0 {
				continue
			}
			imports[i].Provenance = append(imports[i].Provenance[:0], sources...)
		}
	}

	applyImportProvenance(dependencyReport.UsedImports)
	applyImportProvenance(dependencyReport.UnusedImports)
}

func importKey(module, name string) string {
	return normalizeDependencyID(module) + ":" + normalizeDependencyID(name)
}

func buildRecommendations(dep report.DependencyReport) []report.Recommendation {
	recs := make([]report.Recommendation, 0, 1)
	if len(dep.UsedImports) == 0 && len(dep.UnusedImports) == 0 {
		recs = append(recs, report.Recommendation{
			Code:      "remove-unused-module",
			Priority:  "high",
			Message:   fmt.Sprintf("No static module usage was detected for %q; consider removing it.", dep.Name),
			Rationale: "Unused module dependencies increase maintenance and security overhead.",
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

func sortedDependencyUnion(values ...map[string]struct{}) []string {
	set := make(map[string]struct{})
	for _, value := range values {
		for dependency := range value {
			set[dependency] = struct{}{}
		}
	}
	if len(set) == 0 {
		return nil
	}
	dependencies := make([]string, 0, len(set))
	for dependency := range set {
		dependencies = append(dependencies, dependency)
	}
	sort.Strings(dependencies)
	return dependencies
}

func buildPowerShellDependencyProvenance(info powerShellDependencySource) *report.DependencyProvenance {
	if len(info.ManifestPaths) == 0 {
		return nil
	}
	signals := shared.SortedKeys(info.ManifestPaths)
	return &report.DependencyProvenance{
		Source:     dependencySourceManifest,
		Confidence: "high",
		Signals:    signals,
	}
}
