package model

type ScopeMetadata struct {
	Mode     string   `json:"mode"`
	Packages []string `json:"packages,omitempty"`
}

type BaselineComparison struct {
	BaselineKey       string               `json:"baselineKey"`
	CurrentKey        string               `json:"currentKey,omitempty"`
	SummaryDelta      SummaryDelta         `json:"summaryDelta"`
	Dependencies      []DependencyDelta    `json:"dependencies,omitempty"`
	Regressions       []DependencyDelta    `json:"regressions,omitempty"`
	Progressions      []DependencyDelta    `json:"progressions,omitempty"`
	Added             []DependencyDelta    `json:"added,omitempty"`
	Removed           []DependencyDelta    `json:"removed,omitempty"`
	NewDeniedLicenses []DeniedLicenseDelta `json:"newDeniedLicenses,omitempty"`
	UnchangedRows     int                  `json:"unchangedRows,omitempty"`
}

type SummaryDelta struct {
	DependencyCountDelta     int     `json:"dependencyCountDelta"`
	UsedExportsCountDelta    int     `json:"usedExportsCountDelta"`
	TotalExportsCountDelta   int     `json:"totalExportsCountDelta"`
	UsedPercentDelta         float64 `json:"usedPercentDelta"`
	WastePercentDelta        float64 `json:"wastePercentDelta"`
	UnusedBytesDelta         int64   `json:"unusedBytesDelta"`
	KnownLicenseCountDelta   int     `json:"knownLicenseCountDelta"`
	UnknownLicenseCountDelta int     `json:"unknownLicenseCountDelta"`
	DeniedLicenseCountDelta  int     `json:"deniedLicenseCountDelta"`
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
	DeniedIntroduced          bool                `json:"deniedIntroduced,omitempty"`
}

type DeniedLicenseDelta struct {
	Language string `json:"language,omitempty"`
	Name     string `json:"name"`
	SPDX     string `json:"spdx,omitempty"`
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
	MaxUncertainImportCount           int `json:"maxUncertainImportCount"`
}

type EffectivePolicy struct {
	Sources                 []string                `json:"sources,omitempty"`
	Thresholds              EffectiveThresholds     `json:"thresholds"`
	RemovalCandidateWeights RemovalCandidateWeights `json:"removalCandidateWeights"`
	License                 LicensePolicy           `json:"license"`
}

type Summary struct {
	DependencyCount     int                 `json:"dependencyCount"`
	UsedExportsCount    int                 `json:"usedExportsCount"`
	TotalExportsCount   int                 `json:"totalExportsCount"`
	UsedPercent         float64             `json:"usedPercent"`
	KnownLicenseCount   int                 `json:"knownLicenseCount"`
	UnknownLicenseCount int                 `json:"unknownLicenseCount"`
	DeniedLicenseCount  int                 `json:"deniedLicenseCount"`
	Reachability        *ReachabilityRollup `json:"reachability,omitempty"`
}

type LicensePolicy struct {
	Deny                      []string `json:"deny,omitempty"`
	FailOnDenied              bool     `json:"failOnDenied"`
	IncludeRegistryProvenance bool     `json:"includeRegistryProvenance"`
}

type UsageUncertainty struct {
	ConfirmedImportUses int        `json:"confirmedImportUses"`
	UncertainImportUses int        `json:"uncertainImportUses"`
	Samples             []Location `json:"samples,omitempty"`
}

type LanguageSummary struct {
	Language          string  `json:"language"`
	DependencyCount   int     `json:"dependencyCount"`
	UsedExportsCount  int     `json:"usedExportsCount"`
	TotalExportsCount int     `json:"totalExportsCount"`
	UsedPercent       float64 `json:"usedPercent"`
}
