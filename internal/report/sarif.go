package report

import (
	"encoding/json"
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

const (
	sarifSchemaURI = "https://json.schemastore.org/sarif-2.1.0.json"
	sarifVersion   = "2.1.0"
)

type sarifLog struct {
	Schema  string     `json:"$schema"`
	Version string     `json:"version"`
	Runs    []sarifRun `json:"runs"`
}

type sarifRun struct {
	Tool    sarifTool     `json:"tool"`
	Results []sarifResult `json:"results"`
}

type sarifTool struct {
	Driver sarifDriver `json:"driver"`
}

type sarifDriver struct {
	Name           string      `json:"name"`
	InformationURI string      `json:"informationUri,omitempty"`
	Version        string      `json:"version,omitempty"`
	Rules          []sarifRule `json:"rules,omitempty"`
}

type sarifRule struct {
	ID               string                 `json:"id"`
	Name             string                 `json:"name,omitempty"`
	ShortDescription sarifMessage           `json:"shortDescription"`
	Help             *sarifMessage          `json:"help,omitempty"`
	Properties       map[string]interface{} `json:"properties,omitempty"`
}

type sarifResult struct {
	RuleID     string                 `json:"ruleId"`
	Level      string                 `json:"level,omitempty"`
	Message    sarifMessage           `json:"message"`
	Locations  []sarifLocation        `json:"locations,omitempty"`
	Properties map[string]interface{} `json:"properties,omitempty"`
}

type sarifMessage struct {
	Text string `json:"text"`
}

type sarifLocation struct {
	PhysicalLocation sarifPhysicalLocation `json:"physicalLocation"`
}

type sarifPhysicalLocation struct {
	ArtifactLocation sarifArtifactLocation `json:"artifactLocation"`
	Region           *sarifRegion          `json:"region,omitempty"`
}

type sarifArtifactLocation struct {
	URI string `json:"uri"`
}

type sarifRegion struct {
	StartLine   int `json:"startLine,omitempty"`
	StartColumn int `json:"startColumn,omitempty"`
}

type sarifRuleBuilder struct {
	rules map[string]sarifRule
}

type sarifSignal struct {
	RuleID           string
	RuleName         string
	RuleShort        string
	RuleHelp         string
	RuleCategory     string
	RuleCode         string
	Level            string
	Message          string
	MessageFieldName string
	MessageFieldVal  string
}

type sarifSignalSpec struct {
	IDPrefix         string
	Code             string
	ShortText        string
	HelpText         string
	Category         string
	Level            string
	Message          string
	MessageFieldName string
	MessageFieldVal  string
}

func newSARIFRuleBuilder() *sarifRuleBuilder {
	return &sarifRuleBuilder{rules: make(map[string]sarifRule)}
}

func (b *sarifRuleBuilder) add(rule sarifRule) {
	if _, ok := b.rules[rule.ID]; ok {
		return
	}
	b.rules[rule.ID] = rule
}

func (b *sarifRuleBuilder) list() []sarifRule {
	ids := make([]string, 0, len(b.rules))
	for id := range b.rules {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	items := make([]sarifRule, 0, len(ids))
	for _, id := range ids {
		items = append(items, b.rules[id])
	}
	return items
}

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
			Properties: map[string]interface{}{
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
			Properties: map[string]interface{}{
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
			Properties: map[string]interface{}{
				"category": "waste",
			},
		})

		result := sarifResult{
			RuleID:  ruleID,
			Level:   "warning",
			Message: sarifMessage{Text: fmt.Sprintf("%s does not appear to use export %q from %q.", dep.Name, sym.Name, sym.Module)},
			Properties: map[string]interface{}{
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
		Properties: map[string]interface{}{
			"category": "waste",
		},
	})
	*results = append(*results, sarifResult{
		RuleID:  ruleID,
		Level:   "warning",
		Message: sarifMessage{Text: fmt.Sprintf("Overall dependency waste increased by %.1f%% compared with baseline.", *wasteIncreasePercent)},
		Properties: map[string]interface{}{
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
		Properties: map[string]interface{}{
			"category": signal.RuleCategory,
			"code":     signal.RuleCode,
		},
	})

	props := map[string]interface{}{
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

func dependencySignals(dep DependencyReport) []sarifSignal {
	signals := make([]sarifSignal, 0, len(dep.RiskCues)+len(dep.Recommendations))
	for _, cue := range dep.RiskCues {
		signals = append(signals, newSignal(sarifSignalSpec{
			IDPrefix:         "lopper/risk/",
			Code:             cue.Code,
			ShortText:        "Dependency risk cue",
			HelpText:         "Review risk cues to reduce dependency attack surface and operational uncertainty.",
			Category:         "risk",
			Level:            severityToSARIFLevel(cue.Severity),
			Message:          cue.Message,
			MessageFieldName: "severity",
			MessageFieldVal:  cue.Severity,
		}))
	}
	for _, recommendation := range dep.Recommendations {
		signals = append(signals, newSignal(sarifSignalSpec{
			IDPrefix:         "lopper/recommendation/",
			Code:             recommendation.Code,
			ShortText:        "Dependency optimization recommendation",
			HelpText:         "Apply recommendations to reduce unused dependency surface area.",
			Category:         "recommendation",
			Level:            priorityToSARIFLevel(recommendation.Priority),
			Message:          recommendation.Message,
			MessageFieldName: "priority",
			MessageFieldVal:  recommendation.Priority,
		}))
	}
	return signals
}

func newSignal(spec sarifSignalSpec) sarifSignal {
	return sarifSignal{
		RuleID:           spec.IDPrefix + normalizeRuleToken(spec.Code),
		RuleName:         spec.Code,
		RuleShort:        spec.ShortText,
		RuleHelp:         spec.HelpText,
		RuleCategory:     spec.Category,
		RuleCode:         spec.Code,
		Level:            spec.Level,
		Message:          spec.Message,
		MessageFieldName: spec.MessageFieldName,
		MessageFieldVal:  spec.MessageFieldVal,
	}
}

func dependencyAnchorLocation(dep DependencyReport) *sarifLocation {
	locations := make([]Location, 0)
	for _, imp := range dep.UsedImports {
		locations = append(locations, imp.Locations...)
	}
	for _, imp := range dep.UnusedImports {
		locations = append(locations, imp.Locations...)
	}
	if len(locations) == 0 {
		return nil
	}
	sort.SliceStable(locations, func(i, j int) bool {
		left := filepath.ToSlash(locations[i].File)
		right := filepath.ToSlash(locations[j].File)
		if left != right {
			return left < right
		}
		if locations[i].Line != locations[j].Line {
			return locations[i].Line < locations[j].Line
		}
		return locations[i].Column < locations[j].Column
	})
	loc, ok := toSARIFLocation(locations[0])
	if !ok {
		return nil
	}
	return &loc
}

func toSARIFLocations(locations []Location) []sarifLocation {
	if len(locations) == 0 {
		return nil
	}
	result := make([]sarifLocation, 0, len(locations))
	seen := make(map[string]struct{})
	for _, location := range locations {
		loc, ok := toSARIFLocation(location)
		if !ok {
			continue
		}
		key := sarifLocationKey(loc)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, loc)
	}
	sort.SliceStable(result, func(i, j int) bool {
		li := result[i].PhysicalLocation.ArtifactLocation.URI
		lj := result[j].PhysicalLocation.ArtifactLocation.URI
		if li != lj {
			return li < lj
		}
		regionI := result[i].PhysicalLocation.Region
		regionJ := result[j].PhysicalLocation.Region
		lineI, colI := 0, 0
		lineJ, colJ := 0, 0
		if regionI != nil {
			lineI, colI = regionI.StartLine, regionI.StartColumn
		}
		if regionJ != nil {
			lineJ, colJ = regionJ.StartLine, regionJ.StartColumn
		}
		if lineI != lineJ {
			return lineI < lineJ
		}
		return colI < colJ
	})
	if len(result) == 0 {
		return nil
	}
	return result
}

func sarifLocationKey(location sarifLocation) string {
	line := 0
	column := 0
	if location.PhysicalLocation.Region != nil {
		line = location.PhysicalLocation.Region.StartLine
		column = location.PhysicalLocation.Region.StartColumn
	}
	return location.PhysicalLocation.ArtifactLocation.URI + "\x00" + fmt.Sprintf("%d:%d", line, column)
}

func toSARIFLocation(location Location) (sarifLocation, bool) {
	file := strings.TrimSpace(location.File)
	if file == "" {
		return sarifLocation{}, false
	}
	file = strings.ReplaceAll(file, "\\", "/")
	file = path.Clean(file)
	loc := sarifLocation{
		PhysicalLocation: sarifPhysicalLocation{
			ArtifactLocation: sarifArtifactLocation{URI: file},
		},
	}
	if location.Line > 0 || location.Column > 0 {
		region := &sarifRegion{}
		if location.Line > 0 {
			region.StartLine = location.Line
		}
		if location.Column > 0 {
			region.StartColumn = location.Column
		}
		loc.PhysicalLocation.Region = region
	}
	return loc, true
}

func resultLocationKey(result sarifResult) string {
	if len(result.Locations) == 0 {
		return ""
	}
	loc := result.Locations[0]
	line, col := 0, 0
	if loc.PhysicalLocation.Region != nil {
		line = loc.PhysicalLocation.Region.StartLine
		col = loc.PhysicalLocation.Region.StartColumn
	}
	return fmt.Sprintf("%s:%d:%d", loc.PhysicalLocation.ArtifactLocation.URI, line, col)
}

func normalizeRuleToken(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "unknown"
	}
	var builder strings.Builder
	builder.Grow(len(value))
	lastDash := false
	for _, ch := range value {
		isAlpha := ch >= 'a' && ch <= 'z'
		isDigit := ch >= '0' && ch <= '9'
		if isAlpha || isDigit {
			builder.WriteRune(ch)
			lastDash = false
			continue
		}
		if !lastDash {
			builder.WriteByte('-')
			lastDash = true
		}
	}
	normalized := strings.Trim(builder.String(), "-")
	if normalized == "" {
		return "unknown"
	}
	return normalized
}

func severityToSARIFLevel(severity string) string {
	switch strings.ToLower(strings.TrimSpace(severity)) {
	case "critical", "high":
		return "error"
	case "medium":
		return "warning"
	default:
		return "note"
	}
}

func priorityToSARIFLevel(priority string) string {
	switch strings.ToLower(strings.TrimSpace(priority)) {
	case "critical", "high":
		return "warning"
	default:
		return "note"
	}
}
