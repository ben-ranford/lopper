package rust

import (
	"path/filepath"
	"slices"
	"testing"
)

func TestScanRootsCollapsesOverlappingWorkspaceRoots(t *testing.T) {
	repo := t.TempDir()
	manifestPaths := []string{
		filepath.Join(repo, "crates", "alpha", cargoTomlName),
		filepath.Join(repo, cargoTomlName),
		filepath.Join(repo, "crates", "beta", cargoTomlName),
	}

	roots := scanRoots(manifestPaths, repo)
	if len(roots) != 1 || !samePath(roots[0], repo) {
		t.Fatalf("expected overlapping scan roots to collapse to repo root, got %#v", roots)
	}
}

func TestScanRootsRetainsMemberRootsWithoutParentManifest(t *testing.T) {
	repo := t.TempDir()
	manifestPaths := []string{
		filepath.Join(repo, "crates", "alpha", cargoTomlName),
		filepath.Join(repo, "crates", "beta", cargoTomlName),
	}

	roots := scanRoots(manifestPaths, repo)
	want := []string{
		filepath.Join(repo, "crates", "alpha"),
		filepath.Join(repo, "crates", "beta"),
	}
	if !slices.Equal(roots, want) {
		t.Fatalf("expected workspace member roots without parent to be preserved, got %#v", roots)
	}
}
