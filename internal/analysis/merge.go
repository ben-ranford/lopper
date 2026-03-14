package analysis

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ben-ranford/lopper/internal/report"
)

func mergeReports(repoPath string, reports []report.Report) report.Report {
	result := report.Report{
		RepoPath: repoPath,
	}
	mergedByKey := make(map[string]report.DependencyReport)
	orderedKeys := make([]string, 0)

	for _, current := range reports {
		result.Warnings = append(result.Warnings, current.Warnings...)
		result.UsageUncertainty = mergeUsageUncertainty(result.UsageUncertainty, current.UsageUncertainty)
		if current.GeneratedAt.After(result.GeneratedAt) {
			result.GeneratedAt = current.GeneratedAt
		}
		for _, dep := range current.Dependencies {
			key := dep.Language + "\x00" + dep.Name
			if existing, ok := mergedByKey[key]; ok {
				mergedByKey[key] = mergeDependency(existing, dep)
				continue
			}
			mergedByKey[key] = dep
			orderedKeys = append(orderedKeys, key)
		}
	}

	sort.Strings(orderedKeys)
	result.Dependencies = make([]report.DependencyReport, 0, len(orderedKeys))
	for _, key := range orderedKeys {
		result.Dependencies = append(result.Dependencies, mergedByKey[key])
	}
	return result
}

func mergeUsageUncertainty(left, right *report.UsageUncertainty) *report.UsageUncertainty {
	if left == nil {
		if right == nil {
			return nil
		}
		copyRight := *right
		copyRight.Samples = cappedSampleCopy(right.Samples)
		return &copyRight
	}
	if right == nil {
		copyLeft := *left
		copyLeft.Samples = cappedSampleCopy(left.Samples)
		return &copyLeft
	}

	merged := &report.UsageUncertainty{
		ConfirmedImportUses: left.ConfirmedImportUses + right.ConfirmedImportUses,
		UncertainImportUses: left.UncertainImportUses + right.UncertainImportUses,
	}
	merged.Samples = append(merged.Samples, left.Samples...)
	if len(merged.Samples) < 5 {
		remaining := 5 - len(merged.Samples)
		if remaining > len(right.Samples) {
			remaining = len(right.Samples)
		}
		merged.Samples = append(merged.Samples, right.Samples[:remaining]...)
	}
	return merged
}

func cappedSampleCopy(samples []report.Location) []report.Location {
	if len(samples) > 5 {
		samples = samples[:5]
	}
	return append([]report.Location{}, samples...)
}

func mergeDependency(left, right report.DependencyReport) report.DependencyReport {
	merged := left
	merged.UsedExportsCount += right.UsedExportsCount
	merged.TotalExportsCount += right.TotalExportsCount
	if merged.TotalExportsCount > 0 {
		merged.UsedPercent = (float64(merged.UsedExportsCount) / float64(merged.TotalExportsCount)) * 100
	}
	merged.EstimatedUnusedBytes += right.EstimatedUnusedBytes

	merged.UsedImports = mergeImportUses(left.UsedImports, right.UsedImports)
	merged.UnusedImports = mergeImportUses(left.UnusedImports, right.UnusedImports)
	merged.UnusedImports = filterUsedOverlaps(merged.UnusedImports, merged.UsedImports)
	merged.UnusedExports = mergeSymbolRefs(left.UnusedExports, right.UnusedExports)
	merged.RiskCues = mergeRiskCues(left.RiskCues, right.RiskCues)
	merged.Recommendations = mergeRecommendations(left.Recommendations, right.Recommendations)
	merged.Codemod = mergeCodemodReport(left.Codemod, right.Codemod)
	merged.TopUsedSymbols = mergeTopSymbols(left.TopUsedSymbols, right.TopUsedSymbols)
	merged.RuntimeUsage = mergeRuntimeUsage(left.RuntimeUsage, right.RuntimeUsage)

	return merged
}

func filterUsedOverlaps(unused, used []report.ImportUse) []report.ImportUse {
	if len(unused) == 0 || len(used) == 0 {
		return unused
	}

	usedLookup := make(map[string]struct{}, len(used))
	for i := range used {
		usedLookup[importUseKey(used[i])] = struct{}{}
	}

	filtered := unused[:0]
	for i := range unused {
		item := unused[i]
		if _, exists := usedLookup[importUseKey(item)]; exists {
			continue
		}
		filtered = append(filtered, item)
	}
	return filtered
}

func importUseKey(item report.ImportUse) string {
	return item.Module + "\x00" + item.Name
}

