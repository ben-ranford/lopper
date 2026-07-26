package safeio

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRootCapabilitiesAndRelativeWriteRoot(t *testing.T) {
	rootDir, root := openRootCapabilitiesFixture(t)
	assertRootDirectoryRead(t, root)
	assertRootSymlinkCapabilities(t, root)
	assertRelativeWriteRootCreation(t, rootDir, root)
	assertRelativeWriteRootGuards(t, rootDir, root)
}

func openRootCapabilitiesFixture(t *testing.T) (string, Root) {
	t.Helper()
	rootDir := t.TempDir()
	canonicalRootDir, err := filepath.EvalSymlinks(rootDir)
	if err != nil {
		t.Fatalf("canonicalize temporary root: %v", err)
	}
	if err := os.Mkdir(filepath.Join(rootDir, "existing"), 0o750); err != nil {
		t.Fatalf("mkdir existing child: %v", err)
	}
	root, err := OpenRootNoFollow(canonicalRootDir)
	if err != nil {
		t.Fatalf("open no-follow root: %v", err)
	}
	t.Cleanup(func() {
		if err := root.Close(); err != nil {
			t.Errorf("close no-follow root: %v", err)
		}
	})
	return rootDir, root
}

func assertRootDirectoryRead(t *testing.T, root Root) {
	t.Helper()
	entries, err := ReadDirWithinRoot(root, ".")
	if err != nil {
		t.Fatalf("read rooted directory: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "existing" {
		t.Fatalf("unexpected rooted directory entries: %#v", entries)
	}
}

func assertRootSymlinkCapabilities(t *testing.T, root Root) {
	t.Helper()
	if err := SymlinkWithinRoot(root, "existing", "existing-link"); err != nil {
		t.Logf("rooted symlink creation unsupported: %v", err)
		return
	}
	linkTarget, err := ReadlinkWithinRoot(root, "existing-link")
	if err != nil {
		t.Fatalf("read rooted symlink: %v", err)
	}
	if linkTarget != "existing" {
		t.Fatalf("unexpected rooted symlink target: %q", linkTarget)
	}
	if _, err := OpenRelativeWriteRoot(root, filepath.Join("existing-link", "child"), true, 0o750); err == nil {
		t.Fatal("expected relative root traversal through symlink to fail")
	}
}

func assertRelativeWriteRootCreation(t *testing.T, rootDir string, root Root) {
	t.Helper()
	child, err := OpenRelativeWriteRoot(root, filepath.Join("existing", "created"), true, 0o750)
	if err != nil {
		t.Fatalf("open created relative write root: %v", err)
	}
	if child.CanonicalPath() != filepath.Join("existing", "created") {
		t.Fatalf("unexpected relative write-root identity: %q", child.CanonicalPath())
	}
	if err := child.WriteFileExclusiveCreatingParents(filepath.Join("nested", "result.txt"), []byte("bound\n"), 0o600, 0o750); err != nil {
		t.Fatalf("write exclusive rooted file: %v", err)
	}
	if err := child.WriteFileExclusiveCreatingParents(filepath.Join("nested", "result.txt"), []byte("replace\n"), 0o600, 0o750); !errors.Is(err, os.ErrExist) {
		t.Fatalf("expected duplicate exclusive write rejection, got %v", err)
	}
	if err := child.Close(); err != nil {
		t.Fatalf("close relative write root: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(rootDir, "existing", "created", "nested", "result.txt")); err != nil || string(data) != "bound\n" {
		t.Fatalf("inspect exclusive rooted write: data=%q err=%v", data, err)
	}
}

func assertRelativeWriteRootGuards(t *testing.T, rootDir string, root Root) {
	t.Helper()
	rootCopy, err := OpenRelativeWriteRoot(root, ".", false, 0)
	if err != nil {
		t.Fatalf("open relative root copy: %v", err)
	}
	if err := rootCopy.Close(); err != nil {
		t.Fatalf("close relative root copy: %v", err)
	}
	for _, target := range []string{filepath.Join(rootDir, "absolute"), ".."} {
		if _, err := OpenRelativeWriteRoot(root, target, false, 0); err == nil {
			t.Fatalf("expected unsafe relative root %q to fail", target)
		}
	}
	if _, err := OpenRelativeWriteRoot(root, "missing", false, 0); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected missing relative root rejection, got %v", err)
	}
	if _, err := OpenRelativeWriteRoot(nil, ".", false, 0); err == nil {
		t.Fatal("expected nil parent root rejection")
	}
}

func TestRootCapabilityHelpersRejectUnsupportedImplementations(t *testing.T) {
	expectedOpenErr := errors.New("open blocked")
	openFailure := &fakeRoot{
		Root: &fakeRoot{},
		open: func(string) (File, error) {
			return nil, expectedOpenErr
		},
	}
	if _, err := ReadDirWithinRoot(openFailure, "."); !errors.Is(err, expectedOpenErr) {
		t.Fatalf("expected directory open error, got %v", err)
	}

	unsupportedFile := &fakeFile{
		close: func() error { return nil },
	}
	unsupportedRoot := &fakeRoot{
		Root: &fakeRoot{},
		open: func(string) (File, error) {
			return unsupportedFile, nil
		},
	}
	if _, err := ReadDirWithinRoot(unsupportedRoot, "."); err == nil || !strings.Contains(err.Error(), "does not support directory reads") {
		t.Fatalf("expected unsupported directory reader error, got %v", err)
	}
	if _, err := ReadlinkWithinRoot(unsupportedRoot, "link"); err == nil || !strings.Contains(err.Error(), "does not support symlink reads") {
		t.Fatalf("expected unsupported readlink error, got %v", err)
	}
	if err := SymlinkWithinRoot(unsupportedRoot, "target", "link"); err == nil || !strings.Contains(err.Error(), "does not support symlink creation") {
		t.Fatalf("expected unsupported symlink error, got %v", err)
	}
}

func TestOpenRootChildNoFollowRejectsDeviceBoundary(t *testing.T) {
	previous := sameDeviceFileInfoFn
	sameDeviceFileInfoFn = func(Root, Root, fs.FileInfo, fs.FileInfo) bool { return false }
	t.Cleanup(func() {
		sameDeviceFileInfoFn = previous
	})

	dirInfo := statTestPath(t, t.TempDir())
	root := &fakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			return dirInfo, nil
		},
		openRoot: func(string) (Root, error) {
			return &fakeRoot{
				lstat: func(string) (fs.FileInfo, error) { return dirInfo, nil },
				close: func() error { return nil },
			}, nil
		},
	}
	opened, err := openRootChildNoFollow(root, "child", "/root/child")
	if opened != nil {
		t.Fatal("expected device-boundary child root rejection")
	}
	if err == nil || !strings.Contains(err.Error(), "device boundary") {
		t.Fatalf("expected device-boundary error, got %v", err)
	}
}

