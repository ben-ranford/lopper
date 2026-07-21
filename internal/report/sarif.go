package report

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

func formatSARIF(rep Report) (string, error) {
	rules := newSARIFRuleBuilder()
	results := buildSARIFResults(rep, rules)

	log := sarifLog{
		Schema:  sarifSchemaURI,
		Version: sarifVersion,
		Runs: []sarifRun{
			{
				Tool: sarifTool{
					Driver: sarifDriver{
						Name:           "lopper",
						InformationURI: "https://github.com/ben-ranford/lopper",
						Version:        reportVersion(rep),
						Rules:          rules.list(),
					},
				},
				Results: results,
			},
		},
	}

	payload, err := json.MarshalIndent(log, "", "  ")
	if err != nil {
		return "", err
	}
	return string(payload) + "\n", nil
}

func reportVersion(rep Report) string {
	version := strings.TrimSpace(rep.SchemaVersion)
	if version == "" {
		version = SchemaVersion
	}
	return version
}

func buildSARIFResults(rep Report, rules *sarifRuleBuilder) []sarifResult {
	results := make([]sarifResult, 0)
	baselineDeltas := baselineDependencyDeltasForDependencies(rep.Dependencies, rep.BaselineComparison)

	for index, dep := range rep.Dependencies {
		results = append(results, dependencySARIFResults(dep, rules, baselineDeltas[index])...)
	}

	appendWasteIncreaseResult(&results, rules, rep.WasteIncreasePercent, rep.BaselineComparison)
	sortSARIFResults(results)

	return results
}

func dependencySARIFResults(dep DependencyReport, rules *sarifRuleBuilder, baselineDelta *DependencyDelta) []sarifResult {
	anchor := dependencyAnchorLocation(dep)
	results := make([]sarifResult, 0, len(dep.UnusedImports)+len(dep.UnusedExports)+len(dep.RiskCues)+len(dep.Recommendations))
	results = appendUnusedImportResults(results, rules, dep, anchor, baselineDelta)
	results = appendUnusedExportResults(results, rules, dep, anchor, baselineDelta)
	results = appendVulnerabilityResults(results, rules, dep, anchor, baselineDelta)
	for _, signal := range dependencySignals(dep) {
		results = appendSignalResult(results, rules, dep, anchor, signal, baselineDelta)
	}
	return results
}

func appendVulnerabilityResults(results []sarifResult, rules *sarifRuleBuilder, dep DependencyReport, anchor *sarifLocation, baselineDelta *DependencyDelta) []sarifResult {
	for _, finding := range dep.Vulnerabilities {
		if FindingSuppressedByException(finding) {
			continue
		}
		ruleID := "lopper/vulnerability/" + normalizeRuleToken(finding.AdvisoryID)
		rules.add(sarifRule{
			ID:               ruleID,
			Name:             finding.AdvisoryID,
			ShortDescription: sarifMessage{Text: "Dependency vulnerability finding"},
			Help:             &sarifMessage{Text: "Prioritize vulnerable dependencies using Lopper reachability, runtime, and static import evidence. Lopper does not claim exploitability."},
			Properties: map[string]any{
				"category": "vulnerability",
				"severity": finding.Severity,
				"priority": finding.Priority,
			},
		})

		props := sarifDependencyProperties(dep, baselineDelta, map[string]any{
			"advisoryId":    finding.AdvisoryID,
			"package":       finding.Package,
			"severity":      finding.Severity,
			"fixedVersion":  finding.FixedVersion,
			"source":        finding.Source,
			"priority":      finding.Priority,
			"priorityScore": finding.PriorityScore,
			"reachable":     finding.Reachable,
			"evidence":      append([]string{}, finding.Evidence...),
		})
		result := sarifResult{
			RuleID:     ruleID,
			Level:      vulnerabilitySARIFLevel(finding),
			Message:    sarifMessage{Text: fmt.Sprintf("%s has advisory %s with %s severity and %s reachability-weighted priority.", dep.Name, finding.AdvisoryID, finding.Severity, finding.Priority)},
			Properties: props,
		}
		if anchor != nil {
			result.Locations = []sarifLocation{*anchor}
		}
		results = append(results, result)
	}
	return results
}

