package analysis

import "github.com/ben-ranford/lopper/internal/report"

type CacheOptions struct {
	Enabled  bool
	Path     string
	ReadOnly bool
}

type Request struct {
	RepoPath                          string
	Dependency                        string
	TopN                              int
	SuggestOnly                       bool
	Language                          string
	ConfigPath                        string
	RuntimeProfile                    string
	RuntimeTracePath                  string
	LowConfidenceWarningPercent       *int
	MinUsagePercentForRecommendations *int
	RemovalCandidateWeights           *report.RemovalCandidateWeights
	Cache                             *CacheOptions
}
