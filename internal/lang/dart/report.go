package dart

import (
	"fmt"
	"sort"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/thresholds"
)

func buildRequestedDartDependencies(req language.Request, scan scanResult) ([]report.DependencyReport, []string) {
	minUsageThreshold := resolveMinUsageRecommendationThreshold(req.MinUsagePercentForRecommendations)
	switch {
	case req.Dependency != "":
		dependency := normalizeDependencyID(req.Dependency)
		depReport, warnings := buildDependencyReport(dependency, scan, minUsageThreshold)
		return []report.DependencyReport{depReport}, warnings
	case req.TopN > 0:
		return buildTopDartDependencies(req.TopN, scan, minUsageThreshold)
	default:
		return nil, []string{"no dependency or top-N target provided"}
	}
}

func buildTopDartDependencies(topN int, scan scanResult, minUsageThreshold int) ([]report.DependencyReport, []string) {
	dependencies := allDependencies(scan)
	return shared.BuildTopReports(topN, dependencies, func(dependency string) (report.DependencyReport, []string) {
		return buildDependencyReport(dependency, scan, minUsageThreshold)
	})
}

func allDependencies(scan scanResult) []string {
	set := make(map[string]struct{})
	for dependency, info := range scan.DeclaredDependencies {
		if info.LocalPath {
			continue
		}
		set[dependency] = struct{}{}
	}

	fileUsages := dartFileUsages(scan)
	for _, dependency := range shared.ListDependencies(fileUsages, normalizeDependencyID) {
		set[dependency] = struct{}{}
	}
	return shared.SortedKeys(set)
}

func buildDependencyReport(dependency string, scan scanResult, minUsageThreshold int) (report.DependencyReport, []string) {
	stats := shared.BuildDependencyStats(dependency, dartFileUsages(scan), normalizeDependencyID)
	meta, declared := scan.DeclaredDependencies[dependency]

	dep := report.DependencyReport{
		Language:             "dart",
		Name:                 dependency,
		UsedExportsCount:     stats.UsedCount,
		TotalExportsCount:    stats.TotalCount,
		UsedPercent:          stats.UsedPercent,
		EstimatedUnusedBytes: 0,
		TopUsedSymbols:       stats.TopSymbols,
		UsedImports:          stats.UsedImports,
		UnusedImports:        stats.UnusedImports,
	}

	warnings := make([]string, 0, 1)
	if !stats.HasImports {
		warnings = append(warnings, fmt.Sprintf("no imports found for dependency %q", dependency))
	}

	if !declared && stats.HasImports {
		dep.RiskCues = append(dep.RiskCues, report.RiskCue{
			Code:     "undeclared-package-import",
			Severity: "medium",
			Message:  "package import was detected but the dependency is not declared in pubspec",
		})
		dep.Recommendations = append(dep.Recommendations, report.Recommendation{
			Code:      "declare-missing-dependency",
			Priority:  "high",
			Message:   fmt.Sprintf("Add %q to pubspec dependencies or remove the import.", dependency),
			Rationale: "Undeclared dependencies can break reproducible builds and reviews.",
		})
	}
	if declared {
		if meta.Override {
			dep.RiskCues = append(dep.RiskCues, report.RiskCue{
				Code:     "dependency-override",
				Severity: "medium",
				Message:  "dependency is marked in dependency_overrides",
			})
			dep.Recommendations = append(dep.Recommendations, report.Recommendation{
				Code:      "review-dependency-override",
				Priority:  "medium",
				Message:   "Review dependency_overrides usage and limit overrides to active blockers.",
				Rationale: "Overrides can hide upstream changes and create drift over time.",
			})
		}
		if meta.FlutterSDK {
			dep.RiskCues = append(dep.RiskCues, report.RiskCue{
				Code:     "flutter-sdk-dependency",
				Severity: "low",
				Message:  "dependency is provided by the Flutter SDK",
			})
		}
		if meta.PluginLike {
			dep.RiskCues = append(dep.RiskCues, report.RiskCue{
				Code:     "flutter-plugin-dependency",
				Severity: "medium",
				Message:  "dependency appears to be a Flutter plugin package with platform bindings",
			})
			dep.Recommendations = append(dep.Recommendations, report.Recommendation{
				Code:      "audit-plugin-removal",
				Priority:  "medium",
				Message:   "Audit native platform impact before removing this Flutter plugin dependency.",
				Rationale: "Plugin dependencies can bind Android/iOS platform code beyond Dart call sites.",
			})
		}
	}
	if stats.WildcardImports > 0 {
		dep.RiskCues = append(dep.RiskCues, report.RiskCue{
			Code:     "broad-imports",
			Severity: "low",
			Message:  "broad import/export directives may reduce static attribution precision",
		})
		dep.Recommendations = append(dep.Recommendations, report.Recommendation{
			Code:      "prefer-explicit-imports",
			Priority:  "low",
			Message:   "Prefer explicit imports (`show`) or prefixes (`as`) for tighter attribution.",
			Rationale: "Explicit import surfaces improve confidence in static usage analysis.",
		})
	}
	if dep.TotalExportsCount > 0 && dep.UsedPercent > 0 && dep.UsedPercent < float64(minUsageThreshold) {
		dep.Recommendations = append(dep.Recommendations, report.Recommendation{
			Code:      "reduce-dart-surface-area",
			Priority:  "low",
			Message:   fmt.Sprintf("Only %.1f%% of %q imports appear used; review if a leaner package would suffice.", dep.UsedPercent, dependency),
			Rationale: "Low observed usage can indicate avoidable dependency surface area.",
		})
	}
	if declared && stats.TotalCount == 0 {
		dep.Recommendations = append(dep.Recommendations, report.Recommendation{
			Code:      "remove-unused-dependency",
			Priority:  "high",
			Message:   "No imports were detected for this declared dependency; consider removing it.",
			Rationale: "Unused dependencies increase attack and maintenance surface.",
		})
	}

	sort.Slice(dep.RiskCues, func(i, j int) bool {
		return dep.RiskCues[i].Code < dep.RiskCues[j].Code
	})
	shared.SortRecommendations(dep.Recommendations, recommendationPriorityRank)

	return dep, warnings
}

func dartFileUsages(scan scanResult) []shared.FileUsage {
	imports := func(file fileScan) []shared.ImportRecord { return file.Imports }
	usage := func(file fileScan) map[string]int { return file.Usage }
	return shared.MapFileUsages(scan.Files, imports, usage)
}

func resolveMinUsageRecommendationThreshold(value *int) int {
	if value != nil {
		return *value
	}
	return thresholds.Defaults().MinUsagePercentForRecommendations
}

func summarizeUnresolved(unresolved map[string]int) []string {
	dependencies := shared.TopCountKeys(unresolved, maxWarningSamples)
	warnings := make([]string, 0, len(dependencies))
	for _, dependency := range dependencies {
		warnings = append(warnings, fmt.Sprintf("could not resolve Dart package import %q from pubspec data", dependency))
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
