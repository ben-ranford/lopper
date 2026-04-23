package jvm

import (
	"context"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/workspace"
)

type Adapter struct {
	language.AdapterLifecycle
}

const (
	pomXMLName         = "pom.xml"
	buildGradleName    = "build.gradle"
	buildGradleKTSName = "build.gradle.kts"
)

var jvmSkippedDirectories = map[string]bool{
	"target":     true,
	".gradle":    true,
	".mvn":       true,
	"out":        true,
	".classpath": true,
	".settings":  true,
}

func NewAdapter() *Adapter {
	adapter := &Adapter{}
	adapter.AdapterLifecycle = language.NewAdapterLifecycle("jvm", []string{"java", "kotlin"}, adapter.DetectWithConfidence)
	return adapter
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

	declaredDependencies, depPrefixes, depAliases, declarationWarnings := collectDeclaredDependencies(repoPath)
	result.Warnings = append(result.Warnings, declarationWarnings...)
	scanResult, err := scanRepo(ctx, repoPath, depPrefixes, depAliases)
	if err != nil {
		return report.Report{}, err
	}
	result.Warnings = append(result.Warnings, scanResult.Warnings...)

	dependencies, warnings := buildRequestedJVMDependencies(req, scanResult)
	result.Dependencies = dependencies
	result.Warnings = append(result.Warnings, warnings...)
	result.Summary = report.ComputeSummary(result.Dependencies)

	if len(declaredDependencies) == 0 {
		result.Warnings = append(result.Warnings, "no JVM dependencies discovered from pom.xml or build.gradle manifests")
	}

	return result, nil
}

func buildRequestedJVMDependencies(req language.Request, scan scanResult) ([]report.DependencyReport, []string) {
	return shared.BuildRequestedDependenciesWithWeights(req, scan, normalizeDependencyID, buildDependencyReport, resolveRemovalCandidateWeights, buildTopJVMDependencies)
}

func buildTopJVMDependencies(topN int, scan scanResult, weights report.RemovalCandidateWeights) ([]report.DependencyReport, []string) {
	fileUsages := shared.MapFileUsages(scan.Files, func(file fileScan) []shared.ImportRecord { return file.Imports }, func(file fileScan) map[string]int { return file.Usage })
	dependencies := shared.ListDependencies(fileUsages, normalizeDependencyID)
	reportBuilder := func(dependency string) (report.DependencyReport, []string) {
		return buildDependencyReport(dependency, scan)
	}
	return shared.BuildTopReports(topN, dependencies, reportBuilder, weights)
}

func resolveRemovalCandidateWeights(value *report.RemovalCandidateWeights) report.RemovalCandidateWeights {
	if value == nil {
		return report.DefaultRemovalCandidateWeights()
	}
	return report.NormalizeRemovalCandidateWeights(*value)
}

func buildDependencyReport(dependency string, scan scanResult) (report.DependencyReport, []string) {
	fileUsages := shared.MapFileUsages(scan.Files, func(file fileScan) []shared.ImportRecord { return file.Imports }, func(file fileScan) map[string]int { return file.Usage })
	stats := shared.BuildDependencyStats(dependency, fileUsages, normalizeDependencyID)
	warnings := make([]string, 0)
	if !stats.HasImports {
		warnings = append(warnings, "no imports found for dependency "+dependency)
	}

	dep := report.DependencyReport{
		Language:             "jvm",
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
			Message:  "found wildcard imports for this dependency",
		})
	}
	dep.Recommendations = buildRecommendations(dep)
	return dep, warnings
}

func buildRecommendations(dep report.DependencyReport) []report.Recommendation {
	recommendations := make([]report.Recommendation, 0, 2)
	if len(dep.UsedImports) == 0 && len(dep.UnusedImports) > 0 {
		recommendations = append(recommendations, report.Recommendation{
			Code:      "remove-unused-dependency",
			Priority:  "high",
			Message:   "No used imports were detected for this dependency; consider removing it.",
			Rationale: "Unused dependencies increase attack and maintenance surface.",
		})
	}
	if shared.HasWildcardImport(dep.UsedImports) || shared.HasWildcardImport(dep.UnusedImports) {
		recommendations = append(recommendations, report.Recommendation{
			Code:      "avoid-wildcard-imports",
			Priority:  "medium",
			Message:   "Wildcard imports were detected; prefer explicit imports.",
			Rationale: "Explicit imports improve analysis precision and maintainability.",
		})
	}
	return recommendations
}

func normalizeDependencyID(value string) string {
	return shared.NormalizeDependencyID(value)
}

func shouldSkipDir(name string) bool {
	return shared.ShouldSkipDir(name, jvmSkippedDirectories)
}
