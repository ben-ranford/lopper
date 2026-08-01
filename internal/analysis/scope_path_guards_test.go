package analysis

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

const scopePathTextFile = "file.txt"

func TestPathWithinRejectsInvalidRoot(t *testing.T) {
	if pathWithin("\x00", filepath.Join(t.TempDir(), scopePathTextFile)) {
		t.Fatalf("expected invalid root to be rejected")
	}
}

func TestCopyFileAdditionalEscapeBranches(t *testing.T) {
	if copyFile(context.Background(), "\x00", t.TempDir(), scopePathTextFile, 0) == nil {
		t.Fatalf("expected invalid source root to be rejected")
	}

	repo := t.TempDir()
	if copyFile(context.Background(), repo, "\x00", scopePathTextFile, 0) == nil {
		t.Fatalf("expected invalid target root to be rejected")
	}
}

func TestScopeWalkerAdditionalBranches(t *testing.T) {
	repo := t.TempDir()
	filePath := filepath.Join(repo, scopePathTextFile)
	if err := os.WriteFile(filePath, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	fileEntry := mustScopePathEntry(t, repo, scopePathTextFile)

	includePattern := compiledPattern{pattern: "**/*", regex: regexp.MustCompile(".*")}
	walker := &scopeWalker{
		repoPath:        "\x00",
		scopedRoot:      t.TempDir(),
		includePatterns: []string{"**/*"},
		includeCompiled: []compiledPattern{includePattern},
		stats:           newScopeStats([]string{"**/*"}, nil),
	}
	walk := walker.walkWithContext(context.Background())
	if walk(filePath, fileEntry, nil) == nil {
		t.Fatalf("expected invalid repo root to fail relative-path resolution")
	}

	walker = &scopeWalker{
		repoPath:        repo,
		scopedRoot:      "\x00",
		includePatterns: []string{"**/*"},
		includeCompiled: []compiledPattern{includePattern},
		stats:           newScopeStats([]string{"**/*"}, nil),
	}
	walk = walker.walkWithContext(context.Background())
	if walk(filePath, fileEntry, nil) == nil {
		t.Fatalf("expected invalid scoped root to fail file copy")
	}

	gitDir := filepath.Join(repo, ".git")
	if err := os.Mkdir(gitDir, 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	entries, err := os.ReadDir(repo)
	if err != nil {
		t.Fatalf("readdir repo with .git: %v", err)
	}
	for _, entry := range entries {
		if entry.Name() != ".git" {
			continue
		}
		walker = &scopeWalker{}
		if err := walker.walkWithContext(context.Background())(gitDir, entry, nil); !errors.Is(err, filepath.SkipDir) {
			t.Fatalf("expected .git directory to be skipped, got %v", err)
		}
		return
	}
	t.Fatal("expected .git entry")
}

func TestScopeWalkerInfoFailureAndBudgetRollbackBranches(t *testing.T) {
	repo := t.TempDir()
	regularPath := filepath.Join(repo, "src", "template.js")
	if err := os.MkdirAll(filepath.Dir(regularPath), 0o750); err != nil {
		t.Fatalf("mkdir regular dir: %v", err)
	}
	if err := os.WriteFile(regularPath, []byte("x"), 0o600); err != nil {
		t.Fatalf("write regular file: %v", err)
	}
	regularInfo, err := os.Stat(regularPath)
	if err != nil {
		t.Fatalf("stat regular file: %v", err)
	}

	walker := &scopeWalker{
		repoPath:        repo,
		scopedRoot:      t.TempDir(),
		includePatterns: []string{"**/*"},
		includeCompiled: []compiledPattern{{pattern: "**/*", regex: regexp.MustCompile(".*")}},
		stats:           newScopeStats([]string{"**/*"}, nil),
		budget:          newScopeCopyBudget(),
	}
	walk := walker.walkWithContext(context.Background())

	infoErr := errors.New("info failed")
	if err := walk(filepath.Join(repo, "src", "bad.js"), &fakeScopeDirEntry{name: "bad.js", infoErr: infoErr}, nil); !errors.Is(err, infoErr) {
		t.Fatalf("expected entry info error, got %v", err)
	}

	disguisedDirPath := filepath.Join(repo, "src", "disguised.js")
	if err := os.MkdirAll(disguisedDirPath, 0o750); err != nil {
		t.Fatalf("mkdir disguised dir: %v", err)
	}
	if err := walk(disguisedDirPath, &fakeScopeDirEntry{name: "disguised.js", info: regularInfo}, nil); err != nil {
		t.Fatalf("expected non-regular source downgrade to be skipped, got %v", err)
	}
	if walker.budget.files != 0 || walker.budget.bytes != 0 {
		t.Fatalf("expected scope budget rollback after non-regular source, got files=%d bytes=%d", walker.budget.files, walker.budget.bytes)
	}
	if !containsWarning(walker.stats.skippedDiagnostics, "disguised.js (is not a regular file (not copied))") {
		t.Fatalf("expected non-regular rollback diagnostic, got %#v", walker.stats.skippedDiagnostics)
	}
}

func mustScopePathEntry(t *testing.T, dir, name string) os.DirEntry {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir %s: %v", dir, err)
	}
	for _, entry := range entries {
		if entry.Name() == name {
			return entry
		}
	}
	t.Fatalf("expected %s entry", name)
	return nil
}

type fakeScopeDirEntry struct {
	name    string
	info    fs.FileInfo
	infoErr error
}

func (e *fakeScopeDirEntry) Name() string               { return e.name }
func (e *fakeScopeDirEntry) IsDir() bool                { return false }
func (e *fakeScopeDirEntry) Type() fs.FileMode          { return 0 }
func (e *fakeScopeDirEntry) Info() (fs.FileInfo, error) { return e.info, e.infoErr }
