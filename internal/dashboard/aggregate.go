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

type AggregateOptions struct {
	IncludeRemediationQueue bool
}

type crossRepoRepository struct {
	Label string
}

type repoSummaryContribution struct {
	TotalDeps                int
	TotalWasteCandidates     int
	CriticalCVEs             int
	VulnerabilityFindings    int
	ReachableVulnerabilities int
	RuntimeTraceData         bool
	RuntimeRegressionCount   int
}

func Aggregate(generatedAt time.Time, analyses []RepoAnalysis) Report {
	return AggregateWithOptions(generatedAt, analyses, AggregateOptions{})
}

func AggregateWithOptions(generatedAt time.Time, analyses []RepoAnalysis, options AggregateOptions) Report {
	results := make([]RepoResult, 0, len(analyses))
	crossRepoIndex := make(map[string]map[string]crossRepoRepository)
	repoNameCounts := countRepoNames(analyses)
	sourceWarnings := make([]string, 0)
	remediationItems := make([]RemediationItem, 0)
	summary := Summary{
		TotalRepos: len(analyses),
	}

	for _, analysis := range analyses {
		repoResult, contribution, warning := aggregateRepoAnalysis(analysis, repoNameCounts, crossRepoIndex)
		addSummaryContribution(&summary, contribution)
		if warning != "" {
			sourceWarnings = append(sourceWarnings, warning)
		}
		if options.IncludeRemediationQueue {
			if analysis.Err != nil {
				remediationItems = append(remediationItems, repoErrorRemediationItem(analysis.Input, repoNameCounts, analysis.Err))
			} else {
				remediationItems = append(remediationItems, repoRemediationItems(analysis, repoNameCounts)...)
			}
		}
		results = append(results, repoResult)
	}

	crossRepoDeps := buildCrossRepoDependencies(crossRepoIndex)
	summary.CrossRepoDuplicates = len(crossRepoDeps)
	if options.IncludeRemediationQueue {
		remediationItems = append(remediationItems, crossRepoRemediationItems(crossRepoDeps)...)
		remediationItems = dedupeAndSortRemediationItems(remediationItems)
	}

	return Report{
		GeneratedAt:      generatedAt.UTC(),
		Repos:            results,
		Summary:          summary,
		CrossRepoDeps:    crossRepoDeps,
		RemediationItems: remediationItems,
		SourceWarnings:   sourceWarnings,
	}
}

func aggregateRepoAnalysis(analysis RepoAnalysis, repoNameCounts map[string]int, crossRepoIndex map[string]map[string]crossRepoRepository) (RepoResult, repoSummaryContribution, string) {
	repoResult := baseRepoResult(analysis.Input)
	if analysis.Err != nil {
		repoResult.Error = analysis.Err.Error()
		return repoResult, repoSummaryContribution{}, analysis.Input.Name + ": " + analysis.Err.Error()
	}

	contribution := summarizeRepo(analysis.Report)
	populateRepoResult(&repoResult, analysis.Report, contribution)
	indexCrossRepoDependencies(crossRepoIndex, analysis.Input, repoNameCounts, analysis.Report.Dependencies)
	return repoResult, contribution, ""
}

func baseRepoResult(input RepoInput) RepoResult {
	result := RepoResult{
		Name:           input.Name,
		Path:           input.Path,
		RepoURL:        input.RepoURL,
		ResolvedCommit: input.ResolvedCommit,
		Language:       input.Language,
	}
	if !input.Revision.IsZero() {
		revision := input.Revision
		result.Revision = &revision
	}
	return result
}

func summarizeRepo(reportData report.Report) repoSummaryContribution {
	_, criticalCVEs := scanRiskSignals(reportData.Dependencies)
	vulnerabilityFindings, reachableVulnerabilities := countVulnerabilities(reportData)
	return repoSummaryContribution{
		TotalDeps:                len(reportData.Dependencies),
		TotalWasteCandidates:     countWasteCandidates(reportData.Dependencies),
		CriticalCVEs:             criticalCVEs,
		VulnerabilityFindings:    vulnerabilityFindings,
		ReachableVulnerabilities: reachableVulnerabilities,
		RuntimeTraceData:         reportHasRuntimeUsage(reportData),
		RuntimeRegressionCount:   runtimeRegressionCount(reportData),
	}
}

