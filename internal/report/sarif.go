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

	for _, dep := range rep.Dependencies {
		results = append(results, dependencySARIFResults(dep, rules)...)
	}

	appendWasteIncreaseResult(&results, rules, rep.WasteIncreasePercent)
	sortSARIFResults(results)

	return results
}

func dependencySARIFResults(dep DependencyReport, rules *sarifRuleBuilder) []sarifResult {
	anchor := dependencyAnchorLocation(dep)
	results := make([]sarifResult, 0, len(dep.UnusedImports)+len(dep.UnusedExports)+len(dep.RiskCues)+len(dep.Recommendations))
	results = appendUnusedImportResults(results, rules, dep, anchor)
	results = appendUnusedExportResults(results, rules, dep, anchor)
	for _, signal := range dependencySignals(dep) {
		results = appendSignalResult(results, rules, dep, anchor, signal)
	}
	return results
}

func appendUnusedImportResults(results []sarifResult, rules *sarifRuleBuilder, dep DependencyReport, anchor *sarifLocation) []sarifResult {
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
			Properties: map[string]any{
				"dependency": dep.Name,
				"language":   dep.Language,
				"module":     imp.Module,
				"symbol":     imp.Name,
			},
		})
	}
	return results
}

func appendUnusedExportResults(results []sarifResult, rules *sarifRuleBuilder, dep DependencyReport, anchor *sarifLocation) []sarifResult {
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
			Properties: map[string]any{
				"dependency": dep.Name,
				"language":   dep.Language,
				"module":     sym.Module,
				"symbol":     sym.Name,
			},
		}
		if anchor != nil {
			result.Locations = []sarifLocation{*anchor}
		}
		results = append(results, result)
	}
	return results
}

func appendWasteIncreaseResult(results *[]sarifResult, rules *sarifRuleBuilder, wasteIncreasePercent *float64) {
	if wasteIncreasePercent == nil {
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
		RuleID:  ruleID,
		Level:   "warning",
		Message: sarifMessage{Text: fmt.Sprintf("Overall dependency waste increased by %.1f%% compared with baseline.", *wasteIncreasePercent)},
		Properties: map[string]any{
			"wasteIncreasePercent": *wasteIncreasePercent,
		},
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

func appendSignalResult(results []sarifResult, rules *sarifRuleBuilder, dep DependencyReport, anchor *sarifLocation, signal sarifSignal) []sarifResult {
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

	props := map[string]any{
		"dependency": dep.Name,
		"language":   dep.Language,
	}
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
