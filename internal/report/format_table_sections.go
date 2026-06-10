package report

import (
	"bytes"
	"sort"
	"strings"
	"text/template"
)

var tableWarningReplacer = strings.NewReplacer("\r\n", "\\n", "\r", "\\n", "\n", "\\n", "\t", "\\t")

var (
	summarySectionTemplate = template.Must(template.New("summary-section").Parse(`Summary: {{.DependencyCount}} deps, Used/Total: {{.UsedExportsCount}}/{{.TotalExportsCount}} ({{printf "%.1f" .UsedPercent}}%)

Licenses: known={{.KnownLicenseCount}}, unknown={{.UnknownLicenseCount}}, denied={{.DeniedLicenseCount}}

{{if .Reachability}}Reachability confidence: avg={{printf "%.1f" .Reachability.AverageScore}} range={{printf "%.1f" .Reachability.LowestScore}}-{{printf "%.1f" .Reachability.HighestScore}} ({{.Reachability.Model}})

{{end}}`))

	effectiveThresholdsSectionTemplate = template.Must(template.New("effective-thresholds-section").Parse(`Effective thresholds:
- fail_on_increase_percent: {{.FailOnIncreasePercent}}
- low_confidence_warning_percent: {{.LowConfidenceWarningPercent}}
- min_usage_percent_for_recommendations: {{.MinUsagePercentForRecommendations}}
- max_uncertain_import_count: {{.MaxUncertainImportCount}}

`))

	languageBreakdownSectionTemplate = template.Must(template.New("language-breakdown-section").Parse(`Languages:
{{range .Items}}- {{.Language}}: {{.DependencyCount}} deps, Used/Total: {{.UsedExportsCount}}/{{.TotalExportsCount}} ({{printf "%.1f" .UsedPercent}}%)
{{end}}
`))
)

func appendSummary(buffer *bytes.Buffer, summary *Summary) error {
	if summary == nil {
		return nil
	}
	return executeReportTemplate(buffer, summarySectionTemplate, newSummarySectionData(summary))
}

func appendUsageUncertainty(buffer *bytes.Buffer, usage *UsageUncertainty) {
	if usage == nil {
		return
	}
	writef(buffer, "Usage certainty: confirmed imports=%d, uncertain imports=%d\n\n", usage.ConfirmedImportUses, usage.UncertainImportUses)
}

func appendScopeMetadata(buffer *bytes.Buffer, scope *ScopeMetadata) {
	if scope == nil {
		return
	}
	buffer.WriteString("Scope:\n")
	writef(buffer, "- mode: %s\n", scope.Mode)
	if len(scope.Packages) > 0 {
		buffer.WriteString("- packages: ")
		buffer.WriteString(strings.Join(sanitizeTerminalStrings(scope.Packages), ", "))
		buffer.WriteString("\n")
	}
	buffer.WriteString("\n")
}

func appendCacheMetadata(buffer *bytes.Buffer, cache *CacheMetadata) {
	if cache == nil {
		return
	}
	buffer.WriteString("Cache:\n")
	writef(buffer, "- enabled: %t\n", cache.Enabled)
	if cache.Path != "" {
		writef(buffer, "- path: %s\n", cache.Path)
	}
	writef(buffer, "- readonly: %t\n", cache.ReadOnly)
	writef(buffer, "- hits: %d\n", cache.Hits)
	writef(buffer, "- misses: %d\n", cache.Misses)
	writef(buffer, "- writes: %d\n", cache.Writes)
	if len(cache.Invalidations) > 0 {
		for _, invalidation := range cache.Invalidations {
			writef(buffer, "- invalidation: %s (%s)\n", invalidation.Key, invalidation.Reason)
		}
	}
	buffer.WriteString("\n")
}

func appendEffectiveThresholds(buffer *bytes.Buffer, report Report) error {
	if report.EffectiveThresholds == nil {
		return nil
	}
	return executeReportTemplate(buffer, effectiveThresholdsSectionTemplate, report.EffectiveThresholds)
}

