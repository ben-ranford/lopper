package app

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestLockfileDriftAdditionalPathAndWalkBranches(t *testing.T) {
	if _, err := detectLockfileDrift(context.Background(), "\x00", false); err == nil {
		t.Fatalf("expected detectLockfileDrift to reject invalid repo path")
	}

	parent := t.TempDir()
	child := filepath.Join(parent, "child")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatalf("mkdir child: %v", err)
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatalf("readdir parent: %v", err)
	}
	if err := os.RemoveAll(child); err != nil {
		t.Fatalf("remove child: %v", err)
	}
	for _, entry := range entries {
		if entry.Name() != "child" {
			continue
		}
		if processLockfileDir(context.Background(), child, entry, nil, lockfileWalkState{normalizedPath: parent, warnings: &[]string{}}) == nil {
			t.Fatalf("expected removed directory to fail when scanning lockfile drift")
		}
	}

	if got := relativeDir("\x00", filepath.Join(parent, "pkg")); got != filepath.Join(parent, "pkg") {
		t.Fatalf("expected relativeDir to fall back to input dir, got %q", got)
	}
	if got := mergeGitPaths(); len(got) != 0 {
		t.Fatalf("expected mergeGitPaths with no groups to return nil, got %#v", got)
	}
}

func TestLockfileDriftGitErrorBranches(t *testing.T) {
	original := resolveGitBinaryPathFn
	defer func() { resolveGitBinaryPathFn = original }()

	repo := t.TempDir()
	fakeGit := writeFakeGitBinary(t)
	resolveGitBinaryPathFn = func() (string, error) { return fakeGit, nil }
	useFakeGitCommandContext(t)

	writeFakeGitMode(t, repo, "lsfail")
	if _, _, err := gitChangedFiles(context.Background(), repo); err == nil || !strings.Contains(err.Error(), "ls-files") {
		t.Fatalf("expected gitChangedFiles to surface ls-files failure, got %v", err)
	}

	writeFakeGitMode(t, repo, "difffail-head")
	if _, err := gitTrackedChanges(context.Background(), repo); err == nil || !strings.Contains(err.Error(), "run git") {
		t.Fatalf("expected gitTrackedChanges HEAD diff failure, got %v", err)
	}

	writeFakeGitMode(t, repo, "difffail-unstaged")
	if _, err := gitTrackedChanges(context.Background(), repo); err == nil || !strings.Contains(err.Error(), "run git") {
		t.Fatalf("expected gitTrackedChanges unstaged diff failure, got %v", err)
	}

	writeFakeGitMode(t, repo, "difffail-cached")
	if _, err := gitTrackedChanges(context.Background(), repo); err == nil || !strings.Contains(err.Error(), "run git") {
		t.Fatalf("expected gitTrackedChanges cached diff failure, got %v", err)
	}

	resolveGitBinaryPathFn = func() (string, error) { return "", context.Canceled }
	if _, err := gitDiffNameOnly(context.Background(), repo); err == nil {
		t.Fatalf("expected gitDiffNameOnly to surface git command creation failure")
	}
}

func TestGitCommandContextConstructorError(t *testing.T) {
	originalResolve := resolveGitBinaryPathFn
	originalExec := execGitCommandContextFn
	resolveGitBinaryPathFn = func() (string, error) { return writeFakeGitBinary(t), nil }
	execGitCommandContextFn = func(context.Context, string, ...string) (*exec.Cmd, error) {
		return nil, errors.New("construct git")
	}
	t.Cleanup(func() {
		resolveGitBinaryPathFn = originalResolve
		execGitCommandContextFn = originalExec
	})

	if _, err := gitCommandContext(context.Background(), t.TempDir(), "status"); err == nil || !strings.Contains(err.Error(), "construct git") {
		t.Fatalf("expected gitCommandContext to return constructor error, got %v", err)
	}
}

func writeFakeGitBinary(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "git")
	script := `#!/bin/sh
args="$*"
mode="${FAKE_GIT_MODE}"
if [ "$1" = "-C" ] && [ -f "$2/.fake-git-mode" ]; then
  mode="$(cat "$2/.fake-git-mode")"
fi
if printf '%s' "$args" | grep -q 'rev-parse --is-inside-work-tree'; then
  echo true
  exit 0
fi
if printf '%s' "$args" | grep -q 'rev-parse --verify --quiet HEAD'; then
  if [ "$mode" = "difffail-cached" ] || [ "$mode" = "difffail-unstaged" ]; then
    exit 1
  fi
  exit 0
fi
if printf '%s' "$args" | grep -q 'ls-files --others --exclude-standard'; then
  if [ "$mode" = "lsfail" ]; then
    echo "ls-files failed" >&2
    exit 1
  fi
  exit 0
fi
if printf '%s' "$args" | grep -q 'diff --no-ext-diff --no-textconv'; then
  if printf '%s' "$args" | grep -q -- '--cached'; then
    if [ "$mode" = "difffail-cached" ]; then
      echo "diff failed" >&2
      exit 1
    fi
    exit 0
  fi
  if [ "$mode" = "difffail-head" ] || [ "$mode" = "difffail-unstaged" ]; then
    echo "diff failed" >&2
    exit 1
  fi
  exit 0
fi
exit 0
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake git script: %v", err)
	}
	return path
}

func writeFakeGitMode(t *testing.T, repo, mode string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, ".fake-git-mode"), []byte(mode), 0o600); err != nil {
		t.Fatalf("write fake git mode: %v", err)
	}
}

func useFakeGitCommandContext(t *testing.T) {
	t.Helper()

	original := execGitCommandContextFn
	execGitCommandContextFn = func(ctx context.Context, gitPath string, args ...string) (*exec.Cmd, error) {
		return exec.CommandContext(ctx, gitPath, args...), nil
	}
	t.Cleanup(func() {
		execGitCommandContextFn = original
	})
}
