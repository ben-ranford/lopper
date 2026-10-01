// Package collections provides dependency-neutral collection operations.
package collections

import "strings"

// UniqueTrimmedStrings trims whitespace, discards blank values, and retains the
// first occurrence of each string. The result does not alias values and is
// non-nil even when values is nil or every value is blank.
func UniqueTrimmedStrings(values []string) []string {
	return UniqueNormalizedStrings(values, strings.TrimSpace)
}

// UniqueNormalizedStrings applies normalize to each value, discards empty
// results, and retains the first occurrence of each normalized string. It never
// changes values and always returns an independently allocated, non-nil slice.
func UniqueNormalizedStrings(values []string, normalize func(string) string) []string {
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