func populateRepoResult(result *RepoResult, reportData report.Report, contribution repoSummaryContribution) {
	topRiskSeverity, _ := scanRiskSignals(reportData.Dependencies)
	result.DependencyCount = contribution.TotalDeps
	result.WasteCandidateCount = contribution.TotalWasteCandidates
	if contribution.TotalDeps > 0 {
		result.WasteCandidatePercent = (float64(contribution.TotalWasteCandidates) / float64(contribution.TotalDeps)) * 100
	}
	result.TopRiskSeverity = topRiskSeverity
	result.CriticalCVEs = contribution.CriticalCVEs
	result.VulnerabilityFindings = contribution.VulnerabilityFindings
	result.ReachableVulnerabilities = contribution.ReachableVulnerabilities
	result.DeniedLicenseCount = countDeniedLicenses(reportData)
	result.RuntimeTraceData = contribution.RuntimeTraceData
	result.RuntimeRegressionCount = contribution.RuntimeRegressionCount
	result.RuntimeImprovementCount = runtimeImprovementCount(reportData)
	result.Warnings = append(result.Warnings, reportData.Warnings...)
}

func addSummaryContribution(summary *Summary, contribution repoSummaryContribution) {
	summary.TotalDeps += contribution.TotalDeps
	summary.TotalWasteCandidates += contribution.TotalWasteCandidates
	summary.CriticalCVEs += contribution.CriticalCVEs
	summary.VulnerabilityFindings += contribution.VulnerabilityFindings
	summary.ReachableVulnerabilities += contribution.ReachableVulnerabilities
	if contribution.RuntimeTraceData {
		summary.ReposWithRuntimeTraceData++
	}
	if contribution.RuntimeTraceData && contribution.RuntimeRegressionCount > 0 {
		summary.ReposWithRuntimeRegressions++
	}
}

func countVulnerabilities(reportData report.Report) (int, int) {
	if reportData.Summary != nil && reportData.Summary.Vulnerabilities != nil {
		return reportData.Summary.Vulnerabilities.TotalFindings, reportData.Summary.Vulnerabilities.ReachableFindings
	}
	total := 0
	reachable := 0
	for _, dependency := range reportData.Dependencies {
		for _, finding := range dependency.Vulnerabilities {
			total++
			if finding.Reachable {
				reachable++
			}
		}
	}
	return total, reachable
}

func indexCrossRepoDependencies(index map[string]map[string]crossRepoRepository, input RepoInput, repoNameCounts map[string]int, dependencies []report.DependencyReport) {
	repoID := repoIdentity(input)
	repoLabel := crossRepoRepositoryLabel(input, repoNameCounts)
	for _, dependency := range dependencies {
		name := strings.TrimSpace(dependency.Name)
		if name == "" {
			continue
		}
		if _, ok := index[name]; !ok {
			index[name] = make(map[string]crossRepoRepository)
		}
		index[name][repoID] = crossRepoRepository{Label: repoLabel}
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

func reportHasRuntimeUsage(reportData report.Report) bool {
	for _, dependency := range reportData.Dependencies {
		if dependency.RuntimeUsage != nil {
			return true
		}
	}
	return false
}

func runtimeRegressionCount(reportData report.Report) int {
	if reportData.BaselineComparison == nil {
		return 0
	}
	return len(reportData.BaselineComparison.RuntimeRegressions)
}

func runtimeImprovementCount(reportData report.Report) int {
	if reportData.BaselineComparison == nil {
		return 0
	}
	return len(reportData.BaselineComparison.RuntimeImprovements)
}

func countRepoNames(analyses []RepoAnalysis) map[string]int {
	counts := make(map[string]int, len(analyses))
	for _, analysis := range analyses {
		name := strings.TrimSpace(analysis.Input.Name)
		if name == "" {
			name = strings.TrimSpace(analysis.Input.Path)
		}
		counts[name]++
	}
	return counts
}

func repoIdentity(input RepoInput) string {
	path := strings.TrimSpace(input.Path)
	if path != "" {
		return path
	}
	return strings.TrimSpace(input.Name)
}

func crossRepoRepositoryLabel(input RepoInput, repoNameCounts map[string]int) string {
	name := strings.TrimSpace(input.Name)
	path := strings.TrimSpace(input.Path)
	if name == "" {
		return path
	}
	if repoNameCounts[name] > 1 && path != "" {
		return name + " (" + path + ")"
	}
	return name
}

func buildCrossRepoDependencies(index map[string]map[string]crossRepoRepository) []CrossRepoDependency {
	items := make([]CrossRepoDependency, 0)
	for dependencyName, repoSet := range index {
		if len(repoSet) < 3 {
			continue
		}
		repositories := make([]string, 0, len(repoSet))
		for _, repo := range repoSet {
			repositories = append(repositories, repo.Label)
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
