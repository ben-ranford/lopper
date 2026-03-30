package shared

import (
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/thresholds"
)

func ResolveMinUsageRecommendationThreshold(value *int) int {
	if value != nil {
		return *value
	}
	return thresholds.Defaults().MinUsagePercentForRecommendations
}

func ResolveRemovalCandidateWeights(value *report.RemovalCandidateWeights) report.RemovalCandidateWeights {
	if value == nil {
		return report.DefaultRemovalCandidateWeights()
	}
	return report.NormalizeRemovalCandidateWeights(*value)
}

func RecommendationPriorityRank(priority string) int {
	switch priority {
	case "high":
		return 0
	case "medium":
		return 1
	default:
		return 2
	}
}
