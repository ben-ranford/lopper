package safeio

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
)

const (
	writeTestFileName   = "file.txt"
	openRootErrFmt      = "open root: %v"
	closeRootErrFmt     = "close root: %v"
	closeTempFileErrFmt = "close temp file: %v"
)

type modeOverrideFileInfo struct {
	fs.FileInfo
	mode os.FileMode
}

func (i *modeOverrideFileInfo) Mode() os.FileMode {
	return i.mode
}

type truncatingFakeFile struct {
	*fakeFile
	truncate func(size int64) error
}

func (f *truncatingFakeFile) Truncate(size int64) error {
	return f.truncate(size)
}

func writePinnedTargetInfoPair(t *testing.T) (fs.FileInfo, fs.FileInfo) {
	t.Helper()
	dir := t.TempDir()
	originalPath := filepath.Join(dir, "original")
	changedPath := filepath.Join(dir, "changed")
	if err := os.WriteFile(originalPath, []byte("original"), 0o640); err != nil {
		t.Fatalf("seed original target: %v", err)
	}
	if err := os.WriteFile(changedPath, []byte("changed"), 0o640); err != nil {
		t.Fatalf("seed changed target: %v", err)
	}
	return statTestPath(t, originalPath), statTestPath(t, changedPath)
}

func assertOverwritePinnedFileRejectsBeforeMutation(t *testing.T, openedInfo fs.FileInfo, lstat func(*testing.T) func(string) (fs.FileInfo, error), beforeRevalidate func(*testing.T) func() error) {
	t.Helper()

	truncateCalls := 0
	writeCalls := 0
	target := &truncatingFakeFile{
		fakeFile: &fakeFile{
			stat: func() (fs.FileInfo, error) { return openedInfo, nil },
			write: func(p []byte) (int, error) {
				writeCalls++
				return len(p), nil
			},
			close: closeWithoutError,
		},
		truncate: func(int64) error {
			truncateCalls++
			return nil
		},
	}
	root := &fakeRoot{
		lstat: lstat(t),
	}

	err := overwritePinnedFile(root, writeTestFileName, target, []byte("after"), beforeRevalidate(t))
	if err == nil {
		t.Fatal("expected overwrite rejection before mutation")
	}
	if truncateCalls != 0 {
		t.Fatalf("expected no truncation before rejection, got %d calls", truncateCalls)
	}
	if writeCalls != 0 {
		t.Fatalf("expected no write before rejection, got %d calls", writeCalls)
	}
}

func openTestWriteRoot(t *testing.T, rootDir string, open func(string) (*WriteRoot, error)) *WriteRoot {
	t.Helper()
	root, err := open(rootDir)
	if err != nil {
		t.Fatalf("open test write root: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := root.Close(); closeErr != nil && !errors.Is(closeErr, os.ErrClosed) {
			t.Errorf("close test write root: %v", closeErr)
		}
	})
	return root
}

func changedDirectoryTestRoot(t *testing.T) (*fakeRoot, *bool) {
	t.Helper()
	expectedInfo := statTestPath(t, t.TempDir())
	changedInfo := statTestPath(t, t.TempDir())
	childClosed := false
	child := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return changedInfo, nil },
		close: func() error {
			childClosed = true
			return nil
		},
	}
	root := &fakeRoot{
		lstat:    func(string) (fs.FileInfo, error) { return expectedInfo, nil },
		openRoot: func(string) (Root, error) { return child, nil },
	}
	return root, &childClosed
}

func assertWriteRootRejectsParent(t *testing.T, root *WriteRoot, wantError, failureMessage string) {
	t.Helper()
	err := root.WriteFileCreatingParents(filepath.Join("reports", writeTestFileName), []byte("after"), 0o600, 0o750)
	if err == nil || !strings.Contains(err.Error(), wantError) {
		t.Fatalf("%s, got %v", failureMessage, err)
	}
}

func assertWriteRootParentLookupError(t *testing.T, invoke func(*WriteRoot) error) {
	t.Helper()
	expectedErr := errors.New("parent lookup failure")
	withFileSystem(t, &fakeFileSystem{openRoot: func(string) (Root, error) {
		return &fakeRoot{
			lstat: func(string) (fs.FileInfo, error) {
				return nil, expectedErr
			},
			close: func() error { return nil },
		}, nil
	}})

	root, err := OpenWriteRoot(t.TempDir())
	if err != nil {
		t.Fatalf("OpenWriteRoot returned error: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Errorf("close write root: %v", closeErr)
		}
	})

	err = invoke(root)
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected parent lookup error, got %v", err)
	}
}

func assertPreservedExistingRegularFileMode(t *testing.T, write func(rootDir, targetPath string, data []byte) error, writeName string) {
	t.Helper()
	rootDir := t.TempDir()
	targetPath := filepath.Join(rootDir, writeTestFileName)
	if err := os.WriteFile(targetPath, []byte("before"), 0o644); err != nil {
		t.Fatalf("seed target file: %v", err)
	}
	if err := os.Chmod(targetPath, 0o644); err != nil {
		t.Fatalf("chmod target file: %v", err)
	}

	if err := write(rootDir, targetPath, []byte("after")); err != nil {
		t.Fatalf("%s returned error: %v", writeName, err)
	}

	data, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("read replaced file: %v", err)
	}
	if string(data) != "after" {
		t.Fatalf("unexpected content: got %q", string(data))
	}
	info, err := os.Stat(targetPath)
	if err != nil {
		t.Fatalf("stat replaced file: %v", err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("expected existing file mode 0644 to be preserved, got %#o", info.Mode().Perm())
	}
}

func assertRejectsSymlinkedParentEscapingRoot(t *testing.T, write func(rootDir, targetPath string, data []byte) error) {
	t.Helper()
	parentDir := t.TempDir()
	rootDir := filepath.Join(parentDir, "root")
	outsideDir := filepath.Join(parentDir, "outside")
	if err := os.MkdirAll(rootDir, 0o755); err != nil {
		t.Fatalf("create root dir: %v", err)
	}
	if err := os.MkdirAll(outsideDir, 0o755); err != nil {
		t.Fatalf("create outside dir: %v", err)
	}
	if err := os.Symlink(filepath.Join("..", "outside"), filepath.Join(rootDir, "src")); err != nil {
		t.Fatalf("create escaping symlink: %v", err)
	}

	targetPath := filepath.Join(rootDir, "src", writeTestFileName)
	err := write(rootDir, targetPath, []byte("secret"))
	if err == nil {
		t.Fatal("expected symlink escape write to fail")
	}
	if _, statErr := os.Stat(filepath.Join(outsideDir, writeTestFileName)); !os.IsNotExist(statErr) {
		t.Fatalf("expected outside file to remain absent, got err=%v", statErr)
	}
}

func assertWriteUnderRejectsPathTraversalOutsideRoot(t *testing.T, write func(rootDir, targetPath string, data []byte) error) {
	t.Helper()
	parentDir := t.TempDir()
	rootDir := filepath.Join(parentDir, "root")
	if err := os.MkdirAll(rootDir, 0o755); err != nil {
		t.Fatalf("create root dir: %v", err)
	}

	outsidePath := filepath.Join(parentDir, "secret.txt")
	err := write(rootDir, outsidePath, []byte("secret"))
	if err == nil {
		t.Fatal("expected error for outside path, got nil")
	}
	if !strings.Contains(err.Error(), escapesRootErr) {
		t.Fatalf(unexpectedErrFmt, err)
	}
}

func assertWriteUnderRejectsNonDirectoryRoot(t *testing.T, write func(rootDir, targetPath string, data []byte) error) {
	t.Helper()
	rootDir := t.TempDir()
	rootFile := filepath.Join(rootDir, "root-file")
	if err := os.WriteFile(rootFile, []byte("not-a-dir"), 0o600); err != nil {
		t.Fatalf("write root file: %v", err)
	}

	err := write(rootFile, filepath.Join(rootFile, "child.txt"), []byte("hello"))
	if err == nil {
		t.Fatal("expected error when root is not a directory")
	}
	if !strings.Contains(err.Error(), "open root") && !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf(unexpectedErrFmt, err)
	}
}

func assertWriteWithinRootRejectsAbsolutePath(t *testing.T, write func(root Root, targetPath string, data []byte, perm os.FileMode) error) {
	t.Helper()
	root := openTestRoot(t, t.TempDir())

	err := write(root, filepath.Join(t.TempDir(), writeTestFileName), []byte("hello"), 0o640)
	if err == nil || !strings.Contains(err.Error(), escapesRootErr) {
		t.Fatalf("expected absolute path rejection, got %v", err)
	}
}

func TestWriteFileUnderWritesFileInsideRoot(t *testing.T) {
	rootDir := t.TempDir()
	targetPath := filepath.Join(rootDir, "nested", writeTestFileName)
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		t.Fatalf("create parent dir: %v", err)
	}

	if err := WriteFileUnder(rootDir, targetPath, []byte("hello"), 0o640); err != nil {
		t.Fatalf("WriteFileUnder returned error: %v", err)
	}

	data, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("unexpected content: got %q", string(data))
	}
	info, err := os.Stat(targetPath)
	if err != nil {
		t.Fatalf("stat written file: %v", err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("unexpected file mode: got %#o", info.Mode().Perm())
	}
}

func TestWriteFileUnderPreservesExistingRegularFileMode(t *testing.T) {
	writeUnder := func(rootDir, targetPath string, data []byte) error {
		return WriteFileUnder(rootDir, targetPath, data, 0o600)
	}
	assertPreservedExistingRegularFileMode(t, writeUnder, "WriteFileUnder")
}

func TestWriteFileReplacingUnderPreservesExistingRegularFileMode(t *testing.T) {
	writeReplacingUnder := func(rootDir, targetPath string, data []byte) error {
		return WriteFileReplacingUnder(rootDir, targetPath, data, 0o600)
	}
	assertPreservedExistingRegularFileMode(t, writeReplacingUnder, "WriteFileReplacingUnder")
}

func TestWriteFileUnderRejectsReadOnlyExistingRegularFile(t *testing.T) {
	rootDir := t.TempDir()
	targetPath := filepath.Join(rootDir, writeTestFileName)
	if err := os.WriteFile(targetPath, []byte("before"), 0o600); err != nil {
		t.Fatalf("seed target file: %v", err)
	}
	if err := os.Chmod(targetPath, 0o400); err != nil {
		t.Fatalf("chmod target file read-only: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(targetPath, 0o600); err != nil && !os.IsNotExist(err) {
			t.Errorf("restore target file permissions: %v", err)
		}
	})

	probe, probeErr := os.OpenFile(targetPath, os.O_WRONLY, 0)
	if probeErr == nil {
		if err := probe.Close(); err != nil {
			t.Fatalf("close writability probe: %v", err)
		}
		t.Skip("effective privileges bypass read-only file permissions")
	}
	if !os.IsPermission(probeErr) {
		t.Skipf("read-only file semantics are not testable: %v", probeErr)
	}

	err := WriteFileUnder(rootDir, targetPath, []byte("after"), 0o600)
	if err == nil {
		t.Fatal("expected read-only existing file to be rejected")
	}
	if !os.IsPermission(err) {
		t.Fatalf("expected permission error, got %v", err)
	}
	data, readErr := os.ReadFile(targetPath)
	if readErr != nil {
		t.Fatalf("read target file: %v", readErr)
	}
	if string(data) != "before" {
		t.Fatalf("expected read-only target to remain unchanged, got %q", string(data))
	}
}

