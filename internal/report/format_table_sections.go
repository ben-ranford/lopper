package report

import (
	"bytes"
	"sort"
	"strings"
)

func appendSummary(buffer *bytes.Buffer, summary *Summary) {
	if summary == nil {
		return
	}
	writef(buffer, "Summary: %d deps, Used/Total: %d/%d (%.1f%%)\n\n", summary.DependencyCount, summary.UsedExportsCount, summary.TotalExportsCount, summary.UsedPercent)
	writef(buffer, "Licenses: known=%d, unknown=%d, denied=%d\n\n", summary.KnownLicenseCount, summary.UnknownLicenseCount, summary.DeniedLicenseCount)
	if summary.Reachability != nil {
		writef(buffer, "Reachability confidence: avg=%.1f range=%.1f-%.1f (%s)\n\n", summary.Reachability.AverageScore, summary.Reachability.LowestScore, summary.Reachability.HighestScore, summary.Reachability.Model)
	}
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

func appendEffectiveThresholds(buffer *bytes.Buffer, report Report) {
	if report.EffectiveThresholds == nil {
		return
	}
	buffer.WriteString("Effective thresholds:\n")
	writef(buffer, "- fail_on_increase_percent: %d\n", report.EffectiveThresholds.FailOnIncreasePercent)
	writef(buffer, "- low_confidence_warning_percent: %d\n", report.EffectiveThresholds.LowConfidenceWarningPercent)
	writef(buffer, "- min_usage_percent_for_recommendations: %d\n", report.EffectiveThresholds.MinUsagePercentForRecommendations)
	writef(buffer, "- max_uncertain_import_count: %d\n", report.EffectiveThresholds.MaxUncertainImportCount)
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
	writef(buffer, "- fail_on_increase_percent: %d\n", report.EffectivePolicy.Thresholds.FailOnIncreasePercent)
	writef(buffer, "- low_confidence_warning_percent: %d\n", report.EffectivePolicy.Thresholds.LowConfidenceWarningPercent)
	writef(buffer, "- min_usage_percent_for_recommendations: %d\n", report.EffectivePolicy.Thresholds.MinUsagePercentForRecommendations)
	writef(buffer, "- max_uncertain_import_count: %d\n", report.EffectivePolicy.Thresholds.MaxUncertainImportCount)
	writef(buffer, "- removal_candidate_weight_usage: %.3f\n", report.EffectivePolicy.RemovalCandidateWeights.Usage)
	writef(buffer, "- removal_candidate_weight_impact: %.3f\n", report.EffectivePolicy.RemovalCandidateWeights.Impact)
	writef(buffer, "- removal_candidate_weight_confidence: %.3f\n", report.EffectivePolicy.RemovalCandidateWeights.Confidence)
	if len(report.EffectivePolicy.License.Deny) > 0 {
		buffer.WriteString("- license_deny: ")
		buffer.WriteString(strings.Join(report.EffectivePolicy.License.Deny, ", "))
		buffer.WriteString("\n")
	}
	writef(buffer, "- license_fail_on_deny: %t\n", report.EffectivePolicy.License.FailOnDenied)
	writef(buffer, "- license_include_registry_provenance: %t\n", report.EffectivePolicy.License.IncludeRegistryProvenance)
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
		writef(buffer, "%d deps, Used/Total: %d/%d (%.1f%%)\n", item.DependencyCount, item.UsedExportsCount, item.TotalExportsCount, item.UsedPercent)
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
		buffer.WriteString(entry.name)
		buffer.WriteString("\n")
		writef(buffer, "  applied: %d file(s), %d patch(es)\n", entry.apply.AppliedFiles, entry.apply.AppliedPatches)
		writef(buffer, "  skipped: %d file(s), %d patch(es)\n", entry.apply.SkippedFiles, entry.apply.SkippedPatches)
		writef(buffer, "  failed: %d file(s), %d patch(es)\n", entry.apply.FailedFiles, entry.apply.FailedPatches)
		if entry.apply.BackupPath != "" {
			buffer.WriteString("  backup: ")
			buffer.WriteString(entry.apply.BackupPath)
			buffer.WriteString("\n")
		}
		for _, result := range entry.apply.Results {
			writef(buffer, "  %s %s (%d patch(es))", result.Status, result.File, result.PatchCount)
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
