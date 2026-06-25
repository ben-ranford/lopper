package report

import (
	"encoding/json"
	"math"
	"net/url"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	cycloneDXBOMFormat   = "CycloneDX"
	cycloneDXSpecVersion = "1.6"
	cycloneDXSchemaURL   = "http://cyclonedx.org/schema/bom-1.6.schema.json"
)

type cycloneDXBOM struct {
	Schema      string               `json:"$schema,omitempty"`
	BOMFormat   string               `json:"bomFormat"`
	SpecVersion string               `json:"specVersion"`
	Version     int                  `json:"version"`
	Metadata    *cycloneDXMetadata   `json:"metadata,omitempty"`
	Components  []cycloneDXComponent `json:"components"`
	Properties  []cycloneDXProperty  `json:"properties,omitempty"`
}

type cycloneDXMetadata struct {
	Timestamp string                  `json:"timestamp,omitempty"`
	Component *cycloneDXRootComponent `json:"component,omitempty"`
}

type cycloneDXRootComponent struct {
	Type string `json:"type"`
	Name string `json:"name"`
}

type cycloneDXComponent struct {
	BOMRef     string                   `json:"bom-ref,omitempty"`
	Type       string                   `json:"type"`
	Name       string                   `json:"name"`
	Licenses   []cycloneDXLicenseChoice `json:"licenses,omitempty"`
	Properties []cycloneDXProperty      `json:"properties,omitempty"`
}

type cycloneDXLicenseChoice struct {
	License cycloneDXLicense `json:"license"`
}

type cycloneDXLicense struct {
	Name string `json:"name"`
}

type cycloneDXProperty struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

func formatCycloneDXJSON(reportData Report) (string, error) {
	bom := cycloneDXBOM{
		Schema:      cycloneDXSchemaURL,
		BOMFormat:   cycloneDXBOMFormat,
		SpecVersion: cycloneDXSpecVersion,
		Version:     1,
		Metadata:    formatCycloneDXMetadata(reportData),
		Components:  formatCycloneDXComponents(reportData),
		Properties:  formatCycloneDXBOMProperties(reportData),
	}

	payload, err := json.MarshalIndent(bom, "", "  ")
	if err != nil {
		return "", err
	}
	return string(payload) + "\n", nil
}

func formatCycloneDXMetadata(reportData Report) *cycloneDXMetadata {
	metadata := cycloneDXMetadata{}
	if !reportData.GeneratedAt.IsZero() {
		metadata.Timestamp = reportData.GeneratedAt.UTC().Format(time.RFC3339Nano)
	}
	if name := strings.TrimSpace(reportData.RepoPath); name != "" {
		metadata.Component = &cycloneDXRootComponent{Type: "application", Name: name}
	}
	if metadata.Timestamp == "" && metadata.Component == nil {
		return nil
	}
	return &metadata
}

func formatCycloneDXComponents(reportData Report) []cycloneDXComponent {
	dependencies := sortedDependenciesForCSV(reportData.Dependencies)
	components := make([]cycloneDXComponent, 0, len(dependencies))
	baselineDeltas := cycloneDXBaselineDeltaByDependency(reportData)
	seenRefs := map[string]int{}

	for _, dep := range dependencies {
		ref := cycloneDXBOMRef(dep)
		seenRefs[ref]++
		if seenRefs[ref] > 1 {
			ref += ":" + strconv.Itoa(seenRefs[ref])
		}

		component := cycloneDXComponent{
			BOMRef:     ref,
			Type:       "library",
			Name:       cycloneDXComponentName(dep),
			Licenses:   formatCycloneDXLicenses(dep.License),
			Properties: formatCycloneDXComponentProperties(dep, baselineDeltas[dependencyKey(dep)]),
		}
		components = append(components, component)
	}

	return components
}

func cycloneDXComponentName(dep DependencyReport) string {
	name := strings.TrimSpace(dep.Name)
	if name == "" {
		return "unknown"
	}
	return name
}

