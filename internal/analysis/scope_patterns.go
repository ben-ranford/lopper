package analysis

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

func normalizePatterns(patterns []string) []string {
	seen := make(map[string]struct{}, len(patterns))
	result := make([]string, 0, len(patterns))
	for _, pattern := range patterns {
		trimmed := strings.TrimSpace(pattern)
		if trimmed == "" {
			continue
		}
		if _, exists := seen[trimmed]; exists {
			continue
		}
		seen[trimmed] = struct{}{}
		result = append(result, filepath.ToSlash(trimmed))
	}
	return result
}

func compileGlobPatterns(patterns []string) ([]compiledPattern, error) {
	compiled := make([]compiledPattern, 0, len(patterns))
	for _, pattern := range patterns {
		regex, err := regexp.Compile(globToRegexp(pattern))
		if err != nil {
			return nil, fmt.Errorf("compile scope pattern %q: %w", pattern, err)
		}
		compiled = append(compiled, compiledPattern{
			pattern: pattern,
			regex:   regex,
		})
	}
	return compiled, nil
}

func matchFirstCompiledPattern(path string, patterns []compiledPattern) (bool, string) {
	for _, pattern := range patterns {
		if pattern.regex.MatchString(path) {
			return true, pattern.pattern
		}
	}
	return false, ""
}

func globMatch(pattern, value string) bool {
	expression := globToRegexp(pattern)
	matched, err := regexp.MatchString(expression, value)
	return err == nil && matched
}

func globToRegexp(pattern string) string {
	var builder strings.Builder
	builder.Grow(len(pattern) * 2)
	builder.WriteString("^")
	for index := 0; index < len(pattern); index++ {
		char := pattern[index]
		if char == '*' {
			segment, next := asteriskSegment(pattern, index)
			builder.WriteString(segment)
			index = next
			continue
		}
		if char == '?' {
			builder.WriteString("[^/]")
			continue
		}
		if strings.ContainsRune(`.+()|[]{}^$\\`, rune(char)) {
			builder.WriteByte('\\')
		}
		builder.WriteByte(char)
	}
	builder.WriteString("$")
	return builder.String()
}

func asteriskSegment(pattern string, index int) (string, int) {
	if index+1 < len(pattern) && pattern[index+1] == '*' {
		if index+2 < len(pattern) && pattern[index+2] == '/' {
			return "(?:.*/)?", index + 2
		}
		return ".*", index + 1
	}
	return "[^/]*", index
}

func formatPatternMatches(patterns []string, matches map[string]int) string {
	parts := make([]string, 0, len(patterns))
	for _, pattern := range patterns {
		parts = append(parts, fmt.Sprintf("%s=%d", pattern, matches[pattern]))
	}
	sort.Strings(parts)
	return strings.Join(parts, ", ")
}
