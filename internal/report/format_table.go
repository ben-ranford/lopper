package report

import (
	"bytes"
	"fmt"
	"sort"
	"strings"
	"text/tabwriter"
)

func formatTable(report Report) (string, error) {
	if len(report.Dependencies) == 0 {
		return formatEmpty(report), nil
	}

	var buffer bytes.Buffer
	appendSummary(&buffer, report.Summary)
	appendUsageUncertainty(&buffer, report.UsageUncertainty)
	appendScopeMetadata(&buffer, report.Scope)
	appendCacheMetadata(&buffer, report.Cache)
	appendEffectiveThresholds(&buffer, report)
	appendEffectivePolicy(&buffer, report)
	appendLanguageBreakdown(&buffer, report.LanguageBreakdown)
	appendBaselineComparison(&buffer, report.BaselineComparison)
	appendCodemodApply(&buffer, report.Dependencies)

	writer := tabwriter.NewWriter(&buffer, 0, 0, 2, ' ', 0)
	showLanguage := hasLanguageColumn(report.Dependencies)
	showRuntime := hasRuntimeColumn(report.Dependencies)
	showReachability := hasReachabilityColumn(report.Dependencies)
	if err := writeTableHeader(writer, showLanguage, showRuntime, showReachability); err != nil {
		return "", err
	}

	for _, dep := range report.Dependencies {
		if _, err := fmt.Fprintln(writer, formatTableRow(dep, showLanguage, showRuntime, showReachability)); err != nil {
			return "", err
		}
	}

	if err := writer.Flush(); err != nil {
		return "", err
	}
	appendWarnings(&buffer, report)
	return buffer.String(), nil
}

func formatEmpty(report Report) string {
	var buffer bytes.Buffer
	buffer.WriteString("No dependencies to report.\n")
	appendUsageUncertainty(&buffer, report.UsageUncertainty)
	appendScopeMetadata(&buffer, report.Scope)
	appendEffectiveThresholds(&buffer, report)
	appendEffectivePolicy(&buffer, report)
	appendCodemodApply(&buffer, report.Dependencies)
	appendWarnings(&buffer, report)
	return buffer.String()
}

func writeTableHeader(writer *tabwriter.Writer, showLanguage, showRuntime, showReachability bool) error {
	columns := make([]string, 0, 12)
	if showLanguage {
		columns = append(columns, "Language")
	}
	columns = append(columns, "Dependency", "Used/Total", "Used%")
	if showRuntime {
		columns = append(columns, "Runtime")
	}
	columns = append(columns, "License", "Provenance", "Est. Unused Size", "Candidate Score", "Score Components")
	if showReachability {
		columns = append(columns, "Reachability")
	}
	columns = append(columns, "Top Symbols")
	_, err := fmt.Fprintln(writer, strings.Join(columns, "\t"))
	return err
}

