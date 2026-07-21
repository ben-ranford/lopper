package analysis

import (
	"github.com/ben-ranford/lopper/internal/featureflags"
	"github.com/ben-ranford/lopper/internal/report"
)

const (
	ScopeModeRepo            = "repo"
	ScopeModePackage         = "package"
	ScopeModeChangedPackages = "changed-packages"
)

type CacheOptions struct {
	Enabled  bool
	Path     string
	ReadOnly bool
}

type Request struct {
	RepoPath                          string
	ChangedFiles                      []string
	ChangedFilesExplicit              bool
	Dependency                        string
	TopN                              int
	ScopeMode                         string
	SuggestOnly                       bool
	Language                          string
	ConfigPath                        string
	RuntimeProfile                    string
	RuntimeTracePath                  string
	RuntimeTracePathExplicit          bool
	PythonRuntimeTraceCaptured        bool
	RuntimeTestCommand                string
	IncludePatterns                   []string
	ExcludePatterns                   []string
	Features                          featureflags.Set
	LowConfidenceWarningPercent       *int
	MinUsagePercentForRecommendations *int
	RemovalCandidateWeights           *report.RemovalCandidateWeights
	LicenseDenyList                   []string
	IncludeRegistryProvenance         bool
	VulnerabilityExceptions           []report.VulnerabilityException
	Cache                             *CacheOptions
}
