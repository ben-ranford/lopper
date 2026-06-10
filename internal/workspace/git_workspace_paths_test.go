package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkspaceAdditionalGitBranches(t *testing.T) {
	t.Run("changed files joins diff and status failures", func(t *testing.T) {
		setupFakeGitResolver(t, "#!/bin/sh\nif [ \"$3\" = \"diff\" ]; then\n  echo \"diff fail\" >&2\n  exit 2\nfi\nif [ \"$3\" = \"status\" ]; then\n  echo \"status fail\" >&2\n  exit 3\nfi\nexit 1\n")

		_, err := ChangedFiles(t.TempDir())
		if err == nil || !strings.Contains(err.Error(), "diff fail") || !strings.Contains(err.Error(), "status fail") {
			t.Fatalf("expected combined diff/status failure, got %v", err)
		}
	})

	t.Run("inspect git dir keeps absolute gitdir path", func(t *testing.T) {
		repo := t.TempDir()
		absoluteGitDir := filepath.Join(t.TempDir(), "git-meta")
		mustWrite(t, filepath.Join(repo, ".git"), "gitdir: "+absoluteGitDir+"\n")

		gitDir, found, err := inspectGitDir(repo)
		if err != nil {
			t.Fatalf("inspect absolute gitdir: %v", err)
		}
		if !found || gitDir != absoluteGitDir {
			t.Fatalf("expected absolute gitdir %q, got found=%v dir=%q", absoluteGitDir, found, gitDir)
		}
	})

	t.Run("resolve git dir returns not-exist when no git metadata is present", func(t *testing.T) {
		_, err := resolveGitDir(t.TempDir())
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("expected missing git metadata error, got %v", err)
		}
	})
}
