package rust

import (
	"fmt"
	"os"
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
	// The regression test also compiles against the base, before maxScanRoots exists.
	const want = 256
	manifestPaths := make([]string, 0, want+1)
	for index := range want + 1 {
		manifestPaths = append(manifestPaths, filepath.Join(repo, "crates", fmt.Sprintf("crate-%03d", index), cargoTomlName))
	}

	roots := scanRoots(manifestPaths, repo)
	if len(roots) != want {
		t.Fatalf("expected workspace roots to cap at %d, got %d", want, len(roots))
	}
	slices.Reverse(manifestPaths)
	if reversedRoots := scanRoots(manifestPaths, repo); !slices.Equal(roots, reversedRoots) {
		t.Fatal("the manifest order changed the retained workspace roots")
	}
}

func TestScanRootsCappedReportsCoverageGap(t *testing.T) {
	repo := t.TempDir()
	manifestPaths := make([]string, 0, 257)
	for index := range 257 {
		root := filepath.Join(repo, fmt.Sprintf("crate-%03d", index))
		if err := os.Mkdir(root, 0o755); err != nil {
			t.Fatal(err)
		}
		manifestPaths = append(manifestPaths, filepath.Join(root, cargoTomlName))
	}

	result, err := scanRepo(t.Context(), repo, manifestPaths, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.CoverageGaps) != 1 {
		t.Fatalf("expected one source scan coverage gap, got %#v", result.CoverageGaps)
	}
	gap := result.CoverageGaps[0]
	const warning = "Rust source scanning capped at 256 roots from 257 candidates"
	if gap.Code != "rust-source-root-scan-truncated" || gap.Language != rustAdapterID || gap.Path != "." ||
		!slices.Equal(gap.Evidence, []string{warning}) || !slices.Contains(result.Warnings, warning) {
		t.Fatalf("expected a source scan root coverage gap and matching warning, got %#v, %v", gap, result.Warnings)
	}
}

func TestScanRootsPreservingNestedRetainsRepositoryRoot(t *testing.T) {
	repo := t.TempDir()
	manifestPaths := []string{filepath.Join(repo, cargoTomlName)}
	for index := range 256 {
		manifestPaths = append(manifestPaths, filepath.Join(repo, "crates", fmt.Sprintf("crate-%03d", index), cargoTomlName))
	}

	roots := scanRootsPreservingNested(manifestPaths, repo)
	if len(roots) != 256 || roots[len(roots)-1] != repo || roots[0] != filepath.Join(repo, "crates", "crate-000") {
		t.Fatal("expected the capped nested roots to retain the repository root last")
	}
}
