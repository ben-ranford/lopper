package analysis

type Request struct {
	RepoPath                          string
	Dependency                        string
	TopN                              int
	Language                          string
	RuntimeProfile                    string
	RuntimeTracePath                  string
	LowConfidenceWarningPercent       *int
	MinUsagePercentForRecommendations *int
}
