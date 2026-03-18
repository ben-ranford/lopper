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

func formatReachabilityConfidence(confidence *ReachabilityConfidence) string {
	if confidence == nil {
		return "-"
	}
	if strings.TrimSpace(confidence.Summary) == "" {
		return fmt.Sprintf("%.1f", confidence.Score)
	}
	return fmt.Sprintf("%.1f (%s)", confidence.Score, confidence.Summary)
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
		switch {
		case usage.RuntimeOnly:
			correlation = string(RuntimeCorrelationRuntimeOnly)
		case usage.LoadCount > 0:
			correlation = string(RuntimeCorrelationOverlap)
		default:
			correlation = string(RuntimeCorrelationStaticOnly)
		}
	}
	return fmt.Sprintf("%s (%d loads)", correlation, usage.LoadCount)
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
		return license.SPDX + " (denied)"
	}
	return license.SPDX
}

func formatDependencyProvenance(provenance *DependencyProvenance) string {
	if provenance == nil {
		return "-"
	}
	if strings.TrimSpace(provenance.Source) == "" && len(provenance.Signals) == 0 {
		return "-"
	}
	if strings.TrimSpace(provenance.Source) != "" && len(provenance.Signals) == 0 {
		return provenance.Source
	}
	return provenance.Source + " (" + strings.Join(provenance.Signals, ", ") + ")"
}
