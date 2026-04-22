package shared

import (
	"context"
	"sort"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
)

type ImportRecord struct {
	Dependency string
	Module     string
	Name       string
	Local      string
	Location   report.Location
	Wildcard   bool
}

type FileUsage struct {
	Imports []ImportRecord
	Usage   map[string]int
}

func FirstContentColumn(line string) int {
	for i := 0; i < len(line); i++ {
		if line[i] != ' ' && line[i] != '\t' {
			return i + 1
		}
	}
	return 1
}

func MapSlice[T any, R any](items []T, mapper func(T) R) []R {
	mapped := make([]R, 0, len(items))
	for _, elem := range items {
		mapped = append(mapped, mapper(elem))
	}
	return mapped
}

func MapFileUsages[T any](files []T, importsOf func(T) []ImportRecord, usageOf func(T) map[string]int) []FileUsage {
	return MapSlice(files, func(file T) FileUsage {
		return FileUsage{
			Imports: importsOf(file),
			Usage:   usageOf(file),
		}
	})
}

func SortedKeys(values map[string]struct{}) []string {
	if len(values) == 0 {
		return nil
	}
	items := make([]string, 0, len(values))
	for value := range values {
		items = append(items, value)
	}
	sort.Strings(items)
	return items
}

func DefaultRepoPath(repoPath string) string {
	if repoPath == "" {
		return "."
	}
	return repoPath
}

// FinalizeDetection applies shared post-processing for language detection.
// It enforces a confidence floor (35) only when matched, always caps confidence at 95,
// and ensures matched detections have at least one root before returning sorted roots.
func FinalizeDetection(repoPath string, detection language.Detection, roots map[string]struct{}) language.Detection {
	if detection.Matched && detection.Confidence < 35 {
		detection.Confidence = 35
	}
	if detection.Confidence > 95 {
		detection.Confidence = 95
	}
	if len(roots) == 0 && detection.Matched {
		roots[repoPath] = struct{}{}
	}
	detection.Roots = SortedKeys(roots)
	return detection
}

func DetectMatched(ctx context.Context, repoPath string, detectWithConfidence func(context.Context, string) (language.Detection, error)) (bool, error) {
	detection, err := detectWithConfidence(ctx, repoPath)
	if err != nil {
		return false, err
	}
	return detection.Matched, nil
}
