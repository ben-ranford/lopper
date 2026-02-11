package analysis

type Request struct {
	RepoPath                          string
	Dependency                        string
	TopN                              int
	Language                          string
	RuntimeTracePath                  string
	LowConfidenceWarningPercent       *int
	MinUsagePercentForRecommendations *int
}