func TestWriteFileWithinRootWritesRelativeFile(t *testing.T) {
	rootDir := t.TempDir()
	root := openTestRoot(t, rootDir)

	if err := WriteFileWithinRoot(root, writeTestFileName, []byte("hello"), 0o640); err != nil {
		t.Fatalf("WriteFileWithinRoot returned error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(rootDir, writeTestFileName))
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("unexpected content: got %q", string(data))
	}
}

func TestWriteFileWithinRootRejectsAbsolutePath(t *testing.T) {
	assertWriteWithinRootRejectsAbsolutePath(t, WriteFileWithinRoot)
}

func TestWriteFileWithinRootReturnsTempCreationError(t *testing.T) {
	expectedErr := errors.New("open temp failure")
	root := &fakeRoot{
		openFile: func(string, int, os.FileMode) (File, error) { return nil, expectedErr },
	}

	err := WriteFileWithinRoot(root, writeTestFileName, []byte("hello"), 0o640)
	if err == nil || !errors.Is(err, expectedErr) {
		t.Fatalf("expected temp creation error, got %v", err)
	}
}

func TestWriteFileUnderRejectsPathTraversalOutsideRoot(t *testing.T) {
	assertWriteUnderRejectsPathTraversalOutsideRoot(t, func(rootDir, targetPath string, data []byte) error {
		return WriteFileUnder(rootDir, targetPath, data, 0o600)
	})
}

func TestWriteFileUnderRejectsSymlinkedParentEscapingRoot(t *testing.T) {
	assertRejectsSymlinkedParentEscapingRoot(t, func(rootDir, targetPath string, data []byte) error {
		return WriteFileUnder(rootDir, targetPath, data, 0o600)
	})
}

func TestWriteFileReplacingUnderRejectsSymlinkedParentEscapingRoot(t *testing.T) {
	assertRejectsSymlinkedParentEscapingRoot(t, func(rootDir, targetPath string, data []byte) error {
		return WriteFileReplacingUnder(rootDir, targetPath, data, 0o600)
	})
}

func TestWriteFileReplacingUnderRejectsPathTraversalOutsideRoot(t *testing.T) {
	assertWriteUnderRejectsPathTraversalOutsideRoot(t, func(rootDir, targetPath string, data []byte) error {
		return WriteFileReplacingUnder(rootDir, targetPath, data, 0o600)
	})
}

func TestWriteFileReplacingUnderRejectsNonDirectoryRoot(t *testing.T) {
	assertWriteUnderRejectsNonDirectoryRoot(t, func(rootDir, targetPath string, data []byte) error {
		return WriteFileReplacingUnder(rootDir, targetPath, data, 0o600)
	})
}

func TestWriteFileUnderRejectsNonDirectoryRoot(t *testing.T) {
	assertWriteUnderRejectsNonDirectoryRoot(t, func(rootDir, targetPath string, data []byte) error {
		return WriteFileUnder(rootDir, targetPath, data, 0o600)
	})
}

func TestWriteFileUnderRootAbsFailureWhenCWDRemoved(t *testing.T) {
	withRemovedWorkingDir(t, "dead-root")

	err := WriteFileUnder(".", writeTestFileName, []byte("hello"), 0o600)
	if err == nil {
		t.Fatal("expected root path resolution error")
	}
	if !strings.Contains(err.Error(), "resolve root path") && !strings.Contains(err.Error(), "open root") {
		t.Fatalf(unexpectedErrFmt, err)
	}
}

func TestWriteFileUnderTargetAbsFailureWhenCWDRemoved(t *testing.T) {
	rootDir := t.TempDir()
	withRemovedWorkingDir(t, "dead-target")

	err := WriteFileUnder(rootDir, "relative-target.txt", []byte("hello"), 0o600)
	if err == nil {
		t.Fatal("expected target path resolution error")
	}
	if !strings.Contains(err.Error(), "resolve target path") && !strings.Contains(err.Error(), escapesRootErr) {
		t.Fatalf(unexpectedErrFmt, err)
	}
}

func TestWriteFileUnderReturnsErrorForMissingParentDir(t *testing.T) {
	rootDir := t.TempDir()
	targetPath := filepath.Join(rootDir, "missing", writeTestFileName)

	err := WriteFileUnder(rootDir, targetPath, []byte("hello"), 0o600)
	if err == nil {
		t.Fatal("expected error for missing parent directory")
	}
}

func TestWriteRootCreatesMissingParentsAndWritesAtomically(t *testing.T) {
	rootDir := t.TempDir()
	root := openTestWriteRoot(t, rootDir, OpenWriteRoot)

	targetPath := filepath.Join("reports", "nested", writeTestFileName)
	if err := root.WriteFileCreatingParents(targetPath, []byte("hello"), 0o640, 0o750); err != nil {
		t.Fatalf("WriteFileCreatingParents returned error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(rootDir, targetPath))
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("unexpected content: %q", string(data))
	}
	info, err := os.Stat(filepath.Join(rootDir, targetPath))
	if err != nil {
		t.Fatalf("stat written file: %v", err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("unexpected file mode: %#o", info.Mode().Perm())
	}

	if err := root.WriteFileCreatingParents("root-file.txt", []byte("root"), 0o600, 0o750); err != nil {
		t.Fatalf("write root-level file: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(rootDir, "root-file.txt")); err != nil {
		t.Fatalf("read root-level file: %v", err)
	} else if string(data) != "root" {
		t.Fatalf("unexpected root-level content: %q", string(data))
	}
}

func TestWriteRootCreatesMissingParentsAndWritesIfAbsent(t *testing.T) {
	rootDir := t.TempDir()
	root := openTestWriteRoot(t, rootDir, OpenWriteRoot)

	targetPath := filepath.Join("reports", "nested", writeTestFileName)
	if err := root.WriteFileCreatingParentsIfAbsent(targetPath, []byte("hello"), 0o640, 0o750); err != nil {
		t.Fatalf("WriteFileCreatingParentsIfAbsent returned error: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(rootDir, targetPath)); err != nil {
		t.Fatalf("read written file: %v", err)
	} else if string(data) != "hello" {
		t.Fatalf("unexpected content: %q", string(data))
	}
}

func TestWriteRootIfAbsentRejectsExistingTarget(t *testing.T) {
	rootDir := t.TempDir()
	root := openTestWriteRoot(t, rootDir, OpenWriteRoot)

	targetPath := filepath.Join("reports", writeTestFileName)
	if err := root.WriteFileCreatingParentsIfAbsent(targetPath, []byte("before"), 0o600, 0o750); err != nil {
		t.Fatalf("seed target: %v", err)
	}
	err := root.WriteFileCreatingParentsIfAbsent(targetPath, []byte("after"), 0o600, 0o750)
	if !errors.Is(err, os.ErrExist) {
		t.Fatalf("expected os.ErrExist, got %v", err)
	}
	if data, readErr := os.ReadFile(filepath.Join(rootDir, targetPath)); readErr != nil {
		t.Fatalf("read existing target: %v", readErr)
	} else if string(data) != "before" {
		t.Fatalf("expected existing target to remain unchanged, got %q", string(data))
	}
}

func TestWriteRootIfAbsentRejectsDanglingTargetSymlink(t *testing.T) {
	rootDir := t.TempDir()
	outside := t.TempDir()
	root := openTestWriteRoot(t, rootDir, OpenWriteRoot)

	targetPath := filepath.Join("reports", writeTestFileName)
	if err := os.MkdirAll(filepath.Join(rootDir, "reports"), 0o755); err != nil {
		t.Fatalf("mkdir reports: %v", err)
	}
	outsideTarget := filepath.Join(outside, "missing", writeTestFileName)
	if err := os.Symlink(outsideTarget, filepath.Join(rootDir, targetPath)); err != nil {
		t.Fatalf("create dangling target symlink: %v", err)
	}

	err := root.WriteFileCreatingParentsIfAbsent(targetPath, []byte("after"), 0o600, 0o750)
	if !errors.Is(err, os.ErrExist) {
		t.Fatalf("expected dangling symlink target to be treated as existing, got %v", err)
	}
	if _, statErr := os.Stat(outsideTarget); !os.IsNotExist(statErr) {
		t.Fatalf("expected symlink target to remain absent, got err=%v", statErr)
	}
}

func TestWriteRootIfAbsentRejectsNonRelativeTargets(t *testing.T) {
	rootDir := t.TempDir()
	root := openTestWriteRoot(t, rootDir, OpenWriteRoot)

	for _, targetPath := range []string{rootDir, "..", filepath.Join("..", writeTestFileName), "."} {
		err := root.WriteFileCreatingParentsIfAbsent(targetPath, []byte("hello"), 0o600, 0o750)
		if err == nil {
			t.Fatalf("expected target %q to be rejected", targetPath)
		}
	}
}

func TestWriteRootIfAbsentPropagatesParentReadyError(t *testing.T) {
	rootDir := t.TempDir()
	root := openTestWriteRoot(t, rootDir, OpenWriteRoot)

	original := writeFileParentReadyFn
	writeFileParentReadyFn = func() error { return errors.New("parent not ready") }
	t.Cleanup(func() {
		writeFileParentReadyFn = original
	})

	err := root.WriteFileCreatingParentsIfAbsent("file.txt", []byte("hello"), 0o600, 0o750)
	if err == nil || !strings.Contains(err.Error(), "parent not ready") {
		t.Fatalf("expected parent ready error, got %v", err)
	}
}

func TestOpenWriteRootPropagatesRootResolutionError(t *testing.T) {
	expectedErr := errors.New("root abs failure")
	withFileSystem(t, &fakeFileSystem{abs: func(string) (string, error) {
		return "", expectedErr
	}})

	root, err := OpenWriteRoot(".")
	if root != nil {
		if closeErr := root.Close(); closeErr != nil {
			t.Fatalf("close unexpected root: %v", closeErr)
		}
	}
	if !errors.Is(err, expectedErr) || !strings.Contains(err.Error(), "resolve root path") {
		t.Fatalf("expected root path resolution error, got %v", err)
	}
}

func TestOpenCanonicalWriteRootWritesInsideCanonicalRoot(t *testing.T) {
	rootDir := filepath.Join(t.TempDir(), "canonical", "root")
	if err := os.MkdirAll(rootDir, 0o755); err != nil {
		t.Fatalf("mkdir canonical root: %v", err)
	}
	canonicalRoot, err := filepath.EvalSymlinks(rootDir)
	if err != nil {
		t.Fatalf("resolve canonical root: %v", err)
	}

	root := openTestWriteRoot(t, canonicalRoot, OpenCanonicalWriteRoot)

	targetPath := filepath.Join("reports", "nested", writeTestFileName)
	if err := root.WriteFileCreatingParents(targetPath, []byte("canonical"), 0o640, 0o750); err != nil {
		t.Fatalf("write through canonical root: %v", err)
	}
	assertFileContent(t, filepath.Join(canonicalRoot, targetPath), "canonical")
}

func TestOpenCanonicalWriteRootPropagatesResolutionError(t *testing.T) {
	expectedErr := errors.New("canonical abs failure")
	withFileSystem(t, &fakeFileSystem{abs: func(string) (string, error) {
		return "", expectedErr
	}})

	root, err := OpenCanonicalWriteRoot(".")
	if root != nil {
		t.Fatal("expected canonical write root to remain nil")
	}
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected canonical resolution error, got %v", err)
	}
}

func TestOpenCanonicalWriteRootWrapsOpenError(t *testing.T) {
	expectedErr := errors.New("canonical open failure")
	withFileSystem(t, &fakeFileSystem{openRootNoFollow: func(string) (Root, error) {
		return nil, expectedErr
	}})

	root, err := OpenCanonicalWriteRoot(t.TempDir())
	if root != nil {
		t.Fatal("expected canonical write root to remain nil")
	}
	if !errors.Is(err, expectedErr) || !strings.Contains(err.Error(), "open canonical root") {
		t.Fatalf("expected wrapped canonical open error, got %v", err)
	}
}

func TestOpenRootNoFollowOpensVolumeRoot(t *testing.T) {
	volumeRoot := filepath.VolumeName(t.TempDir()) + string(os.PathSeparator)
	root, err := (&osFileSystem{}).OpenRootNoFollow(volumeRoot)
	if err != nil {
		t.Fatalf("OpenRootNoFollow returned error: %v", err)
	}
	if err := root.Close(); err != nil {
		t.Fatalf("close volume root: %v", err)
	}
}

func TestOpenRootNoFollowOpensNestedPathWithDotSegments(t *testing.T) {
	parent := t.TempDir()
	nested := filepath.Join(parent, "nested", "child")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested path: %v", err)
	}

	root, err := (&osFileSystem{}).OpenRootNoFollow(filepath.Join(parent, ".", "nested", ".", "child"))
	if err != nil {
		t.Fatalf("OpenRootNoFollow returned error for nested path: %v", err)
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Fatalf("close nested no-follow root: %v", closeErr)
		}
	}()

	openedInfo, err := root.Lstat(".")
	if err != nil {
		t.Fatalf("lstat nested no-follow root: %v", err)
	}
	if !os.SameFile(openedInfo, statTestPath(t, nested)) {
		t.Fatalf("expected nested no-follow root to pin the requested directory")
	}
}

func TestOpenRootNoFollowDelegatesToConfiguredFileSystem(t *testing.T) {
	expected := &fakeRoot{}
	withFileSystem(t, &fakeFileSystem{openRootNoFollow: func(name string) (Root, error) {
		if name != "/delegated" {
			t.Fatalf("expected delegated path, got %q", name)
		}
		return expected, nil
	}})

	root, err := OpenRootNoFollow("/delegated")
	if err != nil {
		t.Fatalf("OpenRootNoFollow returned error: %v", err)
	}
	if root != expected {
		t.Fatalf("expected delegated root, got %#v", root)
	}
}

func TestOpenRootNoFollowRejectsInvalidComponents(t *testing.T) {
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve test parent: %v", err)
	}
	filePath := filepath.Join(parent, "file")
	if err := os.WriteFile(filePath, []byte("file"), 0o600); err != nil {
		t.Fatalf("write non-directory component: %v", err)
	}
	symlinkPath := filepath.Join(parent, "link")
	if err := os.Symlink(parent, symlinkPath); err != nil {
		t.Fatalf("create root symlink: %v", err)
	}

	for _, tc := range []struct {
		name string
		path string
		want string
	}{
		{name: "missing", path: filepath.Join(parent, "missing"), want: "no such file"},
		{name: "file", path: filePath, want: "root is not a directory"},
		{name: "symlink", path: symlinkPath, want: "root contains symlink"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, err := (&osFileSystem{}).OpenRootNoFollow(tc.path)
			if root != nil {
				if closeErr := root.Close(); closeErr != nil {
					t.Fatalf("close unexpected root: %v", closeErr)
				}
				t.Fatal("expected rejected root to remain nil")
			}
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), tc.want) {
				t.Fatalf("expected %q error, got %v", tc.want, err)
			}
		})
	}
}

func TestOpenRootNoFollowAllowsTrustedTempAlias(t *testing.T) {
	aliasPath, resolvedPath, ok := tempDirAliasPair(t)
	if !ok {
		t.Skip("trusted temp alias unavailable")
	}

	root, err := (&osFileSystem{}).OpenRootNoFollow(aliasPath)
	if err != nil {
		t.Fatalf("OpenRootNoFollow returned error: %v", err)
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Fatalf("close trusted alias root: %v", closeErr)
		}
	}()

	aliasInfo, err := os.Stat(resolvedPath)
	if err != nil {
		t.Fatalf("stat resolved trusted alias path: %v", err)
	}
	openedInfo, err := root.Lstat(".")
	if err != nil {
		t.Fatalf("lstat opened trusted alias root: %v", err)
	}
	if !os.SameFile(aliasInfo, openedInfo) {
		t.Fatalf("expected trusted alias root to pin resolved directory identity")
	}
}

func TestOpenRootExistingAncestorNoFollowRejectsUntrustedSymlinkAncestor(t *testing.T) {
	parentDir := t.TempDir()
	outsideDir := filepath.Join(t.TempDir(), "outside")
	if err := os.MkdirAll(outsideDir, 0o755); err != nil {
		t.Fatalf("mkdir outside dir: %v", err)
	}
	linkPath := filepath.Join(parentDir, "cache-link")
	if err := os.Symlink(outsideDir, linkPath); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	root, ancestorPath, missingParts, err := OpenRootExistingAncestorNoFollow(filepath.Join(linkPath, "nested", "cache"))
	if root != nil {
		if closeErr := root.Close(); closeErr != nil {
			t.Fatalf("close unexpected root: %v", closeErr)
		}
		t.Fatal("expected untrusted symlink ancestor to be rejected")
	}
	if ancestorPath != "" || len(missingParts) != 0 {
		t.Fatalf("expected rejected ancestor walk to return no path state, got path=%q missing=%v", ancestorPath, missingParts)
	}
	if err == nil || !strings.Contains(err.Error(), "root contains symlink") {
		t.Fatalf("expected symlink rejection, got %v", err)
	}
}

func TestOpenRootExistingAncestorNoFollowAllowsTrustedAliasAncestor(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("trusted aliases are only enabled on darwin")
	}

	requestedPath := uniqueTrustedAliasMissingPath(t, "cache")
	root, ancestorPath, missingParts, err := OpenRootExistingAncestorNoFollow(requestedPath)
	if err != nil {
		t.Fatalf("OpenRootExistingAncestorNoFollow returned error: %v", err)
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Fatalf("close trusted alias ancestor root: %v", closeErr)
		}
	}()

	wantAncestorPath := filepath.Join(string(os.PathSeparator), "private", "tmp")
	if ancestorPath != wantAncestorPath {
		t.Fatalf("expected trusted alias ancestor path %q, got %q", wantAncestorPath, ancestorPath)
	}
	if len(missingParts) != 2 || missingParts[0] == "" || missingParts[1] != "cache" {
		t.Fatalf("unexpected missing parts: %v", missingParts)
	}

	openedInfo, err := root.Lstat(".")
	if err != nil {
		t.Fatalf("lstat trusted alias ancestor root: %v", err)
	}
	targetInfo, err := os.Stat(wantAncestorPath)
	if err != nil {
		t.Fatalf("stat trusted alias target: %v", err)
	}
	if !os.SameFile(openedInfo, targetInfo) {
		t.Fatalf("expected ancestor root to pin %q", wantAncestorPath)
	}
}

