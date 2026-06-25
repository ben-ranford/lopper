package model

type ScopeMetadata struct {
	Mode     string   `json:"mode"`
	Packages []string `json:"packages,omitempty"`
}

type BaselineComparison struct {
	BaselineKey         string               `json:"baselineKey"`
	CurrentKey          string               `json:"currentKey,omitempty"`
	SummaryDelta        SummaryDelta         `json:"summaryDelta"`
	Dependencies        []DependencyDelta    `json:"dependencies,omitempty"`
	Regressions         []DependencyDelta    `json:"regressions,omitempty"`
	Progressions        []DependencyDelta    `json:"progressions,omitempty"`
	RuntimeRegressions  []DependencyDelta    `json:"runtimeRegressions,omitempty"`
	RuntimeImprovements []DependencyDelta    `json:"runtimeImprovements,omitempty"`
	Added               []DependencyDelta    `json:"added,omitempty"`
	Removed             []DependencyDelta    `json:"removed,omitempty"`
	NewDeniedLicenses   []DeniedLicenseDelta `json:"newDeniedLicenses,omitempty"`
	UnchangedRows       int                  `json:"unchangedRows,omitempty"`
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
	RuntimeDelta              *RuntimeDelta       `json:"runtimeDelta,omitempty"`
	DeniedIntroduced          bool                `json:"deniedIntroduced,omitempty"`
}

type DeniedLicenseDelta struct {
	Language string `json:"language,omitempty"`
	Name     string `json:"name"`
	SPDX     string `json:"spdx,omitempty"`
}

type RuntimeChangeType string

const (
	RuntimeChangeLoadCount              RuntimeChangeType = "load-count"
	RuntimeChangeNewRuntimeLoads        RuntimeChangeType = "new-runtime-loads"
	RuntimeChangeRemovedRuntimeLoads    RuntimeChangeType = "removed-runtime-loads"
	RuntimeChangeCorrelation            RuntimeChangeType = "correlation"
	RuntimeChangeRuntimeOnlyRegression  RuntimeChangeType = "runtime-only-regression"
	RuntimeChangeRuntimeOnlyImprovement RuntimeChangeType = "runtime-only-improvement"
	RuntimeChangeModules                RuntimeChangeType = "modules"
	RuntimeChangeParentModules          RuntimeChangeType = "parent-modules"
	RuntimeChangeEntrypoints            RuntimeChangeType = "entrypoints"
)

type RuntimeDelta struct {
	Comparable             bool                 `json:"comparable"`
	BaselinePresent        bool                 `json:"baselinePresent"`
	CurrentPresent         bool                 `json:"currentPresent"`
	BaselineLoadCount      *int                 `json:"baselineLoadCount,omitempty"`
	CurrentLoadCount       *int                 `json:"currentLoadCount,omitempty"`
	LoadCountDelta         *int                 `json:"loadCountDelta,omitempty"`
	BaselineCorrelation    RuntimeCorrelation   `json:"baselineCorrelation,omitempty"`
	CurrentCorrelation     RuntimeCorrelation   `json:"currentCorrelation,omitempty"`
	ChangeTypes            []RuntimeChangeType  `json:"changeTypes,omitempty"`
	NewRuntimeLoads        bool                 `json:"newRuntimeLoads,omitempty"`
	RemovedRuntimeLoads    bool                 `json:"removedRuntimeLoads,omitempty"`
	RuntimeOnlyRegression  bool                 `json:"runtimeOnlyRegression,omitempty"`
	RuntimeOnlyImprovement bool                 `json:"runtimeOnlyImprovement,omitempty"`
	ModulesAdded           []RuntimeModuleDelta `json:"modulesAdded,omitempty"`
	ModulesRemoved         []RuntimeModuleDelta `json:"modulesRemoved,omitempty"`
	ModulesChanged         []RuntimeModuleDelta `json:"modulesChanged,omitempty"`
	ParentModulesAdded     []RuntimeModuleDelta `json:"parentModulesAdded,omitempty"`
	ParentModulesRemoved   []RuntimeModuleDelta `json:"parentModulesRemoved,omitempty"`
	ParentModulesChanged   []RuntimeModuleDelta `json:"parentModulesChanged,omitempty"`
	EntrypointsAdded       []RuntimeModuleDelta `json:"entrypointsAdded,omitempty"`
	EntrypointsRemoved     []RuntimeModuleDelta `json:"entrypointsRemoved,omitempty"`
	EntrypointsChanged     []RuntimeModuleDelta `json:"entrypointsChanged,omitempty"`
}

type RuntimeModuleDelta struct {
	Module        string `json:"module"`
	BaselineCount int    `json:"baselineCount"`
	CurrentCount  int    `json:"currentCount"`
	CountDelta    int    `json:"countDelta"`
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
	MergeTrace              []PolicyMergeTrace      `json:"mergeTrace,omitempty"`
	Thresholds              EffectiveThresholds     `json:"thresholds"`
	RemovalCandidateWeights RemovalCandidateWeights `json:"removalCandidateWeights"`
	License                 LicensePolicy           `json:"license"`
}

type PolicyMergeTrace struct {
	Field  string `json:"field"`
	Source string `json:"source"`
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
