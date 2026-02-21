package analysis

import "github.com/ben-ranford/lopper/internal/report"

type Request struct {
	RepoPath                          string
	Dependency                        string
	TopN                              int
	Language                          string
	RuntimeProfile                    string
	RuntimeTracePath                  string
	LowConfidenceWarningPercent       *int
	MinUsagePercentForRecommendations *int
	RemovalCandidateWeights           *report.RemovalCandidateWeights
}
