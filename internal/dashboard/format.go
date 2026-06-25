package dashboard

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"html"
	"strings"

	"github.com/ben-ranford/lopper/internal/csvsanitize"
)

const (
	htmlTableBodyOpen  = "</tr></thead><tbody>"
	htmlTableBodyClose = "</tbody></table>"
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
		{"repos_with_runtime_trace_data_delta", fmt.Sprintf("%d", comparison.SummaryDelta.ReposWithRuntimeTraceDataDelta)},
		{"repos_with_runtime_regressions_delta", fmt.Sprintf("%d", comparison.SummaryDelta.ReposWithRuntimeRegressionsDelta)},
	}
	for _, row := range summaryRows {
		if err := write(csvsanitize.EscapeLeadingFormulaRow(row)); err != nil {
			return err
		}
	}
	if len(comparison.RepoDeltas) == 0 {
		return nil
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
			fmt.Sprintf("%d", delta.DeniedLicenseCountDelta),
			fmt.Sprintf("%d", delta.RuntimeRegressionCountDelta),
			fmt.Sprintf("%d", delta.RuntimeImprovementCountDelta),
			delta.CurrentError,
			delta.BaselineError,
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
	buffer.WriteString(metricHTML("Runtime Trace Repos", fmt.Sprintf("%d", reportData.Summary.ReposWithRuntimeTraceData)))
	buffer.WriteString(metricHTML("Runtime Regression Repos", fmt.Sprintf("%d", reportData.Summary.ReposWithRuntimeRegressions)))
	buffer.WriteString("</div></section>")

	buffer.WriteString("<h2>Per-Repo Summary</h2><table><thead><tr>")
	buffer.WriteString("<th>Repo</th><th>Path</th><th>Repo URL</th><th>Revision</th><th>Resolved Commit</th><th>Language</th><th>Deps</th><th>Waste Candidates</th><th>Waste %</th><th>Top Risk</th><th>Critical CVEs</th><th>Denied Licenses</th><th>Runtime Trace</th><th>Runtime Regressions</th><th>Runtime Improvements</th><th>Error</th>")
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
	buffer.WriteString(metricHTML("Runtime Trace Repos Delta", fmt.Sprintf("%+d", comparison.SummaryDelta.ReposWithRuntimeTraceDataDelta)))
	buffer.WriteString(metricHTML("Runtime Regression Repos Delta", fmt.Sprintf("%+d", comparison.SummaryDelta.ReposWithRuntimeRegressionsDelta)))
	buffer.WriteString("</div></section>")

	if len(comparison.RepoDeltas) > 0 {
		buffer.WriteString("<table><thead><tr>")
		buffer.WriteString("<th>Repo</th><th>Path</th><th>Kind</th><th>Deps Δ</th><th>Waste Δ</th><th>Waste % Δ</th><th>Critical CVEs Δ</th><th>Denied Licenses Δ</th><th>Runtime Regressions Δ</th><th>Runtime Improvements Δ</th><th>Current Error</th><th>Baseline Error</th>")
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
			buffer.WriteString("<td>" + fmt.Sprintf("%+d", delta.DeniedLicenseCountDelta) + "</td>")
			buffer.WriteString("<td>" + fmt.Sprintf("%+d", delta.RuntimeRegressionCountDelta) + "</td>")
			buffer.WriteString("<td>" + fmt.Sprintf("%+d", delta.RuntimeImprovementCountDelta) + "</td>")
			buffer.WriteString("<td>" + html.EscapeString(delta.CurrentError) + "</td>")
			buffer.WriteString("<td>" + html.EscapeString(delta.BaselineError) + "</td>")
			buffer.WriteString("</tr>")
		}
		buffer.WriteString(htmlTableBodyClose)
	}

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
