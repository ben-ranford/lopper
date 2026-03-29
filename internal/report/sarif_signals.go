package report

import "strings"

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
