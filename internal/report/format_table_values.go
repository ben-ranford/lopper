package report

import (
	"fmt"
	"strings"
)

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
	return sanitizeTerminalString(strings.Join(items, ", "))
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

func formatReachabilityConfidence(confidence *ReachabilityConfidence) string {
	if confidence == nil {
		return "-"
	}
	if strings.TrimSpace(confidence.Summary) == "" {
		return sanitizeTerminalString(fmt.Sprintf("%.1f", confidence.Score))
	}
	return sanitizeTerminalString(fmt.Sprintf("%.1f (%s)", confidence.Score, confidence.Summary))
}

func formatVulnerabilities(findings []VulnerabilityFinding) string {
	if len(findings) == 0 {
		return "-"
	}
	sorted := append([]VulnerabilityFinding{}, findings...)
	sortVulnerabilityFindings(sorted)
	parts := make([]string, 0, len(sorted))
	for _, finding := range sorted {
		reachable := ""
		if finding.Reachable {
			reachable = " reachable"
		}
		fixed := ""
		if strings.TrimSpace(finding.FixedVersion) != "" {
			fixed = " fixed " + finding.FixedVersion
		}
		parts = append(parts, fmt.Sprintf("%s %s/%s %.1f%s%s", finding.AdvisoryID, finding.Severity, finding.Priority, finding.PriorityScore, reachable, fixed))
	}
	return sanitizeTerminalString(strings.Join(parts, "; "))
}

func formatRuntimeUsage(usage *RuntimeUsage) string {
	if usage == nil {
		return "-"
	}
	correlation := string(usage.Correlation)
	if strings.TrimSpace(correlation) == "" {
		correlation = "-"
	}
	parts := []string{fmt.Sprintf("%s (%d loads)", correlation, usage.LoadCount)}
	if len(usage.ParentModules) > 0 {
		parts = append(parts, "parents: "+formatRuntimeModuleUsageList(usage.ParentModules))
	}
	if len(usage.Entrypoints) > 0 {
		parts = append(parts, "entrypoints: "+formatRuntimeModuleUsageList(usage.Entrypoints))
	}
	return sanitizeTerminalString(strings.Join(parts, "; "))
}

func formatRuntimeDelta(delta *RuntimeDelta) string {
	if delta == nil {
		return "-"
	}
	if !delta.Comparable {
		return formatRuntimeDeltaNotComparable(delta)
	}

	parts := make([]string, 0, 6)
	appendRuntimeDeltaPart(&parts, formatRuntimeLoadDelta(delta))
	appendRuntimeDeltaPart(&parts, runtimeDeltaFlag(delta.NewRuntimeLoads, "new runtime loads"))
	appendRuntimeDeltaPart(&parts, runtimeDeltaFlag(delta.RemovedRuntimeLoads, "removed runtime loads"))
	appendRuntimeDeltaPart(&parts, formatRuntimeCorrelationDelta(delta))
	appendRuntimeDeltaPart(&parts, runtimeDeltaFlag(delta.RuntimeOnlyRegression, "runtime-only regression"))
	appendRuntimeDeltaPart(&parts, runtimeDeltaFlag(delta.RuntimeOnlyImprovement, "runtime-only improvement"))
	appendRuntimeDeltaPart(&parts, runtimeModuleGroupDelta("modules", delta.ModulesAdded, delta.ModulesRemoved, delta.ModulesChanged))
	appendRuntimeDeltaPart(&parts, runtimeModuleGroupDelta("parent modules", delta.ParentModulesAdded, delta.ParentModulesRemoved, delta.ParentModulesChanged))
	appendRuntimeDeltaPart(&parts, runtimeModuleGroupDelta("entrypoints", delta.EntrypointsAdded, delta.EntrypointsRemoved, delta.EntrypointsChanged))
	if len(parts) == 0 {
		return "no runtime delta"
	}
	return sanitizeTerminalString(strings.Join(parts, "; "))
}

func formatRuntimeDeltaNotComparable(delta *RuntimeDelta) string {
	switch {
	case !delta.BaselinePresent && delta.CurrentPresent:
		return "not comparable (baseline runtime data missing)"
	case delta.BaselinePresent && !delta.CurrentPresent:
		return "not comparable (current runtime data missing)"
	default:
		return "not comparable"
	}
}

func formatRuntimeLoadDelta(delta *RuntimeDelta) string {
	if delta.LoadCountDelta == nil || *delta.LoadCountDelta == 0 {
		return ""
	}
	return fmt.Sprintf("loads %+d", *delta.LoadCountDelta)
}

func formatRuntimeCorrelationDelta(delta *RuntimeDelta) string {
	if delta.BaselineCorrelation == delta.CurrentCorrelation {
		return ""
	}
	return fmt.Sprintf("correlation %s -> %s", delta.BaselineCorrelation, delta.CurrentCorrelation)
}

func runtimeDeltaFlag(enabled bool, label string) string {
	if !enabled {
		return ""
	}
	return label
}

func runtimeModuleGroupDelta(label string, added, removed, changed []RuntimeModuleDelta) string {
	if len(added) == 0 && len(removed) == 0 && len(changed) == 0 {
		return ""
	}
	return label + " changed"
}

func appendRuntimeDeltaPart(parts *[]string, value string) {
	if value != "" {
		*parts = append(*parts, value)
	}
}

func formatDependencyLicense(license *DependencyLicense) string {
	if license == nil {
		return "unknown"
	}
	if license.Unknown || strings.TrimSpace(license.SPDX) == "" {
		if license.Denied {
			return "unknown (denied)"
		}
		return "unknown"
	}
	if license.Denied {
		return sanitizeTerminalString(license.SPDX + " (denied)")
	}
	return sanitizeTerminalString(license.SPDX)
}

func formatDependencyProvenance(provenance *DependencyProvenance) string {
	if provenance == nil {
		return "-"
	}
	if strings.TrimSpace(provenance.Source) == "" && len(provenance.Signals) == 0 {
		return "-"
	}
	if strings.TrimSpace(provenance.Source) != "" && len(provenance.Signals) == 0 {
		return sanitizeTerminalString(provenance.Source)
	}
	return sanitizeTerminalString(provenance.Source + " (" + strings.Join(provenance.Signals, ", ") + ")")
}

func formatRuntimeModuleUsageList(items []RuntimeModuleUsage) string {
	if len(items) == 0 {
		return "-"
	}
	parts := make([]string, 0, len(items))
	for _, item := range items {
		if item.Count > 1 {
			parts = append(parts, fmt.Sprintf("%s (%d)", item.Module, item.Count))
			continue
		}
		parts = append(parts, item.Module)
	}
	return strings.Join(parts, ", ")
}
