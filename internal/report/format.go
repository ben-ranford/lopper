package report

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"text/tabwriter"
)

type Formatter struct{}

func NewFormatter() Formatter {
	return Formatter{}
}

func (f Formatter) Format(report Report, format Format) (string, error) {
	switch format {
	case FormatTable:
		return formatTable(report), nil
	case FormatJSON:
		payload, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return "", err
		}
		return string(payload) + "\n", nil
	default:
		return "", ErrUnknownFormat
	}
}

func formatTable(report Report) string {
	if len(report.Dependencies) == 0 {
		return formatEmpty(report)
	}

	var buffer bytes.Buffer
	appendSummary(&buffer, report.Summary)
	appendEffectiveThresholds(&buffer, report)
	appendLanguageBreakdown(&buffer, report.LanguageBreakdown)

	writer := tabwriter.NewWriter(&buffer, 0, 0, 2, ' ', 0)
	showLanguage := hasLanguageColumn(report.Dependencies)
	showRuntime := hasRuntimeColumn(report.Dependencies)
	writeTableHeader(writer, showLanguage, showRuntime)

	for _, dep := range report.Dependencies {
		_, _ = fmt.Fprintln(writer, formatTableRow(dep, showLanguage, showRuntime))
	}

	_ = writer.Flush()
	appendWarnings(&buffer, report)
	return buffer.String()
}

func appendSummary(buffer *bytes.Buffer, summary *Summary) {
	if summary == nil {
		return
	}
	_, _ = fmt.Fprintf(
		buffer,
		"Summary: %d deps, Used/Total: %d/%d (%.1f%%)\n\n",
		summary.DependencyCount,
		summary.UsedExportsCount,
		summary.TotalExportsCount,
		summary.UsedPercent,
	)
}

func appendLanguageBreakdown(buffer *bytes.Buffer, breakdown []LanguageSummary) {
	if len(breakdown) == 0 {
		return
	}
	buffer.WriteString("Languages:\n")
	for _, item := range breakdown {
		buffer.WriteString("- ")
		buffer.WriteString(item.Language)
		buffer.WriteString(": ")
		buffer.WriteString(fmt.Sprintf("%d deps, Used/Total: %d/%d (%.1f%%)\n", item.DependencyCount, item.UsedExportsCount, item.TotalExportsCount, item.UsedPercent))
	}
	buffer.WriteString("\n")
}

func writeTableHeader(writer *tabwriter.Writer, showLanguage, showRuntime bool) {
	columns := make([]string, 0, 9)
	if showLanguage {
		columns = append(columns, "Language")
	}
	columns = append(columns, "Dependency", "Used/Total", "Used%")
	if showRuntime {
		columns = append(columns, "Runtime")
	}
	columns = append(columns, "Est. Unused Size", "Candidate Score", "Score Components", "Top Symbols")
	_, _ = fmt.Fprintln(writer, strings.Join(columns, "\t"))
}

func formatTableRow(dep DependencyReport, showLanguage, showRuntime bool) string {
	usedPercent := dep.UsedPercent
	if usedPercent <= 0 && dep.TotalExportsCount > 0 {
		usedPercent = (float64(dep.UsedExportsCount) / float64(dep.TotalExportsCount)) * 100
	}

	columns := make([]string, 0, 9)
	if showLanguage {
		columns = append(columns, dep.Language)
	}
	columns = append(
		columns,
		dep.Name,
		fmt.Sprintf("%d/%d", dep.UsedExportsCount, dep.TotalExportsCount),
		fmt.Sprintf("%.1f", usedPercent),
	)
	if showRuntime {
		columns = append(columns, formatRuntimeUsage(dep.RuntimeUsage))
	}
	columns = append(
		columns,
		formatBytes(dep.EstimatedUnusedBytes),
		formatCandidateScore(dep.RemovalCandidate),
		formatScoreComponents(dep.RemovalCandidate),
		formatTopSymbols(dep.TopUsedSymbols),
	)
	return strings.Join(columns, "\t")
}

