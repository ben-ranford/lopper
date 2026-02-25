package report

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

type Format string

const (
	FormatTable Format = "table"
	FormatJSON  Format = "json"
	FormatSARIF Format = "sarif"
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
	default:
		return "", fmt.Errorf("%w: %s", ErrUnknownFormat, value)
	}
}

type Report struct {
	SchemaVersion        string               `json:"schemaVersion"`
	GeneratedAt          time.Time            `json:"generatedAt"`
	RepoPath             string               `json:"repoPath"`
	Dependencies         []DependencyReport   `json:"dependencies"`
	Summary              *Summary             `json:"summary,omitempty"`
	LanguageBreakdown    []LanguageSummary    `json:"languageBreakdown,omitempty"`
	Cache                *CacheMetadata       `json:"cache,omitempty"`
	EffectiveThresholds  *EffectiveThresholds `json:"effectiveThresholds,omitempty"`
	Warnings             []string             `json:"warnings,omitempty"`
	WasteIncreasePercent *float64             `json:"wasteIncreasePercent,omitempty"`
	BaselineComparison   *BaselineComparison  `json:"baselineComparison,omitempty"`
}

type BaselineComparison struct {
	BaselineKey   string            `json:"baselineKey"`
	CurrentKey    string            `json:"currentKey,omitempty"`
	SummaryDelta  SummaryDelta      `json:"summaryDelta"`
	Dependencies  []DependencyDelta `json:"dependencies,omitempty"`
	Regressions   []DependencyDelta `json:"regressions,omitempty"`
	Progressions  []DependencyDelta `json:"progressions,omitempty"`
	Added         []DependencyDelta `json:"added,omitempty"`
	Removed       []DependencyDelta `json:"removed,omitempty"`
	UnchangedRows int               `json:"unchangedRows,omitempty"`
}

type SummaryDelta struct {
	DependencyCountDelta   int     `json:"dependencyCountDelta"`
	UsedExportsCountDelta  int     `json:"usedExportsCountDelta"`
	TotalExportsCountDelta int     `json:"totalExportsCountDelta"`
	UsedPercentDelta       float64 `json:"usedPercentDelta"`
	WastePercentDelta      float64 `json:"wastePercentDelta"`
	UnusedBytesDelta       int64   `json:"unusedBytesDelta"`
}

type DependencyDeltaKind string

const (
	DependencyDeltaAdded   DependencyDeltaKind = "added"
	DependencyDeltaRemoved DependencyDeltaKind = "removed"
	DependencyDeltaChanged DependencyDeltaKind = "changed"
)

type DependencyDelta struct {
	Kind                      DependencyDeltaKind `json:"kind"`
	Language                  string              `json:"language,omitempty"`
	Name                      string              `json:"name"`
	UsedExportsCountDelta     int                 `json:"usedExportsCountDelta"`
	TotalExportsCountDelta    int                 `json:"totalExportsCountDelta"`
	UsedPercentDelta          float64             `json:"usedPercentDelta"`
	EstimatedUnusedBytesDelta int64               `json:"estimatedUnusedBytesDelta"`
	WastePercentDelta         float64             `json:"wastePercentDelta"`
}

type CacheMetadata struct {
	Enabled       bool                `json:"enabled"`
	Path          string              `json:"path,omitempty"`
	ReadOnly      bool                `json:"readOnly,omitempty"`
	Hits          int                 `json:"hits"`
	Misses        int                 `json:"misses"`
	Writes        int                 `json:"writes"`
	Invalidations []CacheInvalidation `json:"invalidations,omitempty"`
}

type CacheInvalidation struct {
	Key    string `json:"key"`
	Reason string `json:"reason"`
}

type EffectiveThresholds struct {
	FailOnIncreasePercent             int `json:"failOnIncreasePercent"`
	LowConfidenceWarningPercent       int `json:"lowConfidenceWarningPercent"`
	MinUsagePercentForRecommendations int `json:"minUsagePercentForRecommendations"`
}

type Summary struct {
	DependencyCount   int     `json:"dependencyCount"`
	UsedExportsCount  int     `json:"usedExportsCount"`
	TotalExportsCount int     `json:"totalExportsCount"`
	UsedPercent       float64 `json:"usedPercent"`
}

type LanguageSummary struct {
	Language          string  `json:"language"`
	DependencyCount   int     `json:"dependencyCount"`
	UsedExportsCount  int     `json:"usedExportsCount"`
	TotalExportsCount int     `json:"totalExportsCount"`
	UsedPercent       float64 `json:"usedPercent"`
}

