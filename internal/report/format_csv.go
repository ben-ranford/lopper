package report

import (
	"bytes"
	"encoding/csv"
	"github.com/ben-ranford/lopper/internal/csvsanitize"
	"sort"
	"strconv"
	"strings"
	"time"
)

var analyseCSVHeader = []string{
	"generated_at",
	"schema_version",
	"repo_path",
	"scope_mode",
	"scope_packages",
	"language",
	"dependency_name",
	"used_exports_count",
	"total_exports_count",
	"used_percent",
	"waste_percent",
	"estimated_unused_bytes",
	"top_used_symbols",
	"used_imports",
	"unused_imports",
	"unused_exports",
	"risk_cues",
	"recommendations",
	"runtime_load_count",
	"runtime_correlation",
	"runtime_only",
	"runtime_modules",
	"runtime_top_symbols",
	"reachability_model",
	"reachability_score",
	"reachability_summary",
	"reachability_rationale_codes",
	"removal_candidate_score",
	"removal_candidate_usage",
	"removal_candidate_impact",
	"removal_candidate_confidence",
	"removal_candidate_rationale",
	"license_spdx",
	"license_raw",
	"license_source",
	"license_confidence",
	"license_unknown",
	"license_denied",
	"license_evidence",
	"provenance_source",
	"provenance_confidence",
	"provenance_signals",
}

func formatCSV(reportData Report) (string, error) {
	var buffer bytes.Buffer
	writer := csv.NewWriter(&buffer)

	if err := writer.Write(analyseCSVHeader); err != nil {
		return "", err
	}
	for _, dep := range sortedDependenciesForCSV(reportData.Dependencies) {
		if err := writer.Write(csvsanitize.EscapeLeadingFormulaRow(formatDependencyCSVRow(reportData, dep))); err != nil {
			return "", err
		}
	}

	writer.Flush()
	if err := writer.Error(); err != nil {
		return "", err
	}
	return buffer.String(), nil
}

func sortedDependenciesForCSV(dependencies []DependencyReport) []DependencyReport {
	items := append([]DependencyReport{}, dependencies...)
	sort.Slice(items, func(i, j int) bool {
		if items[i].Language == items[j].Language {
			return items[i].Name < items[j].Name
		}
		return items[i].Language < items[j].Language
	})
	return items
}

func formatDependencyCSVRow(reportData Report, dep DependencyReport) []string {
	scopeMode := ""
	scopePackages := ""
	if reportData.Scope != nil {
		scopeMode = reportData.Scope.Mode
		scopePackages = joinSortedStrings(reportData.Scope.Packages)
	}

	usedPercent := effectiveUsedPercent(dep)
	wastePercent := 0.0
	if dep.TotalExportsCount > 0 {
		wastePercent = 100 - usedPercent
	}
	license := normalizedDependencyLicenseCSV(dep.License)

	return []string{
		formatCSVTime(reportData.GeneratedAt),
		reportData.SchemaVersion,
		reportData.RepoPath,
		scopeMode,
		scopePackages,
		dep.Language,
		dep.Name,
		strconv.Itoa(dep.UsedExportsCount),
		strconv.Itoa(dep.TotalExportsCount),
		formatCSVFloat(usedPercent),
		formatCSVFloat(wastePercent),
		strconv.FormatInt(dep.EstimatedUnusedBytes, 10),
		formatCSVTopUsedSymbols(dep.TopUsedSymbols),
		formatCSVImportUses(dep.UsedImports),
		formatCSVImportUses(dep.UnusedImports),
		formatCSVSymbolRefs(dep.UnusedExports),
		formatCSVRiskCues(dep.RiskCues),
		formatCSVRecommendations(dep.Recommendations),
		formatCSVRuntimeLoadCount(dep.RuntimeUsage),
		formatCSVRuntimeCorrelation(dep.RuntimeUsage),
		formatCSVRuntimeOnly(dep.RuntimeUsage),
		formatCSVRuntimeModules(dep.RuntimeUsage),
		formatCSVRuntimeTopSymbols(dep.RuntimeUsage),
		formatCSVReachabilityModel(dep.ReachabilityConfidence),
		formatCSVReachabilityScore(dep.ReachabilityConfidence),
		formatCSVReachabilitySummary(dep.ReachabilityConfidence),
		formatCSVReachabilityRationale(dep.ReachabilityConfidence),
		formatCSVRemovalCandidateScore(dep.RemovalCandidate),
		formatCSVRemovalCandidateMetric(dep.RemovalCandidate, "usage"),
		formatCSVRemovalCandidateMetric(dep.RemovalCandidate, "impact"),
		formatCSVRemovalCandidateMetric(dep.RemovalCandidate, "confidence"),
		formatCSVRemovalCandidateRationale(dep.RemovalCandidate),
		license.SPDX,
		license.Raw,
		license.Source,
		license.Confidence,
		strconv.FormatBool(license.Unknown),
		strconv.FormatBool(license.Denied),
		joinSortedStrings(license.Evidence),
		formatCSVProvenanceSource(dep.Provenance),
		formatCSVProvenanceConfidence(dep.Provenance),
		formatCSVProvenanceSignals(dep.Provenance),
	}
}