func cycloneDXBOMRef(dep DependencyReport) string {
	return "lopper:dependency:" + escapeCycloneDXBOMRefPart(dep.Language) + ":" + escapeCycloneDXBOMRefPart(dep.Name)
}

func escapeCycloneDXBOMRefPart(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	return url.PathEscape(value)
}

func formatCycloneDXLicenses(license *DependencyLicense) []cycloneDXLicenseChoice {
	normalized := normalizedDependencyLicenseCSV(license)
	name := strings.TrimSpace(normalized.SPDX)
	if name == "" {
		name = strings.TrimSpace(normalized.Raw)
	}
	if name == "" {
		return nil
	}
	return []cycloneDXLicenseChoice{{License: cycloneDXLicense{Name: name}}}
}

func formatCycloneDXBOMProperties(reportData Report) []cycloneDXProperty {
	props := make([]cycloneDXProperty, 0, 16)
	appendCycloneDXProperty(&props, "lopper:export:format", string(FormatCycloneDX))
	appendCycloneDXProperty(&props, "lopper:export:coverage", "direct-dependencies")
	appendCycloneDXProperty(&props, "lopper:schema-version", reportData.SchemaVersion)
	appendCycloneDXProperty(&props, "lopper:repo-path", reportData.RepoPath)
	if reportData.Scope != nil {
		appendCycloneDXProperty(&props, "lopper:scope:mode", reportData.Scope.Mode)
		appendCycloneDXJSONProperty(&props, "lopper:scope:packages", sortedStrings(reportData.Scope.Packages))
	}
	if reportData.Summary != nil {
		appendCycloneDXJSONProperty(&props, "lopper:summary", reportData.Summary)
	}
	if len(reportData.LanguageBreakdown) > 0 {
		appendCycloneDXJSONProperty(&props, "lopper:language-breakdown", sortedLanguageSummaries(reportData.LanguageBreakdown))
	}
	if reportData.EffectivePolicy != nil {
		appendCycloneDXJSONProperty(&props, "lopper:policy:sources", sortedStrings(reportData.EffectivePolicy.Sources))
		appendCycloneDXJSONProperty(&props, "lopper:policy:thresholds", reportData.EffectivePolicy.Thresholds)
		appendCycloneDXJSONProperty(&props, "lopper:policy:removal-candidate-weights", reportData.EffectivePolicy.RemovalCandidateWeights)
		appendCycloneDXJSONProperty(&props, "lopper:policy:license", reportData.EffectivePolicy.License)
		appendCycloneDXJSONProperty(&props, "lopper:policy:merge-trace", sortedPolicyMergeTrace(reportData.EffectivePolicy.MergeTrace))
	}
	if reportData.BaselineComparison != nil {
		appendCycloneDXProperty(&props, "lopper:baseline:key", reportData.BaselineComparison.BaselineKey)
		appendCycloneDXProperty(&props, "lopper:baseline:current-key", reportData.BaselineComparison.CurrentKey)
		appendCycloneDXProperty(&props, "lopper:baseline:unchanged-rows", strconv.Itoa(reportData.BaselineComparison.UnchangedRows))
		appendCycloneDXJSONProperty(&props, "lopper:baseline:summary-delta", reportData.BaselineComparison.SummaryDelta)
		appendCycloneDXJSONProperty(&props, "lopper:baseline:new-denied-licenses", sortedDeniedLicenseDeltas(reportData.BaselineComparison.NewDeniedLicenses))
	}
	return sortedCycloneDXProperties(props)
}

