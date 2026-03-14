package analysis

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

func TestPathWithinRejectsInvalidRoot(t *testing.T) {
	if pathWithin("\x00", filepath.Join(t.TempDir(), "file.txt")) {
		t.Fatalf("expected invalid root to be rejected")
	}
}

func TestCopyFileAdditionalEscapeBranches(t *testing.T) {
	if err := copyFile("\x00", t.TempDir(), "file.txt"); err == nil {
		t.Fatalf("expected invalid source root to be rejected")
	}

	repo := t.TempDir()
	if err := copyFile(repo, "\x00", "file.txt"); err == nil {
		t.Fatalf("expected invalid target root to be rejected")
	}
}

func TestScopeWalkerAdditionalBranches(t *testing.T) {
	repo := t.TempDir()
	filePath := filepath.Join(repo, "file.txt")
	if err := os.WriteFile(filePath, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	var fileEntry os.DirEntry
	entries, err := os.ReadDir(repo)
	if err != nil {
		t.Fatalf("readdir repo: %v", err)
	}
	for _, entry := range entries {
		if entry.Name() == "file.txt" {
			fileEntry = entry
			break
		}
	}
	if fileEntry == nil {
		t.Fatal("expected file entry")
	}

	includePattern := compiledPattern{pattern: "**/*", regex: regexp.MustCompile(".*")}
	walker := &scopeWalker{
		repoPath:        "\x00",
		scopedRoot:      t.TempDir(),
		includePatterns: []string{"**/*"},
		includeCompiled: []compiledPattern{includePattern},
		stats:           newScopeStats([]string{"**/*"}, nil),
	}
	if err := walker.walk(filePath, fileEntry, nil); err == nil {
		t.Fatalf("expected invalid repo root to fail relative-path resolution")
	}

	walker = &scopeWalker{
		repoPath:        repo,
		scopedRoot:      "\x00",
		includePatterns: []string{"**/*"},
		includeCompiled: []compiledPattern{includePattern},
		stats:           newScopeStats([]string{"**/*"}, nil),
	}
	if err := walker.walk(filePath, fileEntry, nil); err == nil {
		t.Fatalf("expected invalid scoped root to fail file copy")
	}

	gitDir := filepath.Join(repo, ".git")
	if err := os.Mkdir(gitDir, 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	entries, err = os.ReadDir(repo)
	if err != nil {
		t.Fatalf("readdir repo with .git: %v", err)
	}
	for _, entry := range entries {
		if entry.Name() != ".git" {
			continue
		}
		walker = &scopeWalker{}
		if err := walker.walk(gitDir, entry, nil); err != filepath.SkipDir {
			t.Fatalf("expected .git directory to be skipped, got %v", err)
		}
		return
	}
	t.Fatal("expected .git entry")
}
