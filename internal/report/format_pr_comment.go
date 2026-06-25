package report

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

func formatPRComment(report Report) string {
	comparison := report.BaselineComparison
	if comparison == nil {
		return "## Lopper (Delta)\n\n_No baseline comparison available. Run with `--baseline` or `--baseline-store` to generate PR delta output._\n"
	}

	var buffer strings.Builder
	buffer.WriteString("## Lopper (Delta)\n\n")
	buffer.WriteString("| Metric delta | Value |\n")
	buffer.WriteString("| --- | --- |\n")
	buffer.WriteString(fmt.Sprintf("| Dependency count | %s |\n", signedInt(comparison.SummaryDelta.DependencyCountDelta)))
	buffer.WriteString(fmt.Sprintf("| Used percent | %s |\n", signedPct(comparison.SummaryDelta.UsedPercentDelta)))
	buffer.WriteString(fmt.Sprintf("| Waste percent | %s |\n", signedPct(comparison.SummaryDelta.WastePercentDelta)))
	buffer.WriteString(fmt.Sprintf("| Estimated unused bytes | %s |\n", signedBytes(comparison.SummaryDelta.UnusedBytesDelta)))
	buffer.WriteString(fmt.Sprintf("| Known licenses | %s |\n", signedInt(comparison.SummaryDelta.KnownLicenseCountDelta)))
	buffer.WriteString(fmt.Sprintf("| Unknown licenses | %s |\n", signedInt(comparison.SummaryDelta.UnknownLicenseCountDelta)))
	buffer.WriteString(fmt.Sprintf("| Denied licenses | %s |\n", signedInt(comparison.SummaryDelta.DeniedLicenseCountDelta)))
	buffer.WriteString(fmt.Sprintf("| Reachable vulnerabilities | %s |\n", signedInt(comparison.SummaryDelta.ReachableVulnerabilityCountDelta)))
	buffer.WriteString("\n")
	buffer.WriteString("| Changed | Regressions | Progressions | Added | Removed | Unchanged |\n")
	buffer.WriteString("| --- | --- | --- | --- | --- | --- |\n")
	buffer.WriteString(fmt.Sprintf("| %d | %d | %d | %d | %d | %d |\n", len(comparison.Dependencies), len(comparison.Regressions), len(comparison.Progressions), len(comparison.Added), len(comparison.Removed), comparison.UnchangedRows))
	if len(comparison.RuntimeRegressions) > 0 || len(comparison.RuntimeImprovements) > 0 {
		buffer.WriteString("\n| Runtime trace deltas | Count |\n")
		buffer.WriteString("| --- | --- |\n")
		buffer.WriteString(fmt.Sprintf("| Runtime regressions | %d |\n", len(comparison.RuntimeRegressions)))
		buffer.WriteString(fmt.Sprintf("| Runtime improvements | %d |\n", len(comparison.RuntimeImprovements)))
	}

	if len(comparison.NewDeniedLicenses) > 0 {
		buffer.WriteString("\n### Newly denied licenses\n\n")
		buffer.WriteString("| # | Dependency | Language | SPDX |\n")
		buffer.WriteString("| --- | --- | --- | --- |\n")
		for i, denied := range comparison.NewDeniedLicenses {
			buffer.WriteString(fmt.Sprintf("| %d | `%s` | %s | %s |\n", i+1, escapeMarkdownTable(denied.Name), escapeMarkdownTable(denied.Language), escapeMarkdownTable(denied.SPDX)))
		}
	}

	if len(comparison.NewReachableVulnerabilities) > 0 {
		buffer.WriteString("\n### Newly reachable vulnerabilities\n\n")
		buffer.WriteString("| # | Dependency | Advisory | Severity | Priority | Fixed version | Source |\n")
		buffer.WriteString("| --- | --- | --- | --- | --- | --- | --- |\n")
		for i, finding := range topVulnerabilityDeltas(comparison.NewReachableVulnerabilities, 10) {
			row := []string{
				fmt.Sprintf("%d", i+1),
				"`" + escapeMarkdownTable(finding.Name) + "`",
				escapeMarkdownTable(finding.AdvisoryID),
				escapeMarkdownTable(finding.Severity),
				escapeMarkdownTable(fmt.Sprintf("%s (%.1f)", finding.Priority, finding.PriorityScore)),
				escapeMarkdownTable(emptyDash(finding.FixedVersion)),
				escapeMarkdownTable(finding.Source),
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
			"`" + escapeMarkdownTable(delta.Name) + "`",
			escapeMarkdownTable(delta.Language),
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
			"`" + escapeMarkdownTable(delta.Name) + "`",
			escapeMarkdownTable(delta.Language),
			escapeMarkdownTable(formatRuntimeDelta(delta.RuntimeDelta)),
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
	return strings.NewReplacer("|", "\\|", "`", "'", "\n", "\\n", "\r", "\\r").Replace(value)
}
