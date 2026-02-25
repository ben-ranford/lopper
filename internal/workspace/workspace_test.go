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

func TestCurrentCommitSHA(t *testing.T) {
	sha, err := CurrentCommitSHA(".")
	if err != nil {
		t.Fatalf("current commit sha: %v", err)
	}
	if len(sha) < 7 {
		t.Fatalf("expected commit sha, got %q", sha)
	}
}

func TestNonPathEnv(t *testing.T) {
	input := []string{
		"HOME=/tmp/example",
		"PATH=/custom/bin",
		"SHELL=/bin/zsh",
	}
	filtered := nonPathEnv(input)
	if len(filtered) != 2 {
		t.Fatalf("expected PATH entry to be removed, got %v", filtered)
	}
	for _, entry := range filtered {
		if entry == "PATH=/custom/bin" {
			t.Fatalf("did not expect PATH entry in filtered env")
		}
	}
}