func TestTrustedRootAliasTargetGuards(t *testing.T) {
	untrustedTarget, ok := trustedRootAliasTarget(filepath.Join(string(os.PathSeparator), "tmp", "nested"))
	if ok || untrustedTarget != "" {
		t.Fatalf("expected nested alias path to be rejected, got target=%q ok=%v", untrustedTarget, ok)
	}

	expectedTmpTarget := filepath.Join(string(os.PathSeparator), "private", "tmp")
	tmpTarget, tmpOK := trustedRootAliasTarget(filepath.Join(string(os.PathSeparator), "tmp"))
	if runtime.GOOS == "darwin" {
		if !tmpOK || tmpTarget != expectedTmpTarget {
			t.Fatalf("expected /tmp alias target %q, got target=%q ok=%v", expectedTmpTarget, tmpTarget, tmpOK)
		}
		expectedVarTarget := filepath.Join(string(os.PathSeparator), "private", "var")
		varTarget, varOK := trustedRootAliasTarget(filepath.Join(string(os.PathSeparator), "var"))
		if !varOK || varTarget != expectedVarTarget {
			t.Fatalf("expected /var alias target %q, got target=%q ok=%v", expectedVarTarget, varTarget, varOK)
		}
		return
	}
	if tmpOK || tmpTarget != "" {
		t.Fatalf("expected trusted aliases to be disabled on %s, got target=%q ok=%v", runtime.GOOS, tmpTarget, tmpOK)
	}
}

func TestOpenRootChildPinnedBranches(t *testing.T) {
	dirInfo := statTestPath(t, t.TempDir())
	child := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return dirInfo, nil },
		close: func() error { return nil },
	}
	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return dirInfo, nil },
		openRoot: func(string) (Root, error) {
			return child, nil
		},
	}

	opened, openedPath, err := (&osFileSystem{}).openRootChildPinned(root, "child", "/root/child")
	if err != nil || opened == nil || openedPath != "/root/child" {
		t.Fatalf("expected non-symlink child to open unchanged, got root=%v path=%q err=%v", opened, openedPath, err)
	}
	if closeErr := opened.Close(); closeErr != nil {
		t.Fatalf("close opened child: %v", closeErr)
	}

	linkDir := t.TempDir()
	linkInfoPath := filepath.Join(linkDir, "link-target")
	testFile := filepath.Join(linkInfoPath, "target")
	if err := os.MkdirAll(linkInfoPath, 0o755); err != nil {
		t.Fatalf("mkdir link target: %v", err)
	}
	linkPath := filepath.Join(linkDir, "link")
	if err := os.Symlink(testFile, linkPath); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	symlinkInfo, err := os.Lstat(linkPath)
	if err != nil {
		t.Fatalf("lstat symlink: %v", err)
	}
	untrustedRoot := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return symlinkInfo, nil },
	}
	rejected, rejectedPath, err := (&osFileSystem{}).openRootChildPinned(untrustedRoot, "link", filepath.Join(string(os.PathSeparator), "repo", "link"))
	if rejected != nil || rejectedPath != "" || err == nil || !strings.Contains(err.Error(), "root contains symlink") {
		t.Fatalf("expected untrusted symlink child rejection, got root=%v path=%q err=%v", rejected, rejectedPath, err)
	}
}

func TestOpenRootChildPinnedPropagatesLookupError(t *testing.T) {
	lookupErr := errors.New("pinned child lookup failure")
	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return nil, lookupErr },
	}

	opened, openedPath, err := (&osFileSystem{}).openRootChildPinned(root, "child", "/root/child")
	if opened != nil || openedPath != "" {
		t.Fatalf("expected lookup failure to return no child root, got root=%v path=%q", opened, openedPath)
	}
	if !errors.Is(err, lookupErr) {
		t.Fatalf("expected lookup error, got %v", err)
	}
}

func TestOpenRootNoFollowWithPropagatesAbsAndVolumeOpenErrors(t *testing.T) {
	absErr := errors.New("abs path failure")
	absFn := func(string) (string, error) { return "", absErr }
	openRootFn := func(string) (Root, error) {
		t.Fatal("unexpected volume root open")
		return nil, nil
	}
	openChildFn := func(Root, string, string) (Root, string, error) {
		t.Fatal("unexpected child open")
		return nil, "", nil
	}
	root, err := openRootNoFollowWith("repo", absFn, filepath.Rel, openRootFn, openChildFn)
	if root != nil || !errors.Is(err, absErr) {
		t.Fatalf("expected abs error, got root=%v err=%v", root, err)
	}

	openErr := errors.New("open volume root")
	absFn = func(string) (string, error) { return filepath.Join(string(os.PathSeparator), "repo"), nil }
	openRootFn = func(string) (Root, error) {
		return nil, openErr
	}
	openChildFn = func(Root, string, string) (Root, string, error) {
		t.Fatal("unexpected child open after volume-root failure")
		return nil, "", nil
	}
	root, err = openRootNoFollowWith("repo", absFn, filepath.Rel, openRootFn, openChildFn)
	if root != nil || !errors.Is(err, openErr) {
		t.Fatalf("expected volume-root open error, got root=%v err=%v", root, err)
	}
}

func TestOpenRootNoFollowWithJoinsOwnedRootCloseError(t *testing.T) {
	closeErr := errors.New("close traversed root")
	childErr := errors.New("open child failure")
	root := &fakeRoot{
		close: func() error { return closeErr },
	}
	absFn := func(string) (string, error) { return filepath.Join(string(os.PathSeparator), "repo", "child"), nil }
	openRootFn := func(string) (Root, error) { return root, nil }
	openChildFn := func(Root, string, string) (Root, string, error) { return nil, "", childErr }

	opened, err := openRootNoFollowWith(filepath.Join(string(os.PathSeparator), "repo", "child"), absFn, filepath.Rel, openRootFn, openChildFn)
	if opened != nil {
		t.Fatalf("expected traversal failure to return no root, got %#v", opened)
	}
	if !errors.Is(err, childErr) || !errors.Is(err, closeErr) {
		t.Fatalf("expected joined child and close errors, got %v", err)
	}
}

func TestOpenPinnedRootAliasWithPropagatesAliasRootAndStatErrors(t *testing.T) {
	openErr := errors.New("open trusted alias")
	openRootNoFollowFn := func(string) (Root, error) { return nil, openErr }
	opened, path, err := openPinnedRootAliasWith("/private/tmp", "/tmp", openRootNoFollowFn, os.Stat, os.SameFile)
	if opened != nil || path != "" || !errors.Is(err, openErr) {
		t.Fatalf("expected trusted-alias open error, got root=%v path=%q err=%v", opened, path, err)
	}

	statErr := errors.New("stat trusted alias")
	closeErr := errors.New("close trusted alias root")
	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return statTestPath(t, t.TempDir()), nil },
		close: func() error { return closeErr },
	}
	openRootNoFollowFn = func(string) (Root, error) { return root, nil }
	statFn := func(string) (fs.FileInfo, error) {
		return nil, statErr
	}
	opened, path, err = openPinnedRootAliasWith("/private/tmp", "/tmp", openRootNoFollowFn, statFn, os.SameFile)
	if opened != nil || path != "" {
		t.Fatalf("expected stat failure to return no trusted-alias root, got root=%v path=%q", opened, path)
	}
	if !errors.Is(err, statErr) || !errors.Is(err, closeErr) {
		t.Fatalf("expected joined stat and close errors, got %v", err)
	}
}

func TestOpenPinnedRootAliasWithRejectsChangedDirectoryIdentity(t *testing.T) {
	targetInfo := statTestPath(t, t.TempDir())
	openedInfo := statTestPath(t, t.TempDir())
	closeErr := errors.New("close changed alias root")
	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return openedInfo, nil },
		close: func() error { return closeErr },
	}
	openRootNoFollowFn := func(string) (Root, error) { return root, nil }
	statFn := func(string) (fs.FileInfo, error) { return targetInfo, nil }
	sameFileFn := func(fs.FileInfo, fs.FileInfo) bool { return false }

	opened, path, err := openPinnedRootAliasWith("/private/tmp", "/tmp", openRootNoFollowFn, statFn, sameFileFn)
	if opened != nil || path != "" {
		t.Fatalf("expected changed trusted-alias root to be rejected, got root=%v path=%q", opened, path)
	}
	if err == nil || !strings.Contains(err.Error(), "root changed while opening") || !errors.Is(err, closeErr) {
		t.Fatalf("expected changed trusted-alias root error with close failure, got %v", err)
	}
}

func TestOpenRootChildPinnedAllowsTrustedAliasTarget(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("trusted aliases are only enabled on darwin")
	}

	aliasInfo, err := os.Lstat(filepath.Join(string(os.PathSeparator), "tmp"))
	if err != nil {
		t.Fatalf("lstat /tmp alias: %v", err)
	}
	if aliasInfo.Mode()&os.ModeSymlink == 0 {
		t.Skip("/tmp is not exposed as a symlink on this host")
	}

	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return aliasInfo, nil },
	}
	opened, openedPath, err := (&osFileSystem{}).openRootChildPinned(root, "tmp", filepath.Join(string(os.PathSeparator), "tmp"))
	if err != nil {
		t.Fatalf("expected trusted alias child open to succeed, got %v", err)
	}
	defer func() {
		if closeErr := opened.Close(); closeErr != nil {
			t.Fatalf("close trusted alias child: %v", closeErr)
		}
	}()

	wantPath := filepath.Join(string(os.PathSeparator), "private", "tmp")
	if openedPath != wantPath {
		t.Fatalf("expected trusted alias target %q, got %q", wantPath, openedPath)
	}
}

func TestOpenRootChildNoFollowPropagatesLookupError(t *testing.T) {
	expectedErr := errors.New("child lookup failure")
	root := &fakeRoot{lstat: func(string) (fs.FileInfo, error) {
		return nil, expectedErr
	}}

	child, err := openRootChildNoFollow(root, "child", "/root/child")
	if child != nil {
		t.Fatal("expected child root to remain nil")
	}
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected child lookup error, got %v", err)
	}
}

func TestOpenRootChildNoFollowRejectsSymlinkAndNonDirectory(t *testing.T) {
	linkDir := t.TempDir()
	targetPath := filepath.Join(linkDir, "target")
	if err := os.WriteFile(targetPath, []byte("x"), 0o600); err != nil {
		t.Fatalf("write symlink target: %v", err)
	}
	linkPath := filepath.Join(linkDir, "link")
	if err := os.Symlink(targetPath, linkPath); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	symlinkInfo, err := os.Lstat(linkPath)
	if err != nil {
		t.Fatalf("lstat symlink: %v", err)
	}
	root := &fakeRoot{lstat: func(string) (fs.FileInfo, error) { return symlinkInfo, nil }}
	if _, err := openRootChildNoFollow(root, "link", "/root/link"); err == nil || !strings.Contains(err.Error(), "root contains symlink") {
		t.Fatalf("expected symlink rejection, got %v", err)
	}

	fileInfo := statTestPath(t, targetPath)
	root = &fakeRoot{lstat: func(string) (fs.FileInfo, error) { return fileInfo, nil }}
	if _, err := openRootChildNoFollow(root, "file", "/root/file"); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("expected non-directory rejection, got %v", err)
	}
}

func TestOpenRootChildNoFollowPropagatesOpenError(t *testing.T) {
	dirInfo := statTestPath(t, t.TempDir())
	expectedErr := errors.New("child open failure")
	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return dirInfo, nil },
		openRoot: func(string) (Root, error) {
			return nil, expectedErr
		},
	}

	child, err := openRootChildNoFollow(root, "child", "/root/child")
	if child != nil {
		t.Fatal("expected child root to remain nil")
	}
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected child open error, got %v", err)
	}
}

func TestOpenRootChildNoFollowJoinsChildLookupAndCloseErrors(t *testing.T) {
	dirInfo := statTestPath(t, t.TempDir())
	lookupErr := errors.New("opened child lookup failure")
	closeErr := errors.New("opened child close failure")
	childClosed := false
	child := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return nil, lookupErr },
		close: func() error {
			childClosed = true
			return closeErr
		},
	}
	root := &fakeRoot{
		lstat:    func(string) (fs.FileInfo, error) { return dirInfo, nil },
		openRoot: func(string) (Root, error) { return child, nil },
	}

	opened, err := openRootChildNoFollow(root, "child", "/root/child")
	if opened != nil {
		t.Fatal("expected rejected child root to remain nil")
	}
	if !errors.Is(err, lookupErr) || !errors.Is(err, closeErr) {
		t.Fatalf("expected joined lookup and close errors, got %v", err)
	}
	if !childClosed {
		t.Fatal("expected rejected child root to be closed")
	}
}

func TestOpenRootChildNoFollowRejectsChangedDirectory(t *testing.T) {
	root, childClosed := changedDirectoryTestRoot(t)

	opened, err := openRootChildNoFollow(root, "child", "/root/child")
	if opened != nil {
		t.Fatal("expected changed child root to remain nil")
	}
	if err == nil || !strings.Contains(err.Error(), "root changed while opening") {
		t.Fatalf("expected changed-directory error, got %v", err)
	}
	if !*childClosed {
		t.Fatal("expected changed child root to be closed")
	}
}

func TestOSRootOpenRootReturnsMissingDirectoryError(t *testing.T) {
	root := openTestRoot(t, t.TempDir())
	child, err := root.OpenRoot("missing")
	if child != nil {
		if closeErr := child.Close(); closeErr != nil {
			t.Fatalf("close unexpected child root: %v", closeErr)
		}
		t.Fatal("expected missing child root to remain nil")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected missing child root error, got %v", err)
	}
}

func TestWriteRootPropagatesParentLookupError(t *testing.T) {
	assertWriteRootParentLookupError(t, func(root *WriteRoot) error {
		return root.WriteFileCreatingParents(filepath.Join("reports", writeTestFileName), []byte("hello"), 0o600, 0o750)
	})
}

