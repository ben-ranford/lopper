package shared

import (
	"context"
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
}
