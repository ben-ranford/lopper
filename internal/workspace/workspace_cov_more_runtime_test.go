package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWorkspaceInspectGitDirSymlinkLoop(t *testing.T) {
	repo := t.TempDir()
	if err := os.Symlink(".git", filepath.Join(repo, ".git")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	if _, _, err := inspectGitDir(repo); err == nil {
		t.Fatalf("expected inspectGitDir to fail on .git symlink loop")
	}
}
