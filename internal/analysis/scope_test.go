package analysis

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const scopeJSGlob = "src/**/*.js"

func TestApplyPathScopeFiltersFilesAndReportsDiagnostics(t *testing.T) {
	repo := t.TempDir()
	writeScopeFile(t, filepath.Join(repo, "src", "keep.js"), "export const keep = true\n")
	writeScopeFile(t, filepath.Join(repo, "src", "skip.test.js"), "export const skip = true\n")
	writeScopeFile(t, filepath.Join(repo, "README.md"), "doc\n")

	scopedPath, warnings, cleanup, err := applyPathScope(repo, []string{scopeJSGlob}, []string{"**/*.test.js"})
	if err != nil {
		t.Fatalf("apply path scope: %v", err)
	}
	defer cleanup()

	if _, err := os.Stat(filepath.Join(scopedPath, "src", "keep.js")); err != nil {
		t.Fatalf("expected kept file copied into scoped workspace: %v", err)
	}
	if _, err := os.Stat(filepath.Join(scopedPath, "src", "skip.test.js")); !os.IsNotExist(err) {
		t.Fatalf("expected excluded file to be omitted, got err=%v", err)
	}
	if len(warnings) == 0 {
		t.Fatalf("expected scope warnings")
	}
	if !containsWarning(warnings, "analysis scope include matches") {
		t.Fatalf("expected include match summary warning, got %#v", warnings)
	}
	if !containsWarning(warnings, "analysis scope skipped file: src/skip.test.js") {
		t.Fatalf("expected skipped file diagnostic, got %#v", warnings)
	}
}

func TestGlobMatchSupportsDoubleStar(t *testing.T) {
	if !globMatch(scopeJSGlob, "src/a/b/c.js") {
		t.Fatalf("expected recursive glob to match nested file")
	}
	if globMatch(scopeJSGlob, "src/a/b/c.ts") {
		t.Fatalf("expected extension mismatch not to match")
	}
}

func TestApplyPathScopeNoPatternsReturnsOriginalPath(t *testing.T) {
	repo := t.TempDir()
	scopedPath, warnings, cleanup, err := applyPathScope(repo, nil, nil)
	if err != nil {
		t.Fatalf("apply path scope without patterns: %v", err)
	}
	defer cleanup()
	if scopedPath != repo {
		t.Fatalf("expected original repo path when no scope patterns are set")
	}
	if len(warnings) != 0 {
		t.Fatalf("expected no scope warnings without scope patterns, got %#v", warnings)
	}
}

func TestNoOpCleanupDoesNothing(t *testing.T) {
	noOpCleanup()
}

func TestCopyFileRejectsUnsafeRelativePath(t *testing.T) {
	repo := t.TempDir()
	scoped := t.TempDir()
	if copyFile(repo, scoped, "../escape.txt") == nil {
		t.Fatalf("expected unsafe relative path rejection")
	}
}

func TestNormalizePatternsTrimsDedupesAndNormalizes(t *testing.T) {
	got := normalizePatterns([]string{" src/**/*.go ", "", "src/**/*.go", "src/**/*.go"})
	if len(got) != 1 || got[0] != "src/**/*.go" {
		t.Fatalf("expected normalized deduped pattern, got %#v", got)
	}
}

func TestRecordScopeSkipReasonAndCap(t *testing.T) {
	stats := newScopeStats([]string{"src/**/*.go"}, nil)
	recordScopeSkip(stats, "README.md", false, "", false, "")
	if len(stats.skippedDiagnostics) != 1 || !strings.Contains(stats.skippedDiagnostics[0], "did not match include patterns") {
		t.Fatalf("expected include-miss reason, got %#v", stats.skippedDiagnostics)
	}

	stats.skippedDiagnostics = make([]string, maxScopeDiagnostics)
	recordScopeSkip(stats, "ignored.go", false, "", false, "")
	if len(stats.skippedDiagnostics) != maxScopeDiagnostics {
		t.Fatalf("expected diagnostics to stay capped at %d, got %d", maxScopeDiagnostics, len(stats.skippedDiagnostics))
	}
}

func TestAsteriskSegmentVariants(t *testing.T) {
	segment, next := asteriskSegment("**/x", 0)
	if segment != "(?:.*/)?" || next != 2 {
		t.Fatalf("expected double-star slash segment, got %q %d", segment, next)
	}

	segment, next = asteriskSegment("**x", 0)
	if segment != ".*" || next != 1 {
		t.Fatalf("expected double-star segment, got %q %d", segment, next)
	}

	segment, next = asteriskSegment("*x", 0)
	if segment != "[^/]*" || next != 0 {
		t.Fatalf("expected single-star segment, got %q %d", segment, next)
	}
}

func TestGlobMatchEscapesRegexMetacharacters(t *testing.T) {
	if !globMatch("a+b?.(txt)", "a+b1.(txt)") {
		t.Fatalf("expected escaped metacharacters to match literally")
	}
}

func TestPathWithinAndSafeRelativePath(t *testing.T) {
	if !pathWithin("/tmp/demo", "/tmp/demo/a.txt") {
		t.Fatalf("expected candidate path within root")
	}
	if pathWithin("/tmp/demo", "/tmp/other/a.txt") {
		t.Fatalf("expected outside candidate path to be rejected")
	}
	if isSafeRelativePath("../x") {
		t.Fatalf("expected upward traversal path to be unsafe")
	}
	if !isSafeRelativePath("src/x.go") {
		t.Fatalf("expected nested relative path to be safe")
	}
	if isSafeRelativePath(".") {
		t.Fatalf("expected current-dir path to be unsafe")
	}
	if isSafeRelativePath(filepath.Join(string(filepath.Separator), "tmp", "x")) {
		t.Fatalf("expected absolute path to be unsafe")
	}
}

func containsWarning(warnings []string, expected string) bool {
	for _, warning := range warnings {
		if strings.Contains(warning, expected) {
			return true
		}
	}
	return false
}

func writeScopeFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
