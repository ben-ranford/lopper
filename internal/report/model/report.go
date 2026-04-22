package model

import "time"

const SchemaVersion = "0.1.0"

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
