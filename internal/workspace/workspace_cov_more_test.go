package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWorkspaceAdditionalBranchCoverage(t *testing.T) {
	t.Run("changed files returns git resolution error", func(t *testing.T) {
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
	})

	t.Run("normalize repo path errors bubble through helpers", func(t *testing.T) {
		if _, err := CurrentCommitSHA("\x00"); err == nil {
			t.Fatalf("expected invalid repo path to fail commit lookup")
		}
		if _, err := ChangedFiles("\x00"); err == nil {
			t.Fatalf("expected invalid repo path to fail changed-files lookup")
		}
	})

	t.Run("empty repo path fails when cwd cannot be resolved", func(t *testing.T) {
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
	})

	t.Run("inspect git dir bubbles read error for unreadable git file", func(t *testing.T) {
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
	})
}
