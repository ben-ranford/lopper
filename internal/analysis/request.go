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

	trustedPin *trustedCachePin
}

type repositoryAuthState struct {
	paths trustedRepoPaths
	nonce uint64
}

// trustedCachePin is created only at the repository validation boundary.
// Downstream code consumes these immutable identities without resolving again.
type trustedCachePin struct {
	kind             trustedCachePathKind
	repositoryState  *repositoryAuthState
	canonicalPath    string
	repoRelativePath string
}

type trustedCachePathKind uint8

const (
	trustedCachePathInRepo trustedCachePathKind = iota + 1
	trustedCachePathExternal
)

type Request struct {
	RepoPath                          string
	Repository                        *RepositoryAuthorization
	RepositoryView                    *RepositoryView
	ChangedFiles                      []string
	ChangedFilesExplicit              bool
	Dependency                        string
	TopN                              int
	ScopeMode                         string
	SuggestOnly                       bool
	Language                          string
	ConfigPath                        string
	ConfigCachePath                   string
	RuntimeProfile                    string
	RuntimeTracePath                  string
	RuntimeTraceCachePath             string
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
