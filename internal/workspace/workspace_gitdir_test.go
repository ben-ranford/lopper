package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInspectGitDirRejectsInvalidGitFile(t *testing.T) {
	repo := t.TempDir()
	assertInspectGitDirErrorContains(t, repo, "bogus\n", "invalid .git file format")
}

func TestInspectGitDirWithDirectory(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git dir: %v", err)
	}

	gitDir, found, err := inspectGitDir(repo)
	if err != nil {
		t.Fatalf("inspect .git dir: %v", err)
	}
	if !found || gitDir != filepath.Join(repo, ".git") {
		t.Fatalf("unexpected inspect result: found=%v dir=%q", found, gitDir)
	}
}

func TestInspectGitDirWithoutEntry(t *testing.T) {
	repo := t.TempDir()

	gitDir, found, err := inspectGitDir(repo)
	if err != nil {
		t.Fatalf("inspect empty dir: %v", err)
	}
	if found || gitDir != "" {
		t.Fatalf("expected no git entry, got found=%v dir=%q", found, gitDir)
	}
}

func TestInspectGitDirWithEmptyGitDirPath(t *testing.T) {
	repo := t.TempDir()
	assertInspectGitDirErrorContains(t, repo, "gitdir:\n", "empty gitdir path")
}

func TestInspectGitDirWithUnreadableGitFile(t *testing.T) {
	repo := t.TempDir()
	gitFile := filepath.Join(repo, ".git")
	mustWrite(t, gitFile, "gitdir: .git-meta\n")
	if err := os.Chmod(gitFile, 0o000); err != nil {
		t.Fatalf("chmod .git file: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(gitFile, 0o600); err != nil {
			t.Fatalf("restore .git file perms: %v", err)
		}
	})

	_, _, err := inspectGitDir(repo)
	if err == nil {
		t.Fatalf("expected inspectGitDir to fail when .git file is unreadable")
	}
}

func TestResolveGitDirOpenError(t *testing.T) {
	_, err := resolveGitDir(filepath.Join(t.TempDir(), "missing", "repo"))
	if err == nil {
		t.Fatalf("expected resolveGitDir to fail for missing path")
	}
}

func TestReadGitPathErrors(t *testing.T) {
	_, err := readGitPath(filepath.Join(t.TempDir(), "missing-gitdir"), "HEAD")
	if err == nil {
		t.Fatalf("expected open root error for missing git dir")
	}

	gitDir := t.TempDir()
	_, err = readGitPath(gitDir, "HEAD")
	if err == nil || !strings.Contains(err.Error(), "HEAD") {
		t.Fatalf("expected missing HEAD read error, got %v", err)
	}
}

func assertInspectGitDirErrorContains(t *testing.T, repo, gitFileContents, want string) {
	t.Helper()

	mustWrite(t, filepath.Join(repo, ".git"), gitFileContents)
	_, _, err := inspectGitDir(repo)
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("expected error containing %q, got %v", want, err)
	}
}
