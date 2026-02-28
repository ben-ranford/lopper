package report

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"text/tabwriter"
)

type Formatter struct{}

func NewFormatter() *Formatter {
	return &Formatter{}
}

func (f *Formatter) Format(report Report, format Format) (string, error) {
	switch format {
	case FormatTable:
		return formatTable(report)
	case FormatJSON:
		payload, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return "", err
		}
		return string(payload) + "\n", nil
	case FormatSARIF:
		return formatSARIF(report)
	case FormatPRComment:
		return formatPRComment(report), nil
	default:
		return "", ErrUnknownFormat
	}
}

func formatTable(report Report) (string, error) {
	if len(report.Dependencies) == 0 {
		return formatEmpty(report), nil
	}

	var buffer bytes.Buffer
	appendSummary(&buffer, report.Summary)
	appendUsageUncertainty(&buffer, report.UsageUncertainty)
	appendCacheMetadata(&buffer, report.Cache)
	appendEffectiveThresholds(&buffer, report)
	appendEffectivePolicy(&buffer, report)
	appendLanguageBreakdown(&buffer, report.LanguageBreakdown)
	appendBaselineComparison(&buffer, report.BaselineComparison)

	writer := tabwriter.NewWriter(&buffer, 0, 0, 2, ' ', 0)
	showLanguage := hasLanguageColumn(report.Dependencies)
	showRuntime := hasRuntimeColumn(report.Dependencies)
	if err := writeTableHeader(writer, showLanguage, showRuntime); err != nil {
		return "", err
	}

	for _, dep := range report.Dependencies {
		if _, err := fmt.Fprintln(writer, formatTableRow(dep, showLanguage, showRuntime)); err != nil {
			return "", err
		}
	}

	if err := writer.Flush(); err != nil {
		return "", err
	}
	appendWarnings(&buffer, report)
	return buffer.String(), nil
}

func appendSummary(buffer *bytes.Buffer, summary *Summary) {
	if summary == nil {
		return
	}
	buffer.WriteString(fmt.Sprintf("Summary: %d deps, Used/Total: %d/%d (%.1f%%)\n\n", summary.DependencyCount, summary.UsedExportsCount, summary.TotalExportsCount, summary.UsedPercent))
}