func formatTableRow(dep DependencyReport, showLanguage, showRuntime, showReachability bool) string {
	usedPercent := dep.UsedPercent
	if usedPercent <= 0 && dep.TotalExportsCount > 0 {
		usedPercent = (float64(dep.UsedExportsCount) / float64(dep.TotalExportsCount)) * 100
	}

	columns := make([]string, 0, 12)
	if showLanguage {
		columns = append(columns, dep.Language)
	}
	columns = append(columns, dep.Name, fmt.Sprintf("%d/%d", dep.UsedExportsCount, dep.TotalExportsCount), fmt.Sprintf("%.1f", usedPercent))
	if showRuntime {
		columns = append(columns, formatRuntimeUsage(dep.RuntimeUsage))
	}
	columns = append(columns, formatDependencyLicense(dep.License), formatDependencyProvenance(dep.Provenance), formatBytes(dep.EstimatedUnusedBytes), formatCandidateScore(dep.RemovalCandidate), formatScoreComponents(dep.RemovalCandidate))
	if showReachability {
		columns = append(columns, formatReachabilityConfidence(dep.ReachabilityConfidence))
	}
	columns = append(columns, formatTopSymbols(dep.TopUsedSymbols))
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

func hasReachabilityColumn(dependencies []DependencyReport) bool {
	for _, dep := range dependencies {
		if dep.ReachabilityConfidence != nil {
			return true
		}
	}
	return false
}

func appendSummary(buffer *bytes.Buffer, summary *Summary) {
	if summary == nil {
		return
	}
	buffer.WriteString(fmt.Sprintf("Summary: %d deps, Used/Total: %d/%d (%.1f%%)\n\n", summary.DependencyCount, summary.UsedExportsCount, summary.TotalExportsCount, summary.UsedPercent))
	buffer.WriteString(fmt.Sprintf("Licenses: known=%d, unknown=%d, denied=%d\n\n", summary.KnownLicenseCount, summary.UnknownLicenseCount, summary.DeniedLicenseCount))
	if summary.Reachability != nil {
		buffer.WriteString(fmt.Sprintf("Reachability confidence: avg=%.1f range=%.1f-%.1f (%s)\n\n", summary.Reachability.AverageScore, summary.Reachability.LowestScore, summary.Reachability.HighestScore, summary.Reachability.Model))
	}
}

func appendUsageUncertainty(buffer *bytes.Buffer, usage *UsageUncertainty) {
	if usage == nil {
		return
	}
	buffer.WriteString(fmt.Sprintf("Usage certainty: confirmed imports=%d, uncertain imports=%d\n\n", usage.ConfirmedImportUses, usage.UncertainImportUses))
}

func appendScopeMetadata(buffer *bytes.Buffer, scope *ScopeMetadata) {
	if scope == nil {
		return
	}
	buffer.WriteString("Scope:\n")
	buffer.WriteString(fmt.Sprintf("- mode: %s\n", scope.Mode))
	if len(scope.Packages) > 0 {
		buffer.WriteString("- packages: ")
		buffer.WriteString(strings.Join(scope.Packages, ", "))
		buffer.WriteString("\n")
	}
	buffer.WriteString("\n")
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
	if len(report.EffectivePolicy.License.Deny) > 0 {
		buffer.WriteString("- license_deny: ")
		buffer.WriteString(strings.Join(report.EffectivePolicy.License.Deny, ", "))
		buffer.WriteString("\n")
	}
	buffer.WriteString(fmt.Sprintf("- license_fail_on_deny: %t\n", report.EffectivePolicy.License.FailOnDenied))
	buffer.WriteString(fmt.Sprintf("- license_include_registry_provenance: %t\n", report.EffectivePolicy.License.IncludeRegistryProvenance))
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
	buffer.WriteString(fmt.Sprintf("- license_delta: known %+d, unknown %+d, denied %+d\n", comparison.SummaryDelta.KnownLicenseCountDelta, comparison.SummaryDelta.UnknownLicenseCountDelta, comparison.SummaryDelta.DeniedLicenseCountDelta))
	buffer.WriteString(fmt.Sprintf("- changed: %d, regressions: %d, progressions: %d, added: %d, removed: %d, unchanged: %d\n", len(comparison.Dependencies), len(comparison.Regressions), len(comparison.Progressions), len(comparison.Added), len(comparison.Removed), comparison.UnchangedRows))
	if len(comparison.NewDeniedLicenses) > 0 {
		for _, denied := range comparison.NewDeniedLicenses {
			buffer.WriteString(fmt.Sprintf("  new denied license %s/%s (%s)\n", denied.Language, denied.Name, denied.SPDX))
		}
	}

	for _, delta := range topWasteDeltas(comparison.Regressions, 3) {
		buffer.WriteString(fmt.Sprintf("  regression %s/%s waste %+0.1f%% used %+0.1f%%\n", delta.Language, delta.Name, delta.WastePercentDelta, delta.UsedPercentDelta))
	}
	for _, delta := range topWasteDeltas(comparison.Progressions, 3) {
		buffer.WriteString(fmt.Sprintf("  progression %s/%s waste %+0.1f%% used %+0.1f%%\n", delta.Language, delta.Name, delta.WastePercentDelta, delta.UsedPercentDelta))
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
		buffer.WriteString(entry.name)
		buffer.WriteString("\n")
		buffer.WriteString(fmt.Sprintf("  applied: %d file(s), %d patch(es)\n", entry.apply.AppliedFiles, entry.apply.AppliedPatches))
		buffer.WriteString(fmt.Sprintf("  skipped: %d file(s), %d patch(es)\n", entry.apply.SkippedFiles, entry.apply.SkippedPatches))
		buffer.WriteString(fmt.Sprintf("  failed: %d file(s), %d patch(es)\n", entry.apply.FailedFiles, entry.apply.FailedPatches))
		if entry.apply.BackupPath != "" {
			buffer.WriteString("  backup: ")
			buffer.WriteString(entry.apply.BackupPath)
			buffer.WriteString("\n")
		}
		for _, result := range entry.apply.Results {
			buffer.WriteString(fmt.Sprintf("  %s %s (%d patch(es))", result.Status, result.File, result.PatchCount))
			if strings.TrimSpace(result.Message) != "" {
				buffer.WriteString(": ")
				buffer.WriteString(result.Message)
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
		buffer.WriteString(warning)
		buffer.WriteString("\n")
	}
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