func formatCycloneDXComponentProperties(dep DependencyReport, baselineDelta DependencyDelta) []cycloneDXProperty {
	props := make([]cycloneDXProperty, 0, 48)
	appendCycloneDXProperty(&props, "lopper:dependency:language", dep.Language)
	if strings.TrimSpace(dep.Name) == "" {
		appendCycloneDXProperty(&props, "lopper:dependency:name:status", "unknown")
	}
	appendCycloneDXProperty(&props, "lopper:dependency:version:status", "unknown")
	appendCycloneDXProperty(&props, "lopper:dependency:purl:status", "unavailable")
	appendCycloneDXProperty(&props, "lopper:used-exports-count", strconv.Itoa(dep.UsedExportsCount))
	appendCycloneDXProperty(&props, "lopper:total-exports-count", strconv.Itoa(dep.TotalExportsCount))
	appendCycloneDXProperty(&props, "lopper:used-percent", formatCycloneDXFloat(dep.UsedPercent))
	appendCycloneDXProperty(&props, "lopper:waste-percent", formatCycloneDXFloat(wasteFromDependency(dep)))
	appendCycloneDXProperty(&props, "lopper:estimated-unused-bytes", strconv.FormatInt(dep.EstimatedUnusedBytes, 10))
	appendCycloneDXProperty(&props, "lopper:used-import-count", strconv.Itoa(len(dep.UsedImports)))
	appendCycloneDXProperty(&props, "lopper:unused-import-count", strconv.Itoa(len(dep.UnusedImports)))
	appendCycloneDXProperty(&props, "lopper:unused-export-count", strconv.Itoa(len(dep.UnusedExports)))
	appendCycloneDXJSONProperty(&props, "lopper:top-used-symbols", sortedSymbolUsages(dep.TopUsedSymbols))
	appendCycloneDXJSONProperty(&props, "lopper:used-imports", sortedImportUses(dep.UsedImports))
	appendCycloneDXJSONProperty(&props, "lopper:unused-imports", sortedImportUses(dep.UnusedImports))
	appendCycloneDXJSONProperty(&props, "lopper:unused-exports", sortedSymbolRefs(dep.UnusedExports))
	appendCycloneDXJSONProperty(&props, "lopper:risk-cues", sortedRiskCues(dep.RiskCues))
	appendCycloneDXJSONProperty(&props, "lopper:recommendations", sortedRecommendations(dep.Recommendations))
	appendCycloneDXRuntimeProperties(&props, dep.RuntimeUsage)
	appendCycloneDXReachabilityProperties(&props, dep.ReachabilityConfidence)
	appendCycloneDXRemovalCandidateProperties(&props, dep.RemovalCandidate)
	appendCycloneDXLicenseProperties(&props, dep.License)
	appendCycloneDXProvenanceProperties(&props, dep.Provenance)
	appendCycloneDXBaselineDeltaProperties(&props, baselineDelta)
	return sortedCycloneDXProperties(props)
}

func appendCycloneDXRuntimeProperties(props *[]cycloneDXProperty, runtime *RuntimeUsage) {
	if runtime == nil {
		return
	}
	appendCycloneDXProperty(props, "lopper:runtime:load-count", strconv.Itoa(runtime.LoadCount))
	appendCycloneDXProperty(props, "lopper:runtime:correlation", string(runtime.Correlation))
	appendCycloneDXProperty(props, "lopper:runtime:runtime-only", strconv.FormatBool(runtime.RuntimeOnly))
	appendCycloneDXJSONProperty(props, "lopper:runtime:modules", sortedRuntimeModuleUsages(runtime.Modules))
	appendCycloneDXJSONProperty(props, "lopper:runtime:parent-modules", sortedRuntimeModuleUsages(runtime.ParentModules))
	appendCycloneDXJSONProperty(props, "lopper:runtime:entrypoints", sortedRuntimeModuleUsages(runtime.Entrypoints))
	appendCycloneDXJSONProperty(props, "lopper:runtime:top-symbols", sortedRuntimeSymbolUsages(runtime.TopSymbols))
}