type DependencyReport struct {
	Language             string            `json:"language,omitempty"`
	Name                 string            `json:"name"`
	UsedExportsCount     int               `json:"usedExportsCount"`
	TotalExportsCount    int               `json:"totalExportsCount"`
	UsedPercent          float64           `json:"usedPercent"`
	EstimatedUnusedBytes int64             `json:"estimatedUnusedBytes"`
	TopUsedSymbols       []SymbolUsage     `json:"topUsedSymbols,omitempty"`
	UsedImports          []ImportUse       `json:"usedImports,omitempty"`
	UnusedImports        []ImportUse       `json:"unusedImports,omitempty"`
	UnusedExports        []SymbolRef       `json:"unusedExports,omitempty"`
	RiskCues             []RiskCue         `json:"riskCues,omitempty"`
	Recommendations      []Recommendation  `json:"recommendations,omitempty"`
	Codemod              *CodemodReport    `json:"codemod,omitempty"`
	RuntimeUsage         *RuntimeUsage     `json:"runtimeUsage,omitempty"`
	RemovalCandidate     *RemovalCandidate `json:"removalCandidate,omitempty"`
}

type CodemodReport struct {
	Mode        string              `json:"mode"`
	Suggestions []CodemodSuggestion `json:"suggestions,omitempty"`
	Skips       []CodemodSkip       `json:"skips,omitempty"`
}

type CodemodSuggestion struct {
	File        string `json:"file"`
	Line        int    `json:"line"`
	ImportName  string `json:"importName"`
	FromModule  string `json:"fromModule"`
	ToModule    string `json:"toModule"`
	Original    string `json:"original"`
	Replacement string `json:"replacement"`
	Patch       string `json:"patch"`
}

type CodemodSkip struct {
	File       string `json:"file"`
	Line       int    `json:"line"`
	ImportName string `json:"importName"`
	Module     string `json:"module"`
	ReasonCode string `json:"reasonCode"`
	Message    string `json:"message"`
}

type RemovalCandidate struct {
	Score      float64                 `json:"score"`
	Usage      float64                 `json:"usage"`
	Impact     float64                 `json:"impact"`
	Confidence float64                 `json:"confidence"`
	Weights    RemovalCandidateWeights `json:"weights"`
	Rationale  []string                `json:"rationale,omitempty"`
}

type RemovalCandidateWeights struct {
	Usage      float64 `json:"usage"`
	Impact     float64 `json:"impact"`
	Confidence float64 `json:"confidence"`
}

type RiskCue struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

type Recommendation struct {
	Code      string `json:"code"`
	Priority  string `json:"priority"`
	Message   string `json:"message"`
	Rationale string `json:"rationale,omitempty"`
}

type RuntimeUsage struct {
	LoadCount   int                  `json:"loadCount"`
	Correlation RuntimeCorrelation   `json:"correlation,omitempty"`
	RuntimeOnly bool                 `json:"runtimeOnly,omitempty"`
	Modules     []RuntimeModuleUsage `json:"modules,omitempty"`
	TopSymbols  []RuntimeSymbolUsage `json:"topSymbols,omitempty"`
}

type RuntimeCorrelation string

const (
	RuntimeCorrelationStaticOnly  RuntimeCorrelation = "static-only"
	RuntimeCorrelationRuntimeOnly RuntimeCorrelation = "runtime-only"
	RuntimeCorrelationOverlap     RuntimeCorrelation = "overlap"
)

type RuntimeModuleUsage struct {
	Module string `json:"module"`
	Count  int    `json:"count"`
}

type RuntimeSymbolUsage struct {
	Symbol string `json:"symbol"`
	Module string `json:"module,omitempty"`
	Count  int    `json:"count"`
}

type SymbolUsage struct {
	Name   string `json:"name"`
	Module string `json:"module,omitempty"`
	Count  int    `json:"count"`
}

type ImportUse struct {
	Name       string     `json:"name"`
	Module     string     `json:"module"`
	Locations  []Location `json:"locations,omitempty"`
	Provenance []string   `json:"provenance,omitempty"`
}

type SymbolRef struct {
	Name   string `json:"name"`
	Module string `json:"module"`
}

type Location struct {
	File   string `json:"file"`
	Line   int    `json:"line"`
	Column int    `json:"column"`
}
