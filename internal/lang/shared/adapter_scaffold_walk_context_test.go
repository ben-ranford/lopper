package shared

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNewReportAndWalkContextErrAdditionalBranches(t *testing.T) {
	if err := WalkContextErr(nilContext(), nil); err != nil {
		t.Fatalf("expected nil walk/context error when context is nil, got %v", err)
	}
	if IsPathWithin("\x00", filepath.Join(t.TempDir(), "child")) {
		t.Fatalf("expected invalid root path to be rejected")
	}
}

func nilContext() context.Context {
	return nil
}

func TestResolvePathWithMissingLeaf(t *testing.T) {
	repo := t.TempDir()
	missingLeaf := filepath.Join(repo, "missing", "leaf.txt")

	resolved, err := resolvePathWithMissingLeaf(missingLeaf)
	if err != nil {
		t.Fatalf("resolvePathWithMissingLeaf: %v", err)
	}
	wantPrefix, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatalf("eval symlinks repo: %v", err)
	}
	want := filepath.Join(wantPrefix, "missing", "leaf.txt")
	if resolved != want {
		t.Fatalf("resolved path = %q, want %q", resolved, want)
	}
	if resolved, err := resolvePathWithMissingLeaf("/"); err != nil || resolved != "/" {
		t.Fatalf("expected root path to resolve to itself, got %q %v", resolved, err)
	}
	if _, err := resolvePathWithMissingLeaf("\x00"); err == nil {
		t.Fatalf("expected invalid path to fail resolution")
	}
	brokenSymlink := filepath.Join(repo, "broken")
	if err := os.Symlink(filepath.Join(repo, "missing-target"), brokenSymlink); err == nil {
		if _, err := resolvePathWithMissingLeaf(brokenSymlink); err == nil {
			t.Fatalf("expected broken symlink resolution to fail")
		}
	}
	if !pathWithin(repo, repo) {
		t.Fatalf("expected pathWithin to accept the root itself")
	}
	if pathWithin(repo, repo+"-sibling") {
		t.Fatalf("expected pathWithin to reject sibling paths with matching prefixes")
	}
}

func TestNewReportUsesDefaultWorkingDirectoryForEmptyRepoPath(t *testing.T) {
	repoPath, got, err := NewReport("", time.Now)
	if err != nil {
		t.Fatalf("NewReport returned error: %v", err)
	}
	if repoPath == "" || got.RepoPath != repoPath {
		t.Fatalf("expected normalized repo path to be recorded, got repoPath=%q report=%#v", repoPath, got)
	}
}

func TestIsPathWithinRejectsBrokenRootSymlink(t *testing.T) {
	linkPath := filepath.Join(t.TempDir(), "repo-link")
	if err := os.Symlink(filepath.Join(t.TempDir(), "missing-target"), linkPath); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	if IsPathWithin(linkPath, filepath.Join(linkPath, "child.txt")) {
		t.Fatal("expected broken root symlink to be rejected")
	}
}

func TestIsPathWithinRejectsBrokenCandidateSymlink(t *testing.T) {
	repo := t.TempDir()
	candidate := filepath.Join(repo, "broken-child")
	if err := os.Symlink(filepath.Join(repo, "missing-target"), candidate); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	if IsPathWithin(repo, candidate) {
		t.Fatal("expected broken candidate symlink to be rejected")
	}
}
