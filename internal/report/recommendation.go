package report

import "strings"

// RecommendationPriorityRank orders recommendations by high, medium, low, then
// blank or unknown priorities. Matching ignores surrounding whitespace and case.
// Equal ranks are ordered by recommendation code by the sorting caller.
func RecommendationPriorityRank(priority string) int {
	switch strings.ToLower(strings.TrimSpace(priority)) {
	case "high":
		return 0
	case "medium":
		return 1
	case "low":
		return 2
	default:
		return 3
	}
}
