package dart

import (
	"fmt"
	"strings"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
)

func buildRequestedDartDependencies(req language.Request, scan scanResult) ([]report.DependencyReport, []string) {
	minUsageThreshold := resolveMinUsageRecommendationThreshold(req.MinUsagePercentForRecommendations)
	previewEnabled := req.Features.Enabled(dartSourceAttributionPreviewFeature)
	switch {
	case req.Dependency != "":
		dependency := normalizeDependencyID(req.Dependency)
		depReport, warnings := buildDependencyReport(dependency, scan, minUsageThreshold, previewEnabled)
		return []report.DependencyReport{depReport}, warnings
	case req.TopN > 0:
		return buildTopDartDependencies(req.TopN, scan, minUsageThreshold, previewEnabled)
	default:
		return nil, []string{"no dependency or top-N target provided"}
	}
}

func buildTopDartDependencies(topN int, scan scanResult, minUsageThreshold int, previewEnabled bool) ([]report.DependencyReport, []string) {
	dependencies := allDependencies(scan, previewEnabled)
	return shared.BuildTopReports(topN, dependencies, func(dependency string) (report.DependencyReport, []string) {
		return buildDependencyReport(dependency, scan, minUsageThreshold, previewEnabled)
	})
}

func allDependencies(scan scanResult, previewEnabled bool) []string {
	set := make(map[string]struct{})
	for dependency, info := range scan.DeclaredDependencies {
		if info.LocalPath && !previewEnabled {
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

func buildDependencyReport(dependency string, scan scanResult, minUsageThreshold int, previewEnabled bool) (report.DependencyReport, []string) {
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
		if previewEnabled {
			dep.Provenance = buildDartDependencyProvenance(meta)
		}
		if meta.Override {
			dep.RiskCues = append(dep.RiskCues, report.RiskCue{
				Code:     "dependency-override",
				Severity: "medium",
				Message:  dependencyOverrideMessage(meta, previewEnabled),
			})
			dep.Recommendations = append(dep.Recommendations, report.Recommendation{
				Code:      "review-dependency-override",
				Priority:  "medium",
				Message:   dependencyOverrideRecommendation(meta, previewEnabled),
				Rationale: "Overrides can hide upstream changes and create drift over time.",
			})
		}

		source := dependencySource(meta)
		if previewEnabled {
			switch source {
			case dependencySourcePath:
				dep.RiskCues = append(dep.RiskCues, report.RiskCue{
					Code:     "local-path-dependency",
					Severity: "low",
					Message:  "dependency is sourced from a local path",
				})
				dep.Recommendations = append(dep.Recommendations, report.Recommendation{
					Code:      "review-local-path-dependency",
					Priority:  "medium",
					Message:   "Treat local path dependencies as internal package edges before removal decisions.",
					Rationale: "Local package links are often workspace-internal and not ordinary third-party surface.",
				})
			case dependencySourceGit:
				dep.RiskCues = append(dep.RiskCues, report.RiskCue{
					Code:     "git-dependency-source",
					Severity: "medium",
					Message:  "dependency resolves from a git source",
				})
				dep.Recommendations = append(dep.Recommendations, report.Recommendation{
					Code:      "pin-git-dependency",
					Priority:  "medium",
					Message:   "Pin git dependencies to reviewed refs and monitor upstream revision drift.",
					Rationale: "Git-sourced dependencies can move outside normal hosted package release workflows.",
				})
			}
		}

		if meta.FlutterSDK {
			dep.RiskCues = append(dep.RiskCues, report.RiskCue{
				Code:     "flutter-sdk-dependency",
				Severity: "low",
				Message:  "dependency is provided by the Flutter SDK",
			})
		}

		pluginLike := meta.PluginLike || (previewEnabled && meta.FederatedPlugin)
		if pluginLike {
			dep.RiskCues = append(dep.RiskCues, report.RiskCue{
				Code:     "flutter-plugin-dependency",
				Severity: "medium",
				Message:  pluginDependencyMessage(meta, previewEnabled),
			})
			dep.Recommendations = append(dep.Recommendations, report.Recommendation{
				Code:      "audit-plugin-removal",
				Priority:  "medium",
				Message:   pluginRemovalRecommendation(meta, previewEnabled),
				Rationale: "Plugin dependencies can bind Android/iOS platform code beyond Dart call sites.",
			})
		}

		if previewEnabled && meta.FederatedPlugin {
			dep.RiskCues = append(dep.RiskCues, report.RiskCue{
				Code:     "flutter-federated-plugin-family",
				Severity: "medium",
				Message:  federatedPluginMessage(meta),
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
	if declared && stats.TotalCount == 0 && (!previewEnabled || !meta.LocalPath) {
		dep.Recommendations = append(dep.Recommendations, report.Recommendation{
			Code:      "remove-unused-dependency",
			Priority:  "high",
			Message:   "No imports were detected for this declared dependency; consider removing it.",
			Rationale: "Unused dependencies increase attack and maintenance surface.",
		})
	}

	shared.SortRiskCues(dep.RiskCues)
	shared.SortRecommendations(dep.Recommendations, recommendationPriorityRank)

	return dep, warnings
}

func dependencySource(meta dependencyInfo) string {
	source := normalizeDependencySource(meta.Source)
	if source != "" {
		return source
	}
	if meta.LocalPath {
		return dependencySourcePath
	}
	if meta.FlutterSDK {
		return dependencySourceSDK
	}
	return ""
}

func dependencySourceLabel(meta dependencyInfo) string {
	switch dependencySource(meta) {
	case dependencySourcePath:
		return "local path"
	case dependencySourceGit:
		return "git"
	case dependencySourceHosted:
		return "hosted"
	case dependencySourceSDK:
		if meta.FlutterSDK {
			return "Flutter SDK"
		}
		return "SDK"
	default:
		return ""
	}
}

func dependencyOverrideMessage(meta dependencyInfo, previewEnabled bool) string {
	if !previewEnabled {
		return "dependency is marked in dependency_overrides"
	}
	if label := dependencySourceLabel(meta); label != "" {
		return fmt.Sprintf("dependency is marked in dependency_overrides (%s source)", label)
	}
	return "dependency is marked in dependency_overrides"
}

func dependencyOverrideRecommendation(meta dependencyInfo, previewEnabled bool) string {
	if !previewEnabled {
		return "Review dependency_overrides usage and limit overrides to active blockers."
	}
	if label := dependencySourceLabel(meta); label != "" {
		return fmt.Sprintf("Review dependency_overrides usage for this %s dependency and limit overrides to active blockers.", label)
	}
	return "Review dependency_overrides usage and limit overrides to active blockers."
}

func pluginDependencyMessage(meta dependencyInfo, previewEnabled bool) string {
	if !previewEnabled || !meta.FederatedPlugin || meta.FederatedFamily == "" {
		return "dependency appears to be a Flutter plugin package with platform bindings"
	}
	return fmt.Sprintf("dependency is part of Flutter federated plugin family %q with platform bindings", meta.FederatedFamily)
}

func pluginRemovalRecommendation(meta dependencyInfo, previewEnabled bool) string {
	if !previewEnabled || !meta.FederatedPlugin || meta.FederatedFamily == "" {
		return "Audit native platform impact before removing this Flutter plugin dependency."
	}
	return fmt.Sprintf("Audit platform impact across the %q federated plugin family before removing this dependency.", meta.FederatedFamily)
}

func federatedPluginMessage(meta dependencyInfo) string {
	if meta.FederatedFamily == "" {
		return "dependency participates in a Flutter federated plugin family"
	}
	if len(meta.FederatedMembers) == 0 {
		return fmt.Sprintf("dependency participates in the %q Flutter federated plugin family", meta.FederatedFamily)
	}
	return fmt.Sprintf("dependency participates in the %q Flutter federated plugin family with related packages: %s", meta.FederatedFamily, strings.Join(meta.FederatedMembers, ", "))
}

func buildDartDependencyProvenance(meta dependencyInfo) *report.DependencyProvenance {
	source := dependencySource(meta)
	if source == "" {
		return nil
	}
	signals := make([]string, 0, 6)
	if meta.DeclaredInManifest {
		signals = append(signals, pubspecYAMLName)
	}
	if meta.ResolvedInLock {
		signals = append(signals, pubspecLockName)
	}
	if meta.SourceDetail != "" {
		signals = append(signals, meta.SourceDetail)
	}
	if meta.Override {
		signals = append(signals, "dependency_overrides")
	}
	if meta.FederatedPlugin && meta.FederatedFamily != "" {
		signals = append(signals, "federated:"+meta.FederatedFamily)
	}
	signals = dedupeStrings(signals)

	confidence := ""
	switch {
	case meta.ResolvedInLock:
		confidence = "high"
	case meta.DeclaredInManifest:
		confidence = "medium"
	}

	return &report.DependencyProvenance{
		Source:     source,
		Confidence: confidence,
		Signals:    signals,
	}
}

func dartFileUsages(scan scanResult) []shared.FileUsage {
	imports := func(file fileScan) []shared.ImportRecord { return file.Imports }
	usage := func(file fileScan) map[string]int { return file.Usage }
	return shared.MapFileUsages(scan.Files, imports, usage)
}

func resolveMinUsageRecommendationThreshold(value *int) int {
	return shared.ResolveMinUsageRecommendationThreshold(value)
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
	return shared.RecommendationPriorityRank(priority)
}
