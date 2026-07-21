package report

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

var prCommentMarkdownPairs = []string{
	"\r\n", "\\n", "\r", "\\n", "\n", "\\n", "\t", "\\t",
	"&", "&amp;", "<", "&lt;", ">", "&gt;",
	"\\", "\\\\", "|", "\\|", "`", "'",
	"[", "\\[", "]", "\\]", "(", "\\(", ")", "\\)", "!", "\\!",
}

var prCommentMarkdownReplacer = strings.NewReplacer(prCommentMarkdownPairs...)

func formatPRComment(report Report) string {
	comparison := report.BaselineComparison
	if comparison == nil {
		return "## Lopper (Delta)\n\n_No baseline comparison available. Run with `--baseline` or `--baseline-store` to generate PR delta output._\n"
	}

	var buffer strings.Builder
	buffer.WriteString("## Lopper (Delta)\n\n")
	buffer.WriteString("| Metric delta | Value |\n")
	buffer.WriteString("| --- | --- |\n")
	fmt.Fprintf(&buffer, "| Dependency count | %s |\n", signedInt(comparison.SummaryDelta.DependencyCountDelta))
	fmt.Fprintf(&buffer, "| Used percent | %s |\n", signedPct(comparison.SummaryDelta.UsedPercentDelta))
	fmt.Fprintf(&buffer, "| Waste percent | %s |\n", signedPct(comparison.SummaryDelta.WastePercentDelta))
	fmt.Fprintf(&buffer, "| Estimated unused bytes | %s |\n", signedBytes(comparison.SummaryDelta.UnusedBytesDelta))
	fmt.Fprintf(&buffer, "| Known licenses | %s |\n", signedInt(comparison.SummaryDelta.KnownLicenseCountDelta))
	fmt.Fprintf(&buffer, "| Unknown licenses | %s |\n", signedInt(comparison.SummaryDelta.UnknownLicenseCountDelta))
	fmt.Fprintf(&buffer, "| Denied licenses | %s |\n", signedInt(comparison.SummaryDelta.DeniedLicenseCountDelta))
	fmt.Fprintf(&buffer, "| Reachable vulnerabilities | %s |\n", signedInt(comparison.SummaryDelta.ReachableVulnerabilityCountDelta))
	buffer.WriteString("\n")
	buffer.WriteString("| Changed | Regressions | Progressions | Added | Removed | Unchanged |\n")
	buffer.WriteString("| --- | --- | --- | --- | --- | --- |\n")
	fmt.Fprintf(&buffer, "| %d | %d | %d | %d | %d | %d |\n", len(comparison.Dependencies), len(comparison.Regressions), len(comparison.Progressions), len(comparison.Added), len(comparison.Removed), comparison.UnchangedRows)
	if len(comparison.RuntimeRegressions) > 0 || len(comparison.RuntimeImprovements) > 0 {
		buffer.WriteString("\n| Runtime trace deltas | Count |\n")
		buffer.WriteString("| --- | --- |\n")
		fmt.Fprintf(&buffer, "| Runtime regressions | %d |\n", len(comparison.RuntimeRegressions))
		fmt.Fprintf(&buffer, "| Runtime improvements | %d |\n", len(comparison.RuntimeImprovements))
	}

	if len(comparison.NewDeniedLicenses) > 0 {
		buffer.WriteString("\n### Newly denied licenses\n\n")
		buffer.WriteString("| # | Dependency | Language | SPDX |\n")
		buffer.WriteString("| --- | --- | --- | --- |\n")
		for i, denied := range comparison.NewDeniedLicenses {
			fmt.Fprintf(&buffer, "| %d | %s | %s | %s |\n", i+1, markdownCodeCell(denied.Name), markdownCodeCell(denied.Language), markdownCodeCell(denied.SPDX))
		}
	}

	if len(comparison.NewReachableVulnerabilities) > 0 {
		buffer.WriteString("\n### Newly reachable vulnerabilities\n\n")
		buffer.WriteString("| # | Dependency | Advisory | Severity | Priority | Fixed version | Source |\n")
		buffer.WriteString("| --- | --- | --- | --- | --- | --- | --- |\n")
		for i, finding := range topVulnerabilityDeltas(comparison.NewReachableVulnerabilities, 10) {
			row := []string{
				fmt.Sprintf("%d", i+1),
				markdownCodeCell(finding.Name),
				markdownCodeCell(finding.AdvisoryID),
				markdownCodeCell(finding.Severity),
				markdownCodeCell(fmt.Sprintf("%s (%.1f)", finding.Priority, finding.PriorityScore)),
				markdownCodeCell(emptyDash(finding.FixedVersion)),
				markdownCodeCell(finding.Source),
			}
			buffer.WriteString("| " + strings.Join(row, " | ") + " |\n")
		}
	}

	appendRuntimePRCommentSection(&buffer, "Runtime regressions", comparison.RuntimeRegressions)
	appendRuntimePRCommentSection(&buffer, "Runtime improvements", comparison.RuntimeImprovements)

	top := topDependencyDeltas(comparison.Dependencies, 10)
	if len(top) == 0 {
		buffer.WriteString("\n_No dependency-surface deltas detected._\n")
		return buffer.String()
	}

	buffer.WriteString("\n### Dependency deltas\n\n")
	buffer.WriteString("| # | Change | Dependency | Language | Used % delta | Used exports delta | Total exports delta | Unused bytes delta |\n")
	buffer.WriteString("| --- | --- | --- | --- | --- | --- | --- | --- |\n")
	for i, delta := range top {
		row := []string{
			fmt.Sprintf("%d", i+1),
			string(delta.Kind),
			markdownCodeCell(delta.Name),
			markdownCodeCell(delta.Language),
			signedPct(delta.UsedPercentDelta),
			signedInt(delta.UsedExportsCountDelta),
			signedInt(delta.TotalExportsCountDelta),
			signedBytes(delta.EstimatedUnusedBytesDelta),
		}
		buffer.WriteString("| " + strings.Join(row, " | ") + " |\n")
	}

	return buffer.String()
}