func TestWriteRootIfAbsentPropagatesParentLookupError(t *testing.T) {
	assertWriteRootParentLookupError(t, func(root *WriteRoot) error {
		return root.WriteFileCreatingParentsIfAbsent(filepath.Join("reports", writeTestFileName), []byte("hello"), 0o600, 0o750)
	})
}

func TestWriteFileIfAbsentAtRootPropagatesUnexpectedLookupError(t *testing.T) {
	expectedErr := errors.New("lookup failure")
	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return nil, expectedErr },
	}

	err := writeFileIfAbsentAtRoot(root, rootedTarget{rel: writeTestFileName}, []byte("hello"), 0o600)
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected lookup error, got %v", err)
	}
}

func TestWriteFileIfAbsentAtRootPropagatesCreateTempError(t *testing.T) {
	expectedErr := errors.New("create temp failure")
	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return nil, os.ErrNotExist },
		openFile: func(string, int, os.FileMode) (File, error) {
			return nil, expectedErr
		},
	}

	err := writeFileIfAbsentAtRoot(root, rootedTarget{rel: writeTestFileName}, []byte("hello"), 0o600)
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected create temp error, got %v", err)
	}
}

func TestWriteFileIfAbsentAtRootPropagatesLinkError(t *testing.T) {
	expectedErr := errors.New("link failure")
	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return nil, os.ErrNotExist },
		openFile: func(string, int, os.FileMode) (File, error) {
			return &fakeFile{
				write: func(p []byte) (int, error) { return len(p), nil },
				chmod: func(os.FileMode) error { return nil },
				close: func() error { return nil },
			}, nil
		},
		link:   func(string, string) error { return expectedErr },
		remove: func(string) error { return nil },
	}

	err := writeFileIfAbsentAtRoot(root, rootedTarget{rel: writeTestFileName}, []byte("hello"), 0o600)
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected link error, got %v", err)
	}
}

func TestWriteRootRejectsNonRelativeTargets(t *testing.T) {
	rootDir := t.TempDir()
	root, err := OpenWriteRoot(rootDir)
	if err != nil {
		t.Fatalf("OpenWriteRoot returned error: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := root.Close(); closeErr != nil && !errors.Is(closeErr, os.ErrClosed) {
			t.Errorf("close write root: %v", closeErr)
		}
	})

	for _, targetPath := range []string{rootDir, "..", filepath.Join("..", writeTestFileName), "."} {
		err := root.WriteFileCreatingParents(targetPath, []byte("hello"), 0o600, 0o750)
		if err == nil {
			t.Fatalf("expected target %q to be rejected", targetPath)
		}
	}
}

func TestWriteRootRejectsSymlinkedParent(t *testing.T) {
	rootDir := t.TempDir()
	outside := t.TempDir()
	outsideTarget := filepath.Join(outside, writeTestFileName)
	if err := os.WriteFile(outsideTarget, []byte("outside-before"), 0o600); err != nil {
		t.Fatalf("seed outside target: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(rootDir, "reports")); err != nil {
		t.Fatalf("create reports symlink: %v", err)
	}
	root := openTestWriteRoot(t, rootDir, OpenWriteRoot)
	assertWriteRootRejectsParent(t, root, "output parent contains symlink", "expected symlinked parent rejection")
	data, readErr := os.ReadFile(outsideTarget)
	if readErr != nil {
		t.Fatalf("read outside target: %v", readErr)
	}
	if string(data) != "outside-before" {
		t.Fatalf("unexpected outside content: %q", string(data))
	}
}

func TestWriteRootRejectsNonDirectoryParent(t *testing.T) {
	rootDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(rootDir, "reports"), []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("write non-directory parent: %v", err)
	}
	root := openTestWriteRoot(t, rootDir, OpenWriteRoot)
	assertWriteRootRejectsParent(t, root, "output parent is not a directory", "expected non-directory parent rejection")
}

func TestOpenTargetParentChildPropagatesOpenError(t *testing.T) {
	dirInfo := statTestPath(t, t.TempDir())
	expectedErr := errors.New("parent open failure")
	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return dirInfo, nil },
		openRoot: func(string) (Root, error) {
			return nil, expectedErr
		},
	}

	child, err := openTargetParentChild(root, "parent", "/root/parent", false, 0)
	if child != nil {
		t.Fatal("expected parent root to remain nil")
	}
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected parent open error, got %v", err)
	}
}

func TestOpenTargetParentChildClosesChildOnLookupError(t *testing.T) {
	dirInfo := statTestPath(t, t.TempDir())
	expectedErr := errors.New("opened parent lookup failure")
	childClosed := false
	child := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return nil, expectedErr },
		close: func() error {
			childClosed = true
			return nil
		},
	}
	root := &fakeRoot{
		lstat:    func(string) (fs.FileInfo, error) { return dirInfo, nil },
		openRoot: func(string) (Root, error) { return child, nil },
	}

	opened, err := openTargetParentChild(root, "parent", "/root/parent", false, 0)
	if opened != nil {
		t.Fatal("expected rejected parent root to remain nil")
	}
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected opened parent lookup error, got %v", err)
	}
	if !childClosed {
		t.Fatal("expected rejected parent root to be closed")
	}
}

func TestOpenTargetParentChildRejectsChangedDirectory(t *testing.T) {
	root, childClosed := changedDirectoryTestRoot(t)

	opened, err := openTargetParentChild(root, "parent", "/root/parent", false, 0)
	if opened != nil {
		t.Fatal("expected changed parent root to remain nil")
	}
	if err == nil || !strings.Contains(err.Error(), "output parent changed while opening") {
		t.Fatalf("expected changed parent error, got %v", err)
	}
	if !*childClosed {
		t.Fatal("expected changed parent root to be closed")
	}
}

func TestLstatOrCreateDirectoryPropagatesMkdirError(t *testing.T) {
	expectedErr := errors.New("mkdir parent failure")
	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return nil, os.ErrNotExist },
		mkdir: func(string, os.FileMode) error {
			return expectedErr
		},
	}

	info, err := lstatOrCreateDirectory(root, "parent", true, 0o750)
	if info != nil {
		t.Fatal("expected directory info to remain nil")
	}
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected mkdir error, got %v", err)
	}
}

func TestOpenTargetParentClosesOwnedParentAfterDescendantError(t *testing.T) {
	dirInfo := statTestPath(t, t.TempDir())
	expectedErr := errors.New("descendant lookup failure")
	ownedClosed := false
	owned := &fakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			if name == "." {
				return dirInfo, nil
			}
			return nil, expectedErr
		},
		close: func() error {
			ownedClosed = true
			return nil
		},
	}
	root := &fakeRoot{
		lstat:    func(string) (fs.FileInfo, error) { return dirInfo, nil },
		openRoot: func(string) (Root, error) { return owned, nil },
	}
	writeRoot := &WriteRoot{root: root, rootAbs: "/root"}
	target := rootedTarget{rootAbs: "/root", rel: filepath.Join("first", "second", writeTestFileName)}

	parent, closeParent, err := writeRoot.openTargetParent(target, false, 0)
	if parent != nil || closeParent {
		t.Fatal("expected descendant failure to return no parent root")
	}
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected descendant lookup error, got %v", err)
	}
	if !ownedClosed {
		t.Fatal("expected owned parent root to be closed")
	}
}

func TestOpenTargetParentReturnsExistingRootForTopLevelTarget(t *testing.T) {
	root := &fakeRoot{}
	writeRoot := &WriteRoot{root: root, rootAbs: "/root"}
	target := rootedTarget{rootAbs: "/root", rel: writeTestFileName}

	parent, closeParent, err := writeRoot.openTargetParent(target, false, 0)
	if err != nil {
		t.Fatalf("expected top-level target parent lookup to succeed, got %v", err)
	}
	if parent != root {
		t.Fatalf("expected top-level target to reuse the write root, got %#v", parent)
	}
	if closeParent {
		t.Fatal("expected top-level target parent to remain caller-owned")
	}
}

func TestOpenTargetParentClosesNextWhenOwnedParentCloseFails(t *testing.T) {
	firstInfo := statTestPath(t, t.TempDir())
	secondInfo := statTestPath(t, t.TempDir())
	closeErr := errors.New("owned parent close failure")
	nextCloseErr := errors.New("next parent close failure")
	nextClosed := false
	next := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return secondInfo, nil },
		close: func() error {
			nextClosed = true
			return nextCloseErr
		},
	}
	owned := &fakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			if name == "." {
				return firstInfo, nil
			}
			return secondInfo, nil
		},
		openRoot: func(string) (Root, error) { return next, nil },
		close:    func() error { return closeErr },
	}
	root := &fakeRoot{
		lstat:    func(string) (fs.FileInfo, error) { return firstInfo, nil },
		openRoot: func(string) (Root, error) { return owned, nil },
	}
	writeRoot := &WriteRoot{root: root, rootAbs: "/root"}
	target := rootedTarget{rootAbs: "/root", rel: filepath.Join("first", "second", writeTestFileName)}

	parent, closeParent, err := writeRoot.openTargetParent(target, false, 0)
	if parent != nil || closeParent {
		t.Fatal("expected close failure to return no parent root")
	}
	if !errors.Is(err, closeErr) || !errors.Is(err, nextCloseErr) {
		t.Fatalf("expected joined parent close errors, got %v", err)
	}
	if !nextClosed {
		t.Fatal("expected next parent root to be closed")
	}
}

func TestWriteFileAtTargetJoinsReadyAndParentCloseErrors(t *testing.T) {
	dirInfo := statTestPath(t, t.TempDir())
	readyErr := errors.New("parent ready failure")
	closeErr := errors.New("parent close failure")
	parentClosed := false
	parent := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return dirInfo, nil },
		close: func() error {
			parentClosed = true
			return closeErr
		},
	}
	root := &fakeRoot{
		lstat:    func(string) (fs.FileInfo, error) { return dirInfo, nil },
		openRoot: func(string) (Root, error) { return parent, nil },
	}
	writeRoot := &WriteRoot{root: root, rootAbs: "/root"}
	target := rootedTarget{rootAbs: "/root", rel: filepath.Join("parent", writeTestFileName)}
	originalReady := writeFileParentReadyFn
	writeFileParentReadyFn = func() error { return readyErr }
	t.Cleanup(func() {
		writeFileParentReadyFn = originalReady
	})

	err := writeRoot.writeFileAtTarget(target, []byte("data"), 0o600, false, 0)
	if !errors.Is(err, readyErr) || !errors.Is(err, closeErr) {
		t.Fatalf("expected joined ready and close errors, got %v", err)
	}
	if !parentClosed {
		t.Fatal("expected pinned parent root to be closed")
	}
}

func TestWriteFileAtRootReturnsExistingTargetCloseError(t *testing.T) {
	targetPath := filepath.Join(t.TempDir(), writeTestFileName)
	if err := os.WriteFile(targetPath, []byte("before"), 0o600); err != nil {
		t.Fatalf("seed existing target: %v", err)
	}
	fileInfo := statTestPath(t, targetPath)
	expectedErr := errors.New("existing target close failure")
	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return fileInfo, nil },
		openFile: func(string, int, os.FileMode) (File, error) {
			return &fakeFile{close: func() error { return expectedErr }}, nil
		},
	}
	target := rootedTarget{rel: writeTestFileName, abs: targetPath}

	err := writeFileAtRoot(root, target, []byte("after"), 0o600)
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected existing target close error, got %v", err)
	}
}

func TestWriteFileAtRootReturnsAtomicSessionCreationError(t *testing.T) {
	expectedErr := errors.New("atomic temp creation failure")
	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return nil, os.ErrNotExist },
		openFile: func(string, int, os.FileMode) (File, error) {
			return nil, expectedErr
		},
	}
	target := rootedTarget{rel: writeTestFileName, abs: filepath.Join(t.TempDir(), writeTestFileName)}

	err := writeFileAtRoot(root, target, []byte("data"), 0o600)
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected atomic session creation error, got %v", err)
	}
}

func TestResolvedWriteFilePermPropagatesLookupError(t *testing.T) {
	expectedErr := errors.New("target lookup failure")
	root := &fakeRoot{lstat: func(string) (fs.FileInfo, error) {
		return nil, expectedErr
	}}
	target := rootedTarget{rel: writeTestFileName, abs: filepath.Join(t.TempDir(), writeTestFileName)}

	perm, existing, err := resolvedWriteFilePerm(root, target, 0o600)
	if perm != 0 || existing != nil {
		t.Fatalf("expected empty permission result, got perm=%#o existing=%v", perm, existing)
	}
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected target lookup error, got %v", err)
	}
}

func TestWriteRootDoesNotCreateOutsideAfterMissingParentSwap(t *testing.T) {
	rootDir := t.TempDir()
	outside := t.TempDir()
	outsideSentinel := filepath.Join(outside, "sentinel.txt")
	if err := os.WriteFile(outsideSentinel, []byte("outside-before"), 0o600); err != nil {
		t.Fatalf("seed outside sentinel: %v", err)
	}

	withFileSystem(t, &fakeFileSystem{openRoot: func(name string) (Root, error) {
		root, err := (&osFileSystem{}).OpenRoot(name)
		if err != nil {
			return nil, err
		}
		return &fakeRoot{
			Root: root,
			mkdir: func(path string, perm os.FileMode) error {
				if err := os.Symlink(outside, filepath.Join(rootDir, "reports")); err != nil {
					return err
				}
				return root.Mkdir(path, perm)
			},
		}, nil
	}})

	root, err := OpenWriteRoot(rootDir)
	if err != nil {
		t.Fatalf("OpenWriteRoot returned error: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := root.Close(); closeErr != nil && !errors.Is(closeErr, os.ErrClosed) {
			t.Errorf("close write root: %v", closeErr)
		}
	})

	err = root.WriteFileCreatingParents(filepath.Join("reports", "nested", writeTestFileName), []byte("after"), 0o600, 0o750)
	if err == nil {
		t.Fatal("expected swapped parent symlink to be rejected")
	}
	if _, statErr := os.Stat(filepath.Join(outside, "nested")); !os.IsNotExist(statErr) {
		t.Fatalf("expected outside nested directory to remain absent, got err=%v", statErr)
	}
	data, readErr := os.ReadFile(outsideSentinel)
	if readErr != nil {
		t.Fatalf("read outside sentinel: %v", readErr)
	}
	if string(data) != "outside-before" {
		t.Fatalf("unexpected outside sentinel: %q", string(data))
	}
}