func runtimeUsageSignals(usage *report.RuntimeUsage) (hasStatic bool, hasRuntime bool) {
	if usage == nil {
		return false, false
	}
	switch usage.Correlation {
	case report.RuntimeCorrelationOverlap:
		return true, true
	case report.RuntimeCorrelationRuntimeOnly:
		return false, true
	case report.RuntimeCorrelationStaticOnly:
		return true, false
	}
	if usage.RuntimeOnly {
		return false, usage.LoadCount > 0
	}
	return true, usage.LoadCount > 0
}

func mergeRuntimeCorrelation(hasStatic, hasRuntime bool) report.RuntimeCorrelation {
	switch {
	case hasStatic && hasRuntime:
		return report.RuntimeCorrelationOverlap
	case hasRuntime:
		return report.RuntimeCorrelationRuntimeOnly
	default:
		return report.RuntimeCorrelationStaticOnly
	}
}

func mergeRuntimeModuleUsage(left, right []report.RuntimeModuleUsage) []report.RuntimeModuleUsage {
	merged := make(map[string]report.RuntimeModuleUsage)
	for _, item := range append(append([]report.RuntimeModuleUsage{}, left...), right...) {
		if current, ok := merged[item.Module]; ok {
			current.Count += item.Count
			merged[item.Module] = current
			continue
		}
		merged[item.Module] = item
	}
	items := make([]report.RuntimeModuleUsage, 0, len(merged))
	for _, item := range merged {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Count == items[j].Count {
			return items[i].Module < items[j].Module
		}
		return items[i].Count > items[j].Count
	})
	return items
}

func mergeRuntimeSymbolUsage(left, right []report.RuntimeSymbolUsage) []report.RuntimeSymbolUsage {
	merged := make(map[string]report.RuntimeSymbolUsage)
	for _, item := range append(append([]report.RuntimeSymbolUsage{}, left...), right...) {
		key := item.Module + "\x00" + item.Symbol
		if current, ok := merged[key]; ok {
			current.Count += item.Count
			merged[key] = current
			continue
		}
		merged[key] = item
	}
	items := make([]report.RuntimeSymbolUsage, 0, len(merged))
	for _, item := range merged {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Count == items[j].Count {
			if items[i].Module == items[j].Module {
				return items[i].Symbol < items[j].Symbol
			}
			return items[i].Module < items[j].Module
		}
		return items[i].Count > items[j].Count
	})
	if len(items) > 5 {
		items = items[:5]
	}
	return items
}

func mergeSymbolRefs(left, right []report.SymbolRef) []report.SymbolRef {
	return mergeUniqueSorted(left, right, symbolRefKey, sortSymbolRefs)
}

func mergeRiskCues(left, right []report.RiskCue) []report.RiskCue {
	return mergeUniqueSorted(left, right, riskCueKey, sortRiskCues)
}

func mergeRecommendations(left, right []report.Recommendation) []report.Recommendation {
	return mergeUniqueSorted(left, right, recommendationKey, sortRecommendations)
}

func mergeCodemodReport(left, right *report.CodemodReport) *report.CodemodReport {
	if left == nil && right == nil {
		return nil
	}
	if left == nil {
		copyRight := *right
		return &copyRight
	}
	if right == nil {
		copyLeft := *left
		return &copyLeft
	}

	mode := left.Mode
	if strings.TrimSpace(mode) == "" {
		mode = right.Mode
	}
	suggestions := mergeUniqueSorted(left.Suggestions, right.Suggestions, codemodSuggestionKey, sortCodemodSuggestions)
	skips := mergeUniqueSorted(left.Skips, right.Skips, codemodSkipKey, sortCodemodSkips)
	return &report.CodemodReport{
		Mode:        mode,
		Suggestions: suggestions,
		Skips:       skips,
	}
}

func mergedSuggestionSortKey(item report.CodemodSuggestion) string {
	return fmt.Sprintf("%s|%09d|%s|%s", item.File, item.Line, item.ImportName, item.ToModule)
}

func mergedSkipSortKey(item report.CodemodSkip) string {
	return fmt.Sprintf("%s|%09d|%s|%s", item.File, item.Line, item.ReasonCode, item.ImportName)
}

func symbolRefKey(item report.SymbolRef) string {
	return item.Module + "\x00" + item.Name
}

func sortSymbolRefs(items []report.SymbolRef) {
	sort.Slice(items, func(i, j int) bool {
		if items[i].Module == items[j].Module {
			return items[i].Name < items[j].Name
		}
		return items[i].Module < items[j].Module
	})
}

func riskCueKey(item report.RiskCue) string {
	return item.Code + "\x00" + item.Severity
}

func sortRiskCues(items []report.RiskCue) {
	sort.Slice(items, func(i, j int) bool {
		return items[i].Code < items[j].Code
	})
}

func recommendationKey(item report.Recommendation) string {
	return item.Code
}

func sortRecommendations(items []report.Recommendation) {
	sort.Slice(items, func(i, j int) bool {
		return recommendationLess(items[i], items[j])
	})
}

