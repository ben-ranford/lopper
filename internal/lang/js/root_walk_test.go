package js

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/safeio"
)

func TestWalkRootNoFollowStopPropagatesAcrossRecursiveFrames(t *testing.T) {
	rootDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temp dir: %v", err)
	}
	for _, rel := range []string{
		filepath.Join("a", "one", "LICENSE"),
		filepath.Join("a", "two", "COPYING"),
		filepath.Join("b", "three", "LICENSE"),
		filepath.Join("b", "four", "COPYING"),
		filepath.Join("c", "five", "LICENSE"),
		filepath.Join("z", "late", "LICENSE"),
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(rootDir, rel)), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(filepath.Join(rootDir, rel), []byte("MIT License"), 0o600); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}

	root, err := safeio.OpenRootNoFollow(rootDir)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Fatalf("close root: %v", closeErr)
		}
	}()

	var visited []string
	err = walkRootNoFollow(root, func(relPath string, info os.FileInfo) (bool, bool, error) {
		if info.IsDir() {
			return false, false, nil
		}
		if isLicenseCandidate(relPath) {
			visited = append(visited, relPath)
		}
		return false, len(visited) >= 5, nil
	})
	if err != nil {
		t.Fatalf("walk root: %v", err)
	}
	if len(visited) != 5 {
		t.Fatalf("expected stop after 5 files, got %d with %#v", len(visited), visited)
	}
	for _, rel := range visited {
		if strings.Contains(rel, filepath.Join("z", "late")) {
			t.Fatalf("expected stop to prevent visiting later sibling subtree, got %#v", visited)
		}
	}
}
