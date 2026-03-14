package rust

import (
	"fmt"
	"sort"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/thresholds"
)

func buildRequestedRustDependencies(req language.Request, scan scanResult) ([]report.DependencyReport, []string) {
	minUsageThreshold := resolveMinUsageRecommendationThreshold(req.MinUsagePercentForRecommendations)
	weights := resolveRemovalCandidateWeights(req.RemovalCandidateWeights)
	switch {
	case req.Dependency != "":
		dependency := normalizeDependencyID(req.Dependency)
		depReport := buildDependencyReport(dependency, scan, minUsageThreshold)
		return []report.DependencyReport{depReport}, nil
	case req.TopN > 0:
		return buildTopRustDependencies(req.TopN, scan, minUsageThreshold, weights)
	default:
		return nil, []string{"no dependency or top-N target provided"}
	}
}

func buildTopRustDependencies(topN int, scan scanResult, minUsageThreshold int, weights report.RemovalCandidateWeights) ([]report.DependencyReport, []string) {
	fileUsages := shared.MapFileUsages(scan.Files, func(file fileScan) []shared.ImportRecord { return file.Imports }, func(file fileScan) map[string]int { return file.Usage })
	dependencies := shared.ListDependencies(fileUsages, normalizeDependencyID)
	reportBuilder := func(dependency string) (report.DependencyReport, []string) {
		return buildDependencyReport(dependency, scan, minUsageThreshold), nil
	}
	return shared.BuildTopReports(topN, dependencies, reportBuilder, weights)
}

func resolveRemovalCandidateWeights(value *report.RemovalCandidateWeights) report.RemovalCandidateWeights {
	if value == nil {
		return report.DefaultRemovalCandidateWeights()
	}
	return report.NormalizeRemovalCandidateWeights(*value)
}

func buildDependencyReport(dependency string, scan scanResult, minUsageThreshold int) report.DependencyReport {
	fileUsages := shared.MapFileUsages(scan.Files, func(file fileScan) []shared.ImportRecord { return file.Imports }, func(file fileScan) map[string]int { return file.Usage })
	stats := shared.BuildDependencyStats(dependency, fileUsages, normalizeDependencyID)
	dep := report.DependencyReport{
		Language:             "rust",
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
			Code:     "broad-imports",
			Severity: "medium",
			Message:  "found broad wildcard imports; prefer narrower symbol imports",
		})
		if dep.UsedPercent > 0 && dep.UsedPercent < float64(minUsageThreshold) {
			dep.Recommendations = append(dep.Recommendations, report.Recommendation{
				Code:      "prefer-explicit-imports",
				Priority:  "medium",
				Message:   "Replace wildcard imports with explicit symbol imports for better precision.",
				Rationale: "Explicit imports improve maintainability and reduce over-coupling.",
			})
		}
	}
	if aliases := scan.RenamedAliasesByDep[dependency]; len(aliases) > 0 {
		dep.RiskCues = append(dep.RiskCues, report.RiskCue{
			Code:     "renamed-crate",
			Severity: "low",
			Message:  "crate is imported via alias/package rename in Cargo.toml",
		})
		dep.Recommendations = append(dep.Recommendations, report.Recommendation{
			Code:      "document-crate-rename",
			Priority:  "low",
			Message:   "Document crate rename mappings to avoid attribution confusion.",
			Rationale: "Renamed crates can hide real package identity in usage reports.",
		})
	}
	if scan.MacroAmbiguityDetected && len(dep.UsedImports) > 0 {
		dep.RiskCues = append(dep.RiskCues, report.RiskCue{
			Code:     "macro-ambiguity",
			Severity: "low",
			Message:  "macro-heavy usage may reduce static import attribution precision",
		})
	}
	if dep.TotalExportsCount > 0 && dep.UsedPercent > 0 && dep.UsedPercent < float64(minUsageThreshold) {
		dep.Recommendations = append(dep.Recommendations, report.Recommendation{
			Code:      "reduce-rust-surface-area",
			Priority:  "low",
			Message:   fmt.Sprintf("Only %.1f%% of %q imports appear used; consider tightening imports or lighter alternatives.", dep.UsedPercent, dependency),
			Rationale: "Low observed usage often indicates avoidable dependency surface area.",
		})
	}
	if len(dep.UsedImports) == 0 && len(dep.UnusedImports) > 0 {
		dep.Recommendations = append(dep.Recommendations, report.Recommendation{
			Code:      "remove-unused-dependency",
			Priority:  "high",
			Message:   "No used imports were detected for this dependency; consider removing it.",
			Rationale: "Unused dependencies increase attack and maintenance surface.",
		})
	}
	sort.Slice(dep.RiskCues, func(i, j int) bool { return dep.RiskCues[i].Code < dep.RiskCues[j].Code })
	sort.Slice(dep.Recommendations, func(i, j int) bool {
		left := recommendationPriorityRank(dep.Recommendations[i].Priority)
		right := recommendationPriorityRank(dep.Recommendations[j].Priority)
		if left == right {
			return dep.Recommendations[i].Code < dep.Recommendations[j].Code
		}
		return left < right
	})
	return dep
}

func resolveMinUsageRecommendationThreshold(value *int) int {
	if value != nil {
		return *value
	}
	return thresholds.Defaults().MinUsagePercentForRecommendations
}

func summarizeUnresolved(unresolved map[string]int) []string {
	if len(unresolved) == 0 {
		return nil
	}
	type unresolvedItem struct {
		dep   string
		count int
	}
	items := make([]unresolvedItem, 0, len(unresolved))
	for dep, count := range unresolved {
		items = append(items, unresolvedItem{dep: dep, count: count})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].count == items[j].count {
			return items[i].dep < items[j].dep
		}
		return items[i].count > items[j].count
	})
	if len(items) > maxWarningSamples {
		items = items[:maxWarningSamples]
	}
	warnings := make([]string, 0, len(items))
	for _, item := range items {
		warnings = append(warnings, fmt.Sprintf("could not resolve Rust crate alias %q from Cargo manifests", item.dep))
	}
	return warnings
}

func recommendationPriorityRank(priority string) int {
	switch priority {
	case "high":
		return 0
	case "medium":
		return 1
	default:
		return 2
	}
}