func effectiveUsedPercent(dep DependencyReport) float64 {
	if dep.UsedPercent > 0 || dep.TotalExportsCount == 0 {
		return dep.UsedPercent
	}
	return (float64(dep.UsedExportsCount) / float64(dep.TotalExportsCount)) * 100
}

func normalizedDependencyLicenseCSV(license *DependencyLicense) DependencyLicense {
	if license == nil {
		return DependencyLicense{
			Source:     licenseSourceUnknown,
			Confidence: "low",
			Unknown:    true,
		}
	}

	copyLicense := *license
	if strings.TrimSpace(copyLicense.Source) == "" {
		copyLicense.Source = licenseSourceUnknown
	}
	if strings.TrimSpace(copyLicense.Confidence) == "" {
		copyLicense.Confidence = "low"
	}
	if copyLicense.Unknown || strings.TrimSpace(copyLicense.SPDX) == "" {
		copyLicense.Unknown = true
	}
	return copyLicense
}

func formatCSVTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.Format(time.RFC3339)
}

func formatCSVFloat(value float64) string {
	return strconv.FormatFloat(value, 'f', 1, 64)
}

func joinSortedStrings(values []string) string {
	if len(values) == 0 {
		return ""
	}
	items := append([]string{}, values...)
	sort.Strings(items)
	return strings.Join(items, "|")
}

func formatCSVSorted[T any](items []T, less func(T, T) bool, render func(T) string) string {
	if len(items) == 0 {
		return ""
	}
	sorted := append([]T{}, items...)
	sort.Slice(sorted, func(i, j int) bool {
		return less(sorted[i], sorted[j])
	})
	formatted := make([]string, 0, len(sorted))
	for _, value := range sorted {
		formatted = append(formatted, render(value))
	}
	return strings.Join(formatted, "|")
}

func lessCSVQualifiedName(moduleA, nameA, moduleB, nameB string) bool {
	if moduleA == moduleB {
		return nameA < nameB
	}
	return moduleA < moduleB
}

func lessCSVQualifiedCount(moduleA, nameA string, countA int, moduleB, nameB string, countB int) bool {
	if countA == countB {
		return lessCSVQualifiedName(moduleA, nameA, moduleB, nameB)
	}
	return countA > countB
}

func formatCSVQualifiedValues[T any](items []T, module func(T) string, name func(T) string) string {
	less := func(a, b T) bool {
		return lessCSVQualifiedName(module(a), name(a), module(b), name(b))
	}
	render := func(item T) string {
		return formatCSVQualifiedName(module(item), name(item))
	}
	return formatCSVSorted(items, less, render)
}

func formatCSVCodeValues[T any](items []T, code func(T) string, value func(T) string) string {
	less := func(a, b T) bool {
		if code(a) == code(b) {
			return value(a) < value(b)
		}
		return code(a) < code(b)
	}
	render := func(item T) string {
		return code(item) + ":" + value(item)
	}
	return formatCSVSorted(items, less, render)
}

func compareCSVSymbolUsage(a, b SymbolUsage) bool {
	return lessCSVQualifiedCount(a.Module, a.Name, a.Count, b.Module, b.Name, b.Count)
}

func importUseModule(item ImportUse) string {
	return item.Module
}

func importUseName(item ImportUse) string {
	return item.Name
}

func symbolRefModule(item SymbolRef) string {
	return item.Module
}

func symbolRefName(item SymbolRef) string {
	return item.Name
}

func riskCueCode(item RiskCue) string {
	return item.Code
}

func riskCueSeverity(item RiskCue) string {
	return item.Severity
}

func recommendationCode(item Recommendation) string {
	return item.Code
}

func recommendationPriority(item Recommendation) string {
	return item.Priority
}

func compareCSVRuntimeModuleUsage(a, b RuntimeModuleUsage) bool {
	if a.Count == b.Count {
		return a.Module < b.Module
	}
	return a.Count > b.Count
}

func formatCSVRuntimeModuleUsage(item RuntimeModuleUsage) string {
	return item.Module + "=" + strconv.Itoa(item.Count)
}

func compareCSVRuntimeSymbolUsage(a, b RuntimeSymbolUsage) bool {
	return lessCSVQualifiedCount(a.Module, a.Symbol, a.Count, b.Module, b.Symbol, b.Count)
}

func formatCSVRuntimeSymbolUsage(item RuntimeSymbolUsage) string {
	name := item.Symbol
	if strings.TrimSpace(item.Module) != "" {
		name = item.Module + ":" + item.Symbol
	}
	return name + "=" + strconv.Itoa(item.Count)
}