func appendUnusedImportResults(results []sarifResult, rules *sarifRuleBuilder, dep DependencyReport, anchor *sarifLocation, baselineDelta *DependencyDelta) []sarifResult {
	for _, imp := range dep.UnusedImports {
		ruleID := "lopper/waste/unused-import"
		rules.add(sarifRule{
			ID:               ruleID,
			Name:             "unused-import",
			ShortDescription: sarifMessage{Text: "Imported symbol is not referenced"},
			Help:             &sarifMessage{Text: "Remove unused imports or narrow dependency usage to reduce surface area."},
			Properties: map[string]any{
				"category": "waste",
			},
		})

		locations := toSARIFLocations(imp.Locations)
		if len(locations) == 0 && anchor != nil {
			locations = []sarifLocation{*anchor}
		}
		results = append(results, sarifResult{
			RuleID:    ruleID,
			Level:     "warning",
			Message:   sarifMessage{Text: fmt.Sprintf("%s imports %q from %q but it is unused.", dep.Name, imp.Name, imp.Module)},
			Locations: locations,
			Properties: sarifDependencyProperties(dep, baselineDelta, map[string]any{
				"module": imp.Module,
				"symbol": imp.Name,
			}),
		})
	}
	return results
}

func appendUnusedExportResults(results []sarifResult, rules *sarifRuleBuilder, dep DependencyReport, anchor *sarifLocation, baselineDelta *DependencyDelta) []sarifResult {
	for _, sym := range dep.UnusedExports {
		ruleID := "lopper/waste/unused-export"
		rules.add(sarifRule{
			ID:               ruleID,
			Name:             "unused-export",
			ShortDescription: sarifMessage{Text: "Dependency export appears unused"},
			Help:             &sarifMessage{Text: "Prefer subpath imports or alternatives that avoid shipping unused exports."},
			Properties: map[string]any{
				"category": "waste",
			},
		})

		result := sarifResult{
			RuleID:  ruleID,
			Level:   "warning",
			Message: sarifMessage{Text: fmt.Sprintf("%s does not appear to use export %q from %q.", dep.Name, sym.Name, sym.Module)},
			Properties: sarifDependencyProperties(dep, baselineDelta, map[string]any{
				"module": sym.Module,
				"symbol": sym.Name,
			}),
		}
		if anchor != nil {
			result.Locations = []sarifLocation{*anchor}
		}
		results = append(results, result)
	}
	return results
}

func appendWasteIncreaseResult(results *[]sarifResult, rules *sarifRuleBuilder, wasteIncreasePercent *float64, comparison *BaselineComparison) {
	if wasteIncreasePercent == nil {
		return
	}
	if *wasteIncreasePercent <= 0 {
		return
	}
	ruleID := "lopper/waste/increase"
	rules.add(sarifRule{
		ID:               ruleID,
		Name:             "waste-increase",
		ShortDescription: sarifMessage{Text: "Dependency waste increased versus baseline"},
		Help:             &sarifMessage{Text: "Compare current and baseline reports to identify the dependencies causing additional waste."},
		Properties: map[string]any{
			"category": "waste",
		},
	})
	*results = append(*results, sarifResult{
		RuleID:     ruleID,
		Level:      "warning",
		Message:    sarifMessage{Text: fmt.Sprintf("Overall dependency waste increased by %.1f%% compared with baseline.", *wasteIncreasePercent)},
		Properties: sarifWasteIncreaseProperties(wasteIncreasePercent, comparison),
	})
}

func sortSARIFResults(results []sarifResult) {
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].RuleID != results[j].RuleID {
			return results[i].RuleID < results[j].RuleID
		}
		if results[i].Message.Text != results[j].Message.Text {
			return results[i].Message.Text < results[j].Message.Text
		}
		return resultLocationKey(results[i]) < resultLocationKey(results[j])
	})
}

func appendSignalResult(results []sarifResult, rules *sarifRuleBuilder, dep DependencyReport, anchor *sarifLocation, signal sarifSignal, baselineDelta *DependencyDelta) []sarifResult {
	rules.add(sarifRule{
		ID:               signal.RuleID,
		Name:             signal.RuleName,
		ShortDescription: sarifMessage{Text: signal.RuleShort},
		Help:             &sarifMessage{Text: signal.RuleHelp},
		Properties: map[string]any{
			"category": signal.RuleCategory,
			"code":     signal.RuleCode,
		},
	})

	props := sarifDependencyProperties(dep, baselineDelta, nil)
	props[signal.MessageFieldName] = signal.MessageFieldVal

	result := sarifResult{
		RuleID:     signal.RuleID,
		Level:      signal.Level,
		Message:    sarifMessage{Text: fmt.Sprintf("%s: %s", dep.Name, signal.Message)},
		Properties: props,
	}
	if anchor != nil {
		result.Locations = []sarifLocation{*anchor}
	}
	return append(results, result)
}

