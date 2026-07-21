package dashboard

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/ben-ranford/lopper/internal/csvsanitize"
)

const (
	htmlTableBodyOpen  = "</tr></thead><tbody>"
	htmlTableBodyClose = "</tbody></table>"
	htmlPriorityCell   = "<td class=\"priority\">"
	htmlStatusCell     = "<td class=\"status\">"
	htmlWrapCell       = "<td class=\"wrap\">"
)

func FormatReport(reportData Report, format Format) (string, error) {
	switch format {
	case FormatJSON:
		payload, err := json.MarshalIndent(reportData, "", "  ")
		if err != nil {
			return "", err
		}
		return string(payload) + "\n", nil
	case FormatCSV:
		return formatCSV(reportData)
	case FormatHTML:
		return formatHTML(reportData), nil
	case FormatSlackSummary:
		return formatTeamSummary(reportData, "slack"), nil
	case FormatTeamsSummary:
		return formatTeamSummary(reportData, "teams"), nil
	case FormatCycloneDXJSON:
		return formatPortfolioCycloneDX(reportData)
	default:
		return "", fmt.Errorf("%w: %s", ErrUnknownFormat, format)
	}
}

func formatCSV(reportData Report) (string, error) {
	buffer := &bytes.Buffer{}
	writer := csv.NewWriter(buffer)

	if err := writeDashboardSummaryCSV(writer.Write, reportData); err != nil {
		return "", err
	}
	if err := writeDashboardRepoRowsCSV(writer.Write, reportData.Repos); err != nil {
		return "", err
	}
	if err := writeDashboardRemediationRowsCSV(writer.Write, reportData.RemediationItems); err != nil {
		return "", err
	}
	if err := writeDashboardCrossRepoRowsCSV(writer.Write, reportData.CrossRepoDeps); err != nil {
		return "", err
	}
	if err := writeDashboardBaselineRowsCSV(writer.Write, reportData.BaselineComparison); err != nil {
		return "", err
	}

	writer.Flush()
	if err := writer.Error(); err != nil {
		return "", err
	}
	return buffer.String(), nil
}

func writeDashboardSummaryCSV(write func([]string) error, reportData Report) error {
	summaryRows := [][]string{
		{"generated_at", reportData.GeneratedAt.Format("2006-01-02T15:04:05Z07:00")},
		{"total_repos", fmt.Sprintf("%d", reportData.Summary.TotalRepos)},
		{"total_deps", fmt.Sprintf("%d", reportData.Summary.TotalDeps)},
		{"total_waste_candidates", fmt.Sprintf("%d", reportData.Summary.TotalWasteCandidates)},
		{"cross_repo_duplicates", fmt.Sprintf("%d", reportData.Summary.CrossRepoDuplicates)},
		{"critical_cves", fmt.Sprintf("%d", reportData.Summary.CriticalCVEs)},
		{"vulnerability_findings", fmt.Sprintf("%d", reportData.Summary.VulnerabilityFindings)},
		{"reachable_vulnerabilities", fmt.Sprintf("%d", reportData.Summary.ReachableVulnerabilities)},
		{"repos_with_runtime_trace_data", fmt.Sprintf("%d", reportData.Summary.ReposWithRuntimeTraceData)},
		{"repos_with_runtime_regressions", fmt.Sprintf("%d", reportData.Summary.ReposWithRuntimeRegressions)},
	}
	for _, row := range summaryRows {
		if err := write(csvsanitize.EscapeLeadingFormulaRow(row)); err != nil {
			return err
		}
	}
	return write(nil)
}

func writeDashboardRepoRowsCSV(write func([]string) error, repos []RepoResult) error {
	if err := write(csvsanitize.EscapeLeadingFormulaRow([]string{
		"repo_name",
		"repo_path",
		"repo_url",
		"revision",
		"resolved_commit",
		"language",
		"dependency_count",
		"waste_candidate_count",
		"waste_candidate_percent",
		"top_risk_severity",
		"critical_cves",
		"vulnerability_findings",
		"reachable_vulnerabilities",
		"denied_license_count",
		"runtime_trace_data",
		"runtime_regression_count",
		"runtime_improvement_count",
		"error",
	})); err != nil {
		return err
	}

	for _, repoResult := range repos {
		if err := write(csvsanitize.EscapeLeadingFormulaRow([]string{
			repoResult.Name,
			repoResult.Path,
			repoResult.RepoURL,
			formatRepoRevision(repoResult.Revision),
			repoResult.ResolvedCommit,
			repoResult.Language,
			fmt.Sprintf("%d", repoResult.DependencyCount),
			fmt.Sprintf("%d", repoResult.WasteCandidateCount),
			fmt.Sprintf("%.2f", repoResult.WasteCandidatePercent),
			repoResult.TopRiskSeverity,
			fmt.Sprintf("%d", repoResult.CriticalCVEs),
			fmt.Sprintf("%d", repoResult.VulnerabilityFindings),
			fmt.Sprintf("%d", repoResult.ReachableVulnerabilities),
			fmt.Sprintf("%d", repoResult.DeniedLicenseCount),
			fmt.Sprintf("%t", repoResult.RuntimeTraceData),
			fmt.Sprintf("%d", repoResult.RuntimeRegressionCount),
			fmt.Sprintf("%d", repoResult.RuntimeImprovementCount),
			repoResult.Error,
		})); err != nil {
			return err
		}
	}
	return nil
}