func emptyDash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}

func appendRuntimePRCommentSection(buffer *strings.Builder, title string, deltas []DependencyDelta) {
	top := topRuntimeDeltas(deltas, 10)
	if len(top) == 0 {
		return
	}
	buffer.WriteString("\n### ")
	buffer.WriteString(title)
	buffer.WriteString("\n\n")
	buffer.WriteString("| # | Dependency | Language | Runtime delta |\n")
	buffer.WriteString("| --- | --- | --- | --- |\n")
	for i, delta := range top {
		row := []string{
			fmt.Sprintf("%d", i+1),
			markdownCodeCell(delta.Name),
			markdownCodeCell(delta.Language),
			markdownCodeCell(formatRuntimeDelta(delta.RuntimeDelta)),
		}
		buffer.WriteString("| " + strings.Join(row, " | ") + " |\n")
	}
}

func topDependencyDeltas(deltas []DependencyDelta, limit int) []DependencyDelta {
	if len(deltas) == 0 || limit <= 0 {
		return make([]DependencyDelta, 0)
	}
	copied := append([]DependencyDelta(nil), deltas...)
	sort.Slice(copied, func(i, j int) bool {
		leftMagnitude := copied[i].EstimatedUnusedBytesDelta
		if leftMagnitude < 0 {
			leftMagnitude = -leftMagnitude
		}
		rightMagnitude := copied[j].EstimatedUnusedBytesDelta
		if rightMagnitude < 0 {
			rightMagnitude = -rightMagnitude
		}
		if leftMagnitude != rightMagnitude {
			return leftMagnitude > rightMagnitude
		}
		if copied[i].Language != copied[j].Language {
			return copied[i].Language < copied[j].Language
		}
		return copied[i].Name < copied[j].Name
	})
	if len(copied) < limit {
		return copied
	}
	return copied[:limit]
}

func topRuntimeDeltas(deltas []DependencyDelta, limit int) []DependencyDelta {
	if len(deltas) == 0 || limit <= 0 {
		return make([]DependencyDelta, 0)
	}
	copied := append([]DependencyDelta(nil), deltas...)
	sort.Slice(copied, func(i, j int) bool {
		leftMagnitude := runtimeDeltaLoadCount(copied[i].RuntimeDelta)
		if leftMagnitude < 0 {
			leftMagnitude = -leftMagnitude
		}
		rightMagnitude := runtimeDeltaLoadCount(copied[j].RuntimeDelta)
		if rightMagnitude < 0 {
			rightMagnitude = -rightMagnitude
		}
		if leftMagnitude != rightMagnitude {
			return leftMagnitude > rightMagnitude
		}
		if copied[i].Language != copied[j].Language {
			return copied[i].Language < copied[j].Language
		}
		return copied[i].Name < copied[j].Name
	})
	if len(copied) < limit {
		return copied
	}
	return copied[:limit]
}

func signedPct(value float64) string {
	rounded := math.RoundToEven(value*10) / 10
	if rounded == 0 {
		return "0.0%"
	}
	if rounded > 0 {
		return fmt.Sprintf("+%.1f%%", rounded)
	}
	return fmt.Sprintf("%.1f%%", rounded)
}

func signedInt(value int) string {
	if value > 0 {
		return fmt.Sprintf("+%d", value)
	}
	return fmt.Sprintf("%d", value)
}

func signedBytes(value int64) string {
	if value > 0 {
		return "+" + formatBytes(value)
	}
	return formatBytes(value)
}

func escapeMarkdownTable(value string) string {
	return sanitizeTerminalString(prCommentMarkdownReplacer.Replace(value))
}

func markdownCodeCell(value string) string {
	return "`" + escapeMarkdownTable(value) + "`"
}
