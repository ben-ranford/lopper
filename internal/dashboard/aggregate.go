package dashboard

import (
	"sort"
	"strings"
	"time"

	"github.com/ben-ranford/lopper/internal/report"
)

type RepoAnalysis struct {
	Input  RepoInput
	Report report.Report
	Err    error
}

func Aggregate(generatedAt time.Time, analyses []RepoAnalysis) Report {
	results := make([]RepoResult, 0, len(analyses))
	crossRepoIndex := make(map[string]map[string]struct{})
	sourceWarnings := make([]string, 0)
	summary := Summary{
		TotalRepos: len(analyses),
	}

	for _, analysis := range analyses {
		repoResult := RepoResult{
			Name:     analysis.Input.Name,
			Path:     analysis.Input.Path,
			Language: analysis.Input.Language,
		}
		if analysis.Err != nil {
			repoResult.Error = analysis.Err.Error()
			sourceWarnings = append(sourceWarnings, analysis.Input.Name+": "+analysis.Err.Error())
			results = append(results, repoResult)
			continue
		}

		dependencyCount := len(analysis.Report.Dependencies)
		wasteCandidateCount := countWasteCandidates(analysis.Report.Dependencies)
		topRiskSeverity, criticalCVEs := scanRiskSignals(analysis.Report.Dependencies)
		deniedLicenseCount := countDeniedLicenses(analysis.Report)

		repoResult.DependencyCount = dependencyCount
		repoResult.WasteCandidateCount = wasteCandidateCount
		if dependencyCount > 0 {
			repoResult.WasteCandidatePercent = (float64(wasteCandidateCount) / float64(dependencyCount)) * 100
		}
		repoResult.TopRiskSeverity = topRiskSeverity
		repoResult.CriticalCVEs = criticalCVEs
		repoResult.DeniedLicenseCount = deniedLicenseCount
		repoResult.Warnings = append(repoResult.Warnings, analysis.Report.Warnings...)

		summary.TotalDeps += dependencyCount
		summary.TotalWasteCandidates += wasteCandidateCount
		summary.CriticalCVEs += criticalCVEs

		repoName := repoResult.Name
		for _, dependency := range analysis.Report.Dependencies {
			name := strings.TrimSpace(dependency.Name)
			if name == "" {
				continue
			}
			if _, ok := crossRepoIndex[name]; !ok {
				crossRepoIndex[name] = make(map[string]struct{})
			}
			crossRepoIndex[name][repoName] = struct{}{}
		}

		results = append(results, repoResult)
	}

	crossRepoDeps := buildCrossRepoDependencies(crossRepoIndex)
	summary.CrossRepoDuplicates = len(crossRepoDeps)

	return Report{
		GeneratedAt:    generatedAt.UTC(),
		Repos:          results,
		Summary:        summary,
		CrossRepoDeps:  crossRepoDeps,
		SourceWarnings: sourceWarnings,
	}
}

func countWasteCandidates(dependencies []report.DependencyReport) int {
	total := 0
	for _, dependency := range dependencies {
		if hasWasteCandidateRecommendation(dependency.Recommendations) {
			total++
		}
	}
	return total
}

func hasWasteCandidateRecommendation(recommendations []report.Recommendation) bool {
	for _, recommendation := range recommendations {
		code := strings.ToLower(strings.TrimSpace(recommendation.Code))
		if code == "" {
			continue
		}
		if strings.HasPrefix(code, "remove-") || strings.Contains(code, "low-usage") {
			return true
		}
	}
	return false
}

func scanRiskSignals(dependencies []report.DependencyReport) (string, int) {
	highestRank := -1
	highestSeverity := ""
	criticalCVEs := 0

	for _, dependency := range dependencies {
		for _, cue := range dependency.RiskCues {
			severity := strings.ToLower(strings.TrimSpace(cue.Severity))
			rank := riskSeverityRank(severity)
			if rank > highestRank {
				highestRank = rank
				highestSeverity = severity
			}
			if severity == "critical" && isCVESignal(cue) {
				criticalCVEs++
			}
		}
	}

	return highestSeverity, criticalCVEs
}

func isCVESignal(cue report.RiskCue) bool {
	code := strings.ToLower(strings.TrimSpace(cue.Code))
	message := strings.ToLower(strings.TrimSpace(cue.Message))
	return strings.Contains(code, "cve") || strings.Contains(code, "vuln") || strings.Contains(message, "cve-") || strings.Contains(message, "vulnerability")
}

func riskSeverityRank(severity string) int {
	switch severity {
	case "critical":
		return 4
	case "high":
		return 3
	case "medium":
		return 2
	case "low":
		return 1
	default:
		return 0
	}
}

func countDeniedLicenses(reportData report.Report) int {
	if reportData.Summary != nil {
		return reportData.Summary.DeniedLicenseCount
	}
	count := 0
	for _, dependency := range reportData.Dependencies {
		if dependency.License != nil && dependency.License.Denied {
			count++
		}
	}
	return count
}

func buildCrossRepoDependencies(index map[string]map[string]struct{}) []CrossRepoDependency {
	items := make([]CrossRepoDependency, 0)
	for dependencyName, repoSet := range index {
		if len(repoSet) < 3 {
			continue
		}
		repositories := make([]string, 0, len(repoSet))
		for repoName := range repoSet {
			repositories = append(repositories, repoName)
		}
		sort.Strings(repositories)
		items = append(items, CrossRepoDependency{
			Name:         dependencyName,
			Repositories: repositories,
			Count:        len(repositories),
		})
	}

	sort.Slice(items, func(i, j int) bool {
		if items[i].Count != items[j].Count {
			return items[i].Count > items[j].Count
		}
		return items[i].Name < items[j].Name
	})

	return items
}
