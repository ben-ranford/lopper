package report

import (
	"sort"
	"strings"
)

// SortedUniqueTrimmedStrings normalizes report text collections by trimming
// whitespace, discarding blank strings, removing duplicates, and sorting.
// It never mutates values. Empty input returns nil; nonempty all-blank input
// returns a non-nil empty slice.
func SortedUniqueTrimmedStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
