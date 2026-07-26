package safeio

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenRootNoFollowUsesConfiguredFileSystem(t *testing.T) {
	called := false
	withFileSystem(t, &fakeFileSystem{
		openRootNoFollow: func(name string) (Root, error) {
			called = true
			if name != "/tmp/runtime-root" {
				t.Fatalf("unexpected root path: %s", name)
			}
			return &fakeRoot{close: func() error { return nil }}, nil
		},
	})

	root, err := OpenRootNoFollow("/tmp/runtime-root")
	if err != nil {
		t.Fatalf("OpenRootNoFollow returned error: %v", err)
	}
	if !called {
		t.Fatal("expected OpenRootNoFollow to delegate to configured filesystem")
	}
	if err := root.Close(); err != nil {
		t.Fatalf("close delegated root: %v", err)
	}
}

func TestOSFileSystemOpenRootNoFollowPropagatesRelativePathError(t *testing.T) {
	root, err := (&osFileSystem{}).OpenRootNoFollow(string([]byte{0}))
	if root != nil {
		if closeErr := root.Close(); closeErr != nil {
			t.Fatalf("close unexpected root: %v", closeErr)
		}
		t.Fatal("expected invalid path root to remain nil")
	}
	if err == nil {
		t.Fatal("expected invalid path error")
	}
}

func TestOpenTargetParentSkipsDotSegments(t *testing.T) {
	rootDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(rootDir, "reports", "nested"), 0o750); err != nil {
		t.Fatalf("mkdir nested parent: %v", err)
	}
	writeRoot := openTestWriteRoot(t, rootDir, OpenWriteRoot)

	target := rootedTarget{
		rootAbs: rootDir,
		rel:     "reports/./nested/file.txt",
	}
	parent, closeParent, err := writeRoot.openTargetParent(target, false, 0)
	if err != nil {
		t.Fatalf("openTargetParent returned error: %v", err)
	}
	if !closeParent {
		t.Fatal("expected nested parent root to be owned")
	}
	if err := parent.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
		t.Fatalf("close nested parent root: %v", err)
	}
}

func TestPublishFileWithinRootRejectsNonRegularExistingTarget(t *testing.T) {
	rootDir := t.TempDir()
	targetDir := filepath.Join(rootDir, "published")
	if err := os.MkdirAll(targetDir, 0o750); err != nil {
		t.Fatalf("mkdir publish target dir: %v", err)
	}
	root := openTestRoot(t, rootDir)

	err := PublishFileWithinRoot(root, "published", []byte("after"), 0o640)
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("expected non-regular target rejection, got %v", err)
	}
}
