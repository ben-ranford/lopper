package shared

import (
	"path/filepath"
	"testing"
)

func TestExcludedPathsForRepoEmptyInputs(t *testing.T) {
	if got := ExcludedPathsForRepo("/repo", nil, nil); got != nil {
		t.Fatalf("expected nil for no directories/files, got %v", got)
	}
}

func TestExcludedPathsForRepoResolvesWithinRepo(t *testing.T) {
	repoPath := t.TempDir()
	dir := filepath.Join(repoPath, "traces")
	file := filepath.Join(repoPath, "cache", "lock.json")

	got := ExcludedPathsForRepo(repoPath, []string{dir}, []string{file})

	if !IsExcludedPath(got, dir) {
		t.Fatalf("expected %s to be excluded", dir)
	}
	if !IsExcludedPath(got, file) {
		t.Fatalf("expected %s to be excluded", file)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 excluded paths, got %d", len(got))
	}
}

func TestExcludedPathsForRepoSkipsBlankAndOutOfRepoPaths(t *testing.T) {
	repoPath := t.TempDir()
	outside := filepath.Join(filepath.Dir(repoPath), "elsewhere")

	got := ExcludedPathsForRepo(repoPath, []string{"  ", outside, repoPath}, []string{"\t"})

	if len(got) != 0 {
		t.Fatalf("expected no excluded paths, got %v", got)
	}
}

func TestIsExcludedPathNotPresent(t *testing.T) {
	paths := map[string]struct{}{filepath.Clean("/repo/traces"): {}}
	if IsExcludedPath(paths, "/repo/other") {
		t.Fatalf("expected /repo/other to not be excluded")
	}
}
