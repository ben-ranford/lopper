package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeRepoPath(t *testing.T) {
	got, err := NormalizeRepoPath("")
	if err != nil {
		t.Fatalf("normalize empty path: %v", err)
	}
	want, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("abs dot: %v", err)
	}
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestCurrentCommitSHA(t *testing.T) {
	sha, err := CurrentCommitSHA(".")
	if err != nil {
		t.Fatalf("current commit sha: %v", err)
	}
	if len(sha) < 7 {
		t.Fatalf("expected commit sha, got %q", sha)
	}
}

func TestResolveGitBinaryPath(t *testing.T) {
	original := fixedGitPaths
	t.Cleanup(func() { fixedGitPaths = original })

	path, err := resolveGitBinaryPath()
	if err != nil {
		t.Fatalf("resolveGitBinaryPath: %v", err)
	}
	if path == "" || path[0] != '/' {
		t.Fatalf("expected absolute git path, got %q", path)
	}
}

func TestResolveGitBinaryPathSkipsInvalidCandidates(t *testing.T) {
	original := fixedGitPaths
	t.Cleanup(func() { fixedGitPaths = original })

	tmp := t.TempDir()
	dirCandidate := filepath.Join(tmp, "dir")
	if err := os.MkdirAll(dirCandidate, 0o755); err != nil {
		t.Fatalf("mkdir dir candidate: %v", err)
	}
	fileCandidate := filepath.Join(tmp, "not-exec")
	if err := os.WriteFile(fileCandidate, []byte("echo hi"), 0o644); err != nil {
		t.Fatalf("write non-executable candidate: %v", err)
	}
	execCandidate := filepath.Join(tmp, "git")
	if err := os.WriteFile(execCandidate, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write executable candidate: %v", err)
	}

	fixedGitPaths = []string{dirCandidate, fileCandidate, execCandidate}
	path, err := resolveGitBinaryPath()
	if err != nil {
		t.Fatalf("resolveGitBinaryPath: %v", err)
	}
	if path != execCandidate {
		t.Fatalf("expected executable candidate %q, got %q", execCandidate, path)
	}
}

func TestResolveGitBinaryPathErrorsWhenUnavailable(t *testing.T) {
	original := fixedGitPaths
	t.Cleanup(func() { fixedGitPaths = original })

	fixedGitPaths = []string{filepath.Join(t.TempDir(), "missing-git")}
	if _, err := resolveGitBinaryPath(); err == nil {
		t.Fatalf("expected resolveGitBinaryPath to fail when no fixed candidates exist")
	}
}
