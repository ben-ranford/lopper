package swift

import (
	"fmt"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/thresholds"
)

func buildRequestedSwiftDependencies(req language.Request, scan scanResult, catalog dependencyCatalog) ([]report.DependencyReport, []string) {
	minUsagePercent := resolveMinUsageRecommendationThreshold(req.MinUsagePercentForRecommendations)
	buildDependency := func(dependency string, scan scanResult) (report.DependencyReport, []string) {
		return buildDependencyReport(dependency, scan, catalog, minUsagePercent)
	}
	return shared.BuildRequestedDependenciesWithWeights(req, scan, normalizeDependencyID, buildDependency, resolveRemovalCandidateWeights, buildTopSwiftDependencies(scan, catalog, minUsagePercent))
}

func buildTopSwiftDependencies(scan scanResult, catalog dependencyCatalog, minUsagePercent int) func(int, scanResult, report.RemovalCandidateWeights) ([]report.DependencyReport, []string) {
	return func(topN int, _ scanResult, weights report.RemovalCandidateWeights) ([]report.DependencyReport, []string) {
		dependencies := allSwiftDependencies(scan)
		reports := make([]report.DependencyReport, 0, len(dependencies))
		warnings := make([]string, 0)
		for _, dependency := range dependencies {
			depReport, depWarnings := buildDependencyReport(dependency, scan, catalog, minUsagePercent)
			reports = append(reports, depReport)
			warnings = append(warnings, depWarnings...)
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
}

func allSwiftDependencies(scan scanResult) []string {
	set := make(map[string]struct{})
	for dependency := range scan.KnownDependencies {
		set[dependency] = struct{}{}
	}
	for dependency := range scan.ImportedDependencies {
		set[dependency] = struct{}{}
	}
	return shared.SortedKeys(set)
}

func buildDependencyReport(dependency string, scan scanResult, catalog dependencyCatalog, minUsagePercent int) (report.DependencyReport, []string) {
	importsOf := func(file fileScan) []shared.ImportRecord { return file.Imports }
	usageOf := func(file fileScan) map[string]int { return file.Usage }
	fileUsages := shared.MapFileUsages(scan.Files, importsOf, usageOf)
	stats := shared.BuildDependencyStats(dependency, fileUsages, normalizeDependencyID)
	depReport := shared.BuildDependencyReportFromStats(dependency, swiftAdapterID, stats)

	meta := catalog.Dependencies[dependency]
	depReport.RiskCues = buildDependencyRiskCues(meta)
	depReport.Recommendations = buildRecommendations(depReport, meta, minUsagePercent)
	if meta.Source != "" {
		depReport.Provenance = &report.DependencyProvenance{
			Source:     "manifest/lockfile",
			Confidence: "high",
			Signals:    []string{meta.Source},
		}
	}

	if stats.HasImports {
		return depReport, nil
	}
	return depReport, []string{fmt.Sprintf("no imports found for dependency %q", dependency)}
}

func buildDependencyRiskCues(meta dependencyMeta) []report.RiskCue {
	cues := make([]report.RiskCue, 0, 1)
	if meta.Declared && !meta.Resolved {
		cues = append(cues, report.RiskCue{
			Code:     "missing-lock-resolution",
			Severity: "medium",
			Message:  "dependency is declared in Package.swift but missing from Package.resolved",
		})
	}
	return cues
}

func buildRecommendations(dep report.DependencyReport, meta dependencyMeta, minUsagePercent int) []report.Recommendation {
	recommendations := make([]report.Recommendation, 0, 3)
	if len(dep.UsedImports) == 0 && len(dep.UnusedImports) > 0 {
		recommendations = append(recommendations, report.Recommendation{
			Code:      "remove-unused-dependency",
			Priority:  "high",
			Message:   fmt.Sprintf("No used imports were detected for %q; consider removing it.", dep.Name),
			Rationale: "Unused dependencies increase maintenance and security surface area.",
		})
	}
	if dep.TotalExportsCount > 0 && dep.UsedPercent < float64(minUsagePercent) {
		recommendations = append(recommendations, report.Recommendation{
			Code:      "low-usage-dependency",
			Priority:  "medium",
			Message:   fmt.Sprintf("Dependency %q has low observed usage (%.1f%%).", dep.Name, dep.UsedPercent),
			Rationale: "Low-usage dependencies are good candidates for cleanup or replacement.",
		})
	}
	if meta.Declared && !meta.Resolved {
		recommendations = append(recommendations, report.Recommendation{
			Code:      "refresh-package-resolved",
			Priority:  "medium",
			Message:   fmt.Sprintf("Dependency %q is declared but not pinned in Package.resolved; refresh lockfile.", dep.Name),
			Rationale: "Keeping lockfile pins aligned improves reproducibility and supply-chain traceability.",
		})
	}
	return recommendations
}

func resolveMinUsageRecommendationThreshold(value *int) int {
	if value != nil {
		return *value
	}
	return thresholds.Defaults().MinUsagePercentForRecommendations
}

func resolveRemovalCandidateWeights(value *report.RemovalCandidateWeights) report.RemovalCandidateWeights {
	if value == nil {
		return report.DefaultRemovalCandidateWeights()
	}
	return report.NormalizeRemovalCandidateWeights(*value)
}