func appendEffectivePolicy(buffer *bytes.Buffer, report Report) {
	if report.EffectivePolicy == nil {
		return
	}
	buffer.WriteString("Effective policy:\n")
	if len(report.EffectivePolicy.Sources) > 0 {
		buffer.WriteString("- sources: ")
		buffer.WriteString(strings.Join(sanitizeTerminalStrings(report.EffectivePolicy.Sources), " > "))
		buffer.WriteString("\n")
	}
	writef(buffer, "- fail_on_increase_percent: %d\n", report.EffectivePolicy.Thresholds.FailOnIncreasePercent)
	writef(buffer, "- low_confidence_warning_percent: %d\n", report.EffectivePolicy.Thresholds.LowConfidenceWarningPercent)
	writef(buffer, "- min_usage_percent_for_recommendations: %d\n", report.EffectivePolicy.Thresholds.MinUsagePercentForRecommendations)
	writef(buffer, "- max_uncertain_import_count: %d\n", report.EffectivePolicy.Thresholds.MaxUncertainImportCount)
	writef(buffer, "- removal_candidate_weight_usage: %.3f\n", report.EffectivePolicy.RemovalCandidateWeights.Usage)
	writef(buffer, "- removal_candidate_weight_impact: %.3f\n", report.EffectivePolicy.RemovalCandidateWeights.Impact)
	writef(buffer, "- removal_candidate_weight_confidence: %.3f\n", report.EffectivePolicy.RemovalCandidateWeights.Confidence)
	if len(report.EffectivePolicy.License.Deny) > 0 {
		buffer.WriteString("- license_deny: ")
		buffer.WriteString(strings.Join(sanitizeTerminalStrings(report.EffectivePolicy.License.Deny), ", "))
		buffer.WriteString("\n")
	}
	writef(buffer, "- license_fail_on_deny: %t\n", report.EffectivePolicy.License.FailOnDenied)
	writef(buffer, "- license_include_registry_provenance: %t\n", report.EffectivePolicy.License.IncludeRegistryProvenance)
	if len(report.EffectivePolicy.MergeTrace) > 0 {
		buffer.WriteString("- merge_trace:\n")
		for _, item := range report.EffectivePolicy.MergeTrace {
			writef(buffer, "  - %s <= %s\n", item.Field, item.Source)
		}
	}
	buffer.WriteString("\n")
}

func appendLanguageBreakdown(buffer *bytes.Buffer, breakdown []LanguageSummary) error {
	if len(breakdown) == 0 {
		return nil
	}
	return executeReportTemplate(buffer, languageBreakdownSectionTemplate, newLanguageBreakdownSectionData(breakdown))
}

type summarySectionData struct {
	DependencyCount     int
	UsedExportsCount    int
	TotalExportsCount   int
	UsedPercent         float64
	KnownLicenseCount   int
	UnknownLicenseCount int
	DeniedLicenseCount  int
	Reachability        *reachabilitySectionData
}

type reachabilitySectionData struct {
	AverageScore float64
	LowestScore  float64
	HighestScore float64
	Model        string
}

type languageBreakdownSectionData struct {
	Items []languageBreakdownSectionItem
}

type languageBreakdownSectionItem struct {
	Language          string
	DependencyCount   int
	UsedExportsCount  int
	TotalExportsCount int
	UsedPercent       float64
}

func newSummarySectionData(summary *Summary) summarySectionData {
	data := summarySectionData{
		DependencyCount:     summary.DependencyCount,
		UsedExportsCount:    summary.UsedExportsCount,
		TotalExportsCount:   summary.TotalExportsCount,
		UsedPercent:         summary.UsedPercent,
		KnownLicenseCount:   summary.KnownLicenseCount,
		UnknownLicenseCount: summary.UnknownLicenseCount,
		DeniedLicenseCount:  summary.DeniedLicenseCount,
	}
	if summary.Reachability != nil {
		data.Reachability = &reachabilitySectionData{
			AverageScore: summary.Reachability.AverageScore,
			LowestScore:  summary.Reachability.LowestScore,
			HighestScore: summary.Reachability.HighestScore,
			Model:        sanitizeTerminalString(summary.Reachability.Model),
		}
	}
	return data
}

func newLanguageBreakdownSectionData(breakdown []LanguageSummary) languageBreakdownSectionData {
	items := make([]languageBreakdownSectionItem, 0, len(breakdown))
	for _, item := range breakdown {
		items = append(items, languageBreakdownSectionItem{
			Language:          sanitizeTerminalString(item.Language),
			DependencyCount:   item.DependencyCount,
			UsedExportsCount:  item.UsedExportsCount,
			TotalExportsCount: item.TotalExportsCount,
			UsedPercent:       item.UsedPercent,
		})
	}
	return languageBreakdownSectionData{Items: items}
}

func executeReportTemplate(buffer *bytes.Buffer, tmpl *template.Template, data any) error {
	return tmpl.Execute(buffer, data)
}

