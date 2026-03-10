package dashboard

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"html"
	"strings"
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

	if err := writeDashboardSummaryCSV(writer, reportData); err != nil {
		return "", err
	}
	if err := writeDashboardRepoRowsCSV(writer, reportData.Repos); err != nil {
		return "", err
	}
	if err := writeDashboardCrossRepoRowsCSV(writer, reportData.CrossRepoDeps); err != nil {
		return "", err
	}

	writer.Flush()
	if err := writer.Error(); err != nil {
		return "", err
	}
	return buffer.String(), nil
}

func writeDashboardSummaryCSV(writer *csv.Writer, reportData Report) error {
	summaryRows := [][]string{
		{"generated_at", reportData.GeneratedAt.Format("2006-01-02T15:04:05Z07:00")},
		{"total_repos", fmt.Sprintf("%d", reportData.Summary.TotalRepos)},
		{"total_deps", fmt.Sprintf("%d", reportData.Summary.TotalDeps)},
		{"total_waste_candidates", fmt.Sprintf("%d", reportData.Summary.TotalWasteCandidates)},
		{"cross_repo_duplicates", fmt.Sprintf("%d", reportData.Summary.CrossRepoDuplicates)},
		{"critical_cves", fmt.Sprintf("%d", reportData.Summary.CriticalCVEs)},
	}
	for _, row := range summaryRows {
		if err := writer.Write(row); err != nil {
			return err
		}
	}
	return writer.Write(nil)
}

func writeDashboardRepoRowsCSV(writer *csv.Writer, repos []RepoResult) error {
	if err := writer.Write([]string{
		"repo_name",
		"repo_path",
		"language",
		"dependency_count",
		"waste_candidate_count",
		"waste_candidate_percent",
		"top_risk_severity",
		"critical_cves",
		"denied_license_count",
		"error",
	}); err != nil {
		return err
	}

	for _, repoResult := range repos {
		if err := writer.Write([]string{
			repoResult.Name,
			repoResult.Path,
			repoResult.Language,
			fmt.Sprintf("%d", repoResult.DependencyCount),
			fmt.Sprintf("%d", repoResult.WasteCandidateCount),
			fmt.Sprintf("%.2f", repoResult.WasteCandidatePercent),
			repoResult.TopRiskSeverity,
			fmt.Sprintf("%d", repoResult.CriticalCVEs),
			fmt.Sprintf("%d", repoResult.DeniedLicenseCount),
			repoResult.Error,
		}); err != nil {
			return err
		}
	}
	return nil
}

func writeDashboardCrossRepoRowsCSV(writer *csv.Writer, dependencies []CrossRepoDependency) error {
	if len(dependencies) == 0 {
		return nil
	}
	if err := writer.Write(nil); err != nil {
		return err
	}
	if err := writer.Write([]string{"dependency_name", "repo_count", "repositories"}); err != nil {
		return err
	}
	for _, dependency := range dependencies {
		if err := writer.Write([]string{
			dependency.Name,
			fmt.Sprintf("%d", dependency.Count),
			strings.Join(dependency.Repositories, "|"),
		}); err != nil {
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
	buffer.WriteString("</div></section>")

	buffer.WriteString("<h2>Per-Repo Summary</h2><table><thead><tr>")
	buffer.WriteString("<th>Repo</th><th>Path</th><th>Language</th><th>Deps</th><th>Waste Candidates</th><th>Waste %</th><th>Top Risk</th><th>Critical CVEs</th><th>Denied Licenses</th><th>Error</th>")
	buffer.WriteString("</tr></thead><tbody>")
	for _, repoResult := range reportData.Repos {
		buffer.WriteString("<tr>")
		buffer.WriteString("<td>" + html.EscapeString(repoResult.Name) + "</td>")
		buffer.WriteString("<td>" + html.EscapeString(repoResult.Path) + "</td>")
		buffer.WriteString("<td>" + html.EscapeString(repoResult.Language) + "</td>")
		buffer.WriteString("<td>" + fmt.Sprintf("%d", repoResult.DependencyCount) + "</td>")
		buffer.WriteString("<td>" + fmt.Sprintf("%d", repoResult.WasteCandidateCount) + "</td>")
		buffer.WriteString("<td>" + fmt.Sprintf("%.1f%%", repoResult.WasteCandidatePercent) + "</td>")
		buffer.WriteString("<td>" + html.EscapeString(repoResult.TopRiskSeverity) + "</td>")
		buffer.WriteString("<td>" + fmt.Sprintf("%d", repoResult.CriticalCVEs) + "</td>")
		buffer.WriteString("<td>" + fmt.Sprintf("%d", repoResult.DeniedLicenseCount) + "</td>")
		buffer.WriteString("<td>" + html.EscapeString(repoResult.Error) + "</td>")
		buffer.WriteString("</tr>")
	}
	buffer.WriteString("</tbody></table>")

	if len(reportData.CrossRepoDeps) > 0 {
		buffer.WriteString("<h2>Cross-Repo Duplicate Dependencies (3+ repos)</h2><table><thead><tr>")
		buffer.WriteString("<th>Dependency</th><th>Repo Count</th><th>Repos</th>")
		buffer.WriteString("</tr></thead><tbody>")
		for _, dependency := range reportData.CrossRepoDeps {
			buffer.WriteString("<tr>")
			buffer.WriteString("<td>" + html.EscapeString(dependency.Name) + "</td>")
			buffer.WriteString("<td>" + fmt.Sprintf("%d", dependency.Count) + "</td>")
			buffer.WriteString("<td>" + html.EscapeString(strings.Join(dependency.Repositories, ", ")) + "</td>")
			buffer.WriteString("</tr>")
		}
		buffer.WriteString("</tbody></table>")
	}

	buffer.WriteString("</body></html>\n")
	return buffer.String()
}

func metricHTML(label, value string) string {
	return "<div class=\"metric\"><span>" + html.EscapeString(label) + "</span><strong>" + html.EscapeString(value) + "</strong></div>"
}