func writeDashboardRemediationRowsCSV(write func([]string) error, items []RemediationItem) error {
	if len(items) == 0 {
		return nil
	}
	if err := write(nil); err != nil {
		return err
	}
	if err := write(csvsanitize.EscapeLeadingFormulaRow([]string{
		"remediation_id",
		"baseline_status",
		"repo",
		"repo_path",
		"dependency",
		"category",
		"owner",
		"team",
		"due",
		"status",
		"routing_source",
		"severity",
		"priority",
		"evidence",
		"suggested_action",
	})); err != nil {
		return err
	}
	for _, item := range items {
		if err := write(csvsanitize.EscapeLeadingFormulaRow([]string{
			item.ID,
			item.BaselineStatus,
			item.Repo,
			item.RepoPath,
			item.Dependency,
			item.Category,
			item.Owner,
			item.Team,
			item.Due,
			item.Status,
			item.RoutingSource,
			item.Severity,
			item.Priority,
			joinDashboardCSVValues(item.Evidence),
			item.SuggestedAction,
		})); err != nil {
			return err
		}
	}
	return nil
}

func writeDashboardCrossRepoRowsCSV(write func([]string) error, dependencies []CrossRepoDependency) error {
	if len(dependencies) == 0 {
		return nil
	}
	if err := write(nil); err != nil {
		return err
	}
	if err := write(csvsanitize.EscapeLeadingFormulaRow([]string{"dependency_name", "repo_count", "repositories"})); err != nil {
		return err
	}
	for _, dependency := range dependencies {
		repositories := make([]string, len(dependency.Repositories))
		for i, repo := range dependency.Repositories {
			repositories[i] = csvsanitize.EscapeLeadingFormula(repo)
		}
		if err := write(csvsanitize.EscapeLeadingFormulaRow([]string{
			dependency.Name,
			fmt.Sprintf("%d", dependency.Count),
			strings.Join(repositories, "|"),
		})); err != nil {
			return err
		}
	}
	return nil
}