func TestWriteRootPinsParentBeforeInRootSymlinkRetarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory replacement semantics are covered on Unix")
	}

	rootDir := t.TempDir()
	originalParent := filepath.Join(rootDir, "reports")
	relocatedParent := filepath.Join(rootDir, "reports-relocated")
	redirectedParent := filepath.Join(rootDir, "redirected")
	if err := os.MkdirAll(originalParent, 0o755); err != nil {
		t.Fatalf("mkdir original parent: %v", err)
	}
	if err := os.MkdirAll(redirectedParent, 0o755); err != nil {
		t.Fatalf("mkdir redirected parent: %v", err)
	}
	originalTarget := filepath.Join(originalParent, writeTestFileName)
	redirectedTarget := filepath.Join(redirectedParent, writeTestFileName)
	if err := os.WriteFile(originalTarget, []byte("original-before"), 0o600); err != nil {
		t.Fatalf("seed original target: %v", err)
	}
	if err := os.WriteFile(redirectedTarget, []byte("redirected-before"), 0o600); err != nil {
		t.Fatalf("seed redirected target: %v", err)
	}

	originalReady := writeFileParentReadyFn
	writeFileParentReadyFn = func() error {
		if err := os.Rename(originalParent, relocatedParent); err != nil {
			return err
		}
		return os.Symlink(filepath.Base(redirectedParent), originalParent)
	}
	t.Cleanup(func() {
		writeFileParentReadyFn = originalReady
	})

	root, err := OpenWriteRoot(rootDir)
	if err != nil {
		t.Fatalf("OpenWriteRoot returned error: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := root.Close(); closeErr != nil && !errors.Is(closeErr, os.ErrClosed) {
			t.Errorf("close write root: %v", closeErr)
		}
	})

	err = root.WriteFileCreatingParents(filepath.Join("reports", writeTestFileName), []byte("after"), 0o600, 0o750)
	if err != nil {
		t.Fatalf("WriteFileCreatingParents returned error: %v", err)
	}
	assertFileContent(t, filepath.Join(relocatedParent, writeTestFileName), "after")
	assertFileContent(t, redirectedTarget, "redirected-before")
}

func TestWriteFileUnderRejectsRootPathTarget(t *testing.T) {
	rootDir := t.TempDir()
	err := WriteFileUnder(rootDir, rootDir, []byte("hello"), 0o600)
	if err == nil {
		t.Fatal("expected root directory target error")
	}
}

func TestWriteFileUnderRejectsExistingDirectoryTarget(t *testing.T) {
	rootDir := t.TempDir()
	targetPath := filepath.Join(rootDir, "existing")
	if err := os.MkdirAll(targetPath, 0o755); err != nil {
		t.Fatalf("create directory target: %v", err)
	}

	err := WriteFileUnder(rootDir, targetPath, []byte("hello"), 0o600)
	if err == nil {
		t.Fatal("expected directory target error")
	}
}

func TestWriteFileUnderRejectsSymlinkTarget(t *testing.T) {
	rootDir := t.TempDir()
	outsidePath := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outsidePath, []byte("secret"), 0o600); err != nil {
		t.Fatalf("seed outside file: %v", err)
	}
	targetPath := filepath.Join(rootDir, writeTestFileName)
	if err := os.Symlink(outsidePath, targetPath); err != nil {
		t.Fatalf("create target symlink: %v", err)
	}

	err := WriteFileUnder(rootDir, targetPath, []byte("hello"), 0o600)
	if err == nil {
		t.Fatal("expected symlink target error")
	}
	data, readErr := os.ReadFile(outsidePath)
	if readErr != nil {
		t.Fatalf("read outside file: %v", readErr)
	}
	if string(data) != "secret" {
		t.Fatalf("expected outside file to remain unchanged, got %q", string(data))
	}
	info, statErr := os.Lstat(targetPath)
	if statErr != nil {
		t.Fatalf("lstat target symlink: %v", statErr)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("expected target path to remain a symlink, got mode %v", info.Mode())
	}
}

func TestCreateAtomicTempFileInRootDir(t *testing.T) {
	rootDir := t.TempDir()
	root := openTestRoot(t, rootDir)

	tempRel, tempFile, err := createAtomicTempFile(root, ".", 0o600)
	if err != nil {
		t.Fatalf("createAtomicTempFile returned error: %v", err)
	}
	if strings.Contains(tempRel, string(os.PathSeparator)) {
		t.Fatalf("expected root-relative temp file name, got %q", tempRel)
	}
	if err := tempFile.Close(); err != nil {
		t.Fatalf(closeTempFileErrFmt, err)
	}
	if err := root.Remove(tempRel); err != nil {
		t.Fatalf("remove temp file: %v", err)
	}
}

func TestCreateAtomicTempFileReturnsErrorForMissingDir(t *testing.T) {
	rootDir := t.TempDir()
	root := openTestRoot(t, rootDir)

	_, tempFile, err := createAtomicTempFile(root, "missing", 0o600)
	if tempFile != nil {
		if closeErr := tempFile.Close(); closeErr != nil {
			t.Fatalf(closeTempFileErrFmt, closeErr)
		}
	}
	if err == nil {
		t.Fatal("expected missing-dir temp file error")
	}
}

func TestCreateAtomicTempFilePropagatesRandomNameError(t *testing.T) {
	rootDir := t.TempDir()
	root := openTestRoot(t, rootDir)

	originalRandomTempNameFn := randomTempNameFn
	randomTempNameFn = func() (string, error) { return "", errors.New("boom") }
	defer func() {
		randomTempNameFn = originalRandomTempNameFn
	}()

	_, _, err := createAtomicTempFile(root, ".", 0o600)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected random temp name error, got %v", err)
	}
}

func TestCreateAtomicTempFileFailsAfterRepeatedCollisions(t *testing.T) {
	rootDir := t.TempDir()

	seedFile, err := os.OpenFile(filepath.Join(rootDir, "fixed"), os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatalf("create colliding temp file: %v", err)
	}
	if _, err := seedFile.Write([]byte("x")); err != nil {
		t.Fatalf("seed colliding temp file: %v", err)
	}
	if err := seedFile.Close(); err != nil {
		t.Fatalf("close colliding temp file: %v", err)
	}

	root := openTestRoot(t, rootDir)

	originalRandomTempNameFn := randomTempNameFn
	randomTempNameFn = func() (string, error) { return "fixed", nil }
	defer func() {
		randomTempNameFn = originalRandomTempNameFn
	}()

	_, _, err = createAtomicTempFile(root, ".", 0o600)
	if err == nil || !strings.Contains(err.Error(), "too many collisions") {
		t.Fatalf("expected collision exhaustion error, got %v", err)
	}
}

func TestCleanupAtomicTempFileIgnoresClosedFileAndMissingTempPath(t *testing.T) {
	rootDir := t.TempDir()
	root := openTestRoot(t, rootDir)

	tempFile, err := root.OpenFile("temp", os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	if err := tempFile.Close(); err != nil {
		t.Fatalf(closeTempFileErrFmt, err)
	}
	if err := root.Remove("temp"); err != nil {
		t.Fatalf("remove temp file: %v", err)
	}

	if err := cleanupAtomicTempFile(root, "temp", tempFile); err != nil {
		t.Fatalf("expected cleanupAtomicTempFile to ignore benign cleanup errors, got %v", err)
	}
}

func TestCleanupAtomicTempFileReturnsRootRemoveError(t *testing.T) {
	rootDir := t.TempDir()
	root := openTestRoot(t, rootDir)

	tempFile, err := root.OpenFile("temp", os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	if err := root.Close(); err != nil {
		t.Fatalf(closeRootErrFmt, err)
	}

	err = cleanupAtomicTempFile(root, "temp", tempFile)
	if err == nil {
		t.Fatal("expected root remove error after closing root")
	}
}

func TestCleanupAtomicTempFileJoinsCloseAndRemoveErrors(t *testing.T) {
	rootDir := t.TempDir()
	root := openTestRoot(t, rootDir)
	if err := root.Close(); err != nil {
		t.Fatalf("close root: %v", err)
	}

	err := cleanupAtomicTempFile(root, "temp", &os.File{})
	if err == nil {
		t.Fatal("expected cleanupAtomicTempFile to join close and remove errors")
	}
	var closeErrno syscall.Errno
	if !errors.Is(err, fs.ErrInvalid) && !errors.As(err, &closeErrno) {
		t.Fatalf("expected joined cleanup error to include a stable close failure, got %v", err)
	}
	if !errors.Is(err, os.ErrClosed) {
		t.Fatalf("expected joined cleanup error to include closed root remove, got %v", err)
	}
}

func TestAtomicWriteSessionCloseTempFileNoopWhenAlreadyClosed(t *testing.T) {
	session := &atomicWriteSession{}

	if err := session.closeTempFile(); err != nil {
		t.Fatalf("expected nil closeTempFile error, got %v", err)
	}
}

func TestRandomTempName(t *testing.T) {
	name, err := randomTempName()
	if err != nil {
		t.Fatalf("randomTempName returned error: %v", err)
	}
	if !strings.HasPrefix(name, atomicTempPrefix) {
		t.Fatalf("expected temp name prefix %q, got %q", atomicTempPrefix, name)
	}
	if len(name) <= len(atomicTempPrefix) {
		t.Fatalf("expected random suffix in temp name, got %q", name)
	}
}

func TestRandomTempNamePropagatesReadError(t *testing.T) {
	originalRandReadFn := randReadFn
	randReadFn = func([]byte) (int, error) { return 0, errors.New("boom") }
	defer func() {
		randReadFn = originalRandReadFn
	}()

	_, err := randomTempName()
	if err == nil || !strings.Contains(err.Error(), "generate temp name") {
		t.Fatalf("expected random read error, got %v", err)
	}
}

func withAtomicWriteFileSystem(t *testing.T, tempFile File, remove func(string) error) {
	t.Helper()
	if remove == nil {
		remove = func(string) error {
			return nil
		}
	}
	withFileSystem(t, &fakeFileSystem{openRoot: func(string) (Root, error) {
		return &fakeRoot{
			lstat: func(string) (fs.FileInfo, error) {
				return nil, os.ErrNotExist
			},
			openFile: func(string, int, os.FileMode) (File, error) {
				return tempFile, nil
			},
			remove: remove,
			close: func() error {
				return nil
			},
		}, nil
	}})
}

func TestWriteFileUnderCloseRootError(t *testing.T) {
	rootDir := t.TempDir()
	targetPath := filepath.Join(rootDir, writeTestFileName)

	expectedErr := errors.New("close root failure")
	withRootCloseError(t, expectedErr)

	err := WriteFileUnder(rootDir, targetPath, []byte("hello"), 0o600)
	if err == nil {
		t.Fatal("expected root close failure to be returned")
	}
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected root close error, got %v", err)
	}
}

func TestWriteFileUnderJoinsPrimaryErrorWithCleanupFailure(t *testing.T) {
	rootDir := t.TempDir()
	targetPath := filepath.Join(rootDir, writeTestFileName)
	writeErr := errors.New("write failure")
	cleanupErr := errors.New("cleanup failure")
	tempFile := &fakeFile{
		write: func([]byte) (int, error) {
			return 0, writeErr
		},
		close: func() error {
			return nil
		},
	}
	remove := func(string) error {
		return cleanupErr
	}
	withAtomicWriteFileSystem(t, tempFile, remove)

	err := WriteFileUnder(rootDir, targetPath, []byte("hello"), 0o600)
	if err == nil {
		t.Fatal("expected write error")
	}
	if !errors.Is(err, writeErr) {
		t.Fatalf("expected write error, got %v", err)
	}
	if !errors.Is(err, cleanupErr) {
		t.Fatalf("expected cleanup error to be joined, got %v", err)
	}
}

func TestWriteFileReplacingWithinRootJoinsRenameErrorWithCleanupFailure(t *testing.T) {
	renameErr := errors.New("rename failure")
	cleanupErr := errors.New("cleanup failure")
	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) {
			return nil, os.ErrNotExist
		},
		openFile: func(name string, flag int, perm os.FileMode) (File, error) {
			return &fakeFile{
				write: func(p []byte) (int, error) { return len(p), nil },
				chmod: chmodWithoutError,
				close: closeWithoutError,
			}, nil
		},
		rename: func(string, string) error {
			return renameErr
		},
		remove: func(string) error {
			return cleanupErr
		},
		close: closeWithoutError,
	}

	err := WriteFileReplacingWithinRoot(root, writeTestFileName, []byte("hello"), 0o600)
	if err == nil {
		t.Fatal("expected rename error")
	}
	if !errors.Is(err, renameErr) {
		t.Fatalf("expected rename error, got %v", err)
	}
	if !errors.Is(err, cleanupErr) {
		t.Fatalf("expected cleanup error to be joined, got %v", err)
	}
}

func TestWriteFileReplacingWithinRootReturnsRenameErrorWithoutFallback(t *testing.T) {
	renameErr := errors.New("rename failure")
	targetInfoPath := filepath.Join(t.TempDir(), writeTestFileName)
	if err := os.WriteFile(targetInfoPath, []byte("before"), 0o640); err != nil {
		t.Fatalf("seed target info path: %v", err)
	}
	info := statTestPath(t, targetInfoPath)

	targetOpened := false
	root := &fakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			if name != writeTestFileName {
				return nil, os.ErrNotExist
			}
			return info, nil
		},
		openFile: func(name string, flag int, perm os.FileMode) (File, error) {
			if name == writeTestFileName {
				targetOpened = true
				return &fakeFile{
					stat:  func() (fs.FileInfo, error) { return info, nil },
					close: closeWithoutError,
				}, nil
			}
			return &fakeFile{
				write: func(p []byte) (int, error) { return len(p), nil },
				chmod: chmodWithoutError,
				close: closeWithoutError,
			}, nil
		},
		rename: func(string, string) error {
			return renameErr
		},
		remove: func(string) error { return nil },
	}

	err := WriteFileReplacingWithinRoot(root, writeTestFileName, []byte("hello"), 0o600)
	if !errors.Is(err, renameErr) {
		t.Fatalf("expected rename error without fallback, got %v", err)
	}
	if runtime.GOOS == "windows" && !targetOpened {
		t.Fatal("expected existing target writability probe on Windows")
	}
	if runtime.GOOS != "windows" && targetOpened {
		t.Fatal("expected non-Windows rename failure to skip pinned target probe")
	}
}