func TestOpenRootChildNoFollowAllowsTraversalWhenDeviceIdentityUnsupported(t *testing.T) {
	withSameDeviceIdentitySupportedFn(t, func() bool { return false })
	withSameDeviceFileInfoFn(t, func(Root, Root, fs.FileInfo, fs.FileInfo) bool { return false })

	dirInfo := statTestPath(t, t.TempDir())
	root := &fakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			return dirInfo, nil
		},
		openRoot: func(string) (Root, error) {
			return &fakeRoot{
				lstat: func(string) (fs.FileInfo, error) { return dirInfo, nil },
				close: func() error { return nil },
			}, nil
		},
	}
	opened, err := openRootChildNoFollow(root, "child", "/root/child")
	if err != nil {
		t.Fatalf("expected traversal without device proof support to remain usable, got %v", err)
	}
	if opened == nil {
		t.Fatal("expected child root when device proof is unsupported")
	}
	if closeErr := opened.Close(); closeErr != nil {
		t.Fatalf("close opened child root: %v", closeErr)
	}
}

func TestWriteFileExclusiveCreatingParentsCleansUpWriteFailure(t *testing.T) {
	expectedWriteErr := errors.New("write failed")
	removed := false
	root := &fakeRoot{
		Root: &fakeRoot{},
		openFile: func(string, int, os.FileMode) (File, error) {
			return &fakeFile{
				write: func([]byte) (int, error) {
					return 0, expectedWriteErr
				},
				close: func() error { return nil },
			}, nil
		},
		remove: func(string) error {
			removed = true
			return nil
		},
	}
	writeRoot := &WriteRoot{root: root, rootAbs: "."}
	err := writeRoot.WriteFileExclusiveCreatingParents("result.txt", []byte("payload"), 0o600, 0o750)
	if !errors.Is(err, expectedWriteErr) {
		t.Fatalf("expected write failure, got %v", err)
	}
	if !removed {
		t.Fatal("expected failed exclusive write cleanup")
	}

	expectedRemoveErr := errors.New("remove failed")
	root.remove = func(string) error {
		return expectedRemoveErr
	}
	err = writeRoot.WriteFileExclusiveCreatingParents("remove-blocked.txt", []byte("payload"), 0o600, 0o750)
	if !errors.Is(err, expectedWriteErr) || !errors.Is(err, expectedRemoveErr) {
		t.Fatalf("expected joined write and cleanup errors, got %v", err)
	}

	expectedCloseErr := errors.New("close failed")
	root.remove = func(string) error { return nil }
	root.openFile = func(string, int, os.FileMode) (File, error) {
		return &fakeFile{
			write: func(data []byte) (int, error) { return len(data), nil },
			close: func() error { return expectedCloseErr },
		}, nil
	}
	if err := writeRoot.WriteFileExclusiveCreatingParents("close-blocked.txt", []byte("payload"), 0o600, 0o750); !errors.Is(err, expectedCloseErr) {
		t.Fatalf("expected close failure, got %v", err)
	}
	if err := writeRoot.WriteFileExclusiveCreatingParents("close-blocked.txt", []byte("payload"), 0o600, 0o750); !errors.Is(err, expectedCloseErr) {
		t.Fatalf("expected retry after close failure to reopen cleanly, got %v", err)
	}

	expectedOpenErr := errors.New("open failed")
	root.openFile = func(string, int, os.FileMode) (File, error) {
		return nil, expectedOpenErr
	}
	if err := writeRoot.WriteFileExclusiveCreatingParents("blocked.txt", []byte("payload"), 0o600, 0o750); !errors.Is(err, expectedOpenErr) {
		t.Fatalf("expected open failure, got %v", err)
	}
	if err := writeRoot.WriteFileExclusiveCreatingParents(filepath.Join(string(os.PathSeparator), "absolute"), nil, 0o600, 0o750); err == nil {
		t.Fatal("expected absolute exclusive write rejection")
	}
}
