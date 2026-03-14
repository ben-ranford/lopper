package analysis

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestScopeAdditionalBranchCoverage(t *testing.T) {
	t.Run("no-op cleanup is callable", func(t *testing.T) {
		noOpCleanup()
	})

	t.Run("missing repo returns walk error", func(t *testing.T) {
		_, _, _, err := applyPathScope(filepath.Join(t.TempDir(), "missing"), []string{scopeJSGlob}, nil)
		if err == nil {
			t.Fatalf("expected missing repo to fail applyPathScope")
		}
	})

	t.Run("walker guard branches", func(t *testing.T) {
		repo := t.TempDir()
		gitDir := filepath.Join(repo, ".git")
		if err := os.MkdirAll(gitDir, 0o755); err != nil {
			t.Fatalf("mkdir .git: %v", err)
		}
		entries, err := os.ReadDir(repo)
		if err != nil {
			t.Fatalf("read repo dir: %v", err)
		}
		var gitEntry os.DirEntry
		for _, entry := range entries {
			if entry.Name() == ".git" {
				gitEntry = entry
				break
			}
		}
		if gitEntry == nil {
			t.Fatalf("expected .git entry")
		}

		walker := &scopeWalker{repoPath: repo, scopedRoot: t.TempDir(), stats: newScopeStats(nil, nil)}
		if err := walker.walk("", nil, errors.New("walk failed")); err == nil {
			t.Fatalf("expected walkErr to be returned")
		}
		if err := walker.walk(gitDir, gitEntry, nil); !errors.Is(err, filepath.SkipDir) {
			t.Fatalf("expected .git to return SkipDir, got %v", err)
		}
	})

	t.Run("skip accounting and matcher helpers", func(t *testing.T) {
		stats := newScopeStats([]string{"src/*.js"}, []string{"**/*.test.js"})
		recordScopeSkip(stats, "src/app.test.js", true, "src/*.js", true, "**/*.test.js")
		if stats.includeMatches["src/*.js"] != 1 || stats.excludeMatches["**/*.test.js"] != 1 {
			t.Fatalf("expected include/exclude counts, got %#v %#v", stats.includeMatches, stats.excludeMatches)
		}
		if len(stats.skippedDiagnostics) != 1 || stats.skippedDiagnostics[0] != "src/app.test.js (matched exclude pattern **/*.test.js)" {
			t.Fatalf("unexpected skip diagnostic: %#v", stats.skippedDiagnostics)
		}

		patterns, err := compileGlobPatterns([]string{"src/*.js"})
		if err != nil {
			t.Fatalf("compile patterns: %v", err)
		}
		if matched, pattern := matchFirstCompiledPattern("src/app.ts", patterns); matched || pattern != "" {
			t.Fatalf("expected no compiled pattern match, got matched=%v pattern=%q", matched, pattern)
		}
		if got := formatPatternMatches([]string{"b", "a"}, map[string]int{"a": 1, "b": 2}); got != "a=1, b=2" {
			t.Fatalf("expected sorted pattern summary, got %q", got)
		}
	})

	t.Run("pattern compile and temp workspace failures", func(t *testing.T) {
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
	})

	t.Run("copy file and relative path guards", func(t *testing.T) {
		repo := t.TempDir()
		sourcePath := filepath.Join(repo, "src", "keep.js")
		writeScopeFile(t, sourcePath, "export const keep = true\n")

		if err := copyFile(repo, t.TempDir(), ".."); err == nil {
			t.Fatalf("expected unsafe relative path to fail")
		}

		blockedRoot := filepath.Join(t.TempDir(), "scoped-root")
		if err := os.WriteFile(blockedRoot, []byte("x"), 0o644); err != nil {
			t.Fatalf("write blocked root: %v", err)
		}
		if err := copyFile(repo, blockedRoot, filepath.Join("src", "keep.js")); err == nil {
			t.Fatalf("expected blocked scoped root to fail copy")
		}
	})
}
