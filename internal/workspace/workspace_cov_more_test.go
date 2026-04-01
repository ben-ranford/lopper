package workspace

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestWorkspaceAdditionalBranchCoverage(t *testing.T) {
	testWorkspaceChangedFilesReturnsGitResolutionError(t)
	testWorkspaceNormalizeRepoPathErrorsBubbleThroughHelpers(t)
	testWorkspaceEmptyRepoPathFailsWhenCWDCannotBeResolved(t)
	testWorkspaceInspectGitDirBubblesReadError(t)
	testWorkspaceRunGitConstructorError(t)
	testWorkspaceInspectGitDirResolvesRelativeGitDir(t)
}

func testWorkspaceChangedFilesReturnsGitResolutionError(t *testing.T) {
	t.Helper()

	original := resolveGitBinaryPathFn
	resolveGitBinaryPathFn = func() (string, error) {
		return "", errors.New("missing git")
	}
	t.Cleanup(func() {
		resolveGitBinaryPathFn = original
	})

	if _, err := ChangedFiles(t.TempDir()); err == nil || err.Error() != "missing git" {
		t.Fatalf("expected git resolver error, got %v", err)
	}
}

func testWorkspaceNormalizeRepoPathErrorsBubbleThroughHelpers(t *testing.T) {
	t.Helper()

	original := normalizeRepoPath
	normalizeRepoPath = func(string) (string, error) {
		return "", errors.New("normalize failed")
	}
	t.Cleanup(func() {
		normalizeRepoPath = original
	})

	if _, err := CurrentCommitSHA("."); err == nil || err.Error() != "normalize failed" {
		t.Fatalf("expected normalization failure to fail commit lookup, got %v", err)
	}
	if _, err := ChangedFiles("."); err == nil || err.Error() != "normalize failed" {
		t.Fatalf("expected normalization failure to fail changed-files lookup, got %v", err)
	}
}

func testWorkspaceEmptyRepoPathFailsWhenCWDCannotBeResolved(t *testing.T) {
	t.Helper()

	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(originalWD); err != nil {
			t.Fatalf("restore wd %s: %v", originalWD, err)
		}
	})

	deadDir := filepath.Join(t.TempDir(), "dead")
	if err := os.MkdirAll(deadDir, 0o755); err != nil {
		t.Fatalf("mkdir dead dir: %v", err)
	}
	if err := os.Chdir(deadDir); err != nil {
		t.Fatalf("chdir dead dir: %v", err)
	}
	if err := os.RemoveAll(deadDir); err != nil {
		t.Fatalf("remove dead dir: %v", err)
	}

	if _, err := CurrentCommitSHA(""); err == nil {
		t.Fatalf("expected commit lookup to fail when cwd cannot be resolved")
	}
	if _, err := ChangedFiles(""); err == nil {
		t.Fatalf("expected changed-files lookup to fail when cwd cannot be resolved")
	}
}

func testWorkspaceInspectGitDirBubblesReadError(t *testing.T) {
	t.Helper()

	repo := t.TempDir()
	gitFile := filepath.Join(repo, ".git")
	if err := os.WriteFile(gitFile, []byte("gitdir: somewhere\n"), 0o000); err != nil {
		t.Fatalf("write unreadable git file: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(gitFile, 0o600); err != nil {
			t.Fatalf("restore git file perms: %v", err)
		}
	})

	if _, _, err := inspectGitDir(repo); err == nil {
		t.Fatalf("expected unreadable git file to fail inspection")
	}
}

func testWorkspaceRunGitConstructorError(t *testing.T) {
	t.Helper()

	original := execGitCommandFn
	expected := errors.New("construct git")
	execGitCommandFn = func(string, ...string) (*exec.Cmd, error) {
		return nil, expected
	}
	t.Cleanup(func() {
		execGitCommandFn = original
	})

	if _, err := runGit("git", t.TempDir(), "status"); !errors.Is(err, expected) {
		t.Fatalf("expected git command construction error, got %v", err)
	}
}

func testWorkspaceInspectGitDirResolvesRelativeGitDir(t *testing.T) {
	t.Helper()

	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	actualGitDir := filepath.Join(root, "actual-git")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	if err := os.MkdirAll(actualGitDir, 0o755); err != nil {
		t.Fatalf("mkdir git dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".git"), []byte("gitdir: ../actual-git\n"), 0o600); err != nil {
		t.Fatalf("write .git file: %v", err)
	}

	gitDir, found, err := inspectGitDir(repo)
	if err != nil {
		t.Fatalf("inspect relative gitdir: %v", err)
	}
	if !found {
		t.Fatalf("expected gitdir file to be discovered")
	}
	if want := filepath.Clean(filepath.Join(repo, "..", "actual-git")); gitDir != want {
		t.Fatalf("expected resolved gitdir %q, got %q", want, gitDir)
	}
}
