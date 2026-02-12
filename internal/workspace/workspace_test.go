package workspace

import (
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
