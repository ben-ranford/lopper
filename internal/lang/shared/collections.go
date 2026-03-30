package shared

import (
	"cmp"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ben-ranford/lopper/internal/report"
)

func UniqueCleanPaths(values []string) []string {
	paths := uniqueNormalizedStrings(values, func(value string) string {
		return filepath.Clean(strings.TrimSpace(value))
	})
	slices.Sort(paths)
	return paths
}

func UniqueTrimmedStrings(values []string) []string {
	return uniqueNormalizedStrings(values, strings.TrimSpace)
}

func SortRecommendations(recommendations []report.Recommendation, priorityRank func(string) int) {
	slices.SortFunc(recommendations, func(left, right report.Recommendation) int {
		if order := cmp.Compare(priorityRank(left.Priority), priorityRank(right.Priority)); order != 0 {
			return order
		}
		return cmp.Compare(left.Code, right.Code)
	})
}

func SortRiskCues(cues []report.RiskCue) {
	slices.SortFunc(cues, func(left, right report.RiskCue) int {
		return cmp.Compare(left.Code, right.Code)
	})
}

func TopCountKeys(values map[string]int, limit int) []string {
	if len(values) == 0 {
		return nil
	}

	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}

	slices.SortFunc(keys, func(left, right string) int {
		if order := cmp.Compare(values[right], values[left]); order != 0 {
			return order
		}
		return cmp.Compare(left, right)
	})

	if limit > 0 && len(keys) > limit {
		keys = keys[:limit]
	}
	return keys
}

func uniqueNormalizedStrings(values []string, normalize func(string) string) []string {
	unique := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, candidate := range values {
		normalized := normalize(candidate)
		if normalized == "" || seen[normalized] {
			continue
		}
		seen[normalized] = true
		unique = append(unique, normalized)
	}
	return unique
}
