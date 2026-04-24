package ruby

import (
	"fmt"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/report"
)

func shapeRubyDependencyReport(dependency string, stats shared.DependencyStats, source rubyDependencySource) (report.DependencyReport, []string) {
	dependencyReport := shared.BuildDependencyReportFromStats(dependency, "ruby", stats)
	dependencyReport.Provenance = buildRubyDependencyProvenance(source)
	appendRuntimeRequireRiskCue(&dependencyReport, stats.WildcardImports)
	dependencyReport.Recommendations = buildRecommendations(dependencyReport)
	return dependencyReport, shapeRubyDependencyWarnings(dependency, stats)
}

func appendRuntimeRequireRiskCue(dep *report.DependencyReport, wildcardImports int) {
	if wildcardImports == 0 {
		return
	}
	dep.RiskCues = append(dep.RiskCues, report.RiskCue{
		Code:     "dynamic-require",
		Severity: "medium",
		Message:  fmt.Sprintf("found %d runtime require signal(s) for this gem", wildcardImports),
	})
}

func shapeRubyDependencyWarnings(dependency string, stats shared.DependencyStats) []string {
	if stats.HasImports {
		return nil
	}
	return []string{fmt.Sprintf("no requires found for dependency %q", dependency)}
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
