package dotnet

import (
	"context"
	"fmt"
	"sort"

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
	centralPackagesFile = "Directory.Packages.props"
	csharpProjectExt    = ".csproj"
	fsharpProjectExt    = ".fsproj"
	solutionFileExt     = ".sln"
	csharpSourceExt     = ".cs"
	fsharpSourceExt     = ".fs"
	maxDetectFiles      = 1024
	maxScanFiles        = 4096
)

func NewAdapter() *Adapter {
	adapter := &Adapter{}
	adapter.AdapterLifecycle = language.NewAdapterLifecycle("dotnet", []string{"csharp", "cs", "fsharp", "fs"}, adapter.DetectWithConfidence)
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

	scan, err := scanRepo(ctx, repoPath)
	if err != nil {
		return report.Report{}, err
	}
	result.Warnings = append(result.Warnings, scan.Warnings...)

	dependencies, warnings := buildRequestedDotNetDependencies(req, scan)
	result.Dependencies = dependencies
	result.Warnings = append(result.Warnings, warnings...)
	result.Summary = report.ComputeSummary(result.Dependencies)

	if len(scan.DeclaredDependencies) == 0 {
		result.Warnings = append(result.Warnings, "no .NET package dependencies discovered from project manifests")
	}
	return result, nil
}

type importBinding = shared.ImportRecord

type fileScan struct {
	Path    string
	Imports []importBinding
	Usage   map[string]int
}

type scanResult struct {
	Files                  []fileScan
	DeclaredDependencies   []string
	Warnings               []string
	AmbiguousByDependency  map[string]int
	UndeclaredByDependency map[string]int
	SkippedGeneratedFiles  int
	SkippedFileLimit       bool
}

func buildRequestedDotNetDependencies(req language.Request, scan scanResult) ([]report.DependencyReport, []string) {
	minUsagePercentForRecommendations := resolveMinUsageRecommendationThreshold(req.MinUsagePercentForRecommendations)
	weights := resolveRemovalCandidateWeights(req.RemovalCandidateWeights)
	switch {
	case req.Dependency != "":
		dependency := normalizeDependencyID(req.Dependency)
		dep, warnings := buildDependencyReport(dependency, scan, minUsagePercentForRecommendations)
		return []report.DependencyReport{dep}, warnings
	case req.TopN > 0:
		return buildTopDotNetDependencies(req.TopN, scan, minUsagePercentForRecommendations, weights)
	default:
		return nil, []string{"no dependency or top-N target provided"}
	}
}

func buildTopDotNetDependencies(topN int, scan scanResult, minUsagePercentForRecommendations int, weights report.RemovalCandidateWeights) ([]report.DependencyReport, []string) {
	set := make(map[string]struct{})
	for _, dep := range scan.DeclaredDependencies {
		if dep != "" {
			set[normalizeDependencyID(dep)] = struct{}{}
		}
	}
	for _, file := range scan.Files {
		for _, imported := range file.Imports {
			if imported.Dependency != "" {
				set[normalizeDependencyID(imported.Dependency)] = struct{}{}
			}
		}
	}

	dependencies := make([]string, 0, len(set))
	for dep := range set {
		dependencies = append(dependencies, dep)
	}
	sort.Strings(dependencies)

	reports := make([]report.DependencyReport, 0, len(dependencies))
	warnings := make([]string, 0)
	for _, dep := range dependencies {
		current, currentWarnings := buildDependencyReport(dep, scan, minUsagePercentForRecommendations)
		reports = append(reports, current)
		warnings = append(warnings, currentWarnings...)
	}
	shared.SortReportsByWaste(reports, weights)
	if topN > 0 && topN < len(reports) {
		reports = reports[:topN]
	}
	if len(reports) == 0 {
		warnings = append(warnings, "no dependency data available for top-N ranking")
	}
	return reports, warnings
}

func buildDependencyReport(dependency string, scan scanResult, minUsagePercentForRecommendations int) (report.DependencyReport, []string) {
	fileUsages := shared.MapFileUsages(scan.Files, func(file fileScan) []shared.ImportRecord { return file.Imports }, func(file fileScan) map[string]int { return file.Usage })
	stats := shared.BuildDependencyStats(dependency, fileUsages, normalizeDependencyID)

	dep := report.DependencyReport{
		Language:          "dotnet",
		Name:              dependency,
		UsedExportsCount:  stats.UsedCount,
		TotalExportsCount: stats.TotalCount,
		UsedPercent:       stats.UsedPercent,
		TopUsedSymbols:    stats.TopSymbols,
		UsedImports:       stats.UsedImports,
		UnusedImports:     stats.UnusedImports,
	}

	ambiguousCount := scan.AmbiguousByDependency[dependency]
	undeclaredCount := scan.UndeclaredByDependency[dependency]
	warnings := make([]string, 0)
	if ambiguousCount > 0 {
		dep.RiskCues = append(dep.RiskCues, report.RiskCue{
			Code:     "ambiguous-namespace-mapping",
			Severity: "medium",
			Message:  "namespace-to-package mapping is ambiguous for one or more imports",
		})
		warnings = append(warnings, fmt.Sprintf("dependency %q has ambiguous namespace mapping in %d import(s)", dependency, ambiguousCount))
	}
	if undeclaredCount > 0 {
		dep.RiskCues = append(dep.RiskCues, report.RiskCue{
			Code:     "undeclared-package-usage",
			Severity: "high",
			Message:  "imports suggest package usage that is not declared in project manifests",
		})
		warnings = append(warnings, fmt.Sprintf("dependency %q appears in source imports but is not declared in project manifests", dependency))
	}
	dep.Recommendations = buildRecommendations(dep, ambiguousCount, undeclaredCount, minUsagePercentForRecommendations)
	return dep, warnings
}

func buildRecommendations(dep report.DependencyReport, ambiguousCount int, undeclaredCount int, minUsagePercentForRecommendations int) []report.Recommendation {
	recommendations := make([]report.Recommendation, 0, 3)
	if undeclaredCount > 0 {
		recommendations = append(recommendations, report.Recommendation{
			Code:      "declare-dependency-explicitly",
			Priority:  "high",
			Message:   "Declare this package explicitly in project manifests to avoid transitive drift.",
			Rationale: "Source imports appear without a direct package declaration.",
		})
	}
	if ambiguousCount > 0 {
		recommendations = append(recommendations, report.Recommendation{
			Code:      "review-namespace-mapping",
			Priority:  "medium",
			Message:   "Review namespace-to-package mapping for this dependency.",
			Rationale: "Multiple declared packages matched the same namespace prefix.",
		})
	}
	if dep.TotalExportsCount > 0 && dep.UsedPercent < float64(minUsagePercentForRecommendations) {
		recommendations = append(recommendations, report.Recommendation{
			Code:      "reduce-low-usage-package-surface",
			Priority:  "low",
			Message:   "Consider reducing or replacing low-usage package references.",
			Rationale: "Only a small portion of observed imports appears used.",
		})
	}
	return recommendations
}

func resolveMinUsageRecommendationThreshold(threshold *int) int {
	if threshold != nil {
		return *threshold
	}
	return thresholds.Defaults().MinUsagePercentForRecommendations
}

func resolveRemovalCandidateWeights(value *report.RemovalCandidateWeights) report.RemovalCandidateWeights {
	if value == nil {
		return report.DefaultRemovalCandidateWeights()
	}
	return report.NormalizeRemovalCandidateWeights(*value)
}