func writeDashboardBaselineRowsCSV(write func([]string) error, comparison *BaselineComparison) error {
	if comparison == nil {
		return nil
	}
	if err := write(nil); err != nil {
		return err
	}
	if err := write(csvsanitize.EscapeLeadingFormulaRow([]string{"baseline_key", comparison.BaselineKey})); err != nil {
		return err
	}
	if comparison.CurrentKey != "" {
		if err := write(csvsanitize.EscapeLeadingFormulaRow([]string{"current_key", comparison.CurrentKey})); err != nil {
			return err
		}
	}
	summaryRows := [][]string{
		{"total_repos_delta", fmt.Sprintf("%d", comparison.SummaryDelta.TotalReposDelta)},
		{"total_deps_delta", fmt.Sprintf("%d", comparison.SummaryDelta.TotalDepsDelta)},
		{"total_waste_candidates_delta", fmt.Sprintf("%d", comparison.SummaryDelta.TotalWasteCandidatesDelta)},
		{"cross_repo_duplicates_delta", fmt.Sprintf("%d", comparison.SummaryDelta.CrossRepoDuplicatesDelta)},
		{"critical_cves_delta", fmt.Sprintf("%d", comparison.SummaryDelta.CriticalCVEsDelta)},
		{"vulnerability_findings_delta", fmt.Sprintf("%d", comparison.SummaryDelta.VulnerabilityFindingsDelta)},
		{"reachable_vulnerabilities_delta", fmt.Sprintf("%d", comparison.SummaryDelta.ReachableVulnerabilitiesDelta)},
		{"repos_with_runtime_trace_data_delta", fmt.Sprintf("%d", comparison.SummaryDelta.ReposWithRuntimeTraceDataDelta)},
		{"repos_with_runtime_regressions_delta", fmt.Sprintf("%d", comparison.SummaryDelta.ReposWithRuntimeRegressionsDelta)},
	}
	for _, row := range summaryRows {
		if err := write(csvsanitize.EscapeLeadingFormulaRow(row)); err != nil {
			return err
		}
	}
	if len(comparison.RepoDeltas) == 0 {
		return writeDashboardBaselineRemediationRowsCSV(write, comparison.RemediationItemDeltas)
	}
	if err := write(nil); err != nil {
		return err
	}
	if err := write(csvsanitize.EscapeLeadingFormulaRow([]string{
		"repo_name",
		"repo_path",
		"kind",
		"dependency_count_delta",
		"waste_candidate_count_delta",
		"waste_candidate_percent_delta",
		"critical_cves_delta",
		"vulnerability_findings_delta",
		"reachable_vulnerabilities_delta",
		"denied_license_count_delta",
		"runtime_regression_count_delta",
		"runtime_improvement_count_delta",
		"current_error",
		"baseline_error",
	})); err != nil {
		return err
	}
	for _, delta := range comparison.RepoDeltas {
		if err := write(csvsanitize.EscapeLeadingFormulaRow([]string{
			delta.Name,
			delta.Path,
			string(delta.Kind),
			fmt.Sprintf("%d", delta.DependencyCountDelta),
			fmt.Sprintf("%d", delta.WasteCandidateCountDelta),
			fmt.Sprintf("%.2f", delta.WasteCandidatePercentDelta),
			fmt.Sprintf("%d", delta.CriticalCVEsDelta),
			fmt.Sprintf("%d", delta.VulnerabilityFindingsDelta),
			fmt.Sprintf("%d", delta.ReachableVulnerabilitiesDelta),
			fmt.Sprintf("%d", delta.DeniedLicenseCountDelta),
			fmt.Sprintf("%d", delta.RuntimeRegressionCountDelta),
			fmt.Sprintf("%d", delta.RuntimeImprovementCountDelta),
			delta.CurrentError,
			delta.BaselineError,
		})); err != nil {
			return err
		}
	}
	return writeDashboardBaselineRemediationRowsCSV(write, comparison.RemediationItemDeltas)
}

func writeDashboardBaselineRemediationRowsCSV(write func([]string) error, deltas []RemediationItemDelta) error {
	if len(deltas) == 0 {
		return nil
	}
	if err := write(nil); err != nil {
		return err
	}
	if err := write(csvsanitize.EscapeLeadingFormulaRow([]string{
		"remediation_id",
		"kind",
		"repo",
		"repo_path",
		"dependency",
		"category",
		"owner",
		"team",
		"due",
		"status",
		"routing_source",
		"severity",
		"priority",
		"baseline_severity",
		"baseline_priority",
		"evidence",
		"suggested_action",
	})); err != nil {
		return err
	}
	for _, delta := range deltas {
		if err := write(csvsanitize.EscapeLeadingFormulaRow([]string{
			delta.ID,
			string(delta.Kind),
			delta.Repo,
			delta.RepoPath,
			delta.Dependency,
			delta.Category,
			delta.Owner,
			delta.Team,
			delta.Due,
			delta.Status,
			delta.RoutingSource,
			delta.Severity,
			delta.Priority,
			delta.BaselineSeverity,
			delta.BaselinePriority,
			joinDashboardCSVValues(delta.Evidence),
			delta.SuggestedAction,
		})); err != nil {
			return err
		}
	}
	return nil
}