func codemodSuggestionKey(item report.CodemodSuggestion) string {
	return item.File + "\x00" + fmt.Sprintf("%d", item.Line) + "\x00" + item.ImportName + "\x00" + item.ToModule
}

func sortCodemodSuggestions(items []report.CodemodSuggestion) {
	sort.Slice(items, func(i, j int) bool {
		return mergedSuggestionSortKey(items[i]) < mergedSuggestionSortKey(items[j])
	})
}

func codemodSkipKey(item report.CodemodSkip) string {
	return item.File + "\x00" + fmt.Sprintf("%d", item.Line) + "\x00" + item.ImportName + "\x00" + item.ReasonCode
}

func sortCodemodSkips(items []report.CodemodSkip) {
	sort.Slice(items, func(i, j int) bool {
		return mergedSkipSortKey(items[i]) < mergedSkipSortKey(items[j])
	})
}

func mergeUniqueSorted[T any](left []T, right []T, keyFn func(T) string, sortFn func([]T)) []T {
	merged := make(map[string]T)
	for _, elem := range left {
		merged[keyFn(elem)] = elem
	}
	for _, elem := range right {
		merged[keyFn(elem)] = elem
	}
	items := make([]T, 0, len(merged))
	for _, elem := range merged {
		items = append(items, elem)
	}
	sortFn(items)
	return items
}

func mergeRuntimeUsage(left, right *report.RuntimeUsage) *report.RuntimeUsage {
	if left == nil && right == nil {
		return nil
	}
	loadCount := 0
	hasStatic := false
	hasRuntime := false
	leftModules := []report.RuntimeModuleUsage(nil)
	leftSymbols := []report.RuntimeSymbolUsage(nil)
	rightModules := []report.RuntimeModuleUsage(nil)
	rightSymbols := []report.RuntimeSymbolUsage(nil)
	if left != nil {
		loadCount += left.LoadCount
		leftHasStatic, leftHasRuntime := runtimeUsageSignals(left)
		hasStatic = hasStatic || leftHasStatic
		hasRuntime = hasRuntime || leftHasRuntime
		leftModules = left.Modules
		leftSymbols = left.TopSymbols
	}
	if right != nil {
		loadCount += right.LoadCount
		rightHasStatic, rightHasRuntime := runtimeUsageSignals(right)
		hasStatic = hasStatic || rightHasStatic
		hasRuntime = hasRuntime || rightHasRuntime
		rightModules = right.Modules
		rightSymbols = right.TopSymbols
	}
	correlation := mergeRuntimeCorrelation(hasStatic, hasRuntime)
	return &report.RuntimeUsage{
		LoadCount:   loadCount,
		Correlation: correlation,
		RuntimeOnly: correlation == report.RuntimeCorrelationRuntimeOnly,
		Modules:     mergeRuntimeModuleUsage(leftModules, rightModules),
		TopSymbols:  mergeRuntimeSymbolUsage(leftSymbols, rightSymbols),
	}
}

func mergeTopSymbols(left, right []report.SymbolUsage) []report.SymbolUsage {
	merged := make(map[string]report.SymbolUsage)
	for _, item := range append(append([]report.SymbolUsage{}, left...), right...) {
		key := item.Module + "\x00" + item.Name
		if current, ok := merged[key]; ok {
			current.Count += item.Count
			merged[key] = current
			continue
		}
		merged[key] = item
	}
	items := make([]report.SymbolUsage, 0, len(merged))
	for _, item := range merged {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Count == items[j].Count {
			return items[i].Name < items[j].Name
		}
		return items[i].Count > items[j].Count
	})
	if len(items) > 5 {
		items = items[:5]
	}
	return items
}

func recommendationPriorityRank(priority string) int {
	switch strings.ToLower(strings.TrimSpace(priority)) {
	case "high":
		return 0
	case "medium":
		return 1
	case "low":
		return 2
	default:
		return 3
	}
}

func recommendationLess(left, right report.Recommendation) bool {
	if left.Priority == right.Priority {
		return left.Code < right.Code
	}
	return recommendationPriorityRank(left.Priority) < recommendationPriorityRank(right.Priority)
}

func mergeImportUses(left, right []report.ImportUse) []report.ImportUse {
	merged := make(map[string]report.ImportUse)
	for _, item := range append(append([]report.ImportUse{}, left...), right...) {
		key := item.Module + "\x00" + item.Name
		if current, ok := merged[key]; ok {
			current.Locations = append(current.Locations, item.Locations...)
			merged[key] = current
			continue
		}
		merged[key] = item
	}
	items := make([]report.ImportUse, 0, len(merged))
	for _, item := range merged {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Module == items[j].Module {
			return items[i].Name < items[j].Name
		}
		return items[i].Module < items[j].Module
	})
	return items
}
