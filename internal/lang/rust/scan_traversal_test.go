package rust

import (
	"fmt"
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

func TestScanRootsCapsSiblingWorkspaceMembers(t *testing.T) {
	repo := t.TempDir()
	const want = 256
	manifestPaths := make([]string, 0, want+1)
	for index := range want + 1 {
		manifestPaths = append(manifestPaths, filepath.Join(repo, "crates", fmt.Sprintf("crate-%03d", index), cargoTomlName))
	}

	roots := scanRoots(manifestPaths, repo)
	if len(roots) != want {
		t.Fatalf("expected workspace roots to cap at %d, got %d", want, len(roots))
	}
}