func sarifDependencyProperties(dep DependencyReport, baselineDelta *DependencyDelta, extra map[string]any) map[string]any {
	props := map[string]any{
		"dependency": dep.Name,
		"language":   dep.Language,
	}
	if dep.License != nil {
		props["license"] = map[string]any{
			"spdx":       dep.License.SPDX,
			"source":     dep.License.Source,
			"confidence": dep.License.Confidence,
			"unknown":    dep.License.Unknown,
			"denied":     dep.License.Denied,
			"evidence":   append([]string(nil), dep.License.Evidence...),
		}
	}
	if dep.Provenance != nil {
		props["provenance"] = map[string]any{
			"source":     dep.Provenance.Source,
			"confidence": dep.Provenance.Confidence,
			"signals":    append([]string(nil), dep.Provenance.Signals...),
		}
	}
	if dep.RuntimeUsage != nil {
		props["runtime"] = map[string]any{
			"loadCount":     dep.RuntimeUsage.LoadCount,
			"correlation":   dep.RuntimeUsage.Correlation,
			"runtimeOnly":   dep.RuntimeUsage.RuntimeOnly,
			"modules":       runtimeModulePropertyBag(dep.RuntimeUsage.Modules),
			"parentModules": runtimeModulePropertyBag(dep.RuntimeUsage.ParentModules),
			"entrypoints":   runtimeModulePropertyBag(dep.RuntimeUsage.Entrypoints),
			"topSymbols":    runtimeSymbolPropertyBag(dep.RuntimeUsage.TopSymbols),
		}
	}
	if dep.ReachabilityConfidence != nil {
		props["reachability"] = map[string]any{
			"model":          dep.ReachabilityConfidence.Model,
			"score":          dep.ReachabilityConfidence.Score,
			"summary":        dep.ReachabilityConfidence.Summary,
			"rationaleCodes": append([]string(nil), dep.ReachabilityConfidence.RationaleCodes...),
		}
	}
	if len(dep.Vulnerabilities) > 0 {
		props["vulnerabilities"] = append([]VulnerabilityFinding{}, dep.Vulnerabilities...)
	}
	if baselineDelta != nil {
		baselineContext := map[string]any{
			"kind":                      baselineDelta.Kind,
			"usedExportsCountDelta":     baselineDelta.UsedExportsCountDelta,
			"totalExportsCountDelta":    baselineDelta.TotalExportsCountDelta,
			"usedPercentDelta":          baselineDelta.UsedPercentDelta,
			"estimatedUnusedBytesDelta": baselineDelta.EstimatedUnusedBytesDelta,
			"wastePercentDelta":         baselineDelta.WastePercentDelta,
			"deniedIntroduced":          baselineDelta.DeniedIntroduced,
		}
		if baselineDelta.RuntimeDelta != nil {
			baselineContext["runtimeDelta"] = baselineDelta.RuntimeDelta
		}
		props["baselineContext"] = baselineContext
	}
	for key, value := range extra {
		props[key] = value
	}
	return props
}

func sarifWasteIncreaseProperties(wasteIncreasePercent *float64, comparison *BaselineComparison) map[string]any {
	props := map[string]any{
		"wasteIncreasePercent": *wasteIncreasePercent,
	}
	if comparison != nil {
		props["baselineContext"] = map[string]any{
			"baselineKey": comparison.BaselineKey,
			"currentKey":  comparison.CurrentKey,
			"summaryDelta": map[string]any{
				"dependencyCountDelta":     comparison.SummaryDelta.DependencyCountDelta,
				"usedExportsCountDelta":    comparison.SummaryDelta.UsedExportsCountDelta,
				"totalExportsCountDelta":   comparison.SummaryDelta.TotalExportsCountDelta,
				"usedPercentDelta":         comparison.SummaryDelta.UsedPercentDelta,
				"wastePercentDelta":        comparison.SummaryDelta.WastePercentDelta,
				"unusedBytesDelta":         comparison.SummaryDelta.UnusedBytesDelta,
				"knownLicenseCountDelta":   comparison.SummaryDelta.KnownLicenseCountDelta,
				"unknownLicenseCountDelta": comparison.SummaryDelta.UnknownLicenseCountDelta,
				"deniedLicenseCountDelta":  comparison.SummaryDelta.DeniedLicenseCountDelta,
			},
		}
	}
	return props
}

func runtimeModulePropertyBag(items []RuntimeModuleUsage) []map[string]any {
	if len(items) == 0 {
		return nil
	}
	values := make([]map[string]any, 0, len(items))
	for _, item := range items {
		values = append(values, map[string]any{
			"module": item.Module,
			"count":  item.Count,
		})
	}
	return values
}

func runtimeSymbolPropertyBag(items []RuntimeSymbolUsage) []map[string]any {
	if len(items) == 0 {
		return nil
	}
	values := make([]map[string]any, 0, len(items))
	for _, item := range items {
		values = append(values, map[string]any{
			"symbol": item.Symbol,
			"module": item.Module,
			"count":  item.Count,
		})
	}
	return values
}
