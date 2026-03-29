package report

import (
	"bytes"
	"fmt"
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

func writef(buffer *bytes.Buffer, format string, args ...any) {
	formatted := fmt.Sprintf(format, args...)
	buffer.WriteString(formatted)
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