func appendCycloneDXReachabilityProperties(props *[]cycloneDXProperty, reachability *ReachabilityConfidence) {
	if reachability == nil {
		return
	}
	appendCycloneDXProperty(props, "lopper:reachability:model", reachability.Model)
	appendCycloneDXProperty(props, "lopper:reachability:score", formatCycloneDXFloat(reachability.Score))
	appendCycloneDXProperty(props, "lopper:reachability:summary", reachability.Summary)
	appendCycloneDXJSONProperty(props, "lopper:reachability:rationale-codes", sortedStrings(reachability.RationaleCodes))
	appendCycloneDXJSONProperty(props, "lopper:reachability:signals", sortedReachabilitySignals(reachability.Signals))
}

func appendCycloneDXRemovalCandidateProperties(props *[]cycloneDXProperty, candidate *RemovalCandidate) {
	if candidate == nil {
		return
	}
	appendCycloneDXProperty(props, "lopper:removal-candidate:score", formatCycloneDXFloat(candidate.Score))
	appendCycloneDXProperty(props, "lopper:removal-candidate:usage", formatCycloneDXFloat(candidate.Usage))
	appendCycloneDXProperty(props, "lopper:removal-candidate:impact", formatCycloneDXFloat(candidate.Impact))
	appendCycloneDXProperty(props, "lopper:removal-candidate:confidence", formatCycloneDXFloat(candidate.Confidence))
	appendCycloneDXJSONProperty(props, "lopper:removal-candidate:weights", candidate.Weights)
	appendCycloneDXJSONProperty(props, "lopper:removal-candidate:rationale", sortedStrings(candidate.Rationale))
}

func appendCycloneDXLicenseProperties(props *[]cycloneDXProperty, license *DependencyLicense) {
	normalized := normalizedDependencyLicenseCSV(license)
	appendCycloneDXProperty(props, "lopper:license:spdx", normalized.SPDX)
	appendCycloneDXProperty(props, "lopper:license:raw", normalized.Raw)
	appendCycloneDXProperty(props, "lopper:license:source", normalized.Source)
	appendCycloneDXProperty(props, "lopper:license:confidence", normalized.Confidence)
	appendCycloneDXProperty(props, "lopper:license:unknown", strconv.FormatBool(normalized.Unknown))
	appendCycloneDXProperty(props, "lopper:license:denied", strconv.FormatBool(normalized.Denied))
	appendCycloneDXJSONProperty(props, "lopper:license:evidence", sortedStrings(normalized.Evidence))
}

func appendCycloneDXProvenanceProperties(props *[]cycloneDXProperty, provenance *DependencyProvenance) {
	if provenance == nil {
		return
	}
	appendCycloneDXProperty(props, "lopper:provenance:source", provenance.Source)
	appendCycloneDXProperty(props, "lopper:provenance:confidence", provenance.Confidence)
	appendCycloneDXJSONProperty(props, "lopper:provenance:signals", sortedStrings(provenance.Signals))
}

func appendCycloneDXBaselineDeltaProperties(props *[]cycloneDXProperty, delta DependencyDelta) {
	if delta.Name == "" && delta.Language == "" && delta.Kind == "" {
		return
	}
	appendCycloneDXProperty(props, "lopper:baseline:kind", string(delta.Kind))
	appendCycloneDXProperty(props, "lopper:baseline:used-exports-count-delta", strconv.Itoa(delta.UsedExportsCountDelta))
	appendCycloneDXProperty(props, "lopper:baseline:total-exports-count-delta", strconv.Itoa(delta.TotalExportsCountDelta))
	appendCycloneDXProperty(props, "lopper:baseline:used-percent-delta", formatCycloneDXFloat(delta.UsedPercentDelta))
	appendCycloneDXProperty(props, "lopper:baseline:waste-percent-delta", formatCycloneDXFloat(delta.WastePercentDelta))
	appendCycloneDXProperty(props, "lopper:baseline:estimated-unused-bytes-delta", strconv.FormatInt(delta.EstimatedUnusedBytesDelta, 10))
	appendCycloneDXProperty(props, "lopper:baseline:denied-introduced", strconv.FormatBool(delta.DeniedIntroduced))
}

