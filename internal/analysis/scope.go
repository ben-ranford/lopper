package analysis

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
)

const (
	maxScopeDiagnostics       = 5
	maxScopedCopyFiles        = 4096
	maxScopedCopyBytes  int64 = 256 << 20
)

var (
	errScopedCopyFileLimitExceeded = errors.New("analysis scope copy file limit exceeded")
	errScopedCopyByteLimitExceeded = errors.New("analysis scope copy size limit exceeded")
	errScopedCopyNonRegularFile    = errors.New("analysis scope copy requires regular files")
)

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
	budget          scopeCopyBudget
}

type compiledPattern struct {
	pattern string
	regex   *regexp.Regexp
}

type scopeCopyBudget struct {
	maxFiles int
	maxBytes int64
	files    int
	bytes    int64
}

func newScopeCopyBudget() scopeCopyBudget {
	return scopeCopyBudget{
		maxFiles: maxScopedCopyFiles,
		maxBytes: maxScopedCopyBytes,
	}
}

func (b *scopeCopyBudget) reserve(path string, size int64) error {
	if b.maxFiles > 0 && b.files+1 > b.maxFiles {
		return fmt.Errorf("%w at %q (maximum files: %d)", errScopedCopyFileLimitExceeded, path, b.maxFiles)
	}
	if size < 0 {
		return fmt.Errorf("analysis scope copy encountered negative file size for %q", path)
	}
	if b.maxBytes > 0 {
		if size > b.maxBytes || b.bytes > b.maxBytes-size {
			return fmt.Errorf("%w at %q (maximum bytes: %d)", errScopedCopyByteLimitExceeded, path, b.maxBytes)
		}
	}
	b.files++
	b.bytes += size
	return nil
}

func applyPathScope(repoPath string, includePatterns []string, excludePatterns []string) (string, []string, func(), error) {
	return applyPathScopeWithContext(context.Background(), repoPath, includePatterns, excludePatterns)
}

func applyPathScopeWithContext(ctx context.Context, repoPath string, includePatterns []string, excludePatterns []string) (string, []string, func(), error) {
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
		budget:          newScopeCopyBudget(),
	}

	walkErr := filepath.WalkDir(repoPath, walker.walkWithContext(ctx))
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

func (w *scopeWalker) walkWithContext(ctx context.Context) fs.WalkDirFunc {
	return func(currentPath string, entry fs.DirEntry, walkErr error) error {
		return w.walkEntry(ctx, currentPath, entry, walkErr)
	}
}

func (w *scopeWalker) walk(currentPath string, entry fs.DirEntry, walkErr error) error {
	return w.walkEntry(context.Background(), currentPath, entry, walkErr)
}

func (w *scopeWalker) walkEntry(ctx context.Context, currentPath string, entry fs.DirEntry, walkErr error) error {
	if err := scopeContextErr(ctx); err != nil {
		return err
	}
	if walkErr != nil {
		return walkErr
	}
	if entry.IsDir() {
		if entry.Name() == ".git" {
			return filepath.SkipDir
		}
		return nil
	}
	return w.walkFile(ctx, currentPath, entry)
}

func (w *scopeWalker) walkFile(ctx context.Context, currentPath string, entry fs.DirEntry) error {
	w.stats.totalFiles++
	relativePath, err := filepath.Rel(w.repoPath, currentPath)
	if err != nil {
		return err
	}
	slashed := filepath.ToSlash(filepath.Clean(relativePath))
	includeMatched, includePattern, skip := w.scopePathMatch(slashed)
	if skip {
		return nil
	}
	return w.copyScopedEntry(ctx, entry, relativePath, slashed, includeMatched, includePattern)
}

func (w *scopeWalker) scopePathMatch(slashed string) (bool, string, bool) {
	includeMatched, includePattern := matchFirstCompiledPattern(slashed, w.includeCompiled)
	excludeMatched, excludePattern := matchFirstCompiledPattern(slashed, w.excludeCompiled)
	if (len(w.includePatterns) > 0 && !includeMatched) || excludeMatched {
		recordScopeSkip(w.stats, slashed, includeMatched, includePattern, excludeMatched, excludePattern)
		return false, "", true
	}
	return includeMatched, includePattern, false
}

func (w *scopeWalker) copyScopedEntry(ctx context.Context, entry fs.DirEntry, relativePath, slashed string, includeMatched bool, includePattern string) error {
	if entry.Type()&fs.ModeSymlink != 0 {
		recordScopeSkipReason(w.stats, slashed, "is symlink (not copied)")
		return nil
	}
	info, err := entry.Info()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		recordScopeSkipReason(w.stats, slashed, "is not a regular file (not copied)")
		return nil
	}
	if err := w.budget.reserve(slashed, info.Size()); err != nil {
		return err
	}
	if includeMatched {
		w.stats.includeMatches[includePattern]++
	}
	if err := copyFileWithContext(ctx, w.repoPath, w.scopedRoot, relativePath, info.Size()); err != nil {
		if errors.Is(err, errScopedCopyNonRegularFile) {
			w.budget.files--
			w.budget.bytes -= info.Size()
			recordScopeSkipReason(w.stats, slashed, "is not a regular file (not copied)")
			return nil
		}
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

func scopeContextErr(ctx context.Context) error {
	return ctx.Err()
}