func hasLanguageColumn(dependencies []DependencyReport) bool {
	for _, dep := range dependencies {
		if strings.TrimSpace(dep.Language) != "" {
			return true
		}
	}
	return false
}

func hasRuntimeColumn(dependencies []DependencyReport) bool {
	for _, dep := range dependencies {
		if dep.RuntimeUsage != nil {
			return true
		}
	}
	return false
}

func formatEmpty(report Report) string {
	var buffer bytes.Buffer
	buffer.WriteString("No dependencies to report.\n")
	appendEffectiveThresholds(&buffer, report)
	appendWarnings(&buffer, report)
	return buffer.String()
}

func appendEffectiveThresholds(buffer *bytes.Buffer, report Report) {
	if report.EffectiveThresholds == nil {
		return
	}
	buffer.WriteString("Effective thresholds:\n")
	buffer.WriteString(fmt.Sprintf("- fail_on_increase_percent: %d\n", report.EffectiveThresholds.FailOnIncreasePercent))
	buffer.WriteString(fmt.Sprintf("- low_confidence_warning_percent: %d\n", report.EffectiveThresholds.LowConfidenceWarningPercent))
	buffer.WriteString(fmt.Sprintf("- min_usage_percent_for_recommendations: %d\n", report.EffectiveThresholds.MinUsagePercentForRecommendations))
	buffer.WriteString("\n")
}

func appendWarnings(buffer *bytes.Buffer, report Report) {
	if len(report.Warnings) == 0 {
		return
	}
	buffer.WriteString("\nWarnings:\n")
	for _, warning := range report.Warnings {
		buffer.WriteString("- ")
		buffer.WriteString(warning)
		buffer.WriteString("\n")
	}
}

func formatTopSymbols(symbols []SymbolUsage) string {
	if len(symbols) == 0 {
		return "-"
	}

	items := make([]string, 0, len(symbols))
	for _, symbol := range symbols {
		if symbol.Count > 1 {
			items = append(items, fmt.Sprintf("%s (%d)", symbol.Name, symbol.Count))
		} else {
			items = append(items, symbol.Name)
		}
	}
	return strings.Join(items, ", ")
}

func formatCandidateScore(candidate *RemovalCandidate) string {
	if candidate == nil {
		return "-"
	}
	return fmt.Sprintf("%.1f", candidate.Score)
}

func formatScoreComponents(candidate *RemovalCandidate) string {
	if candidate == nil {
		return "-"
	}
	return fmt.Sprintf("U:%.1f I:%.1f C:%.1f", candidate.Usage, candidate.Impact, candidate.Confidence)
}

func formatBytes(value int64) string {
	if value == 0 {
		return "0 B"
	}

	floatValue := float64(value)
	if floatValue < 0 {
		floatValue = -floatValue
	}

	unit := "B"
	if floatValue >= 1024 {
		floatValue /= 1024
		unit = "KB"
		if floatValue >= 1024 {
			floatValue /= 1024
			unit = "MB"
			if floatValue >= 1024 {
				floatValue /= 1024
				unit = "GB"
			}
		}
	}

	formatted := fmt.Sprintf("%.1f %s", floatValue, unit)
	if value < 0 {
		return "-" + formatted
	}
	return formatted
}

func formatRuntimeUsage(usage *RuntimeUsage) string {
	if usage == nil {
		return "-"
	}
	correlation := string(usage.Correlation)
	if correlation == "" {
		if usage.RuntimeOnly {
			correlation = string(RuntimeCorrelationRuntimeOnly)
		} else if usage.LoadCount > 0 {
			correlation = string(RuntimeCorrelationOverlap)
		} else {
			correlation = string(RuntimeCorrelationStaticOnly)
		}
	}
	return fmt.Sprintf("%s (%d loads)", correlation, usage.LoadCount)
}
