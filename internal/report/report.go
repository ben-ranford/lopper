package report

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

type Format string

const (
	FormatTable     Format = "table"
	FormatJSON      Format = "json"
	FormatSARIF     Format = "sarif"
	FormatPRComment Format = "pr-comment"
)

const SchemaVersion = "0.1.0"

var ErrUnknownFormat = errors.New("unknown format")

func ParseFormat(value string) (Format, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", string(FormatTable):
		return FormatTable, nil
	case string(FormatJSON):
		return FormatJSON, nil
	case string(FormatSARIF):
		return FormatSARIF, nil
	case string(FormatPRComment):
		return FormatPRComment, nil
	default:
		return "", fmt.Errorf("%w: %s", ErrUnknownFormat, value)
	}
}

type Report struct {
	SchemaVersion        string               `json:"schemaVersion"`
	GeneratedAt          time.Time            `json:"generatedAt"`
	RepoPath             string               `json:"repoPath"`
	Scope                *ScopeMetadata       `json:"scope,omitempty"`
	Dependencies         []DependencyReport   `json:"dependencies"`
	UsageUncertainty     *UsageUncertainty    `json:"usageUncertainty,omitempty"`
	Summary              *Summary             `json:"summary,omitempty"`
	LanguageBreakdown    []LanguageSummary    `json:"languageBreakdown,omitempty"`
	Cache                *CacheMetadata       `json:"cache,omitempty"`
	EffectiveThresholds  *EffectiveThresholds `json:"effectiveThresholds,omitempty"`
	EffectivePolicy      *EffectivePolicy     `json:"effectivePolicy,omitempty"`
	Warnings             []string             `json:"warnings,omitempty"`
	WasteIncreasePercent *float64             `json:"wasteIncreasePercent,omitempty"`
	BaselineComparison   *BaselineComparison  `json:"baselineComparison,omitempty"`
}