func formatCSVTopUsedSymbols(symbols []SymbolUsage) string {
	return formatCSVSorted(symbols, compareCSVSymbolUsage, formatCSVSymbolUsage)
}

func formatCSVSymbolUsage(item SymbolUsage) string {
	name := item.Name
	if strings.TrimSpace(item.Module) != "" {
		name = item.Module + ":" + item.Name
	}
	return name + "=" + strconv.Itoa(item.Count)
}

func formatCSVImportUses(imports []ImportUse) string {
	return formatCSVQualifiedValues(imports, importUseModule, importUseName)
}

func formatCSVSymbolRefs(refs []SymbolRef) string {
	return formatCSVQualifiedValues(refs, symbolRefModule, symbolRefName)
}

func formatCSVQualifiedName(module, name string) string {
	if strings.TrimSpace(module) == "" {
		return name
	}
	return module + ":" + name
}

func formatCSVRiskCues(cues []RiskCue) string {
	return formatCSVCodeValues(cues, riskCueCode, riskCueSeverity)
}

func formatCSVRecommendations(recommendations []Recommendation) string {
	return formatCSVCodeValues(recommendations, recommendationCode, recommendationPriority)
}

func runtimeCorrelationValue(usage *RuntimeUsage) string {
	if usage == nil {
		return ""
	}
	if usage.Correlation != "" {
		return string(usage.Correlation)
	}
	switch {
	case usage.RuntimeOnly:
		return string(RuntimeCorrelationRuntimeOnly)
	case usage.LoadCount > 0:
		return string(RuntimeCorrelationOverlap)
	default:
		return string(RuntimeCorrelationStaticOnly)
	}
}

func formatCSVRuntimeLoadCount(usage *RuntimeUsage) string {
	if usage == nil {
		return ""
	}
	return strconv.Itoa(usage.LoadCount)
}

func formatCSVRuntimeCorrelation(usage *RuntimeUsage) string {
	return runtimeCorrelationValue(usage)
}

func formatCSVRuntimeOnly(usage *RuntimeUsage) string {
	if usage == nil {
		return ""
	}
	return strconv.FormatBool(usage.RuntimeOnly)
}

func formatCSVRuntimeModules(usage *RuntimeUsage) string {
	if usage == nil || len(usage.Modules) == 0 {
		return ""
	}
	return formatCSVSorted(usage.Modules, compareCSVRuntimeModuleUsage, formatCSVRuntimeModuleUsage)
}

func formatCSVRuntimeTopSymbols(usage *RuntimeUsage) string {
	if usage == nil || len(usage.TopSymbols) == 0 {
		return ""
	}
	return formatCSVSorted(usage.TopSymbols, compareCSVRuntimeSymbolUsage, formatCSVRuntimeSymbolUsage)
}

func formatCSVReachabilityModel(confidence *ReachabilityConfidence) string {
	if confidence == nil {
		return ""
	}
	return confidence.Model
}

func formatCSVReachabilityScore(confidence *ReachabilityConfidence) string {
	if confidence == nil {
		return ""
	}
	return formatCSVFloat(confidence.Score)
}

func formatCSVReachabilitySummary(confidence *ReachabilityConfidence) string {
	if confidence == nil {
		return ""
	}
	return confidence.Summary
}

func formatCSVReachabilityRationale(confidence *ReachabilityConfidence) string {
	if confidence == nil {
		return ""
	}
	return joinSortedStrings(confidence.RationaleCodes)
}

func formatCSVRemovalCandidateScore(candidate *RemovalCandidate) string {
	if candidate == nil {
		return ""
	}
	return formatCSVFloat(candidate.Score)
}

func formatCSVRemovalCandidateMetric(candidate *RemovalCandidate, field string) string {
	if candidate == nil {
		return ""
	}
	switch field {
	case "usage":
		return formatCSVFloat(candidate.Usage)
	case "impact":
		return formatCSVFloat(candidate.Impact)
	case "confidence":
		return formatCSVFloat(candidate.Confidence)
	default:
		return ""
	}
}

func formatCSVRemovalCandidateRationale(candidate *RemovalCandidate) string {
	if candidate == nil {
		return ""
	}
	return joinSortedStrings(candidate.Rationale)
}

func formatCSVProvenanceSource(provenance *DependencyProvenance) string {
	if provenance == nil {
		return ""
	}
	return provenance.Source
}

func formatCSVProvenanceConfidence(provenance *DependencyProvenance) string {
	if provenance == nil {
		return ""
	}
	return provenance.Confidence
}

func formatCSVProvenanceSignals(provenance *DependencyProvenance) string {
	if provenance == nil {
		return ""
	}
	return joinSortedStrings(provenance.Signals)
}
