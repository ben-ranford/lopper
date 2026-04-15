package analysis

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestAnalysisPipelineAdditionalSetupBranches(t *testing.T) {
	service := &Service{Registry: language.NewRegistry()}
	invalidPattern := string([]byte{0xff})

	if _, err := service.newAnalysisPipeline(context.Background(), Request{
		RepoPath:        ".",
		IncludePatterns: []string{invalidPattern},
	}); err == nil {
		t.Fatalf("expected newAnalysisPipeline to surface applyPathScope failures")
	}
}

func TestScopedCandidateRootsChangedPackagesSuccessBranch(t *testing.T) {
	repoRoot := t.TempDir()
	rootA := filepath.Join(repoRoot, "packages", "a")
	rootB := filepath.Join(repoRoot, "packages", "b")
	writeFile(t, filepath.Join(rootA, "a.txt"), "a1\n")
	writeFile(t, filepath.Join(rootB, "b.txt"), "b1\n")

	testutil.RunGit(t, repoRoot, "init", "-b", "main")
	testutil.RunGit(t, repoRoot, "config", "user.email", "codex@example.com")
	testutil.RunGit(t, repoRoot, "config", "user.name", "Codex")
	testutil.RunGit(t, repoRoot, "add", ".")
	testutil.RunGit(t, repoRoot, "commit", "-m", "base")

	writeFile(t, filepath.Join(rootA, "a.txt"), "a2\n")
	testutil.RunGit(t, repoRoot, "add", ".")
	testutil.RunGit(t, repoRoot, "commit", "-m", "change package a")
	writeFile(t, filepath.Join(rootB, "b-dirty.txt"), "dirty\n")

	roots, warnings := scopedCandidateRoots(ScopeModeChangedPackages, []string{rootA, rootB}, repoRoot)
	if len(warnings) != 0 {
		t.Fatalf("expected changed-packages resolution without warnings, got %#v", warnings)
	}
	if len(roots) != 2 || roots[0] != rootA || roots[1] != rootB {
		t.Fatalf("expected changed-packages scope to include changed and dirty package roots, got %#v", roots)
	}
}