func appendCycloneDXProperty(props *[]cycloneDXProperty, name, value string) {
	name = strings.TrimSpace(name)
	value = strings.TrimSpace(value)
	if name == "" || value == "" {
		return
	}
	*props = append(*props, cycloneDXProperty{Name: name, Value: value})
}

func appendCycloneDXJSONProperty(props *[]cycloneDXProperty, name string, value any) {
	if isCycloneDXEmptyJSONProperty(value) {
		return
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return
	}
	appendCycloneDXProperty(props, name, string(payload))
}

func isCycloneDXEmptyJSONProperty(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Array, reflect.Map, reflect.Slice:
		return reflected.Len() == 0
	default:
		return false
	}
}

func sortedCycloneDXCopy[T any](values []T, less func(T, T) bool) []T {
	if len(values) == 0 {
		return nil
	}
	sorted := append([]T{}, values...)
	sort.Slice(sorted, func(i, j int) bool {
		return less(sorted[i], sorted[j])
	})
	return sorted
}

type cycloneDXSortKey struct {
	strings []string
	ints    []int
}

func sortedCycloneDXByKey[T any](values []T, key func(T) cycloneDXSortKey) []T {
	return sortedCycloneDXCopy(values, func(left, right T) bool {
		return lessCycloneDXSortKey(key(left), key(right))
	})
}

func sortedCycloneDXWithNormalizedReasons[T any](values []T, key func(T) cycloneDXSortKey, normalize func(*T)) []T {
	sorted := sortedCycloneDXByKey(values, key)
	for i := range sorted {
		normalize(&sorted[i])
	}
	return sorted
}

func lessCycloneDXSortKey(left, right cycloneDXSortKey) bool {
	for index := 0; index < len(left.strings) && index < len(right.strings); index++ {
		if left.strings[index] != right.strings[index] {
			return left.strings[index] < right.strings[index]
		}
	}
	for index := 0; index < len(left.ints) && index < len(right.ints); index++ {
		if left.ints[index] != right.ints[index] {
			return left.ints[index] < right.ints[index]
		}
	}
	return len(left.strings)+len(left.ints) < len(right.strings)+len(right.ints)
}

func sortedCycloneDXProperties(props []cycloneDXProperty) []cycloneDXProperty {
	return sortedCycloneDXByKey(props, func(value cycloneDXProperty) cycloneDXSortKey {
		return cycloneDXSortKey{strings: []string{value.Name, value.Value}}
	})
}

func cycloneDXBaselineDeltaByDependency(reportData Report) map[string]DependencyDelta {
	if reportData.BaselineComparison == nil || len(reportData.BaselineComparison.Dependencies) == 0 {
		return nil
	}
	deltas := make(map[string]DependencyDelta, len(reportData.BaselineComparison.Dependencies))
	for _, delta := range reportData.BaselineComparison.Dependencies {
		dep := DependencyReport{Name: delta.Name, Language: delta.Language}
		deltas[dependencyKey(dep)] = delta
	}
	return deltas
}

func sortedStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	sorted := append([]string{}, values...)
	sort.Strings(sorted)
	return sorted
}

func sortedLocations(values []Location) []Location {
	return sortedCycloneDXByKey(values, func(value Location) cycloneDXSortKey {
		return cycloneDXSortKey{strings: []string{value.File}, ints: []int{value.Line, value.Column}}
	})
}

func sortedImportUses(values []ImportUse) []ImportUse {
	sorted := sortedCycloneDXByKey(values, func(value ImportUse) cycloneDXSortKey {
		return cycloneDXSortKey{strings: []string{value.Module, value.Name}}
	})
	for i := range sorted {
		sorted[i].Locations = sortedLocations(sorted[i].Locations)
		sorted[i].Provenance = sortedStrings(sorted[i].Provenance)
		sorted[i].ConfidenceReasonCodes = sortedStrings(sorted[i].ConfidenceReasonCodes)
	}
	return sorted
}

