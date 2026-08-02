package analysis

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const scopeJSGlob = "src/**/*.js"
const scopeGoGlob = "src/**/*.go"
const scopeKeepJSPath = "src/keep.js"
const scopeCopyFileLimit = 4096
const scopeCopyByteLimit = 256 << 20

func TestApplyPathScopeFiltersFilesAndReportsDiagnostics(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, scopeKeepJSPath), "export const keep = true\n")
	writeFile(t, filepath.Join(repo, "src", "skip.test.js"), "export const skip = true\n")
	writeFile(t, filepath.Join(repo, "README.md"), "doc\n")

	scopedPath, warnings, cleanup, err := applyPathScope(repo, []string{scopeJSGlob}, []string{"**/*.test.js"})
	if err != nil {
		t.Fatalf("apply path scope: %v", err)
	}
	defer cleanup()

	if _, err := os.Stat(filepath.Join(scopedPath, scopeKeepJSPath)); err != nil {
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
	cleanup := noOpCleanup
	cleanup()
}

func TestCopyFileRejectsUnsafeRelativePath(t *testing.T) {
	repo := t.TempDir()
	scoped := t.TempDir()
	if copyFile(repo, scoped, "../escape.txt") == nil {
		t.Fatalf("expected unsafe relative path rejection")
	}
}

func TestCopyFileFailsWhenSourceRootCannotOpen(t *testing.T) {
	temp := t.TempDir()
	repo := filepath.Join(temp, "missing-repo")
	scoped := t.TempDir()

	err := copyFile(repo, scoped, "src/keep.js")
	if err == nil {
		t.Fatalf("expected source-root open failure for missing repository root")
	}
}

func TestCopyFileFailsWhenSourceFileMissing(t *testing.T) {
	repo := t.TempDir()
	scoped := t.TempDir()

	err := copyFile(repo, scoped, "src/missing.js")
	if err == nil {
		t.Fatalf("expected missing source file error")
	}
}

func TestApplyPathScopeSkipsSymlinkedFiles(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, scopeKeepJSPath), "export const keep = true\n")
	target := filepath.Join(repo, "outside.js")
	writeFile(t, target, "export const outside = true\n")
	linkPath := filepath.Join(repo, "src", "linked.js")
	if err := os.Symlink(target, linkPath); err != nil {
		t.Skipf("symlink unsupported in test environment: %v", err)
	}

	scopedPath, warnings := applyScopedJSScope(t, repo)

	if _, err := os.Stat(filepath.Join(scopedPath, scopeKeepJSPath)); err != nil {
		t.Fatalf("expected regular in-scope file to be copied: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(scopedPath, "src", "linked.js")); !os.IsNotExist(err) {
		t.Fatalf("expected symlinked file to be skipped, got err=%v", err)
	}
	if !containsWarning(warnings, "analysis scope include matches: src/**/*.js=1") {
		t.Fatalf("expected include summary to count only copied files, got %#v", warnings)
	}
	if !containsWarning(warnings, "analysis scope skipped file: src/linked.js (is symlink (not copied))") {
		t.Fatalf("expected symlink skip diagnostic, got %#v", warnings)
	}
}

func TestNormalizePatternsTrimsDedupesAndNormalizes(t *testing.T) {
	got := normalizePatterns([]string{" " + scopeGoGlob + " ", "", scopeGoGlob, scopeGoGlob})
	if len(got) != 1 || got[0] != scopeGoGlob {
		t.Fatalf("expected normalized deduped pattern, got %#v", got)
	}
}

func TestRecordScopeSkipReasonAndCap(t *testing.T) {
	stats := newScopeStats([]string{scopeGoGlob}, nil)
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

func TestApplyPathScopeRejectsOversizedScopedCopy(t *testing.T) {
	repo := t.TempDir()
	oversizedPath := filepath.Join(repo, scopeKeepJSPath)
	if err := os.MkdirAll(filepath.Dir(oversizedPath), 0o750); err != nil {
		t.Fatalf("mkdir oversized file parent: %v", err)
	}
	file, err := os.Create(oversizedPath)
	if err != nil {
		t.Fatalf("create oversized file: %v", err)
	}
	if err := file.Truncate(scopeCopyByteLimit + 1); err != nil {
		if closeErr := file.Close(); closeErr != nil {
			t.Fatalf("close oversized file after truncate error: %v", closeErr)
		}
		t.Fatalf("truncate oversized file: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close oversized file: %v", err)
	}

	expectScopedJSScopeFailure(t, repo, "expected oversized scoped copy to fail")
}

func TestApplyPathScopeRejectsTooManyCopiedFiles(t *testing.T) {
	repo := t.TempDir()
	for i := 0; i <= scopeCopyFileLimit; i++ {
		writeFile(t, filepath.Join(repo, "src", "pkg", fmt.Sprintf("f-%d.js", i)), "export const keep = true\n")
	}

	expectScopedJSScopeFailure(t, repo, "expected scoped copy file limit to fail")
}

func containsWarning(warnings []string, expected string) bool {
	for _, warning := range warnings {
		if strings.Contains(warning, expected) {
			return true
		}
	}
	return false
}

func applyScopedJSScope(t *testing.T, repo string) (string, []string) {
	t.Helper()

	scopedPath, warnings, cleanup, err := applyPathScope(repo, []string{scopeJSGlob}, nil)
	if err != nil {
		t.Fatalf("apply path scope: %v", err)
	}
	t.Cleanup(cleanup)

	return scopedPath, warnings
}

func expectScopedJSScopeFailure(t *testing.T, repo, message string) {
	t.Helper()

	if _, _, cleanup, err := applyPathScope(repo, []string{scopeJSGlob}, nil); err == nil {
		if cleanup != nil {
			cleanup()
		}
		t.Fatal(message)
	}
}
