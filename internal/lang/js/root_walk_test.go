package js

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/safeio"
)

func TestWalkRootNoFollowStopPropagatesAcrossRecursiveFrames(t *testing.T) {
	rootDir := createRootWalkFixture(t, []string{
		filepath.Join("a", "one", "LICENSE"),
		filepath.Join("a", "two", "COPYING"),
		filepath.Join("b", "three", "LICENSE"),
		filepath.Join("b", "four", "COPYING"),
		filepath.Join("c", "five", "LICENSE"),
		filepath.Join("z", "late", "LICENSE"),
	})
	root := openRootWalkFixture(t, rootDir)
	defer closeRootWalkFixture(t, root)

	var visited []string
	err := walkRootNoFollow(root, func(relPath string, info os.FileInfo) (bool, bool, error) {
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
	assertRootWalkOmitsLateSubtree(t, visited, filepath.Join("z", "late"))
}

func TestWalkRootNoFollowBestEffortContinuesPastUnreadableSubtree(t *testing.T) {
	rootDir := createRootWalkFixture(t, []string{filepath.Join("z", "LICENSE")})
	blockedDir := filepath.Join(rootDir, "a", "blocked")
	if err := os.MkdirAll(blockedDir, 0o755); err != nil {
		t.Fatalf("mkdir blocked dir: %v", err)
	}
	if err := os.Chmod(blockedDir, 0o000); err != nil {
		t.Fatalf("chmod blocked dir: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(blockedDir, 0o755); err != nil {
			t.Fatalf("restore blocked dir permissions: %v", err)
		}
	})

	root := openRootWalkFixture(t, rootDir)
	defer closeRootWalkFixture(t, root)

	var visited []string
	err := walkRootNoFollowBestEffort(root, func(relPath string, info os.FileInfo) (bool, bool, error) {
		if !info.IsDir() && isLicenseCandidate(relPath) {
			visited = append(visited, relPath)
		}
		return false, false, nil
	})
	if err != nil {
		t.Fatalf("walk root best effort: %v", err)
	}
	if len(visited) != 1 || visited[0] != filepath.Join("z", "LICENSE") {
		t.Fatalf("expected later license to be preserved, got %#v", visited)
	}
}

func createRootWalkFixture(t *testing.T, relPaths []string) string {
	t.Helper()

	rootDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temp dir: %v", err)
	}
	for _, rel := range relPaths {
		fullPath := filepath.Join(rootDir, rel)
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(fullPath, []byte("MIT License"), 0o600); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	return rootDir
}

func openRootWalkFixture(t *testing.T, rootDir string) safeio.Root {
	t.Helper()

	resolvedRootDir, err := filepath.EvalSymlinks(rootDir)
	if err != nil {
		t.Fatalf("resolve root dir: %v", err)
	}
	root, err := safeio.OpenRootNoFollow(resolvedRootDir)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	return root
}

func closeRootWalkFixture(t *testing.T, root safeio.Root) {
	t.Helper()

	if err := root.Close(); err != nil {
		t.Fatalf("close root: %v", err)
	}
}

func assertRootWalkOmitsLateSubtree(t *testing.T, visited []string, relPrefix string) {
	t.Helper()

	for _, rel := range visited {
		if strings.Contains(rel, relPrefix) {
			t.Fatalf("expected stop to prevent visiting later sibling subtree, got %#v", visited)
		}
	}
}

func TestWalkRootNoFollowBestEffortContinuesPastLstatFailure(t *testing.T) {
	rootDir := t.TempDir()
	licensePath := filepath.Join(rootDir, "LICENSE")
	if err := os.WriteFile(licensePath, []byte("MIT License"), 0o600); err != nil {
		t.Fatalf("write license: %v", err)
	}
	licenseInfo, err := os.Lstat(licensePath)
	if err != nil {
		t.Fatalf("lstat license: %v", err)
	}

	root := &fakeJSRoot{
		open: func(name string) (safeio.File, error) {
			if name != "." {
				return nil, errors.New("unexpected open path")
			}
			return &fakeReadDirFile{
				entries: []os.DirEntry{
					&fakeDirEntry{name: "missing.js", mode: 0, info: licenseInfo},
					&fakeDirEntry{name: "LICENSE", mode: licenseInfo.Mode(), info: licenseInfo},
				},
			}, nil
		},
		lstat: func(name string) (os.FileInfo, error) {
			switch name {
			case "missing.js":
				return nil, errors.New("lstat failed")
			case "LICENSE":
				return licenseInfo, nil
			default:
				return nil, errors.New("unexpected lstat path")
			}
		},
	}

	var visited []string
	err = walkRootNoFollowBestEffort(root, func(relPath string, info os.FileInfo) (bool, bool, error) {
		if !info.IsDir() {
			visited = append(visited, relPath)
		}
		return false, false, nil
	})
	if err != nil {
		t.Fatalf("walk root best effort with lstat failure: %v", err)
	}
	if len(visited) != 1 || visited[0] != "LICENSE" {
		t.Fatalf("expected later readable entry to be visited, got %#v", visited)
	}
}

func TestWalkRootNoFollowBestEffortReturnsUnreadableRoot(t *testing.T) {
	root := &fakeJSRoot{
		open: func(string) (safeio.File, error) {
			return nil, errors.New("open root failed")
		},
	}

	visited := false
	err := walkRootNoFollowBestEffort(root, func(string, os.FileInfo) (bool, bool, error) {
		visited = true
		return false, false, nil
	})
	if err == nil || !strings.Contains(err.Error(), "open root failed") {
		t.Fatalf("expected unreadable root to return its error, got %v", err)
	}
	if visited {
		t.Fatal("expected unreadable root failure to prevent visits")
	}
}

func TestWalkRootNoFollowPropagatesLstatErrorWithoutBestEffort(t *testing.T) {
	root := &fakeJSRoot{
		open: func(name string) (safeio.File, error) {
			if name != "." {
				return nil, errors.New("unexpected open path")
			}
			return &fakeReadDirFile{entries: []os.DirEntry{&fakeDirEntry{name: "bad.js"}}}, nil
		},
		lstat: func(string) (os.FileInfo, error) {
			return nil, errors.New("lstat failed")
		},
	}

	err := walkRootNoFollow(root, func(string, os.FileInfo) (bool, bool, error) {
		return false, false, nil
	})
	if err == nil || !strings.Contains(err.Error(), "lstat failed") {
		t.Fatalf("expected lstat failure to propagate without best-effort mode, got %v", err)
	}
}

func TestWalkRootNoFollowPropagatesChildOpenErrorWithoutBestEffort(t *testing.T) {
	rootDir := t.TempDir()
	childDir := filepath.Join(rootDir, "child")
	if err := os.Mkdir(childDir, 0o755); err != nil {
		t.Fatalf("mkdir child dir: %v", err)
	}
	childInfo, err := os.Lstat(childDir)
	if err != nil {
		t.Fatalf("lstat child dir: %v", err)
	}

	root := &fakeJSRoot{
		open: func(name string) (safeio.File, error) {
			if name != "." {
				return nil, errors.New("unexpected open path")
			}
			return &fakeReadDirFile{entries: []os.DirEntry{&fakeDirEntry{name: "child", mode: childInfo.Mode(), info: childInfo}}}, nil
		},
		lstat: func(string) (os.FileInfo, error) {
			return childInfo, nil
		},
		openRoot: func(string) (safeio.Root, error) {
			return nil, errors.New("open child failed")
		},
	}

	err = walkRootNoFollow(root, func(string, os.FileInfo) (bool, bool, error) {
		return false, false, nil
	})
	if err == nil || !strings.Contains(err.Error(), "open child failed") {
		t.Fatalf("expected child-open failure to propagate without best-effort mode, got %v", err)
	}
}