func appendBaselineComparison(buffer *bytes.Buffer, comparison *BaselineComparison) {
	if comparison == nil {
		return
	}
	buffer.WriteString("Baseline comparison:\n")
	if strings.TrimSpace(comparison.BaselineKey) != "" {
		buffer.WriteString("- baseline_key: ")
		buffer.WriteString(sanitizeTerminalString(comparison.BaselineKey))
		buffer.WriteString("\n")
	}
	if strings.TrimSpace(comparison.CurrentKey) != "" {
		buffer.WriteString("- current_key: ")
		buffer.WriteString(sanitizeTerminalString(comparison.CurrentKey))
		buffer.WriteString("\n")
	}
	writef(buffer, "- summary_delta: deps %+d, used %% %+0.1f, waste %% %+0.1f, unused bytes %+d\n", comparison.SummaryDelta.DependencyCountDelta, comparison.SummaryDelta.UsedPercentDelta, comparison.SummaryDelta.WastePercentDelta, comparison.SummaryDelta.UnusedBytesDelta)
	writef(buffer, "- license_delta: known %+d, unknown %+d, denied %+d\n", comparison.SummaryDelta.KnownLicenseCountDelta, comparison.SummaryDelta.UnknownLicenseCountDelta, comparison.SummaryDelta.DeniedLicenseCountDelta)
	writef(buffer, "- changed: %d, regressions: %d, progressions: %d, added: %d, removed: %d, unchanged: %d\n", len(comparison.Dependencies), len(comparison.Regressions), len(comparison.Progressions), len(comparison.Added), len(comparison.Removed), comparison.UnchangedRows)
	if len(comparison.NewDeniedLicenses) > 0 {
		for _, denied := range comparison.NewDeniedLicenses {
			writef(buffer, "  new denied license %s/%s (%s)\n", denied.Language, denied.Name, denied.SPDX)
		}
	}

	for _, delta := range topWasteDeltas(comparison.Regressions, 3) {
		writef(buffer, "  regression %s/%s waste %+0.1f%% used %+0.1f%%\n", delta.Language, delta.Name, delta.WastePercentDelta, delta.UsedPercentDelta)
	}
	for _, delta := range topWasteDeltas(comparison.Progressions, 3) {
		writef(buffer, "  progression %s/%s waste %+0.1f%% used %+0.1f%%\n", delta.Language, delta.Name, delta.WastePercentDelta, delta.UsedPercentDelta)
	}
	buffer.WriteString("\n")
}

func appendCodemodApply(buffer *bytes.Buffer, dependencies []DependencyReport) {
	entries := collectCodemodApplyEntries(dependencies)
	if len(entries) == 0 {
		return
	}
	buffer.WriteString("Codemod apply:\n")
	for _, entry := range entries {
		buffer.WriteString("- dependency: ")
		buffer.WriteString(sanitizeTerminalString(entry.name))
		buffer.WriteString("\n")
		writef(buffer, "  applied: %d file(s), %d patch(es)\n", entry.apply.AppliedFiles, entry.apply.AppliedPatches)
		writef(buffer, "  skipped: %d file(s), %d patch(es)\n", entry.apply.SkippedFiles, entry.apply.SkippedPatches)
		writef(buffer, "  failed: %d file(s), %d patch(es)\n", entry.apply.FailedFiles, entry.apply.FailedPatches)
		if entry.apply.BackupPath != "" {
			buffer.WriteString("  backup: ")
			buffer.WriteString(sanitizeTerminalString(entry.apply.BackupPath))
			buffer.WriteString("\n")
		}
		for _, result := range entry.apply.Results {
			writef(buffer, "  %s %s (%d patch(es))", result.Status, result.File, result.PatchCount)
			if strings.TrimSpace(result.Message) != "" {
				buffer.WriteString(": ")
				buffer.WriteString(sanitizeTerminalString(result.Message))
			}
			buffer.WriteString("\n")
		}
	}
	buffer.WriteString("\n")
}

type codemodApplyEntry struct {
	name  string
	apply *CodemodApplyReport
}

func collectCodemodApplyEntries(dependencies []DependencyReport) []codemodApplyEntry {
	entries := make([]codemodApplyEntry, 0)
	for _, dep := range dependencies {
		if dep.Codemod == nil || dep.Codemod.Apply == nil {
			continue
		}
		entries = append(entries, codemodApplyEntry{name: dep.Name, apply: dep.Codemod.Apply})
	}
	return entries
}

func appendWarnings(buffer *bytes.Buffer, report Report) {
	if len(report.Warnings) == 0 {
		return
	}
	buffer.WriteString("\nWarnings:\n")
	for _, warning := range report.Warnings {
		buffer.WriteString("- ")
		buffer.WriteString(escapeTableWarning(warning))
		buffer.WriteString("\n")
	}
}

func escapeTableWarning(warning string) string {
	return sanitizeTerminalString(tableWarningReplacer.Replace(warning))
}

func topWasteDeltas(deltas []DependencyDelta, limit int) []DependencyDelta {
	if len(deltas) == 0 || limit <= 0 {
		return make([]DependencyDelta, 0)
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