func appendCacheMetadata(buffer *bytes.Buffer, cache *CacheMetadata) {
	if cache == nil {
		return
	}
	buffer.WriteString("Cache:\n")
	buffer.WriteString(fmt.Sprintf("- enabled: %t\n", cache.Enabled))
	if cache.Path != "" {
		buffer.WriteString(fmt.Sprintf("- path: %s\n", cache.Path))
	}
	buffer.WriteString(fmt.Sprintf("- readonly: %t\n", cache.ReadOnly))
	buffer.WriteString(fmt.Sprintf("- hits: %d\n", cache.Hits))
	buffer.WriteString(fmt.Sprintf("- misses: %d\n", cache.Misses))
	buffer.WriteString(fmt.Sprintf("- writes: %d\n", cache.Writes))
	if len(cache.Invalidations) > 0 {
		for _, invalidation := range cache.Invalidations {
			buffer.WriteString(fmt.Sprintf("- invalidation: %s (%s)\n", invalidation.Key, invalidation.Reason))
		}
	}
	buffer.WriteString("\n")
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

func writeTableHeader(writer *tabwriter.Writer, showLanguage, showRuntime bool) error {
	columns := make([]string, 0, 9)
	if showLanguage {
		columns = append(columns, "Language")
	}
	columns = append(columns, "Dependency", "Used/Total", "Used%")
	if showRuntime {
		columns = append(columns, "Runtime")
	}
	columns = append(columns, "Est. Unused Size", "Candidate Score", "Score Components", "Top Symbols")
	_, err := fmt.Fprintln(writer, strings.Join(columns, "\t"))
	return err
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
	columns = append(columns, dep.Name, fmt.Sprintf("%d/%d", dep.UsedExportsCount, dep.TotalExportsCount), fmt.Sprintf("%.1f", usedPercent))
	if showRuntime {
		columns = append(columns, formatRuntimeUsage(dep.RuntimeUsage))
	}
	columns = append(columns, formatBytes(dep.EstimatedUnusedBytes), formatCandidateScore(dep.RemovalCandidate), formatScoreComponents(dep.RemovalCandidate), formatTopSymbols(dep.TopUsedSymbols))
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
	appendUsageUncertainty(&buffer, report.UsageUncertainty)
	appendEffectiveThresholds(&buffer, report)
	appendEffectivePolicy(&buffer, report)
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
	buffer.WriteString(fmt.Sprintf("- max_uncertain_import_count: %d\n", report.EffectiveThresholds.MaxUncertainImportCount))
	buffer.WriteString("\n")
}

func appendEffectivePolicy(buffer *bytes.Buffer, report Report) {
	if report.EffectivePolicy == nil {
		return
	}
	buffer.WriteString("Effective policy:\n")
	if len(report.EffectivePolicy.Sources) > 0 {
		buffer.WriteString("- sources: ")
		buffer.WriteString(strings.Join(report.EffectivePolicy.Sources, " > "))
		buffer.WriteString("\n")
	}
	buffer.WriteString(fmt.Sprintf("- fail_on_increase_percent: %d\n", report.EffectivePolicy.Thresholds.FailOnIncreasePercent))
	buffer.WriteString(fmt.Sprintf("- low_confidence_warning_percent: %d\n", report.EffectivePolicy.Thresholds.LowConfidenceWarningPercent))
	buffer.WriteString(fmt.Sprintf("- min_usage_percent_for_recommendations: %d\n", report.EffectivePolicy.Thresholds.MinUsagePercentForRecommendations))
	buffer.WriteString(fmt.Sprintf("- max_uncertain_import_count: %d\n", report.EffectivePolicy.Thresholds.MaxUncertainImportCount))
	buffer.WriteString(fmt.Sprintf("- removal_candidate_weight_usage: %.3f\n", report.EffectivePolicy.RemovalCandidateWeights.Usage))
	buffer.WriteString(fmt.Sprintf("- removal_candidate_weight_impact: %.3f\n", report.EffectivePolicy.RemovalCandidateWeights.Impact))
	buffer.WriteString(fmt.Sprintf("- removal_candidate_weight_confidence: %.3f\n", report.EffectivePolicy.RemovalCandidateWeights.Confidence))
	buffer.WriteString("\n")
}

func appendUsageUncertainty(buffer *bytes.Buffer, usage *UsageUncertainty) {
	if usage == nil {
		return
	}
	buffer.WriteString(fmt.Sprintf("Usage certainty: confirmed imports=%d, uncertain imports=%d\n\n", usage.ConfirmedImportUses, usage.UncertainImportUses))
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

func appendBaselineComparison(buffer *bytes.Buffer, comparison *BaselineComparison) {
	if comparison == nil {
		return
	}
	buffer.WriteString("Baseline comparison:\n")
	if strings.TrimSpace(comparison.BaselineKey) != "" {
		buffer.WriteString("- baseline_key: ")
		buffer.WriteString(comparison.BaselineKey)
		buffer.WriteString("\n")
	}
	if strings.TrimSpace(comparison.CurrentKey) != "" {
		buffer.WriteString("- current_key: ")
		buffer.WriteString(comparison.CurrentKey)
		buffer.WriteString("\n")
	}
	buffer.WriteString(fmt.Sprintf("- summary_delta: deps %+d, used %% %+0.1f, waste %% %+0.1f, unused bytes %+d\n", comparison.SummaryDelta.DependencyCountDelta, comparison.SummaryDelta.UsedPercentDelta, comparison.SummaryDelta.WastePercentDelta, comparison.SummaryDelta.UnusedBytesDelta))
	buffer.WriteString(fmt.Sprintf("- changed: %d, regressions: %d, progressions: %d, added: %d, removed: %d, unchanged: %d\n", len(comparison.Dependencies), len(comparison.Regressions), len(comparison.Progressions), len(comparison.Added), len(comparison.Removed), comparison.UnchangedRows))

	for _, delta := range topWasteDeltas(comparison.Regressions, 3) {
		buffer.WriteString(fmt.Sprintf("  regression %s/%s waste %+0.1f%% used %+0.1f%%\n", delta.Language, delta.Name, delta.WastePercentDelta, delta.UsedPercentDelta))
	}
	for _, delta := range topWasteDeltas(comparison.Progressions, 3) {
		buffer.WriteString(fmt.Sprintf("  progression %s/%s waste %+0.1f%% used %+0.1f%%\n", delta.Language, delta.Name, delta.WastePercentDelta, delta.UsedPercentDelta))
	}
	buffer.WriteString("\n")
}

func topWasteDeltas(deltas []DependencyDelta, limit int) []DependencyDelta {
	if len(deltas) == 0 || limit <= 0 {
		return nil
	}
	copied := append([]DependencyDelta(nil), deltas...)
	sort.Slice(copied, func(i, j int) bool {
		left := copied[i]
		right := copied[j]
		leftMagnitude := left.WastePercentDelta
		if leftMagnitude < 0 {
			leftMagnitude = -leftMagnitude
		}
		rightMagnitude := right.WastePercentDelta
		if rightMagnitude < 0 {
			rightMagnitude = -rightMagnitude
		}
		if leftMagnitude != rightMagnitude {
			return leftMagnitude > rightMagnitude
		}
		if left.Language != right.Language {
			return left.Language < right.Language
		}
		return left.Name < right.Name
	})
	if len(copied) < limit {
		return copied
	}
	return copied[:limit]
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

	scaled := float64(value)
	if scaled < 0 {
		scaled = -scaled
	}

	unit := "B"
	if scaled >= 1024 {
		scaled /= 1024
		unit = "KB"
		if scaled >= 1024 {
			scaled /= 1024
			unit = "MB"
			if scaled >= 1024 {
				scaled /= 1024
				unit = "GB"
			}
		}
	}

	formatted := fmt.Sprintf("%.1f %s", scaled, unit)
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
	buffer.WriteString("\n")
	buffer.WriteString("| Changed | Regressions | Progressions | Added | Removed | Unchanged |\n")
	buffer.WriteString("| --- | --- | --- | --- | --- | --- |\n")
	buffer.WriteString(fmt.Sprintf("| %d | %d | %d | %d | %d | %d |\n", len(comparison.Dependencies), len(comparison.Regressions), len(comparison.Progressions), len(comparison.Added), len(comparison.Removed), comparison.UnchangedRows))

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

func topDependencyDeltas(deltas []DependencyDelta, limit int) []DependencyDelta {
	if len(deltas) == 0 || limit <= 0 {
		return nil
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

func signedPct(value float64) string {
	if value >= 0 {
		return fmt.Sprintf("+%.1f%%", value)
	}
	return fmt.Sprintf("%.1f%%", value)
}

func signedInt(value int) string {
	if value >= 0 {
		return fmt.Sprintf("+%d", value)
	}
	return fmt.Sprintf("%d", value)
}

func signedBytes(value int64) string {
	if value >= 0 {
		return "+" + formatBytes(value)
	}
	return formatBytes(value)
}

func escapeMarkdownTable(value string) string {
	return strings.NewReplacer("|", "\\|", "`", "'").Replace(value)
}
