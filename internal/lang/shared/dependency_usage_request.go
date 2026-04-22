package shared

import (
	"strings"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
)

func BuildRequestedDependencies[S any](req language.Request, scan S, normalizeDependencyID func(string) string, buildDependency func(string, S) (report.DependencyReport, []string), buildTop func(int, S) ([]report.DependencyReport, []string)) ([]report.DependencyReport, []string) {
	if req.TopN > 0 {
		return buildTop(req.TopN, scan)
	}
	dependency := normalizeDependencyID(req.Dependency)
	if dependency == "" {
		return nil, []string{"no dependency or top-N target provided"}
	}
	depReport, warnings := buildDependency(dependency, scan)
	return []report.DependencyReport{depReport}, warnings
}

func BuildRequestedDependenciesWithWeights[S any](req language.Request, scan S, normalizeDependencyID func(string) string, buildDependency func(string, S) (report.DependencyReport, []string), resolveWeights func(*report.RemovalCandidateWeights) report.RemovalCandidateWeights, buildTop func(int, S, report.RemovalCandidateWeights) ([]report.DependencyReport, []string)) ([]report.DependencyReport, []string) {
	buildTopWithWeights := func(topN int, current S) ([]report.DependencyReport, []string) {
		return buildTop(topN, current, resolveWeights(req.RemovalCandidateWeights))
	}
	return BuildRequestedDependencies(req, scan, normalizeDependencyID, buildDependency, buildTopWithWeights)
}

func NormalizeDependencyID(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
