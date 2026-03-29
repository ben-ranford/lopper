package kotlinandroid

import (
	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
)

func buildRequestedKotlinAndroidDependencies(req language.Request, scan scanResult) ([]report.DependencyReport, []string) {
	return shared.BuildRequestedDependenciesWithWeights(req, scan, normalizeDependencyID, buildDependencyReport, resolveRemovalCandidateWeights, buildTopKotlinAndroidDependencies)
}

func buildTopKotlinAndroidDependencies(topN int, scan scanResult, weights report.RemovalCandidateWeights) ([]report.DependencyReport, []string) {
	reportBuilder := func(dependency string) (report.DependencyReport, []string) {
		return buildDependencyReport(dependency, scan)
	}
	dependencies := shared.ListDependencies(kotlinAndroidFileUsages(scan), normalizeDependencyID)
	return shared.BuildTopReports(topN, dependencies, reportBuilder, weights)
}

func kotlinAndroidFileUsages(scan scanResult) []shared.FileUsage {
	importsOf := func(file fileScan) []shared.ImportRecord { return file.Imports }
	usageOf := func(file fileScan) map[string]int { return file.Usage }
	return shared.MapFileUsages(scan.Files, importsOf, usageOf)
}

func resolveRemovalCandidateWeights(value *report.RemovalCandidateWeights) report.RemovalCandidateWeights {
	return shared.ResolveRemovalCandidateWeights(value)
}

func buildDependencyReport(dependency string, scan scanResult) (report.DependencyReport, []string) {
	stats := shared.BuildDependencyStats(dependency, kotlinAndroidFileUsages(scan), normalizeDependencyID)
	dep := shared.BuildDependencyReportFromStats(dependency, "kotlin-android", stats)
	dep.RiskCues = kotlinAndroidRiskCues(dependency, scan, stats)
	warnings := kotlinAndroidDependencyWarnings(dependency, stats)
	dep.Recommendations = buildRecommendations(dep)
	return dep, warnings
}

func kotlinAndroidDependencyWarnings(dependency string, stats shared.DependencyStats) []string {
	if stats.HasImports {
		return nil
	}
	return []string{"no imports found for dependency " + dependency}
}

func kotlinAndroidRiskCues(dependency string, scan scanResult, stats shared.DependencyStats) []report.RiskCue {
	cues := make([]report.RiskCue, 0, 3)
	if stats.WildcardImports > 0 {
		cues = append(cues, report.RiskCue{
			Code:     "wildcard-import",
			Severity: "medium",
			Message:  "found wildcard imports for this dependency",
		})
	}
	if _, ok := scan.AmbiguousDependencies[dependency]; ok {
		cues = append(cues, report.RiskCue{
			Code:     "ambiguous-import-mapping",
			Severity: "medium",
			Message:  "some imports matched multiple Gradle dependency candidates",
		})
	}
	if _, ok := scan.UndeclaredDependencies[dependency]; ok {
		cues = append(cues, report.RiskCue{
			Code:     "undeclared-import-attribution",
			Severity: "low",
			Message:  "dependency inferred from imports but not declared in Gradle manifests",
		})
	}
	return cues
}

func buildRecommendations(dep report.DependencyReport) []report.Recommendation {
	recommendations := make([]report.Recommendation, 0, 4)
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
	if hasRiskCue(dep, "ambiguous-import-mapping") {
		recommendations = append(recommendations, report.Recommendation{
			Code:      "review-ambiguous-gradle-mappings",
			Priority:  "medium",
			Message:   "Review imports that map to multiple Gradle coordinates and tighten declarations.",
			Rationale: "Ambiguous attribution reduces confidence in dependency removal scoring.",
		})
	}
	if hasRiskCue(dep, "undeclared-import-attribution") {
		recommendations = append(recommendations, report.Recommendation{
			Code:      "declare-missing-gradle-dependency",
			Priority:  "medium",
			Message:   "Import evidence suggests this dependency is used but not declared in Gradle manifests.",
			Rationale: "Keeping manifests aligned with imports improves build reproducibility and SBOM fidelity.",
		})
	}
	return recommendations
}

func hasRiskCue(dep report.DependencyReport, code string) bool {
	for _, cue := range dep.RiskCues {
		if cue.Code == code {
			return true
		}
	}
	return false
}