func TestWriteAtomicReplacementReturnsPinnedTargetCloseErrorAfterCommit(t *testing.T) {
	targetInfoPath := filepath.Join(t.TempDir(), writeTestFileName)
	if err := os.WriteFile(targetInfoPath, []byte("before"), 0o640); err != nil {
		t.Fatalf("seed target info path: %v", err)
	}
	info := statTestPath(t, targetInfoPath)
	closeErr := errors.New("pinned target close failure")

	root := &fakeRoot{
		openFile: func(name string, flag int, perm os.FileMode) (File, error) {
			if name == writeTestFileName {
				return &fakeFile{
					stat:  func() (fs.FileInfo, error) { return info, nil },
					close: func() error { return closeErr },
				}, nil
			}
			return &fakeFile{
				write: func(p []byte) (int, error) { return len(p), nil },
				chmod: chmodWithoutError,
				close: closeWithoutError,
			}, nil
		},
		rename: func(string, string) error { return nil },
		remove: func(string) error { return nil },
	}

	err := writeAtomicReplacement(root, writeTestFileName, []byte("after"), 0o600, info)
	if runtime.GOOS == "windows" {
		if !errors.Is(err, closeErr) {
			t.Fatalf("expected pinned target close error, got %v", err)
		}
		return
	}
	if err != nil {
		t.Fatalf("expected non-Windows commit to succeed without pinned target close, got %v", err)
	}
}

func TestWriteAtomicReplacementReturnsPinnedTargetOpenError(t *testing.T) {
	expectedErr := errors.New("open pinned target failure")
	root := &fakeRoot{
		openFile: func(name string, flag int, perm os.FileMode) (File, error) {
			if name == writeTestFileName {
				return nil, expectedErr
			}
			return &fakeFile{
				write: func(p []byte) (int, error) { return len(p), nil },
				chmod: chmodWithoutError,
				close: closeWithoutError,
			}, nil
		},
		rename: func(string, string) error { return nil },
		remove: func(string) error { return nil },
	}

	err := writeAtomicReplacement(root, writeTestFileName, []byte("after"), 0o600, statTestPath(t, t.TempDir()))
	if runtime.GOOS == "windows" {
		if !errors.Is(err, expectedErr) {
			t.Fatalf("expected pinned target open error, got %v", err)
		}
		return
	}
	if err != nil {
		t.Fatalf("expected non-Windows write to skip pinned target open, got %v", err)
	}
}

func TestOpenPinnedReplacementTargetReturnsOpenError(t *testing.T) {
	expectedErr := errors.New("open target failure")
	root := &fakeRoot{
		openFile: func(string, int, os.FileMode) (File, error) {
			return nil, expectedErr
		},
	}

	file, err := openPinnedReplacementTarget(root, writeTestFileName, statTestPath(t, t.TempDir()))
	if file != nil {
		t.Fatal("expected pinned target file to remain nil")
	}
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected open target error, got %v", err)
	}
}

func TestOpenPinnedReplacementTargetReturnsOpenedFile(t *testing.T) {
	targetPath := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(targetPath, []byte("target"), 0o600); err != nil {
		t.Fatalf("seed pinned target: %v", err)
	}
	expectedInfo := statTestPath(t, targetPath)
	root := &fakeRoot{
		openFile: func(string, int, os.FileMode) (File, error) {
			return &fakeFile{
				stat:  func() (fs.FileInfo, error) { return expectedInfo, nil },
				close: closeWithoutError,
			}, nil
		},
	}

	file, err := openPinnedReplacementTarget(root, writeTestFileName, expectedInfo)
	if err != nil {
		t.Fatalf("expected pinned target open success, got %v", err)
	}
	if file == nil {
		t.Fatal("expected opened pinned target file")
	}
	if closeErr := file.Close(); closeErr != nil {
		t.Fatalf("close opened pinned target: %v", closeErr)
	}
}

func TestOpenPinnedReplacementTargetKeepsStatErrorWhenCloseAlsoFails(t *testing.T) {
	statErr := errors.New("stat target failure")
	closeErr := errors.New("close target failure")
	root := &fakeRoot{
		openFile: func(string, int, os.FileMode) (File, error) {
			return &fakeFile{
				stat:  func() (fs.FileInfo, error) { return nil, statErr },
				close: func() error { return closeErr },
			}, nil
		},
	}

	file, err := openPinnedReplacementTarget(root, writeTestFileName, statTestPath(t, t.TempDir()))
	if file != nil {
		t.Fatal("expected pinned target file to remain nil")
	}
	if !errors.Is(err, statErr) {
		t.Fatalf("expected stat error, got %v", err)
	}
	if errors.Is(err, closeErr) {
		t.Fatalf("expected close error to remain secondary, got %v", err)
	}
}

func TestOpenPinnedReplacementTargetRejectsChangedTarget(t *testing.T) {
	dir := t.TempDir()
	originalPath := filepath.Join(dir, "original")
	changedPath := filepath.Join(dir, "changed")
	if err := os.WriteFile(originalPath, []byte("original"), 0o640); err != nil {
		t.Fatalf("seed original target: %v", err)
	}
	if err := os.WriteFile(changedPath, []byte("changed"), 0o640); err != nil {
		t.Fatalf("seed changed target: %v", err)
	}
	originalInfo := statTestPath(t, originalPath)
	changedInfo := statTestPath(t, changedPath)

	tests := []struct {
		name       string
		openedInfo fs.FileInfo
	}{
		{name: "non-regular", openedInfo: &modeOverrideFileInfo{FileInfo: changedInfo, mode: os.ModeDir | 0o755}},
		{name: "different identity", openedInfo: changedInfo},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			closeCalls := 0
			root := &fakeRoot{
				openFile: func(string, int, os.FileMode) (File, error) {
					return &fakeFile{
						stat: func() (fs.FileInfo, error) { return tt.openedInfo, nil },
						close: func() error {
							closeCalls++
							return nil
						},
					}, nil
				},
			}

			file, err := openPinnedReplacementTarget(root, writeTestFileName, originalInfo)
			if file != nil {
				t.Fatal("expected pinned target file to remain nil")
			}
			if err == nil || !strings.Contains(err.Error(), "target changed while opening for replacement") {
				t.Fatalf("expected target change rejection, got %v", err)
			}
			if closeCalls != 1 {
				t.Fatalf("expected rejected pinned target to close once, got %d closes", closeCalls)
			}
		})
	}
}

func TestOverwritePinnedFileWritesAfterIdentityRevalidation(t *testing.T) {
	targetInfoPath := filepath.Join(t.TempDir(), writeTestFileName)
	if err := os.WriteFile(targetInfoPath, []byte("before"), 0o640); err != nil {
		t.Fatalf("seed target info path: %v", err)
	}
	info := statTestPath(t, targetInfoPath)

	data := "before"
	target := &truncatingFakeFile{
		fakeFile: &fakeFile{
			stat: func() (fs.FileInfo, error) { return info, nil },
			write: func(p []byte) (int, error) {
				data += string(p)
				return len(p), nil
			},
			close: closeWithoutError,
		},
		truncate: func(size int64) error {
			if size != 0 {
				t.Fatalf("unexpected truncate size: %d", size)
			}
			data = ""
			return nil
		},
	}
	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return info, nil },
	}

	if err := overwritePinnedFile(root, writeTestFileName, target, []byte("after"), nil); err != nil {
		t.Fatalf("overwrite pinned file: %v", err)
	}
	if data != "after" {
		t.Fatalf("unexpected pinned target data: %q", data)
	}
}

func TestOverwritePinnedFileReturnsBeforeRevalidateError(t *testing.T) {
	expectedErr := errors.New("before revalidate failure")
	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) {
			t.Fatal("expected early return before path revalidation")
			return nil, nil
		},
	}

	err := overwritePinnedFile(root, writeTestFileName, &fakeFile{}, []byte("after"), func() error {
		return expectedErr
	})
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected beforeRevalidate error, got %v", err)
	}
}

func TestOverwritePinnedFilePropagatesTargetLookupError(t *testing.T) {
	expectedErr := errors.New("target lookup failure")
	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return nil, expectedErr },
	}

	err := overwritePinnedFile(root, writeTestFileName, &fakeFile{}, []byte("after"), nil)
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected target lookup error, got %v", err)
	}
}

func TestOverwritePinnedFileRejectsTargetSwapBeforeMutation(t *testing.T) {
	originalInfo, swappedInfo := writePinnedTargetInfoPair(t)

	tests := []struct {
		name        string
		swappedInfo fs.FileInfo
	}{
		{name: "symlink", swappedInfo: &modeOverrideFileInfo{FileInfo: swappedInfo, mode: os.ModeSymlink | 0o777}},
		{name: "different identity", swappedInfo: swappedInfo},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			swapInjected := false
			lstatAfterSwap := func(t *testing.T) func(string) (fs.FileInfo, error) {
				t.Helper()
				return func(string) (fs.FileInfo, error) {
					if !swapInjected {
						t.Fatal("target revalidated before swap seam")
					}
					return tt.swappedInfo, nil
				}
			}
			injectSwap := func(t *testing.T) func() error {
				t.Helper()
				return func() error {
					swapInjected = true
					return nil
				}
			}
			assertOverwritePinnedFileRejectsBeforeMutation(t, originalInfo, lstatAfterSwap, injectSwap)
			if !swapInjected {
				t.Fatal("expected swap injection seam to run")
			}
		})
	}
}

func TestOverwritePinnedFileRejectsNonRegularPathBeforeMutation(t *testing.T) {
	originalPath := filepath.Join(t.TempDir(), "original")
	if err := os.WriteFile(originalPath, []byte("original"), 0o640); err != nil {
		t.Fatalf("seed original target: %v", err)
	}
	originalInfo := statTestPath(t, originalPath)

	tests := []struct {
		name     string
		pathInfo fs.FileInfo
		want     string
	}{
		{name: "directory", pathInfo: &modeOverrideFileInfo{FileInfo: originalInfo, mode: os.ModeDir | 0o755}, want: "not a regular file"},
		{name: "symlink", pathInfo: &modeOverrideFileInfo{FileInfo: originalInfo, mode: os.ModeSymlink | 0o777}, want: "became a symlink"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			statCalls := 0
			target := &truncatingFakeFile{
				fakeFile: &fakeFile{
					stat: func() (fs.FileInfo, error) {
						statCalls++
						return originalInfo, nil
					},
					write: func(p []byte) (int, error) { return len(p), nil },
					close: closeWithoutError,
				},
				truncate: func(int64) error { return nil },
			}
			root := &fakeRoot{
				lstat: func(string) (fs.FileInfo, error) { return tt.pathInfo, nil },
			}

			err := overwritePinnedFile(root, writeTestFileName, target, []byte("after"), nil)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("expected %q rejection, got %v", tt.want, err)
			}
			if statCalls != 0 {
				t.Fatalf("expected no opened-file stat after path rejection, got %d calls", statCalls)
			}
		})
	}
}

func TestOverwritePinnedFilePropagatesOpenedFileStatError(t *testing.T) {
	targetInfoPath := filepath.Join(t.TempDir(), writeTestFileName)
	if err := os.WriteFile(targetInfoPath, []byte("before"), 0o640); err != nil {
		t.Fatalf("seed target info path: %v", err)
	}
	info := statTestPath(t, targetInfoPath)
	expectedErr := errors.New("opened target stat failure")
	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return info, nil },
	}
	target := &fakeFile{
		stat:  func() (fs.FileInfo, error) { return nil, expectedErr },
		close: closeWithoutError,
	}

	err := overwritePinnedFile(root, writeTestFileName, target, []byte("after"), nil)
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected opened file stat error, got %v", err)
	}
}

func TestOverwritePinnedFileRejectsFileWithoutTruncate(t *testing.T) {
	targetInfoPath := filepath.Join(t.TempDir(), writeTestFileName)
	if err := os.WriteFile(targetInfoPath, []byte("before"), 0o640); err != nil {
		t.Fatalf("seed target info path: %v", err)
	}
	info := statTestPath(t, targetInfoPath)
	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return info, nil },
	}
	target := &fakeFile{
		stat:  func() (fs.FileInfo, error) { return info, nil },
		close: closeWithoutError,
	}

	err := overwritePinnedFile(root, writeTestFileName, target, []byte("after"), nil)
	if err == nil || !strings.Contains(err.Error(), "does not support truncation") {
		t.Fatalf("expected truncation support error, got %v", err)
	}
}

func TestOverwritePinnedFilePropagatesTruncateError(t *testing.T) {
	targetInfoPath := filepath.Join(t.TempDir(), writeTestFileName)
	if err := os.WriteFile(targetInfoPath, []byte("before"), 0o640); err != nil {
		t.Fatalf("seed target info path: %v", err)
	}
	info := statTestPath(t, targetInfoPath)
	expectedErr := errors.New("truncate target failure")
	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return info, nil },
	}
	target := &truncatingFakeFile{
		fakeFile: &fakeFile{
			stat:  func() (fs.FileInfo, error) { return info, nil },
			write: func(p []byte) (int, error) { return len(p), nil },
			close: closeWithoutError,
		},
		truncate: func(int64) error { return expectedErr },
	}

	err := overwritePinnedFile(root, writeTestFileName, target, []byte("after"), nil)
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected truncate error, got %v", err)
	}
}

func TestOverwritePinnedFilePropagatesWriteError(t *testing.T) {
	targetInfoPath := filepath.Join(t.TempDir(), writeTestFileName)
	if err := os.WriteFile(targetInfoPath, []byte("before"), 0o640); err != nil {
		t.Fatalf("seed target info path: %v", err)
	}
	info := statTestPath(t, targetInfoPath)
	expectedErr := errors.New("write target failure")
	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return info, nil },
	}
	target := &truncatingFakeFile{
		fakeFile: &fakeFile{
			stat: func() (fs.FileInfo, error) { return info, nil },
			write: func([]byte) (int, error) {
				return 0, expectedErr
			},
			close: closeWithoutError,
		},
		truncate: func(int64) error { return nil },
	}

	err := overwritePinnedFile(root, writeTestFileName, target, []byte("after"), nil)
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected write error, got %v", err)
	}
}

func TestCloseFilePreservingPrimaryReturnsCloseErrorWithoutPrimary(t *testing.T) {
	expectedErr := errors.New("close file failure")
	file := &fakeFile{
		close: func() error { return expectedErr },
	}

	err := closeFilePreservingPrimary(file, nil)
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected close error, got %v", err)
	}
}

func TestCloseFilePreservingPrimaryKeepsPrimaryError(t *testing.T) {
	primaryErr := errors.New("primary failure")
	closeErr := errors.New("close file failure")
	file := &fakeFile{
		close: func() error { return closeErr },
	}

	err := closeFilePreservingPrimary(file, primaryErr)
	if !errors.Is(err, primaryErr) {
		t.Fatalf("expected primary error, got %v", err)
	}
	if errors.Is(err, closeErr) {
		t.Fatalf("expected close error to remain secondary, got %v", err)
	}
}