func sortedSymbolRefs(values []SymbolRef) []SymbolRef {
	return sortedCycloneDXWithNormalizedReasons(values, symbolRefCycloneDXSortKey, normalizeSymbolRefCycloneDXReasons)
}

func sortedSymbolUsages(values []SymbolUsage) []SymbolUsage {
	return sortedCycloneDXByKey(values, func(value SymbolUsage) cycloneDXSortKey {
		return cycloneDXSortKey{strings: []string{value.Module, value.Name}, ints: []int{value.Count}}
	})
}

func sortedRuntimeModuleUsages(values []RuntimeModuleUsage) []RuntimeModuleUsage {
	return sortedCycloneDXByKey(values, func(value RuntimeModuleUsage) cycloneDXSortKey {
		return cycloneDXSortKey{strings: []string{value.Module}, ints: []int{value.Count}}
	})
}

func sortedRuntimeSymbolUsages(values []RuntimeSymbolUsage) []RuntimeSymbolUsage {
	return sortedCycloneDXByKey(values, func(value RuntimeSymbolUsage) cycloneDXSortKey {
		return cycloneDXSortKey{strings: []string{value.Module, value.Symbol}, ints: []int{value.Count}}
	})
}

func sortedReachabilitySignals(values []ReachabilitySignal) []ReachabilitySignal {
	return sortedCycloneDXByKey(values, func(value ReachabilitySignal) cycloneDXSortKey {
		return cycloneDXSortKey{strings: []string{value.Code}}
	})
}

func sortedRiskCues(values []RiskCue) []RiskCue {
	return sortedCycloneDXWithNormalizedReasons(values, riskCueCycloneDXSortKey, normalizeRiskCueCycloneDXReasons)
}

func sortedRecommendations(values []Recommendation) []Recommendation {
	return sortedCycloneDXWithNormalizedReasons(values, recommendationCycloneDXSortKey, normalizeRecommendationCycloneDXReasons)
}

func sortedLanguageSummaries(values []LanguageSummary) []LanguageSummary {
	return sortedCycloneDXByKey(values, func(value LanguageSummary) cycloneDXSortKey {
		return cycloneDXSortKey{strings: []string{value.Language}}
	})
}

func sortedPolicyMergeTrace(values []PolicyMergeTrace) []PolicyMergeTrace {
	return sortedCycloneDXByKey(values, func(value PolicyMergeTrace) cycloneDXSortKey {
		return cycloneDXSortKey{strings: []string{value.Field, value.Source}}
	})
}

func sortedDeniedLicenseDeltas(values []DeniedLicenseDelta) []DeniedLicenseDelta {
	return sortedCycloneDXByKey(values, func(value DeniedLicenseDelta) cycloneDXSortKey {
		return cycloneDXSortKey{strings: []string{value.Language, value.Name, value.SPDX}}
	})
}

func symbolRefCycloneDXSortKey(value SymbolRef) cycloneDXSortKey {
	return cycloneDXSortKey{strings: []string{value.Module, value.Name}}
}

func riskCueCycloneDXSortKey(value RiskCue) cycloneDXSortKey {
	return cycloneDXSortKey{strings: []string{value.Severity, value.Code}}
}

func recommendationCycloneDXSortKey(value Recommendation) cycloneDXSortKey {
	return cycloneDXSortKey{strings: []string{value.Priority, value.Code}}
}

func normalizeSymbolRefCycloneDXReasons(value *SymbolRef) {
	value.ConfidenceReasonCodes = sortedStrings(value.ConfidenceReasonCodes)
}

func normalizeRiskCueCycloneDXReasons(value *RiskCue) {
	value.ConfidenceReasonCodes = sortedStrings(value.ConfidenceReasonCodes)
}

func normalizeRecommendationCycloneDXReasons(value *Recommendation) {
	value.ConfidenceReasonCodes = sortedStrings(value.ConfidenceReasonCodes)
}

func formatCycloneDXFloat(value float64) string {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return "0"
	}
	return strconv.FormatFloat(value, 'f', -1, 64)
}
