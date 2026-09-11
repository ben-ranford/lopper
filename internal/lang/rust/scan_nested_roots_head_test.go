//go:build !regressionproof

package rust

import (
	"context"
	"io/fs"
	"path/filepath"
	"testing"

	"github.com/ben-ranford/lopper/internal/lang/shared"
)

func TestWalkRustScanFilesSkipsCompletedNestedRoots(t *testing.T) {
	repo := t.TempDir()
	root := repo
	completedRoots := make(map[string]struct{}, 32)
	for range 32 {
		writeFile(t, filepath.Join(root, cargoTomlName), "[package]\nname=\"crate\"\nversion=\"0.1.0\"\n")
		writeFile(t, filepath.Join(root, "src", "lib.rs"), "pub fn crate_root() {}\n")
		root = filepath.Join(root, "nested")
		completedRoots[filepath.Clean(root)] = struct{}{}
	}
	writeFile(t, filepath.Join(root, cargoTomlName), "[package]\nname=\"leaf\"\nversion=\"0.1.0\"\n")
	writeFile(t, filepath.Join(root, "src", "lib.rs"), "pub fn leaf() {}\n")

	withoutExclusion := 0
	if err := shared.WalkRepoFiles(context.Background(), repo, 0, shouldSkipDir, func(string, fs.DirEntry) error {
		withoutExclusion++
		return nil
	}); err != nil {
		t.Fatalf("walk nested Rust roots without exclusions: %v", err)
	}
	visited := 0
	if err := walkRustScanFiles(context.Background(), repo, completedRoots, func(string) error {
		visited++
		return nil
	}); err != nil {
		t.Fatalf("walk nested Rust roots: %v", err)
	}
	if visited != 2 {
		t.Fatalf("expected root-only scan after completed descendants, visited %d files", visited)
	}
	if withoutExclusion <= visited*16 {
		t.Fatalf("fixture did not expose repeated descendant traversal: without exclusion=%d, with exclusion=%d", withoutExclusion, visited)
	}
}
