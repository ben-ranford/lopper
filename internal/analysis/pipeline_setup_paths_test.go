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

func TestScopedCandidateRootsUsesExplicitChangedFilesWithoutWorkspaceFallback(t *testing.T) {
	repoRoot := t.TempDir()
	rootA := filepath.Join(repoRoot, "packages", "a")
	rootB := filepath.Join(repoRoot, "packages", "b")
	writeFile(t, filepath.Join(rootA, "a.txt"), "a1\n")
	writeFile(t, filepath.Join(rootB, "b.txt"), "b1\n")

	req := Request{
		ScopeMode:            ScopeModeChangedPackages,
		ChangedFiles:         []string{"packages/b/b.txt"},
		ChangedFilesExplicit: true,
	}
	roots, warnings := scopedCandidateRootsForRequest(req, []string{rootA, rootB}, repoRoot)
	if len(warnings) != 0 {
		t.Fatalf("expected explicit changed-packages roots without warnings, got %#v", warnings)
	}
	if len(roots) != 1 || roots[0] != rootB {
		t.Fatalf("expected explicit changed files to select package b only, got %#v", roots)
	}
}

func TestScopeMetadataIncludesRepoRootAsDot(t *testing.T) {
	repoRoot := t.TempDir()
	metadata := scopeMetadata("unexpected", repoRoot, []string{
		filepath.Join(repoRoot, "packages", "b"),
		repoRoot,
	})
	if metadata == nil {
		t.Fatalf("expected scope metadata")
	}
	if metadata.Mode != ScopeModePackage {
		t.Fatalf("expected scope mode normalization to package, got %q", metadata.Mode)
	}
	if len(metadata.Packages) != 2 || metadata.Packages[0] != "." || metadata.Packages[1] != "packages/b" {
		t.Fatalf("expected repo root package to map to dot, got %#v", metadata.Packages)
	}
}

func TestScopeMetadataDropsInvalidRepoPaths(t *testing.T) {
	metadata := scopeMetadata(ScopeModePackage, string([]byte{0}), []string{"/repo/pkg"})
	if metadata == nil {
		t.Fatalf("expected scope metadata")
	}
	if len(metadata.Packages) != 0 {
		t.Fatalf("expected invalid repo path to drop package entries, got %#v", metadata.Packages)
	}
}
