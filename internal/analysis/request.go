package analysis

import "github.com/ben-ranford/lopper/internal/report"

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
	Dependency                        string
	TopN                              int
	ScopeMode                         string
	SuggestOnly                       bool
	Language                          string
	ConfigPath                        string
	RuntimeProfile                    string
	RuntimeTracePath                  string
	IncludePatterns                   []string
	ExcludePatterns                   []string
	LowConfidenceWarningPercent       *int
	MinUsagePercentForRecommendations *int
	RemovalCandidateWeights           *report.RemovalCandidateWeights
	LicenseDenyList                   []string
	LicenseFailOnDeny                 bool
	IncludeRegistryProvenance         bool
	Cache                             *CacheOptions
}
