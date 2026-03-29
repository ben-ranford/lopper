package report

import "slices"

func AnnotateFindingConfidence(dependencies []DependencyReport) {
	for depIndex := range dependencies {
		confidence := resolveReachabilityConfidence(dependencies[depIndex])
		if confidence == nil {
			continue
		}
		score := roundTo(confidence.Score, 1)
		reasonCodes := orderedConfidenceReasonCodes(confidence.RationaleCodes)

		for findingIndex := range dependencies[depIndex].UnusedExports {
			dependencies[depIndex].UnusedExports[findingIndex].ConfidenceScore = score
			dependencies[depIndex].UnusedExports[findingIndex].ConfidenceReasonCodes = reasonCodes
		}
		for findingIndex := range dependencies[depIndex].UnusedImports {
			dependencies[depIndex].UnusedImports[findingIndex].ConfidenceScore = score
			dependencies[depIndex].UnusedImports[findingIndex].ConfidenceReasonCodes = reasonCodes
		}
		for findingIndex := range dependencies[depIndex].Recommendations {
			dependencies[depIndex].Recommendations[findingIndex].ConfidenceScore = score
			dependencies[depIndex].Recommendations[findingIndex].ConfidenceReasonCodes = reasonCodes
		}
		for findingIndex := range dependencies[depIndex].RiskCues {
			dependencies[depIndex].RiskCues[findingIndex].ConfidenceScore = score
			dependencies[depIndex].RiskCues[findingIndex].ConfidenceReasonCodes = reasonCodes
		}
	}
}

func FilterFindingsByConfidence(dependencies []DependencyReport, minConfidence float64) {
	if minConfidence <= 0 {
		return
	}
	for depIndex := range dependencies {
		dependencies[depIndex].UnusedExports = filterByConfidenceScore(dependencies[depIndex].UnusedExports, minConfidence, func(item SymbolRef) float64 {
			return item.ConfidenceScore
		})
		dependencies[depIndex].UnusedImports = filterByConfidenceScore(dependencies[depIndex].UnusedImports, minConfidence, func(item ImportUse) float64 {
			return item.ConfidenceScore
		})
		dependencies[depIndex].Recommendations = filterByConfidenceScore(dependencies[depIndex].Recommendations, minConfidence, func(item Recommendation) float64 {
			return item.ConfidenceScore
		})
		dependencies[depIndex].RiskCues = filterByConfidenceScore(dependencies[depIndex].RiskCues, minConfidence, func(item RiskCue) float64 {
			return item.ConfidenceScore
		})
	}
}

func orderedConfidenceReasonCodes(reasonCodes []string) []string {
	result := make([]string, 0, len(orderedConfidenceReasonCodeValues))
	for _, candidate := range orderedConfidenceReasonCodeValues {
		if !slices.Contains(reasonCodes, candidate) {
			continue
		}
		result = append(result, candidate)
	}
	return result
}

func filterByConfidenceScore[T any](values []T, minConfidence float64, score func(item T) float64) []T {
	filtered := make([]T, 0, len(values))
	for _, value := range values {
		if score(value) < minConfidence {
			continue
		}
		filtered = append(filtered, value)
	}
	return filtered
}