func formatHTML(reportData Report) string {
	var buffer strings.Builder
	buffer.WriteString("<!doctype html><html lang=\"en\"><head><meta charset=\"utf-8\">")
	buffer.WriteString("<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">")
	buffer.WriteString("<title>Lopper Dashboard</title>")
	buffer.WriteString("<style>")
	buffer.WriteString("body{font-family:system-ui,-apple-system,Segoe UI,Roboto,sans-serif;margin:24px;color:#111827;background:#f8fafc}")
	buffer.WriteString("h1,h2{margin:0 0 12px}")
	buffer.WriteString(".meta{margin:0 0 20px;color:#475569}")
	buffer.WriteString("table{width:100%;border-collapse:collapse;background:#fff;border:1px solid #e2e8f0;margin-bottom:20px}")
	buffer.WriteString("th,td{padding:10px;border-bottom:1px solid #e2e8f0;text-align:left;font-size:14px}")
	buffer.WriteString("th{background:#f1f5f9}")
	buffer.WriteString(".card{background:#fff;border:1px solid #e2e8f0;padding:16px;margin-bottom:20px}")
	buffer.WriteString(".grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(180px,1fr));gap:12px}")
	buffer.WriteString(".metric{background:#f8fafc;border:1px solid #e2e8f0;padding:12px;border-radius:8px}")
	buffer.WriteString(".metric strong{display:block;font-size:22px}")
	buffer.WriteString(".priority{text-transform:capitalize;font-weight:600}.status{text-transform:capitalize;color:#334155}.wrap{max-width:360px;white-space:normal}")
	buffer.WriteString("</style></head><body>")
	buffer.WriteString("<h1>Lopper Org Dashboard</h1>")
	buffer.WriteString("<p class=\"meta\">Generated at ")
	buffer.WriteString(html.EscapeString(reportData.GeneratedAt.Format("2006-01-02 15:04:05 MST")))
	buffer.WriteString("</p>")
	buffer.WriteString("<section class=\"card\"><div class=\"grid\">")
	buffer.WriteString(metricHTML("Repos", fmt.Sprintf("%d", reportData.Summary.TotalRepos)))
	buffer.WriteString(metricHTML("Dependencies", fmt.Sprintf("%d", reportData.Summary.TotalDeps)))
	buffer.WriteString(metricHTML("Waste Candidates", fmt.Sprintf("%d", reportData.Summary.TotalWasteCandidates)))
	buffer.WriteString(metricHTML("Cross-Repo Duplicates", fmt.Sprintf("%d", reportData.Summary.CrossRepoDuplicates)))
	buffer.WriteString(metricHTML("Critical CVEs", fmt.Sprintf("%d", reportData.Summary.CriticalCVEs)))
	buffer.WriteString(metricHTML("Vulnerability Findings", fmt.Sprintf("%d", reportData.Summary.VulnerabilityFindings)))
	buffer.WriteString(metricHTML("Reachable Vulnerabilities", fmt.Sprintf("%d", reportData.Summary.ReachableVulnerabilities)))
	buffer.WriteString(metricHTML("Runtime Trace Repos", fmt.Sprintf("%d", reportData.Summary.ReposWithRuntimeTraceData)))
	buffer.WriteString(metricHTML("Runtime Regression Repos", fmt.Sprintf("%d", reportData.Summary.ReposWithRuntimeRegressions)))
	buffer.WriteString("</div></section>")

	buffer.WriteString(formatDashboardRemediationHTML(reportData.RemediationItems))

	buffer.WriteString("<h2>Per-Repo Summary</h2><table><thead><tr>")
	buffer.WriteString("<th>Repo</th><th>Path</th><th>Repo URL</th><th>Revision</th><th>Resolved Commit</th><th>Language</th><th>Deps</th><th>Waste Candidates</th><th>Waste %</th><th>Top Risk</th><th>Critical CVEs</th><th>Vuln Findings</th><th>Reachable Vulns</th><th>Denied Licenses</th><th>Runtime Trace</th><th>Runtime Regressions</th><th>Runtime Improvements</th><th>Error</th>")
	buffer.WriteString("</tr></thead><tbody>")
	for _, repoResult := range reportData.Repos {
		buffer.WriteString("<tr>")
		buffer.WriteString("<td>" + html.EscapeString(repoResult.Name) + "</td>")
		buffer.WriteString("<td>" + html.EscapeString(repoResult.Path) + "</td>")
		buffer.WriteString("<td>" + html.EscapeString(repoResult.RepoURL) + "</td>")
		buffer.WriteString("<td>" + html.EscapeString(formatRepoRevision(repoResult.Revision)) + "</td>")
		buffer.WriteString("<td>" + html.EscapeString(repoResult.ResolvedCommit) + "</td>")
		buffer.WriteString("<td>" + html.EscapeString(repoResult.Language) + "</td>")
		buffer.WriteString("<td>" + fmt.Sprintf("%d", repoResult.DependencyCount) + "</td>")
		buffer.WriteString("<td>" + fmt.Sprintf("%d", repoResult.WasteCandidateCount) + "</td>")
		buffer.WriteString("<td>" + fmt.Sprintf("%.1f%%", repoResult.WasteCandidatePercent) + "</td>")
		buffer.WriteString("<td>" + html.EscapeString(repoResult.TopRiskSeverity) + "</td>")
		buffer.WriteString("<td>" + fmt.Sprintf("%d", repoResult.CriticalCVEs) + "</td>")
		buffer.WriteString("<td>" + fmt.Sprintf("%d", repoResult.VulnerabilityFindings) + "</td>")
		buffer.WriteString("<td>" + fmt.Sprintf("%d", repoResult.ReachableVulnerabilities) + "</td>")
		buffer.WriteString("<td>" + fmt.Sprintf("%d", repoResult.DeniedLicenseCount) + "</td>")
		buffer.WriteString("<td>" + fmt.Sprintf("%t", repoResult.RuntimeTraceData) + "</td>")
		buffer.WriteString("<td>" + fmt.Sprintf("%d", repoResult.RuntimeRegressionCount) + "</td>")
		buffer.WriteString("<td>" + fmt.Sprintf("%d", repoResult.RuntimeImprovementCount) + "</td>")
		buffer.WriteString("<td>" + html.EscapeString(repoResult.Error) + "</td>")
		buffer.WriteString("</tr>")
	}
	buffer.WriteString("</tbody></table>")

	if len(reportData.CrossRepoDeps) > 0 {
		buffer.WriteString("<h2>Cross-Repo Duplicate Dependencies (3+ repos)</h2><table><thead><tr>")
		buffer.WriteString("<th>Dependency</th><th>Repo Count</th><th>Repos</th>")
		buffer.WriteString(htmlTableBodyOpen)
		for _, dependency := range reportData.CrossRepoDeps {
			buffer.WriteString("<tr>")
			buffer.WriteString("<td>" + html.EscapeString(dependency.Name) + "</td>")
			buffer.WriteString("<td>" + fmt.Sprintf("%d", dependency.Count) + "</td>")
			buffer.WriteString("<td>" + html.EscapeString(strings.Join(dependency.Repositories, ", ")) + "</td>")
			buffer.WriteString("</tr>")
		}
		buffer.WriteString(htmlTableBodyClose)
	}

	buffer.WriteString(formatDashboardBaselineHTML(reportData.BaselineComparison))

	buffer.WriteString("</body></html>\n")
	return buffer.String()
}

func formatDashboardRemediationHTML(items []RemediationItem) string {
	if len(items) == 0 {
		return ""
	}
	var buffer strings.Builder
	buffer.WriteString("<h2>Remediation Queue</h2><table><thead><tr>")
	buffer.WriteString("<th>Priority</th><th>Category</th><th>Repo</th><th>Dependency</th><th>Owner</th><th>Team</th><th>Due</th><th>Status</th><th>Evidence</th><th>Suggested Action</th><th>Baseline</th><th>ID</th>")
	buffer.WriteString(htmlTableBodyOpen)
	for _, item := range items {
		buffer.WriteString("<tr>")
		buffer.WriteString(htmlPriorityCell + html.EscapeString(item.Priority) + "</td>")
		buffer.WriteString("<td>" + html.EscapeString(item.Category) + "</td>")
		buffer.WriteString("<td>" + html.EscapeString(item.Repo) + "</td>")
		buffer.WriteString("<td>" + html.EscapeString(item.Dependency) + "</td>")
		buffer.WriteString("<td>" + html.EscapeString(item.Owner) + "</td>")
		buffer.WriteString("<td>" + html.EscapeString(item.Team) + "</td>")
		buffer.WriteString("<td>" + html.EscapeString(item.Due) + "</td>")
		buffer.WriteString(htmlStatusCell + html.EscapeString(item.Status) + "</td>")
		buffer.WriteString(htmlWrapCell + html.EscapeString(strings.Join(item.Evidence, "; ")) + "</td>")
		buffer.WriteString(htmlWrapCell + html.EscapeString(item.SuggestedAction) + "</td>")
		buffer.WriteString(htmlStatusCell + html.EscapeString(item.BaselineStatus) + "</td>")
		buffer.WriteString("<td>" + html.EscapeString(item.ID) + "</td>")
		buffer.WriteString("</tr>")
	}
	buffer.WriteString(htmlTableBodyClose)
	return buffer.String()
}

func formatDashboardBaselineHTML(comparison *BaselineComparison) string {
	if comparison == nil {
		return ""
	}
	var buffer strings.Builder
	buffer.WriteString("<h2>Baseline Comparison</h2>")
	buffer.WriteString("<section class=\"card\"><div class=\"grid\">")
	buffer.WriteString(metricHTML("Baseline", comparison.BaselineKey))
	buffer.WriteString(metricHTML("Repos Delta", fmt.Sprintf("%+d", comparison.SummaryDelta.TotalReposDelta)))
	buffer.WriteString(metricHTML("Deps Delta", fmt.Sprintf("%+d", comparison.SummaryDelta.TotalDepsDelta)))
	buffer.WriteString(metricHTML("Waste Candidates Delta", fmt.Sprintf("%+d", comparison.SummaryDelta.TotalWasteCandidatesDelta)))
	buffer.WriteString(metricHTML("Cross-Repo Delta", fmt.Sprintf("%+d", comparison.SummaryDelta.CrossRepoDuplicatesDelta)))
	buffer.WriteString(metricHTML("Critical CVEs Delta", fmt.Sprintf("%+d", comparison.SummaryDelta.CriticalCVEsDelta)))
	buffer.WriteString(metricHTML("Vuln Findings Delta", fmt.Sprintf("%+d", comparison.SummaryDelta.VulnerabilityFindingsDelta)))
	buffer.WriteString(metricHTML("Reachable Vulns Delta", fmt.Sprintf("%+d", comparison.SummaryDelta.ReachableVulnerabilitiesDelta)))
	buffer.WriteString(metricHTML("Runtime Trace Repos Delta", fmt.Sprintf("%+d", comparison.SummaryDelta.ReposWithRuntimeTraceDataDelta)))
	buffer.WriteString(metricHTML("Runtime Regression Repos Delta", fmt.Sprintf("%+d", comparison.SummaryDelta.ReposWithRuntimeRegressionsDelta)))
	buffer.WriteString("</div></section>")

	if len(comparison.RepoDeltas) > 0 {
		buffer.WriteString("<table><thead><tr>")
		buffer.WriteString("<th>Repo</th><th>Path</th><th>Kind</th><th>Deps Δ</th><th>Waste Δ</th><th>Waste % Δ</th><th>Critical CVEs Δ</th><th>Vuln Findings Δ</th><th>Reachable Vulns Δ</th><th>Denied Licenses Δ</th><th>Runtime Regressions Δ</th><th>Runtime Improvements Δ</th><th>Current Error</th><th>Baseline Error</th>")
		buffer.WriteString(htmlTableBodyOpen)
		for _, delta := range comparison.RepoDeltas {
			buffer.WriteString("<tr>")
			buffer.WriteString("<td>" + html.EscapeString(delta.Name) + "</td>")
			buffer.WriteString("<td>" + html.EscapeString(delta.Path) + "</td>")
			buffer.WriteString("<td>" + html.EscapeString(string(delta.Kind)) + "</td>")
			buffer.WriteString("<td>" + fmt.Sprintf("%+d", delta.DependencyCountDelta) + "</td>")
			buffer.WriteString("<td>" + fmt.Sprintf("%+d", delta.WasteCandidateCountDelta) + "</td>")
			buffer.WriteString("<td>" + fmt.Sprintf("%+0.2f%%", delta.WasteCandidatePercentDelta) + "</td>")
			buffer.WriteString("<td>" + fmt.Sprintf("%+d", delta.CriticalCVEsDelta) + "</td>")
			buffer.WriteString("<td>" + fmt.Sprintf("%+d", delta.VulnerabilityFindingsDelta) + "</td>")
			buffer.WriteString("<td>" + fmt.Sprintf("%+d", delta.ReachableVulnerabilitiesDelta) + "</td>")
			buffer.WriteString("<td>" + fmt.Sprintf("%+d", delta.DeniedLicenseCountDelta) + "</td>")
			buffer.WriteString("<td>" + fmt.Sprintf("%+d", delta.RuntimeRegressionCountDelta) + "</td>")
			buffer.WriteString("<td>" + fmt.Sprintf("%+d", delta.RuntimeImprovementCountDelta) + "</td>")
			buffer.WriteString("<td>" + html.EscapeString(delta.CurrentError) + "</td>")
			buffer.WriteString("<td>" + html.EscapeString(delta.BaselineError) + "</td>")
			buffer.WriteString("</tr>")
		}
		buffer.WriteString(htmlTableBodyClose)
	}

	buffer.WriteString(formatDashboardBaselineRemediationHTML(comparison.RemediationItemDeltas))

	return buffer.String()
}

func formatDashboardBaselineRemediationHTML(deltas []RemediationItemDelta) string {
	if len(deltas) == 0 {
		return ""
	}
	var buffer strings.Builder
	buffer.WriteString("<h2>Remediation Queue Baseline</h2><table><thead><tr>")
	buffer.WriteString("<th>Kind</th><th>Priority</th><th>Baseline Priority</th><th>Category</th><th>Repo</th><th>Dependency</th><th>Owner</th><th>Team</th><th>Status</th><th>Suggested Action</th><th>ID</th>")
	buffer.WriteString(htmlTableBodyOpen)
	for _, delta := range deltas {
		buffer.WriteString("<tr>")
		buffer.WriteString(htmlStatusCell + html.EscapeString(string(delta.Kind)) + "</td>")
		buffer.WriteString(htmlPriorityCell + html.EscapeString(delta.Priority) + "</td>")
		buffer.WriteString(htmlPriorityCell + html.EscapeString(delta.BaselinePriority) + "</td>")
		buffer.WriteString("<td>" + html.EscapeString(delta.Category) + "</td>")
		buffer.WriteString("<td>" + html.EscapeString(delta.Repo) + "</td>")
		buffer.WriteString("<td>" + html.EscapeString(delta.Dependency) + "</td>")
		buffer.WriteString("<td>" + html.EscapeString(delta.Owner) + "</td>")
		buffer.WriteString("<td>" + html.EscapeString(delta.Team) + "</td>")
		buffer.WriteString(htmlStatusCell + html.EscapeString(delta.Status) + "</td>")
		buffer.WriteString(htmlWrapCell + html.EscapeString(delta.SuggestedAction) + "</td>")
		buffer.WriteString("<td>" + html.EscapeString(delta.ID) + "</td>")
		buffer.WriteString("</tr>")
	}
	buffer.WriteString(htmlTableBodyClose)
	return buffer.String()
}

func metricHTML(label, value string) string {
	return "<div class=\"metric\"><span>" + html.EscapeString(label) + "</span><strong>" + html.EscapeString(value) + "</strong></div>"
}

func formatRepoRevision(revision *RepoRevision) string {
	if revision == nil || revision.IsZero() {
		return ""
	}
	return revision.Kind() + ":" + revision.Value()
}

func joinDashboardCSVValues(values []string) string {
	escaped := make([]string, 0, len(values))
	for _, value := range values {
		escaped = append(escaped, csvsanitize.EscapeLeadingFormula(value))
	}
	return strings.Join(escaped, "|")
}

func formatTeamSummary(reportData Report, _ string) string {
	items := append([]RemediationItem{}, reportData.RemediationItems...)
	sortRemediationItems(items)
	var buffer strings.Builder
	buffer.WriteString("Lopper remediation summary")
	buffer.WriteString("\n")
	buffer.WriteString(fmt.Sprintf("Repos: %d | Items: %d | Reachable vulnerabilities: %d\n", reportData.Summary.TotalRepos, len(items), reportData.Summary.ReachableVulnerabilities))
	grouped := remediationByTeam(items)
	teams := make([]string, 0, len(grouped))
	for team := range grouped {
		teams = append(teams, team)
	}
	sort.Strings(teams)
	for _, team := range teams {
		buffer.WriteString("\n")
		buffer.WriteString(team)
		buffer.WriteString("\n")
		for _, item := range grouped[team] {
			line := fmt.Sprintf("- [%s] %s/%s %s", firstNonBlank(item.Priority, item.Severity, "unknown"), item.Repo, item.Category, item.SuggestedAction)
			if item.Dependency != "" {
				line += " (" + item.Dependency + ")"
			}
			if item.Owner != "" {
				line += " owner=" + item.Owner
			}
			if item.Due != "" {
				line += " due=" + item.Due
			}
			buffer.WriteString(line)
			buffer.WriteString("\n")
		}
	}
	return buffer.String()
}

func remediationByTeam(items []RemediationItem) map[string][]RemediationItem {
	grouped := make(map[string][]RemediationItem)
	for _, item := range items {
		team := firstNonBlank(item.Team, item.Owner, "unassigned")
		grouped[team] = append(grouped[team], item)
	}
	return grouped
}

func formatPortfolioCycloneDX(reportData Report) (string, error) {
	bom := map[string]any{
		"$schema":     "http://cyclonedx.org/schema/bom-1.6.schema.json",
		"bomFormat":   "CycloneDX",
		"specVersion": "1.6",
		"version":     1,
		"metadata": map[string]any{
			"timestamp": reportData.GeneratedAt.Format("2006-01-02T15:04:05Z07:00"),
			"component": map[string]any{
				"type": "application",
				"name": "lopper-dashboard",
			},
		},
		"components": portfolioCycloneDXComponents(reportData),
		"properties": []map[string]string{
			{"name": "lopper:dashboard:total-repos", "value": fmt.Sprintf("%d", reportData.Summary.TotalRepos)},
			{"name": "lopper:dashboard:partial-failures", "value": fmt.Sprintf("%d", dashboardErrorCount(reportData.Repos))},
		},
	}
	payload, err := json.MarshalIndent(bom, "", "  ")
	if err != nil {
		return "", err
	}
	return string(payload) + "\n", nil
}

func portfolioCycloneDXComponents(reportData Report) []map[string]any {
	components := make([]map[string]any, 0, len(reportData.Repos)+len(reportData.PortfolioComponents))
	repos := sortPortfolioCycloneDXRepos(reportData.Repos)
	deps := sortPortfolioCycloneDXComponents(reportData.PortfolioComponents)
	baseRefs := make([]string, 0, len(repos)+len(deps))
	for _, repo := range repos {
		baseRefs = append(baseRefs, portfolioRepoRef(repo))
	}
	for _, dep := range deps {
		baseRefs = append(baseRefs, portfolioDependencyRef(dep))
	}
	refAllocator := newPortfolioRefAllocator(baseRefs)
	for _, repo := range repos {
		component := map[string]any{
			"type":    "application",
			"name":    repo.Name,
			"bom-ref": refAllocator.allocate(portfolioRepoRef(repo)),
		}
		if repo.ResolvedCommit != "" {
			component["version"] = repo.ResolvedCommit
		}
		components = append(components, component)
	}
	for _, dep := range deps {
		component := map[string]any{
			"type":    "library",
			"name":    dep.Name,
			"bom-ref": refAllocator.allocate(portfolioDependencyRef(dep)),
			"properties": []map[string]string{
				{"name": "lopper:repo", "value": dep.Repo},
				{"name": "lopper:language", "value": dep.Language},
				{"name": "lopper:ecosystem", "value": dep.Ecosystem},
			},
		}
		if dep.Version != "" {
			component["version"] = dep.Version
		}
		if dep.PURL != "" {
			component["purl"] = dep.PURL
		}
		components = append(components, component)
	}
	return components
}

func dashboardErrorCount(repos []RepoResult) int {
	count := 0
	for _, repo := range repos {
		if strings.TrimSpace(repo.Error) != "" {
			count++
		}
	}
	return count
}

func portfolioRepoRef(repo RepoResult) string {
	parts := []string{repo.Name}
	if path := stablePortfolioRefPath(repo.Path); path != "" {
		parts = append(parts, path)
	}
	return "lopper:repo:" + joinPortfolioRefParts(parts...)
}

func portfolioDependencyRef(dep PortfolioComponent) string {
	parts := []string{dep.Repo}
	if path := stablePortfolioRefPath(dep.RepoPath); path != "" {
		parts = append(parts, path)
	}
	parts = append(parts, dep.Language, dep.Name, dep.Version)
	return "lopper:dependency:" + joinPortfolioRefParts(parts...)
}

type portfolioRefAllocator struct {
	reserved map[string]struct{}
	used     map[string]struct{}
}

func newPortfolioRefAllocator(bases []string) portfolioRefAllocator {
	reserved := make(map[string]struct{}, len(bases))
	for _, base := range bases {
		reserved[base] = struct{}{}
	}
	return portfolioRefAllocator{
		reserved: reserved,
		used:     make(map[string]struct{}, len(bases)),
	}
}

func (a *portfolioRefAllocator) allocate(base string) string {
	if _, exists := a.used[base]; !exists {
		a.used[base] = struct{}{}
		return base
	}
	for suffix := 2; ; suffix++ {
		candidate := base + ":" + strconv.Itoa(suffix)
		if _, reserved := a.reserved[candidate]; reserved {
			continue
		}
		if _, exists := a.used[candidate]; exists {
			continue
		}
		a.used[candidate] = struct{}{}
		return candidate
	}
}

func joinPortfolioRefParts(parts ...string) string {
	encoded := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		encoded = append(encoded, escapePortfolioRefPart(part))
	}
	return strings.Join(encoded, ":")
}

