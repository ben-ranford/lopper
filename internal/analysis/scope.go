package analysis

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const maxScopeDiagnostics = 5

func applyPathScope(repoPath string, includePatterns []string, excludePatterns []string) (string, []string, func(), error) {
	includePatterns = normalizePatterns(includePatterns)
	excludePatterns = normalizePatterns(excludePatterns)
	if len(includePatterns) == 0 && len(excludePatterns) == 0 {
		return repoPath, nil, func() {}, nil
	}

	scopedRoot, err := os.MkdirTemp("", "lopper-scope-*")
	if err != nil {
		return "", nil, nil, fmt.Errorf("create analysis scope workspace: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(scopedRoot) }

	includeMatches := make(map[string]int, len(includePatterns))
	excludeMatches := make(map[string]int, len(excludePatterns))
	skippedDiagnostics := make([]string, 0, maxScopeDiagnostics)
	keptFiles := 0
	totalFiles := 0

	walkErr := filepath.WalkDir(repoPath, func(currentPath string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		totalFiles++
		relativePath, relErr := filepath.Rel(repoPath, currentPath)
		if relErr != nil {
			return relErr
		}
		slashed := filepath.ToSlash(filepath.Clean(relativePath))
		includeMatched, includePattern := matchFirstPattern(slashed, includePatterns)
		excludeMatched, excludePattern := matchFirstPattern(slashed, excludePatterns)
		shouldInclude := len(includePatterns) == 0 || includeMatched
		shouldSkip := !shouldInclude || excludeMatched
		if shouldSkip {
			if includeMatched {
				includeMatches[includePattern]++
			}
			if excludeMatched {
				excludeMatches[excludePattern]++
			}
			if len(skippedDiagnostics) < maxScopeDiagnostics {
				reason := "did not match include patterns"
				if excludeMatched {
					reason = "matched exclude pattern " + excludePattern
				}
				skippedDiagnostics = append(skippedDiagnostics, slashed+" ("+reason+")")
			}
			return nil
		}
		if includeMatched {
			includeMatches[includePattern]++
		}
		targetPath := filepath.Join(scopedRoot, relativePath)
		if copyErr := copyFile(currentPath, targetPath); copyErr != nil {
			return copyErr
		}
		keptFiles++
		return nil
	})
	if walkErr != nil {
		cleanup()
		return "", nil, nil, fmt.Errorf("apply path scope: %w", walkErr)
	}

	warnings := []string{
		fmt.Sprintf("analysis scope applied: kept %d/%d files", keptFiles, totalFiles),
	}
	if len(includePatterns) > 0 {
		warnings = append(warnings, "analysis scope include matches: "+formatPatternMatches(includePatterns, includeMatches))
	}
	if len(excludePatterns) > 0 {
		warnings = append(warnings, "analysis scope exclude matches: "+formatPatternMatches(excludePatterns, excludeMatches))
	}
	for _, item := range skippedDiagnostics {
		warnings = append(warnings, "analysis scope skipped file: "+item)
	}
	return scopedRoot, warnings, cleanup, nil
}

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

func matchFirstPattern(path string, patterns []string) (bool, string) {
	for _, pattern := range patterns {
		if globMatch(pattern, path) {
			return true, pattern
		}
	}
	return false, ""
}

func globMatch(pattern string, value string) bool {
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
			if index+1 < len(pattern) && pattern[index+1] == '*' {
				if index+2 < len(pattern) && pattern[index+2] == '/' {
					builder.WriteString("(?:.*/)?")
					index += 2
					continue
				}
				builder.WriteString(".*")
				index++
				continue
			}
			builder.WriteString("[^/]*")
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

func formatPatternMatches(patterns []string, matches map[string]int) string {
	parts := make([]string, 0, len(patterns))
	for _, pattern := range patterns {
		parts = append(parts, fmt.Sprintf("%s=%d", pattern, matches[pattern]))
	}
	sort.Strings(parts)
	return strings.Join(parts, ", ")
}

func copyFile(sourcePath string, targetPath string) error {
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o750); err != nil {
		return err
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer source.Close()
	target, err := os.Create(targetPath)
	if err != nil {
		return err
	}
	defer target.Close()
	if _, err := io.Copy(target, source); err != nil {
		return err
	}
	return nil
}