func TestWriteFileReplacingUnderWritesFileInsideRoot(t *testing.T) {
	rootDir := t.TempDir()
	targetPath := filepath.Join(rootDir, "nested", writeTestFileName)
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		t.Fatalf("create parent dir: %v", err)
	}

	if err := WriteFileReplacingUnder(rootDir, targetPath, []byte("hello"), 0o640); err != nil {
		t.Fatalf("WriteFileReplacingUnder returned error: %v", err)
	}
	assertFileContent(t, targetPath, "hello")
}

func TestWriteFileReplacingUnderReturnsCloseRootError(t *testing.T) {
	rootDir := t.TempDir()
	targetPath := filepath.Join(rootDir, writeTestFileName)
	expectedErr := errors.New("close root failure")
	withRootCloseError(t, expectedErr)

	err := WriteFileReplacingUnder(rootDir, targetPath, []byte("hello"), 0o600)
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected root close error, got %v", err)
	}
}

func TestWriteFileReplacingWithinRootRejectsAbsolutePath(t *testing.T) {
	assertWriteWithinRootRejectsAbsolutePath(t, WriteFileReplacingWithinRoot)
}

func TestWriteFileUnderWriteError(t *testing.T) {
	rootDir := t.TempDir()
	targetPath := filepath.Join(rootDir, writeTestFileName)

	writeErr := errors.New("write failure")
	tempFile := &fakeFile{
		write: func([]byte) (int, error) {
			return 0, writeErr
		},
		close: func() error {
			return nil
		},
	}
	withAtomicWriteFileSystem(t, tempFile, nil)

	err := WriteFileUnder(rootDir, targetPath, []byte("hello"), 0o600)
	if err == nil {
		t.Fatal("expected write error")
	}
	if !errors.Is(err, writeErr) {
		t.Fatalf("expected write error, got %v", err)
	}
}

func TestWriteFileUnderTempFileOperationErrors(t *testing.T) {
	for _, tc := range []struct {
		name      string
		configure func(*fakeFile, error)
		assertion string
		expected  error
	}{
		{
			name:      "chmod",
			assertion: "expected chmod error",
			expected:  errors.New("chmod failure"),
			configure: configureTempChmodError,
		},
		{
			name:      "close",
			assertion: "expected temp close error",
			expected:  errors.New("temp close failure"),
			configure: configureTempCloseError,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rootDir := t.TempDir()
			targetPath := filepath.Join(rootDir, writeTestFileName)
			tempFile := &fakeFile{
				write: func(data []byte) (int, error) {
					return len(data), nil
				},
			}
			tc.configure(tempFile, tc.expected)
			withAtomicWriteFileSystem(t, tempFile, nil)

			err := WriteFileUnder(rootDir, targetPath, []byte("hello"), 0o600)
			if err == nil {
				t.Fatal(tc.assertion)
			}
			if !errors.Is(err, tc.expected) {
				t.Fatalf("%s, got %v", tc.assertion, err)
			}
		})
	}
}

func configureTempChmodError(file *fakeFile, err error) {
	file.chmod = func(os.FileMode) error {
		return err
	}
	file.close = closeWithoutError
}

func configureTempCloseError(file *fakeFile, err error) {
	file.chmod = chmodWithoutError
	file.close = func() error {
		return err
	}
}

func chmodWithoutError(os.FileMode) error {
	return nil
}

func closeWithoutError() error {
	return nil
}

func TestResolveWriteTargetAbsFailuresViaFileSystem(t *testing.T) {
	for _, tc := range []struct {
		name     string
		hookPath func(rootDir, targetPath string) string
		hookErr  error
		expected string
	}{
		{
			name: "root",
			hookPath: func(rootDir, _ string) string {
				return rootDir
			},
			hookErr:  errors.New("root abs failure"),
			expected: "resolve root path",
		},
		{
			name: "target",
			hookPath: func(_, targetPath string) string {
				return targetPath
			},
			hookErr:  errors.New("target abs failure"),
			expected: "resolve target path",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rootDir := t.TempDir()
			targetPath := filepath.Join(rootDir, writeTestFileName)

			withFileSystem(t, &fakeFileSystem{abs: func(path string) (string, error) {
				if path == tc.hookPath(rootDir, targetPath) {
					return "", tc.hookErr
				}
				return (&osFileSystem{}).Abs(path)
			}})

			err := WriteFileUnder(rootDir, targetPath, []byte("hello"), 0o600)
			if err == nil {
				t.Fatalf("expected %s error", tc.expected)
			}
			if !strings.Contains(err.Error(), tc.expected) {
				t.Fatalf(unexpectedErrFmt, err)
			}
		})
	}
}

func TestResolveWriteTargetRelFailureViaFileSystem(t *testing.T) {
	rootDir := t.TempDir()
	targetPath := filepath.Join(rootDir, writeTestFileName)

	withFileSystem(t, &fakeFileSystem{rel: func(_, _ string) (string, error) {
		return "", errors.New("rel failure")
	}})

	err := WriteFileUnder(rootDir, targetPath, []byte("hello"), 0o600)
	if err == nil {
		t.Fatal("expected relative path computation error")
	}
	if !strings.Contains(err.Error(), "compute relative path") {
		t.Fatalf(unexpectedErrFmt, err)
	}
}

func TestMoveFileUnderRenamesWithinRootAndSetsMode(t *testing.T) {
	rootDir := t.TempDir()
	sourcePath := filepath.Join(rootDir, "snapshots", "temp.json")
	targetPath := filepath.Join(rootDir, "snapshots", "final.json")
	if err := os.MkdirAll(filepath.Dir(sourcePath), 0o755); err != nil {
		t.Fatalf("create source dir: %v", err)
	}
	if err := os.WriteFile(sourcePath, []byte("hello"), 0o600); err != nil {
		t.Fatalf("write source file: %v", err)
	}

	if err := MoveFileUnder(rootDir, sourcePath, targetPath, 0o750, 0o640); err != nil {
		t.Fatalf("MoveFileUnder returned error: %v", err)
	}

	if _, err := os.Stat(sourcePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected source file to be moved away, got %v", err)
	}
	data, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("read moved file: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("unexpected moved content %q", string(data))
	}
	info, err := os.Stat(targetPath)
	if err != nil {
		t.Fatalf("stat moved file: %v", err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("unexpected moved file mode: got %#o", info.Mode().Perm())
	}
}

func TestMoveFileUnderFallsBackToCopyAndSetsMode(t *testing.T) {
	rootDir := t.TempDir()
	sourcePath := filepath.Join(rootDir, "snapshots", "temp.json")
	targetPath := filepath.Join(rootDir, "snapshots", "final.json")
	if err := os.MkdirAll(filepath.Dir(sourcePath), 0o755); err != nil {
		t.Fatalf("create source dir: %v", err)
	}
	if err := os.WriteFile(sourcePath, []byte("copied"), 0o600); err != nil {
		t.Fatalf("write source file: %v", err)
	}

	withFileSystem(t, &fakeFileSystem{openRoot: func(name string) (Root, error) {
		root, err := (&osFileSystem{}).OpenRoot(name)
		if err != nil {
			return nil, err
		}
		return &fakeRoot{
			Root: root,
			rename: func(oldName, newName string) error {
				if strings.Contains(oldName, atomicTempPrefix) {
					return root.Rename(oldName, newName)
				}
				return syscall.EXDEV
			},
		}, nil
	}})

	if err := MoveFileUnder(rootDir, sourcePath, targetPath, 0o750, 0o640); err != nil {
		t.Fatalf("MoveFileUnder returned error: %v", err)
	}

	if _, err := os.Stat(sourcePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected source file to be removed after copy fallback, got %v", err)
	}
	data, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("read copied file: %v", err)
	}
	if string(data) != "copied" {
		t.Fatalf("unexpected copied content %q", string(data))
	}
	info, err := os.Stat(targetPath)
	if err != nil {
		t.Fatalf("stat copied file: %v", err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("unexpected copied file mode: got %#o", info.Mode().Perm())
	}
}

func TestMoveFileUnderReturnsRenameErrorWithoutFallback(t *testing.T) {
	rootDir := t.TempDir()
	sourcePath := filepath.Join(rootDir, "snapshots", "temp.json")
	targetPath := filepath.Join(rootDir, "snapshots", "final.json")
	renameErr := errors.New("rename failure")

	withFileSystem(t, &fakeFileSystem{openRoot: func(string) (Root, error) {
		return &fakeRoot{
			chmod: func(string, os.FileMode) error {
				return nil
			},
			mkdirAll: func(string, os.FileMode) error {
				return nil
			},
			rename: func(string, string) error {
				return renameErr
			},
			remove: func(string) error {
				return nil
			},
			close: func() error {
				return nil
			},
		}, nil
	}})

	err := MoveFileUnder(rootDir, sourcePath, targetPath, 0o750, 0o640)
	if !errors.Is(err, renameErr) {
		t.Fatalf("expected rename error without fallback copy, got %v", err)
	}
}

func TestMoveFileWithinRootPreservesSourceOnNonEXDEVRenameError(t *testing.T) {
	rootDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(rootDir, "snapshots"), 0o755); err != nil {
		t.Fatalf("create source dir: %v", err)
	}
	sourcePath := filepath.Join(rootDir, "snapshots", "temp.json")
	targetPath := filepath.Join(rootDir, "snapshots", "final.json")
	if err := os.WriteFile(sourcePath, []byte("hello"), 0o600); err != nil {
		t.Fatalf("write source file: %v", err)
	}

	renameErr := errors.New("rename failure")
	root := openTestRoot(t, rootDir)
	failingRoot := &fakeRoot{
		Root: root,
		rename: func(oldName, newName string) error {
			if oldName != filepath.Join("snapshots", "temp.json") || newName != filepath.Join("snapshots", "final.json") {
				t.Fatalf("unexpected rename %q -> %q", oldName, newName)
			}
			return renameErr
		},
	}

	err := MoveFileWithinRoot(failingRoot, filepath.Join("snapshots", "temp.json"), filepath.Join("snapshots", "final.json"), 0o750, 0o640)
	if !errors.Is(err, renameErr) {
		t.Fatalf("expected rename error without fallback copy, got %v", err)
	}
	if _, err := os.Stat(sourcePath); err != nil {
		t.Fatalf("expected source file to remain after rename error, got %v", err)
	}
	if _, err := os.Stat(targetPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected target file to remain absent after rename error, got %v", err)
	}
}

func TestMoveFileWithinRootPreservesSourceOnChmodError(t *testing.T) {
	rootDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(rootDir, "snapshots"), 0o755); err != nil {
		t.Fatalf("create source dir: %v", err)
	}
	sourcePath := filepath.Join(rootDir, "snapshots", "temp.json")
	targetPath := filepath.Join(rootDir, "snapshots", "final.json")
	if err := os.WriteFile(sourcePath, []byte("hello"), 0o600); err != nil {
		t.Fatalf("write source file: %v", err)
	}

	chmodErr := errors.New("chmod failure")
	root := openTestRoot(t, rootDir)
	failingRoot := &fakeRoot{
		Root: root,
		chmod: func(name string, perm os.FileMode) error {
			if name != filepath.Join("snapshots", "temp.json") || perm != 0o640 {
				t.Fatalf("unexpected chmod %q %#o", name, perm)
			}
			return chmodErr
		},
		rename: func(oldName, newName string) error {
			t.Fatalf("unexpected rename %q -> %q", oldName, newName)
			return nil
		},
	}

	err := MoveFileWithinRoot(failingRoot, filepath.Join("snapshots", "temp.json"), filepath.Join("snapshots", "final.json"), 0o750, 0o640)
	if !errors.Is(err, chmodErr) {
		t.Fatalf("expected chmod error without fallback copy, got %v", err)
	}
	if _, err := os.Stat(sourcePath); err != nil {
		t.Fatalf("expected source file to remain after chmod error, got %v", err)
	}
	if _, err := os.Stat(targetPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected target file to remain absent after chmod error, got %v", err)
	}
}

func TestMoveFileUnderValidationAndSetupErrors(t *testing.T) {
	rootDir := t.TempDir()
	sourcePath := filepath.Join(rootDir, "snapshots", "temp.json")
	targetPath := filepath.Join(rootDir, "snapshots", "final.json")

	t.Run("source escapes root", func(t *testing.T) {
		err := MoveFileUnder(rootDir, filepath.Join(rootDir, "..", "temp.json"), targetPath, 0o750, 0o640)
		if err == nil || !strings.Contains(err.Error(), escapesRootErr) {
			t.Fatalf("expected source escape error, got %v", err)
		}
	})

	t.Run("target escapes root", func(t *testing.T) {
		err := MoveFileUnder(rootDir, sourcePath, filepath.Join(rootDir, "..", "final.json"), 0o750, 0o640)
		if err == nil || !strings.Contains(err.Error(), escapesRootErr) {
			t.Fatalf("expected target escape error, got %v", err)
		}
	})

	t.Run("open root error", func(t *testing.T) {
		openRootErr := errors.New("open root failure")
		withFileSystem(t, &fakeFileSystem{openRoot: func(string) (Root, error) {
			return nil, openRootErr
		}})

		err := MoveFileUnder(rootDir, sourcePath, targetPath, 0o750, 0o640)
		if !errors.Is(err, openRootErr) {
			t.Fatalf("expected open root error, got %v", err)
		}
	})

	t.Run("mkdir all error", func(t *testing.T) {
		mkdirErr := errors.New("mkdir failure")
		withFileSystem(t, &fakeFileSystem{openRoot: func(string) (Root, error) {
			return &fakeRoot{
				mkdirAll: func(string, os.FileMode) error {
					return mkdirErr
				},
				close: closeWithoutError,
			}, nil
		}})

		err := MoveFileUnder(rootDir, sourcePath, targetPath, 0o750, 0o640)
		if !errors.Is(err, mkdirErr) {
			t.Fatalf("expected mkdir error, got %v", err)
		}
	})
}

func TestMoveFileUnderCopyFallbackIgnoresMissingSourceRemoval(t *testing.T) {
	rootDir := t.TempDir()
	sourcePath := filepath.Join(rootDir, "snapshots", "temp.json")
	targetPath := filepath.Join(rootDir, "snapshots", "final.json")

	withMoveFallbackFileSystem(t, moveFallbackConfig{
		sourcePath:      filepath.Join("snapshots", "temp.json"),
		sourceData:      "copied",
		sourceRemoveErr: os.ErrNotExist,
	})

	if err := MoveFileUnder(rootDir, sourcePath, targetPath, 0o750, 0o640); err != nil {
		t.Fatalf("expected os.ErrNotExist source cleanup to be ignored, got %v", err)
	}
}

