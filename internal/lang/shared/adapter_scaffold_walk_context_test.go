package shared

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestNewReportAndWalkContextErrAdditionalBranches(t *testing.T) {
	if err := WalkContextErr(nilContext(), nil); err != nil {
		t.Fatalf("expected nil walk/context error when context is nil, got %v", err)
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
