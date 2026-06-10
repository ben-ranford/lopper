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
