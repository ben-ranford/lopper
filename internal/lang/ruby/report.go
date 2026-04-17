package ruby

import (
	"fmt"

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
	importsOf := func(file fileScan) []shared.ImportRecord {
		return file.Imports
	}
	usageOf := func(file fileScan) map[string]int {
		return file.Usage
	}
	fileUsages := shared.MapFileUsages(scan.Files, importsOf, usageOf)
	stats := shared.BuildDependencyStats(dependency, fileUsages, normalizeDependencyID)

	dependencyReport := shared.BuildDependencyReportFromStats(dependency, "ruby", stats)
	dependencyReport.Provenance = buildRubyDependencyProvenance(scan.DeclaredSources[dependency])
	if stats.WildcardImports > 0 {
		dependencyReport.RiskCues = append(dependencyReport.RiskCues, report.RiskCue{
			Code:     "dynamic-require",
			Severity: "medium",
			Message:  fmt.Sprintf("found %d runtime require signal(s) for this gem", stats.WildcardImports),
		})
	}
	dependencyReport.Recommendations = buildRecommendations(dependencyReport)

	if stats.HasImports {
		return dependencyReport, nil
	}
	return dependencyReport, []string{fmt.Sprintf("no requires found for dependency %q", dependency)}
}

func buildRecommendations(dep report.DependencyReport) []report.Recommendation {
	recs := make([]report.Recommendation, 0, 2)
	if len(dep.UsedImports) == 0 && len(dep.UnusedImports) > 0 {
		recs = append(recs, report.Recommendation{
			Code:      "remove-unused-gem",
			Priority:  "high",
			Message:   fmt.Sprintf("No require usage was detected for %q; consider removing it.", dep.Name),
			Rationale: "Unused gems add maintenance and security overhead.",
		})
	}
	if len(dep.RiskCues) > 0 {
		recs = append(recs, report.Recommendation{
			Code:      "review-runtime-requires",
			Priority:  "medium",
			Message:   "Runtime require signals were detected; manually verify usage before removal.",
			Rationale: "Runtime require loading can hide usage from static analysis.",
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
	return shared.SortedKeys(set)
}

func buildRubyDependencyProvenance(info rubyDependencySource) *report.DependencyProvenance {
	source := rubyDependencyProvenanceSource(info)
	if source == "" {
		return nil
	}
	return &report.DependencyProvenance{
		Source:     source,
		Confidence: rubyDependencyProvenanceConfidence(info),
		Signals:    rubyDependencyProvenanceSignals(info),
	}
}

func rubyDependencyProvenanceSource(info rubyDependencySource) string {
	kinds := 0
	source := ""
	if info.Rubygems {
		kinds++
		source = rubyDependencySourceRubygems
	}
	if info.Git {
		kinds++
		source = rubyDependencySourceGit
	}
	if info.Path {
		kinds++
		source = rubyDependencySourcePath
	}
	switch kinds {
	case 0:
		return ""
	case 1:
		return source
	default:
		return rubyDependencySourceBundler
	}
}

func rubyDependencyProvenanceConfidence(info rubyDependencySource) string {
	switch {
	case info.DeclaredLock || info.Git || info.Path:
		return "high"
	case info.Rubygems:
		return "medium"
	default:
		return ""
	}
}

func rubyDependencyProvenanceSignals(info rubyDependencySource) []string {
	signals := make([]string, 0, 4)
	if rubyDependencyProvenanceSource(info) == rubyDependencySourceBundler {
		if info.Git {
			signals = append(signals, rubyDependencySourceGit)
		}
		if info.Path {
			signals = append(signals, rubyDependencySourcePath)
		}
		if info.Rubygems {
			signals = append(signals, rubyDependencySourceRubygems)
		}
	}
	if info.DeclaredGemfile {
		signals = append(signals, gemfileName)
	}
	if info.DeclaredLock {
		signals = append(signals, gemfileLockName)
	}
	return signals
}
