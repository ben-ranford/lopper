package analysis

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

const (
	scopeSourcePattern  = "src/*.js"
	scopeExcludePattern = "**/*.test.js"
)

func TestScopeNoOpCleanupIsCallable(t *testing.T) {
	noOpCleanup()
}

func TestScopeApplyPathScopeReturnsWalkErrorForMissingRepo(t *testing.T) {
	_, _, _, err := applyPathScope(filepath.Join(t.TempDir(), "missing"), []string{scopeJSGlob}, nil)
	if err == nil {
		t.Fatalf("expected missing repo to fail applyPathScope")
	}
}

func TestScopeWalkerGuardBranches(t *testing.T) {
	repo := t.TempDir()
	gitDir := filepath.Join(repo, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	gitEntry := mustScopeDirEntry(t, repo, ".git")

	walker := &scopeWalker{repoPath: repo, scopedRoot: t.TempDir(), stats: newScopeStats(nil, nil)}
	if walker.walk("", nil, errors.New("walk failed")) == nil {
		t.Fatalf("expected walkErr to be returned")
	}
	if err := walker.walk(gitDir, gitEntry, nil); !errors.Is(err, filepath.SkipDir) {
		t.Fatalf("expected .git to return SkipDir, got %v", err)
	}
}

func TestScopeSkipAccountingAndMatcherHelpers(t *testing.T) {
	stats := newScopeStats([]string{scopeSourcePattern}, []string{scopeExcludePattern})
	recordScopeSkip(stats, "src/app.test.js", true, scopeSourcePattern, true, scopeExcludePattern)
	if stats.includeMatches[scopeSourcePattern] != 1 || stats.excludeMatches[scopeExcludePattern] != 1 {
		t.Fatalf("expected include/exclude counts, got %#v %#v", stats.includeMatches, stats.excludeMatches)
	}
	if len(stats.skippedDiagnostics) != 1 || stats.skippedDiagnostics[0] != "src/app.test.js (matched exclude pattern **/*.test.js)" {
		t.Fatalf("unexpected skip diagnostic: %#v", stats.skippedDiagnostics)
	}

	patterns, err := compileGlobPatterns([]string{scopeSourcePattern})
	if err != nil {
		t.Fatalf("compile patterns: %v", err)
	}
	if matched, pattern := matchFirstCompiledPattern("src/app.ts", patterns); matched || pattern != "" {
		t.Fatalf("expected no compiled pattern match, got matched=%v pattern=%q", matched, pattern)
	}
	formatted := formatPatternMatches([]string{"b", "a"}, map[string]int{"a": 1, "b": 2})
	if formatted != "a=1, b=2" {
		t.Fatalf("expected sorted pattern summary, got %q", formatted)
	}
}

func TestScopePatternCompileAndTempWorkspaceFailures(t *testing.T) {
	invalidPattern := string([]byte{0xff})
	if _, err := compileGlobPatterns([]string{invalidPattern}); err == nil {
		t.Fatalf("expected invalid utf-8 pattern to fail compilation")
	}
	if _, _, _, err := applyPathScope(t.TempDir(), []string{invalidPattern}, nil); err == nil {
		t.Fatalf("expected include pattern compile error")
	}
	if _, _, _, err := applyPathScope(t.TempDir(), nil, []string{invalidPattern}); err == nil {
		t.Fatalf("expected exclude pattern compile error")
	}

	tmpRoot := t.TempDir()
	tmpFile := filepath.Join(tmpRoot, "tmp-as-file")
	if err := os.WriteFile(tmpFile, []byte("x"), 0o644); err != nil {
		t.Fatalf("write tmp file: %v", err)
	}
	t.Setenv("TMPDIR", tmpFile)
	if _, _, _, err := applyPathScope(t.TempDir(), []string{scopeJSGlob}, nil); err == nil {
		t.Fatalf("expected temp workspace creation to fail")
	}
}

func TestScopeCopyFileAndRelativePathGuards(t *testing.T) {
	repo := t.TempDir()
	sourcePath := filepath.Join(repo, "src", "keep.js")
	writeScopeFile(t, sourcePath, "export const keep = true\n")

	if copyFile(repo, t.TempDir(), "..") == nil {
		t.Fatalf("expected unsafe relative path to fail")
	}

	blockedRoot := filepath.Join(t.TempDir(), "scoped-root")
	if err := os.WriteFile(blockedRoot, []byte("x"), 0o644); err != nil {
		t.Fatalf("write blocked root: %v", err)
	}
	if copyFile(repo, blockedRoot, filepath.Join("src", "keep.js")) == nil {
		t.Fatalf("expected blocked scoped root to fail copy")
	}
}

func mustScopeDirEntry(t *testing.T, dir, name string) os.DirEntry {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir %s: %v", dir, err)
	}
	for _, entry := range entries {
		if entry.Name() == name {
			return entry
		}
	}
	t.Fatalf("expected %s entry", name)
	return nil
}