func escapePortfolioRefPart(value string) string {
	escaped := url.PathEscape(value)
	return strings.ReplaceAll(escaped, ":", "%3A")
}

func stablePortfolioRefPath(value string) string {
	trimmed := filepath.ToSlash(strings.TrimSpace(value))
	trimmed = strings.TrimPrefix(trimmed, "./")
	if trimmed == "" || filepath.IsAbs(value) {
		return ""
	}
	return strings.TrimPrefix(trimmed, "/")
}

func sortPortfolioCycloneDXComponents(components []PortfolioComponent) []PortfolioComponent {
	items := append([]PortfolioComponent{}, components...)
	sort.Slice(items, func(i, j int) bool {
		left := portfolioComponentRefSortKey(items[i])
		right := portfolioComponentRefSortKey(items[j])
		return left < right
	})
	return items
}

func sortPortfolioCycloneDXRepos(repos []RepoResult) []RepoResult {
	items := append([]RepoResult{}, repos...)
	sort.Slice(items, func(i, j int) bool {
		left := portfolioRepoRef(items[i]) + "\x00" + strings.TrimSpace(items[i].ResolvedCommit)
		right := portfolioRepoRef(items[j]) + "\x00" + strings.TrimSpace(items[j].ResolvedCommit)
		return left < right
	})
	return items
}

func portfolioComponentRefSortKey(component PortfolioComponent) string {
	parts := []string{
		component.Repo,
		component.Language,
		component.Name,
		component.Version,
		component.PURL,
		component.Ecosystem,
		component.RepoPath,
	}
	var key strings.Builder
	for _, part := range parts {
		part = strings.TrimSpace(part)
		key.WriteString(strconv.Itoa(len(part)))
		key.WriteByte(':')
		key.WriteString(url.PathEscape(part))
	}
	return key.String()
}
