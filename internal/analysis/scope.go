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

func noOpCleanup() {
	// Intentionally empty: when no scope patterns are configured, there is no temporary workspace to clean up.
}

type scopeStats struct {
	includeMatches     map[string]int
	excludeMatches     map[string]int
	skippedDiagnostics []string
	keptFiles          int
	totalFiles         int
}

func newScopeStats(includePatterns, excludePatterns []string) *scopeStats {
	return &scopeStats{
		includeMatches:     make(map[string]int, len(includePatterns)),
		excludeMatches:     make(map[string]int, len(excludePatterns)),
		skippedDiagnostics: make([]string, 0, maxScopeDiagnostics),
	}
}

type scopeWalker struct {
	repoPath        string
	scopedRoot      string
	includePatterns []string
	excludePatterns []string
	includeCompiled []compiledPattern
	excludeCompiled []compiledPattern
	stats           *scopeStats
}

type compiledPattern struct {
	pattern string
	regex   *regexp.Regexp
}

func applyPathScope(repoPath string, includePatterns []string, excludePatterns []string) (string, []string, func(), error) {
	includePatterns = normalizePatterns(includePatterns)
	excludePatterns = normalizePatterns(excludePatterns)
	if len(includePatterns) == 0 && len(excludePatterns) == 0 {
		return repoPath, nil, noOpCleanup, nil
	}
	includeCompiled, err := compileGlobPatterns(includePatterns)
	if err != nil {
		return "", nil, nil, err
	}
	excludeCompiled, err := compileGlobPatterns(excludePatterns)
	if err != nil {
		return "", nil, nil, err
	}

	scopedRoot, err := os.MkdirTemp("", "lopper-scope-*")
	if err != nil {
		return "", nil, nil, fmt.Errorf("create analysis scope workspace: %w", err)
	}
	cleanup := func() {
		if err := os.RemoveAll(scopedRoot); err != nil {
			fmt.Fprintf(os.Stderr, "cleanup scoped workspace %s: %v\n", scopedRoot, err)
		}
	}

	stats := newScopeStats(includePatterns, excludePatterns)
	walker := &scopeWalker{
		repoPath:        repoPath,
		scopedRoot:      scopedRoot,
		includePatterns: includePatterns,
		excludePatterns: excludePatterns,
		includeCompiled: includeCompiled,
		excludeCompiled: excludeCompiled,
		stats:           stats,
	}

	walkErr := filepath.WalkDir(repoPath, walker.walk)
	if walkErr != nil {
		cleanup()
		return "", nil, nil, fmt.Errorf("apply path scope: %w", walkErr)
	}

	warnings := []string{
		fmt.Sprintf("analysis scope applied: kept %d/%d files", stats.keptFiles, stats.totalFiles),
	}
	if len(includePatterns) > 0 {
		warnings = append(warnings, "analysis scope include matches: "+formatPatternMatches(includePatterns, stats.includeMatches))
	}
	if len(excludePatterns) > 0 {
		warnings = append(warnings, "analysis scope exclude matches: "+formatPatternMatches(excludePatterns, stats.excludeMatches))
	}
	for _, item := range stats.skippedDiagnostics {
		warnings = append(warnings, "analysis scope skipped file: "+item)
	}
	return scopedRoot, warnings, cleanup, nil
}

func (w *scopeWalker) walk(currentPath string, entry fs.DirEntry, walkErr error) error {
	if walkErr != nil {
		return walkErr
	}
	if entry.IsDir() {
		if entry.Name() == ".git" {
			return filepath.SkipDir
		}
		return nil
	}
	w.stats.totalFiles++
	relativePath, err := filepath.Rel(w.repoPath, currentPath)
	if err != nil {
		return err
	}
	slashed := filepath.ToSlash(filepath.Clean(relativePath))
	includeMatched, includePattern := matchFirstCompiledPattern(slashed, w.includeCompiled)
	excludeMatched, excludePattern := matchFirstCompiledPattern(slashed, w.excludeCompiled)
	shouldSkip := (len(w.includePatterns) > 0 && !includeMatched) || excludeMatched
	if shouldSkip {
		recordScopeSkip(w.stats, slashed, includeMatched, includePattern, excludeMatched, excludePattern)
		return nil
	}
	if entry.Type()&fs.ModeSymlink != 0 {
		if includeMatched {
			w.stats.includeMatches[includePattern]++
		}
		recordScopeSkipReason(w.stats, slashed, "is symlink (not copied)")
		return nil
	}
	if includeMatched {
		w.stats.includeMatches[includePattern]++
	}
	if err := copyFile(w.repoPath, w.scopedRoot, relativePath); err != nil {
		return err
	}
	w.stats.keptFiles++
	return nil
}

func recordScopeSkip(stats *scopeStats, slashed string, includeMatched bool, includePattern string, excludeMatched bool, excludePattern string) {
	if includeMatched {
		stats.includeMatches[includePattern]++
	}
	if excludeMatched {
		stats.excludeMatches[excludePattern]++
	}
	reason := "did not match include patterns"
	if excludeMatched {
		reason = "matched exclude pattern " + excludePattern
	}
	recordScopeSkipReason(stats, slashed, reason)
}

func recordScopeSkipReason(stats *scopeStats, slashed string, reason string) {
	if len(stats.skippedDiagnostics) >= maxScopeDiagnostics {
		return
	}
	stats.skippedDiagnostics = append(stats.skippedDiagnostics, slashed+" ("+reason+")")
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

func copyFile(repoPath, scopedRoot, relativePath string) error {
	if !isSafeRelativePath(relativePath) {
		return fmt.Errorf("invalid relative path for scoped copy: %s", relativePath)
	}
	sourcePath := filepath.Join(repoPath, relativePath)
	targetPath := filepath.Join(scopedRoot, relativePath)
	if !pathWithin(repoPath, sourcePath) {
		return fmt.Errorf("source path escapes repository scope: %s", sourcePath)
	}
	if !pathWithin(scopedRoot, targetPath) {
		return fmt.Errorf("target path escapes scoped workspace: %s", targetPath)
	}
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o750); err != nil {
		return err
	}
	// #nosec G304 -- sourcePath originates from WalkDir over the repository root and passes pathWithin checks above.
	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer source.Close()
	// #nosec G304 -- targetPath is derived from validated relativePath and constrained by pathWithin checks above.
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

func pathWithin(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}

func isSafeRelativePath(relativePath string) bool {
	if filepath.IsAbs(relativePath) {
		return false
	}
	cleaned := filepath.Clean(relativePath)
	if cleaned == "." {
		return false
	}
	return cleaned != ".." && !strings.HasPrefix(cleaned, ".."+string(filepath.Separator))
}
