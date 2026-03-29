package report

type DependencyReport struct {
	Language               string                  `json:"language,omitempty"`
	Name                   string                  `json:"name"`
	UsedExportsCount       int                     `json:"usedExportsCount"`
	TotalExportsCount      int                     `json:"totalExportsCount"`
	UsedPercent            float64                 `json:"usedPercent"`
	EstimatedUnusedBytes   int64                   `json:"estimatedUnusedBytes"`
	TopUsedSymbols         []SymbolUsage           `json:"topUsedSymbols,omitempty"`
	UsedImports            []ImportUse             `json:"usedImports,omitempty"`
	UnusedImports          []ImportUse             `json:"unusedImports,omitempty"`
	UnusedExports          []SymbolRef             `json:"unusedExports,omitempty"`
	RiskCues               []RiskCue               `json:"riskCues,omitempty"`
	Recommendations        []Recommendation        `json:"recommendations,omitempty"`
	Codemod                *CodemodReport          `json:"codemod,omitempty"`
	RuntimeUsage           *RuntimeUsage           `json:"runtimeUsage,omitempty"`
	ReachabilityConfidence *ReachabilityConfidence `json:"reachabilityConfidence,omitempty"`
	RemovalCandidate       *RemovalCandidate       `json:"removalCandidate,omitempty"`
	License                *DependencyLicense      `json:"license,omitempty"`
	Provenance             *DependencyProvenance   `json:"provenance,omitempty"`
}

type DependencyLicense struct {
	SPDX       string   `json:"spdx,omitempty"`
	Raw        string   `json:"raw,omitempty"`
	Source     string   `json:"source,omitempty"`
	Confidence string   `json:"confidence,omitempty"`
	Unknown    bool     `json:"unknown,omitempty"`
	Denied     bool     `json:"denied,omitempty"`
	Evidence   []string `json:"evidence,omitempty"`
}

type DependencyProvenance struct {
	Source     string   `json:"source,omitempty"`
	Confidence string   `json:"confidence,omitempty"`
	Signals    []string `json:"signals,omitempty"`
}

type CodemodReport struct {
	Mode        string              `json:"mode"`
	Suggestions []CodemodSuggestion `json:"suggestions,omitempty"`
	Skips       []CodemodSkip       `json:"skips,omitempty"`
	Apply       *CodemodApplyReport `json:"apply,omitempty"`
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

type CodemodApplyReport struct {
	AppliedFiles   int                  `json:"appliedFiles,omitempty"`
	AppliedPatches int                  `json:"appliedPatches,omitempty"`
	SkippedFiles   int                  `json:"skippedFiles,omitempty"`
	SkippedPatches int                  `json:"skippedPatches,omitempty"`
	FailedFiles    int                  `json:"failedFiles,omitempty"`
	FailedPatches  int                  `json:"failedPatches,omitempty"`
	BackupPath     string               `json:"backupPath,omitempty"`
	Results        []CodemodApplyResult `json:"results,omitempty"`
}

type CodemodApplyResult struct {
	File       string `json:"file"`
	Status     string `json:"status"`
	PatchCount int    `json:"patchCount"`
	Message    string `json:"message,omitempty"`
}

type RemovalCandidate struct {
	Score      float64                 `json:"score"`
	Usage      float64                 `json:"usage"`
	Impact     float64                 `json:"impact"`
	Confidence float64                 `json:"confidence"`
	Weights    RemovalCandidateWeights `json:"weights"`
	Rationale  []string                `json:"rationale,omitempty"`
}

type ReachabilityConfidence struct {
	Model          string               `json:"model"`
	Score          float64              `json:"score"`
	Summary        string               `json:"summary,omitempty"`
	RationaleCodes []string             `json:"rationaleCodes,omitempty"`
	Signals        []ReachabilitySignal `json:"signals,omitempty"`
}

type ReachabilitySignal struct {
	Code         string  `json:"code"`
	Score        float64 `json:"score"`
	Weight       float64 `json:"weight"`
	Contribution float64 `json:"contribution"`
	Rationale    string  `json:"rationale,omitempty"`
}

type ReachabilityRollup struct {
	Model        string  `json:"model"`
	AverageScore float64 `json:"averageScore"`
	LowestScore  float64 `json:"lowestScore"`
	HighestScore float64 `json:"highestScore"`
}

type RemovalCandidateWeights struct {
	Usage      float64 `json:"usage"`
	Impact     float64 `json:"impact"`
	Confidence float64 `json:"confidence"`
}

type RiskCue struct {
	Code                  string   `json:"code"`
	Severity              string   `json:"severity"`
	Message               string   `json:"message"`
	ConfidenceScore       float64  `json:"confidenceScore,omitempty"`
	ConfidenceReasonCodes []string `json:"confidenceReasonCodes,omitempty"`
}

type Recommendation struct {
	Code                  string   `json:"code"`
	Priority              string   `json:"priority"`
	Message               string   `json:"message"`
	Rationale             string   `json:"rationale,omitempty"`
	ConfidenceScore       float64  `json:"confidenceScore,omitempty"`
	ConfidenceReasonCodes []string `json:"confidenceReasonCodes,omitempty"`
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
	Name                  string     `json:"name"`
	Module                string     `json:"module"`
	Locations             []Location `json:"locations,omitempty"`
	Provenance            []string   `json:"provenance,omitempty"`
	ConfidenceScore       float64    `json:"confidenceScore,omitempty"`
	ConfidenceReasonCodes []string   `json:"confidenceReasonCodes,omitempty"`
}

type SymbolRef struct {
	Name                  string   `json:"name"`
	Module                string   `json:"module"`
	ConfidenceScore       float64  `json:"confidenceScore,omitempty"`
	ConfidenceReasonCodes []string `json:"confidenceReasonCodes,omitempty"`
}

type Location struct {
	File   string `json:"file"`
	Line   int    `json:"line"`
	Column int    `json:"column"`
}