func TestMoveFileWithinRootReturnsSourceRemovalErrorAfterCopyFallback(t *testing.T) {
	sourceRemoveErr := errors.New("remove source failure")
	root := newMoveFallbackRoot(moveFallbackConfig{
		sourcePath:      "source",
		sourceData:      "copied",
		sourceRemoveErr: sourceRemoveErr,
	})

	if err := MoveFileWithinRoot(root, "source", "target", 0o750, 0o640); !errors.Is(err, sourceRemoveErr) {
		t.Fatalf("expected source removal error after copy fallback, got %v", err)
	}
}

func TestMoveFileWithinRootPreservesSourceWhenCopyFallbackFails(t *testing.T) {
	for _, tc := range []struct {
		name  string
		stage moveFallbackFailureStage
	}{
		{name: "open source", stage: moveFallbackFailSourceOpen},
		{name: "read source", stage: moveFallbackFailSourceRead},
		{name: "open temp", stage: moveFallbackFailTempOpen},
		{name: "write temp", stage: moveFallbackFailTempWrite},
		{name: "chmod temp", stage: moveFallbackFailTempChmod},
		{name: "close temp", stage: moveFallbackFailTempClose},
		{name: "rename temp", stage: moveFallbackFailTempRename},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sentinel := errors.New(tc.name + " failure")
			state := &moveFallbackState{}
			cfg := moveFallbackConfig{
				sourcePath:   "source",
				sourceData:   "copied",
				failureStage: tc.stage,
				failureErr:   sentinel,
				state:        state,
			}

			err := MoveFileWithinRoot(newMoveFallbackRoot(cfg), "source", "target", 0o750, 0o640)
			if err == nil {
				t.Fatal("expected copy fallback failure")
			}
			if !errors.Is(err, syscall.EXDEV) {
				t.Fatalf("expected EXDEV fallback error, got %v", err)
			}
			if !errors.Is(err, sentinel) {
				t.Fatalf("expected %s error, got %v", tc.name, err)
			}

			assertMoveFallbackFailureState(t, state, "copied")
		})
	}
}

type moveFallbackConfig struct {
	sourcePath      string
	sourceData      string
	sourceReadErr   error
	sourceCloseErr  error
	tempRemoveErr   error
	sourceRemoveErr error
	rootCloseErr    error
	failureStage    moveFallbackFailureStage
	failureErr      error
	state           *moveFallbackState
}

type moveFallbackFailureStage uint8

const (
	moveFallbackFailSourceOpen moveFallbackFailureStage = iota + 1
	moveFallbackFailSourceRead
	moveFallbackFailTempOpen
	moveFallbackFailTempWrite
	moveFallbackFailTempChmod
	moveFallbackFailTempClose
	moveFallbackFailTempRename
)

type moveFallbackState struct {
	sourceData   string
	sourceExists bool
	tempData     string
	tempExists   bool
	targetData   string
	targetExists bool
}

func withMoveFallbackFileSystem(t *testing.T, cfg moveFallbackConfig) {
	t.Helper()
	withFileSystem(t, &fakeFileSystem{openRoot: func(string) (Root, error) {
		return newMoveFallbackRoot(cfg), nil
	}})
}

func newMoveFallbackRoot(cfg moveFallbackConfig) *fakeRoot {
	state := newMoveFallbackState(cfg)
	return &fakeRoot{
		open:     newMoveFallbackOpenHook(cfg, state),
		openFile: newMoveFallbackOpenFileHook(cfg, state),
		chmod: func(string, os.FileMode) error {
			return nil
		},
		mkdirAll: func(string, os.FileMode) error { return nil },
		rename:   newMoveFallbackRenameHook(cfg, state),
		remove:   newMoveFallbackRemoveHook(cfg, state),
		close:    func() error { return cfg.rootCloseErr },
	}
}

func newMoveFallbackState(cfg moveFallbackConfig) *moveFallbackState {
	state := cfg.state
	if state == nil {
		state = &moveFallbackState{}
	}
	state.sourceData = cfg.sourceData
	state.sourceExists = true
	return state
}

func (c *moveFallbackConfig) failure(stage moveFallbackFailureStage) error {
	if stage == moveFallbackFailSourceRead && c.sourceReadErr != nil {
		return c.sourceReadErr
	}
	if c.failureStage == stage {
		return c.failureErr
	}
	return nil
}

func newMoveFallbackOpenHook(cfg moveFallbackConfig, state *moveFallbackState) func(string) (File, error) {
	return func(name string) (File, error) {
		if name != cfg.sourcePath {
			return nil, errors.New("unexpected source open path")
		}
		if err := cfg.failure(moveFallbackFailSourceOpen); err != nil {
			return nil, err
		}
		if !state.sourceExists {
			return nil, os.ErrNotExist
		}
		return newMoveFallbackSourceFile(cfg, state), nil
	}
}

func newMoveFallbackSourceFile(cfg moveFallbackConfig, state *moveFallbackState) File {
	reader := strings.NewReader(state.sourceData)
	return &fakeFile{
		read: func(p []byte) (int, error) {
			if err := cfg.failure(moveFallbackFailSourceRead); err != nil {
				return 0, err
			}
			return reader.Read(p)
		},
		close: func() error { return cfg.sourceCloseErr },
	}
}

func newMoveFallbackOpenFileHook(cfg moveFallbackConfig, state *moveFallbackState) func(string, int, os.FileMode) (File, error) {
	return func(name string, _ int, _ os.FileMode) (File, error) {
		if !isMoveFallbackTempPath(name) {
			return nil, errors.New("unexpected temp file path")
		}
		if err := cfg.failure(moveFallbackFailTempOpen); err != nil {
			return nil, err
		}
		state.tempData = ""
		state.tempExists = true
		return newMoveFallbackTempFile(cfg, state), nil
	}
}

func newMoveFallbackTempFile(cfg moveFallbackConfig, state *moveFallbackState) File {
	return &fakeFile{
		write: func(p []byte) (int, error) {
			if err := cfg.failure(moveFallbackFailTempWrite); err != nil {
				return 0, err
			}
			state.tempData += string(p)
			return len(p), nil
		},
		close: func() error { return cfg.failure(moveFallbackFailTempClose) },
		chmod: func(os.FileMode) error { return cfg.failure(moveFallbackFailTempChmod) },
	}
}

func newMoveFallbackRenameHook(cfg moveFallbackConfig, state *moveFallbackState) func(string, string) error {
	return func(oldName, _ string) error {
		if oldName == cfg.sourcePath {
			return syscall.EXDEV
		}
		if !isMoveFallbackTempPath(oldName) {
			return nil
		}
		if err := cfg.failure(moveFallbackFailTempRename); err != nil {
			return err
		}
		state.targetData = state.tempData
		state.targetExists = true
		state.tempData = ""
		state.tempExists = false
		return nil
	}
}

func newMoveFallbackRemoveHook(cfg moveFallbackConfig, state *moveFallbackState) func(string) error {
	return func(name string) error {
		if isMoveFallbackTempPath(name) {
			state.tempData = ""
			state.tempExists = false
			return cfg.tempRemoveErr
		}
		if name != cfg.sourcePath {
			return nil
		}
		if cfg.sourceRemoveErr == nil {
			state.sourceExists = false
		}
		return cfg.sourceRemoveErr
	}
}

func isMoveFallbackTempPath(name string) bool {
	return strings.Contains(name, atomicTempPrefix)
}

func assertMoveFallbackFailureState(t *testing.T, state *moveFallbackState, wantSourceData string) {
	t.Helper()
	if !state.sourceExists || state.sourceData != wantSourceData {
		t.Fatalf("expected source to remain %q after fallback failure, got exists=%t data=%q", wantSourceData, state.sourceExists, state.sourceData)
	}
	if state.targetExists {
		t.Fatalf("expected target to remain absent after fallback failure, got %q", state.targetData)
	}
	if state.tempExists {
		t.Fatalf("expected temp cleanup after fallback failure, found %q", state.tempData)
	}
}

func copyRootWithOpenError(openErr error) *fakeRoot {
	return &fakeRoot{
		open: func(string) (File, error) { return nil, openErr },
	}
}

func copyRootWithTempOpenError(openFileErr error) *fakeRoot {
	return copyRootWithTempFile(tempFileOptions{openFileErr: openFileErr})
}

func copyRootWithTempChmodError(chmodErr error) *fakeRoot {
	return copyRootWithTempFile(tempFileOptions{data: "x", chmodErr: chmodErr})
}

func copyRootWithTempCloseError(closeErr error) *fakeRoot {
	return copyRootWithTempFile(tempFileOptions{data: "x", closeErr: closeErr})
}

func copyRootWithTempRenameError(renameErr error) *fakeRoot {
	return copyRootWithTempFile(tempFileOptions{data: "x", renameErr: renameErr})
}

type tempFileOptions struct {
	data        string
	openFileErr error
	chmodErr    error
	closeErr    error
	renameErr   error
}

func copyRootWithTempFile(opts tempFileOptions) *fakeRoot {
	return &fakeRoot{
		open: func(string) (File, error) {
			return &fakeFile{
				read: func(p []byte) (int, error) {
					copy(p, opts.data)
					return len(opts.data), io.EOF
				},
				close: closeWithoutError,
			}, nil
		},
		mkdirAll: func(string, os.FileMode) error { return nil },
		openFile: func(string, int, os.FileMode) (File, error) {
			if opts.openFileErr != nil {
				return nil, opts.openFileErr
			}
			return &fakeFile{
				write: func(p []byte) (int, error) { return len(p), nil },
				close: func() error { return opts.closeErr },
				chmod: func(os.FileMode) error { return opts.chmodErr },
			}, nil
		},
		rename: func(oldName, _ string) error {
			if strings.Contains(oldName, atomicTempPrefix) {
				return opts.renameErr
			}
			return nil
		},
		remove: func(string) error { return nil },
	}
}

func TestOpenRootReturnsUsableRoot(t *testing.T) {
	rootDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(rootDir, "file.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatalf("write root file: %v", err)
	}

	root, err := OpenRoot(rootDir)
	if err != nil {
		t.Fatalf("OpenRoot returned error: %v", err)
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Fatalf(closeRootErrFmt, closeErr)
		}
	}()

	file, err := root.Open("file.txt")
	if err != nil {
		t.Fatalf("open file through root: %v", err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			t.Fatalf("close root-opened file: %v", closeErr)
		}
	}()

	data, err := io.ReadAll(file)
	if err != nil {
		t.Fatalf("read file through root: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("unexpected root-opened content %q", string(data))
	}
}

func TestCreateTempFileWithinRootCreatesRemovableTempFile(t *testing.T) {
	rootDir := t.TempDir()
	root := openTestRoot(t, rootDir)

	tempRel, tempFile, err := CreateTempFileWithinRoot(root, ".", 0o600)
	if err != nil {
		t.Fatalf("CreateTempFileWithinRoot returned error: %v", err)
	}
	if !strings.HasPrefix(filepath.Base(tempRel), atomicTempPrefix) {
		t.Fatalf("expected atomic temp prefix, got %q", tempRel)
	}

	if _, err := tempFile.Write([]byte("hello")); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	if err := CleanupTempFileWithinRoot(root, tempRel, tempFile); err != nil {
		t.Fatalf("cleanup temp file: %v", err)
	}
	if _, err := root.Open(tempRel); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected temp file removal, got %v", err)
	}
}

func TestMoveFileUnderJoinsPrimaryAndCleanupErrors(t *testing.T) {
	rootDir := t.TempDir()
	sourcePath := filepath.Join(rootDir, "snapshots", "temp.json")
	targetPath := filepath.Join(rootDir, "snapshots", "final.json")
	primaryErr := errors.New("copy read failure")
	sourceCloseErr := errors.New("close source failure")
	tempRemoveErr := errors.New("remove temp failure")
	sourceRemoveErr := errors.New("remove source failure")
	rootCloseErr := errors.New("close root failure")

	withMoveFallbackFileSystem(t, moveFallbackConfig{
		sourcePath:      filepath.Join("snapshots", "temp.json"),
		sourceReadErr:   primaryErr,
		sourceCloseErr:  sourceCloseErr,
		tempRemoveErr:   tempRemoveErr,
		sourceRemoveErr: sourceRemoveErr,
		rootCloseErr:    rootCloseErr,
	})

	err := MoveFileUnder(rootDir, sourcePath, targetPath, 0o750, 0o640)
	if err == nil {
		t.Fatal("expected MoveFileUnder error")
	}
	for _, expected := range []error{primaryErr, sourceCloseErr, tempRemoveErr, rootCloseErr} {
		if !errors.Is(err, expected) {
			t.Fatalf("expected joined error to include %v, got %v", expected, err)
		}
	}
	if errors.Is(err, sourceRemoveErr) {
		t.Fatalf("expected fallback failure to preserve source instead of removing it, got %v", err)
	}
}

func TestPrepareAndRenameWithinRootErrors(t *testing.T) {
	chmodErr := errors.New("chmod failure")
	chmodRoot := &fakeRoot{
		chmod: func(string, os.FileMode) error {
			return chmodErr
		},
	}
	if err := prepareAndRenameWithinRoot(chmodRoot, "source", "target", 0o640); !errors.Is(err, chmodErr) {
		t.Fatalf("expected chmod error, got %v", err)
	}

	renameErr := errors.New("rename failure")
	renameRoot := &fakeRoot{
		chmod: func(string, os.FileMode) error {
			return nil
		},
		rename: func(string, string) error {
			return renameErr
		},
	}
	if err := prepareAndRenameWithinRoot(renameRoot, "source", "target", 0o640); !errors.Is(err, renameErr) {
		t.Fatalf("expected rename error, got %v", err)
	}
}

func TestCopyFileWithinRootErrorBranches(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		root func(error) *fakeRoot
	}{
		{name: "open source error", err: errors.New("open source failure"), root: copyRootWithOpenError},
		{name: "create temp file error", err: errors.New("open temp failure"), root: copyRootWithTempOpenError},
		{name: "temp chmod error", err: errors.New("temp chmod failure"), root: copyRootWithTempChmodError},
		{name: "temp close error", err: errors.New("temp close failure"), root: copyRootWithTempCloseError},
		{name: "rename temp file error", err: errors.New("rename temp failure"), root: copyRootWithTempRenameError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := copyFileWithinRoot(tc.root(tc.err), "source", "target", 0o640)
			if !errors.Is(err, tc.err) {
				t.Fatalf("expected %s, got %v", tc.name, err)
			}
		})
	}
}
