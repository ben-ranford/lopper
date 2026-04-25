package analysis

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
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
