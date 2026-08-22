package safeio

import (
	"bytes"
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

type unsafeTargetModeCase struct {
	name string
	mode os.FileMode
	want string
}

type moveFallbackFailureCase struct {
	name  string
	stage moveFallbackFailureStage
}

type exclusiveCreateState struct {
	wroteTarget  []byte
	chmodTarget  os.FileMode
	targetClosed bool
	lstatCalls   int
}

type exclusiveCreatedTargetCase struct {
	name      string
	fileInfo  fs.FileInfo
	pathInfo  fs.FileInfo
	lstatErr  error
	wantError string
	cleanup   bool
}

func (i *modeOverrideFileInfo) Mode() os.FileMode {
	return i.mode
}

func unsafeTargetModeCases() []unsafeTargetModeCase {
	return []unsafeTargetModeCase{
		{name: "directory", mode: os.ModeDir | 0o755, want: "not a regular file"},
		{name: "symlink", mode: os.ModeSymlink | 0o777, want: "became a symlink"},
	}
}

func moveFallbackFailureCases() []moveFallbackFailureCase {
	return []moveFallbackFailureCase{
		{name: "open source", stage: moveFallbackFailSourceOpen},
		{name: "read source", stage: moveFallbackFailSourceRead},
		{name: "open temp", stage: moveFallbackFailTempOpen},
		{name: "write temp", stage: moveFallbackFailTempWrite},
		{name: "chmod temp", stage: moveFallbackFailTempChmod},
		{name: "close temp", stage: moveFallbackFailTempClose},
		{name: "rename temp", stage: moveFallbackFailTempRename},
	}
}

func newPinnedTargetInfo(t *testing.T, contents string) fs.FileInfo {
	t.Helper()

	targetInfoPath := filepath.Join(t.TempDir(), writeTestFileName)
	if err := os.WriteFile(targetInfoPath, []byte(contents), 0o640); err != nil {
		t.Fatalf("seed target info path: %v", err)
	}
	return statTestPath(t, targetInfoPath)
}

func assertMovedFileResult(t *testing.T, sourcePath, targetPath, wantData, sourceAction string) {
	t.Helper()

	if _, err := os.Stat(sourcePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected source file to %s, got %v", sourceAction, err)
	}
	data, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("read moved file: %v", err)
	}
	if string(data) != wantData {
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

func newExclusiveCreateRoot(t *testing.T, targetInfo fs.FileInfo) (*fakeRoot, *exclusiveCreateState) {
	t.Helper()

	state := &exclusiveCreateState{}
	return &fakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			if name != writeTestFileName {
				t.Fatalf("unexpected target lookup: %s", name)
			}
			state.lstatCalls++
			if state.lstatCalls == 1 {
				return nil, os.ErrNotExist
			}
			return targetInfo, nil
		},
		openFile: func(name string, flag int, perm os.FileMode) (File, error) {
			assertExclusiveCreateOpen(t, name, flag, perm)
			return &fakeFile{
				stat: func() (fs.FileInfo, error) { return targetInfo, nil },
				write: func(p []byte) (int, error) {
					state.wroteTarget = append([]byte(nil), p...)
					return len(p), nil
				},
				chmod: func(mode os.FileMode) error {
					state.chmodTarget = mode
					return nil
				},
				close: func() error {
					state.targetClosed = true
					return nil
				},
			}, nil
		},
		link: func(string, string) error {
			t.Fatal("write-if-absent should not link through a replaceable temp pathname")
			return nil
		},
	}, state
}

func assertExclusiveCreateOpen(t *testing.T, name string, flag int, perm os.FileMode) {
	t.Helper()
	if name != writeTestFileName {
		t.Fatalf("unexpected create path: %s", name)
	}
	if flag != os.O_RDWR|os.O_CREATE|os.O_EXCL {
		t.Fatalf("unexpected target open flags: %#x", flag)
	}
	if perm != 0o640 {
		t.Fatalf("unexpected target perm: %#o", perm)
	}
}

func assertExclusiveCreateSuccess(t *testing.T, state *exclusiveCreateState) {
	t.Helper()
	if string(state.wroteTarget) != "hello" {
		t.Fatalf("unexpected target content: %q", string(state.wroteTarget))
	}
	if state.chmodTarget != 0o640 {
		t.Fatalf("unexpected target chmod: %#o", state.chmodTarget)
	}
	if !state.targetClosed {
		t.Fatal("expected exclusive-create target file to close")
	}
	if state.lstatCalls != 2 {
		t.Fatalf("expected target revalidation after close, got %d lookups", state.lstatCalls)
	}
}

func assertExclusiveCreatedTargetRejected(t *testing.T, tc exclusiveCreatedTargetCase) {
	t.Helper()
	closed := false
	removed := false
	lstatCalls := 0
	root := &fakeRoot{
		openFile: func(string, int, os.FileMode) (File, error) {
			return &fakeFile{
				stat:  func() (fs.FileInfo, error) { return tc.fileInfo, nil },
				write: func(p []byte) (int, error) { return len(p), nil },
				chmod: chmodWithoutError,
				close: func() error { closed = true; return nil },
			}, nil
		},
		lstat: func(string) (fs.FileInfo, error) {
			lstatCalls++
			if lstatCalls == 1 {
				return nil, os.ErrNotExist
			}
			return tc.pathInfo, tc.lstatErr
		},
		remove: func(string) error { removed = true; return nil },
	}

	err := writeFileIfAbsentAtRoot(root, rootedTarget{rel: writeTestFileName}, []byte("hello"), 0o600)
	if err == nil || !strings.Contains(err.Error(), tc.wantError) {
		t.Fatalf("expected %q, got %v", tc.wantError, err)
	}
	assertExclusiveCreatedTargetCleanup(t, tc.cleanup, closed, removed)
}

func assertExclusiveCreatedTargetCleanup(t *testing.T, cleanup, closed, removed bool) {
	t.Helper()
	if !closed {
		t.Fatal("expected created target to close")
	}
	if cleanup && !removed {
		t.Fatal("expected cleanup of rejected created target")
	}
	if !cleanup && removed {
		t.Fatal("must not remove a target after failed post-close validation")
	}
}

func newPinnedFallbackTargetFile(t *testing.T, info fs.FileInfo, initial string) (File, *[]byte) {
	t.Helper()
	targetData := []byte(initial)
	targetFile := &truncatingFakeFile{
		fakeFile: &fakeFile{
			stat: func() (fs.FileInfo, error) { return info, nil },
			write: func(p []byte) (int, error) {
				targetData = append(targetData, p...)
				return len(p), nil
			},
			close: closeWithoutError,
		},
		truncate: func(size int64) error {
			if size != 0 {
				t.Fatalf("unexpected truncate size: %d", size)
			}
			targetData = targetData[:0]
			return nil
		},
	}
	return targetFile, &targetData
}

func openTargetOrTempFile(targetName string, openTarget func() (File, error), tempInfo fs.FileInfo, tempOpenErr error) func(string, int, os.FileMode) (File, error) {
	return func(name string, flag int, perm os.FileMode) (File, error) {
		if name == targetName {
			return openTarget()
		}
		if tempOpenErr != nil {
			return nil, tempOpenErr
		}
		return &fakeFile{
			stat:  func() (fs.FileInfo, error) { return tempInfo, nil },
			write: func(p []byte) (int, error) { return len(p), nil },
			chmod: chmodWithoutError,
			close: closeWithoutError,
		}, nil
	}
}

func newCommittedTargetValidationRoot(t *testing.T, tempInfo fs.FileInfo, lstatTarget func() (fs.FileInfo, error), rename func() error, remove func(string) error, tempClosed *bool) *fakeRoot {
	t.Helper()

	lstat := func(string) (fs.FileInfo, error) {
		return lstatTarget()
	}
	renameFn := func(string, string) error {
		return rename()
	}
	closeFn := func() error {
		*tempClosed = true
		return nil
	}
	return newCommittedTargetTestRoot(t, tempInfo, lstat, renameFn, remove, closeFn)
}

func newCommittedTargetTestRoot(t *testing.T, tempInfo fs.FileInfo, lstat func(string) (fs.FileInfo, error), rename func(string, string) error, remove func(string) error, closeFn func() error) *fakeRoot {
	t.Helper()

	return &fakeRoot{
		openFile: func(name string, flag int, perm os.FileMode) (File, error) {
			if !strings.HasPrefix(name, atomicTempPrefix) {
				t.Fatalf("unexpected open path: %s", name)
			}
			return newCommittedTargetTempFile(t, tempInfo, closeFn), nil
		},
		lstat: func(name string) (fs.FileInfo, error) {
			if name != writeTestFileName {
				t.Fatalf("unexpected lstat path: %s", name)
			}
			return lstat(name)
		},
		rename: rename,
		remove: remove,
	}
}

func newCommittedTargetLstatErrorRoot(t *testing.T, tempInfo fs.FileInfo, expectedErr error, tempClosed *bool) *fakeRoot {
	t.Helper()

	lstatTarget := func() (fs.FileInfo, error) {
		return nil, expectedErr
	}
	rename := func() error {
		return nil
	}
	remove := func(string) error {
		return nil
	}
	return newCommittedTargetValidationRoot(t, tempInfo, lstatTarget, rename, remove, tempClosed)
}

func newCommittedTargetTempFile(t *testing.T, tempInfo fs.FileInfo, closeFn func() error) *fakeFile {
	t.Helper()

	return &fakeFile{
		stat:  func() (fs.FileInfo, error) { return tempInfo, nil },
		write: func(p []byte) (int, error) { return len(p), nil },
		chmod: chmodWithoutError,
		close: closeFn,
	}
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

func assertWriteRootCreatesMissingParentsAndWrites(t *testing.T, writeName string, invoke func(*WriteRoot, string, []byte, os.FileMode, os.FileMode) error) {
	t.Helper()
	rootDir := t.TempDir()
	root := openTestWriteRoot(t, rootDir, OpenWriteRoot)

	targetPath := filepath.Join("reports", "nested", writeTestFileName)
	if err := invoke(root, targetPath, []byte("hello"), 0o640, 0o750); err != nil {
		t.Fatalf("%s returned error: %v", writeName, err)
	}

	data, err := os.ReadFile(filepath.Join(rootDir, targetPath))
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("unexpected content: %q", string(data))
	}
}

func assertWriteRootRejectsExistingTarget(t *testing.T, writeName string, invoke func(*WriteRoot, string, []byte, os.FileMode, os.FileMode) error) {
	t.Helper()
	rootDir := t.TempDir()
	root := openTestWriteRoot(t, rootDir, OpenWriteRoot)

	targetPath := filepath.Join("reports", writeTestFileName)
	if err := invoke(root, targetPath, []byte("before"), 0o600, 0o750); err != nil {
		t.Fatalf("seed target with %s: %v", writeName, err)
	}
	err := invoke(root, targetPath, []byte("after"), 0o600, 0o750)
	if !errors.Is(err, os.ErrExist) {
		t.Fatalf("expected os.ErrExist, got %v", err)
	}
	if data, readErr := os.ReadFile(filepath.Join(rootDir, targetPath)); readErr != nil {
		t.Fatalf("read existing target: %v", readErr)
	} else if string(data) != "before" {
		t.Fatalf("expected existing target to remain unchanged, got %q", string(data))
	}
}

func assertWriteRootRejectsNonRelativeTargets(t *testing.T, invoke func(*WriteRoot, string, []byte, os.FileMode, os.FileMode) error) {
	t.Helper()
	rootDir := t.TempDir()
	root := openTestWriteRoot(t, rootDir, OpenWriteRoot)

	for _, targetPath := range []string{rootDir, "..", filepath.Join("..", writeTestFileName), "."} {
		err := invoke(root, targetPath, []byte("hello"), 0o600, 0o750)
		if err == nil {
			t.Fatalf("expected target %q to be rejected", targetPath)
		}
	}
}

func makeFakeFallbackWriteRoot(targetFile func() File, remove func(string) error) *fakeRoot {
	if remove == nil {
		remove = func(string) error { return nil }
	}
	return &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return nil, os.ErrNotExist },
		openFile: func(name string, flag int, perm os.FileMode) (File, error) {
			if name == writeTestFileName {
				return targetFile(), nil
			}
			return &fakeFile{
				write: func(p []byte) (int, error) { return len(p), nil },
				chmod: func(os.FileMode) error { return nil },
				close: func() error { return nil },
			}, nil
		},
		link:   func(string, string) error { return errors.ErrUnsupported },
		remove: remove,
	}
}

func assertExclusiveCreateCleanup(t *testing.T, removed []string) {
	t.Helper()
	if len(removed) != 1 {
		t.Fatalf("expected cleanup for incomplete exclusive-create target, got %v", removed)
	}
	if removed[0] != writeTestFileName {
		t.Fatalf("expected exclusive-create target cleanup for %q, got %v", writeTestFileName, removed)
	}
}

func assertWriteIfAbsentFallbackCleanup(t *testing.T, expectedErr error, targetFile func(*bool) File) {
	t.Helper()

	var removed []string
	targetClosed := false
	targetInfo := newPinnedTargetInfo(t, "target")
	remove := func(name string) error {
		removed = append(removed, name)
		return nil
	}

	withTargetInfo := func() File {
		file := targetFile(&targetClosed)
		fake, ok := file.(*fakeFile)
		if !ok {
			t.Fatalf("expected fake file, got %T", file)
		}
		fake.stat = func() (fs.FileInfo, error) { return targetInfo, nil }
		return fake
	}
	root := makeFakeFallbackWriteRoot(withTargetInfo, remove)

	err := writeFileIfAbsentAtRoot(root, rootedTarget{rel: writeTestFileName}, []byte("hello"), 0o600)
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected fallback error %v, got %v", expectedErr, err)
	}
	assertExclusiveCreateCleanup(t, removed)
	if !targetClosed {
		t.Fatal("expected fallback target file close attempt before cleanup")
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
	assertWriteRootCreatesMissingParentsAndWrites(t, "WriteFileCreatingParents", (*WriteRoot).WriteFileCreatingParents)

	rootDir := t.TempDir()
	root := openTestWriteRoot(t, rootDir, OpenWriteRoot)
	if err := root.WriteFileCreatingParents(filepath.Join("reports", "nested", writeTestFileName), []byte("hello"), 0o640, 0o750); err != nil {
		t.Fatalf("WriteFileCreatingParents returned error: %v", err)
	}
	info, err := os.Stat(filepath.Join(rootDir, "reports", "nested", writeTestFileName))
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

func TestWriteRootCreatesParentsAfterParentReady(t *testing.T) {
	rootDir := t.TempDir()
	root := openTestWriteRoot(t, rootDir, OpenWriteRoot)
	target := filepath.Join("reports", "nested", writeTestFileName)
	readyCalls := 0

	err := root.WriteFileCreatingParentsAfterParentReady(target, []byte("hello"), 0o640, 0o750, func() error {
		readyCalls++
		if _, statErr := os.Stat(filepath.Join(rootDir, "reports", "nested")); statErr != nil {
			t.Fatalf("expected parent to be created before parentReady: %v", statErr)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WriteFileCreatingParentsAfterParentReady returned error: %v", err)
	}
	if readyCalls != 1 {
		t.Fatalf("expected one parentReady call, got %d", readyCalls)
	}
	assertFileContent(t, filepath.Join(rootDir, target), "hello")
}

func TestWriteRootAfterParentReadyErrorPreventsWrite(t *testing.T) {
	rootDir := t.TempDir()
	root := openTestWriteRoot(t, rootDir, OpenWriteRoot)
	expectedErr := errors.New("parent no longer valid")
	target := filepath.Join("reports", writeTestFileName)

	err := root.WriteFileCreatingParentsAfterParentReady(target, []byte("hello"), 0o640, 0o750, func() error {
		return expectedErr
	})
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected parentReady error, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(rootDir, target)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("expected target to remain absent, got %v", statErr)
	}
}

func TestWriteRootAfterParentReadyRejectsNonRelativeTarget(t *testing.T) {
	root := openTestWriteRoot(t, t.TempDir(), OpenWriteRoot)

	err := root.WriteFileCreatingParentsAfterParentReady(filepath.Join(t.TempDir(), writeTestFileName), []byte("hello"), 0o640, 0o750, func() error {
		t.Fatal("parentReady should not run for invalid target")
		return nil
	})
	if err == nil {
		t.Fatal("expected absolute target to be rejected")
	}
}

func TestWriteRootCreatesParentsAfterParentReadyWithPreWriteCheck(t *testing.T) {
	rootDir := t.TempDir()
	root := openTestWriteRoot(t, rootDir, OpenWriteRoot)
	target := filepath.Join("reports", "nested", writeTestFileName)
	var calls []string

	err := root.WriteFileCreatingParentsAfterParentReadyWithPreWriteCheck(target, []byte("hello"), 0o640, 0o750, func() error {
		calls = append(calls, "parentReady")
		if _, statErr := os.Stat(filepath.Join(rootDir, "reports", "nested")); statErr != nil {
			t.Fatalf("expected parent to be created before parentReady: %v", statErr)
		}
		return nil
	}, func() error {
		calls = append(calls, "preWrite")
		if len(calls) == 2 {
			if _, statErr := os.Stat(filepath.Join(rootDir, target)); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("expected target to remain absent before preWrite, got %v", statErr)
			}
			return nil
		}
		if _, statErr := os.Stat(filepath.Join(rootDir, target)); statErr != nil {
			t.Fatalf("expected target to exist during post-write check, got %v", statErr)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WriteFileCreatingParentsAfterParentReadyWithPreWriteCheck returned error: %v", err)
	}
	if got, want := strings.Join(calls, ","), "parentReady,preWrite,preWrite"; got != want {
		t.Fatalf("callback order = %s, want %s", got, want)
	}
	assertFileContent(t, filepath.Join(rootDir, target), "hello")
}

func TestWriteRootPublishCheckRunsImmediatelyBeforeCommitAndAfterWrite(t *testing.T) {
	rootDir := t.TempDir()
	root := openTestWriteRoot(t, rootDir, OpenWriteRoot)
	target := filepath.Join("reports", writeTestFileName)

	originalPublishReady := writeFilePublishReadyFn
	writeFilePublishReadyFn = func() error {
		if _, err := os.Stat(filepath.Join(rootDir, target)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("expected target to remain absent before the commit check, got %v", err)
		}
		return nil
	}
	t.Cleanup(func() {
		writeFilePublishReadyFn = originalPublishReady
	})

	publishChecks := 0
	err := root.WriteFileCreatingParentsAfterParentReadyWithPublishCheck(target, []byte("hello"), 0o640, 0o750, func() error {
		return nil
	}, func() error {
		publishChecks++
		if publishChecks == 1 {
			if _, err := os.Stat(filepath.Join(rootDir, target)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("expected target to remain absent during commit check, got %v", err)
			}
			return nil
		}
		if _, err := os.Stat(filepath.Join(rootDir, target)); err != nil {
			t.Fatalf("expected target to exist during post-write check: %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WriteFileCreatingParentsAfterParentReadyWithPublishCheck returned error: %v", err)
	}
	if publishChecks != 2 {
		t.Fatalf("expected commit and post-write checks, got %d", publishChecks)
	}
	assertFileContent(t, filepath.Join(rootDir, target), "hello")
}

func TestWriteRootPinnedParentPublishCheckRejectsRetargetedParentPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory replacement semantics are covered on Unix")
	}

	rootDir := t.TempDir()
	originalParent := filepath.Join(rootDir, "reports")
	relocatedParent := filepath.Join(rootDir, "reports-relocated")
	if err := os.MkdirAll(originalParent, 0o750); err != nil {
		t.Fatalf("mkdir original parent: %v", err)
	}

	originalReady := writeFileParentReadyFn
	writeFileParentReadyFn = func() error {
		if err := os.Rename(originalParent, relocatedParent); err != nil {
			return err
		}
		return os.Mkdir(originalParent, 0o750)
	}
	t.Cleanup(func() {
		writeFileParentReadyFn = originalReady
	})

	root := openTestWriteRoot(t, rootDir, OpenWriteRoot)
	err := root.WriteFileCreatingParentsAfterParentReadyWithPinnedParentPublishCheck(
		filepath.Join("reports", writeTestFileName),
		[]byte("hello"),
		0o640,
		0o750,
		VerifyDirectoryIdentity,
	)
	if err == nil || !strings.Contains(err.Error(), "directory identity changed") {
		t.Fatalf("expected retargeted parent identity error, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(relocatedParent, writeTestFileName)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("expected relocated parent target to remain absent, got %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(originalParent, writeTestFileName)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("expected replacement parent target to remain absent, got %v", statErr)
	}
}

func TestWriteRootPinnedParentPublishCheckPublishesThroughValidatedParentPath(t *testing.T) {
	rootDir := t.TempDir()
	parent := filepath.Join(rootDir, "reports")
	if err := os.MkdirAll(parent, 0o750); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}

	root := openTestWriteRoot(t, rootDir, OpenWriteRoot)
	publishChecks := 0
	err := root.WriteFileCreatingParentsAfterParentReadyWithPinnedParentPublishCheck(
		filepath.Join("reports", writeTestFileName),
		[]byte("hello"),
		0o640,
		0o750,
		func(parentPath string, parentIdentity fs.FileInfo) error {
			publishChecks++
			return VerifyDirectoryIdentity(parentPath, parentIdentity)
		},
	)
	if err != nil {
		t.Fatalf("WriteFileCreatingParentsAfterParentReadyWithPinnedParentPublishCheck returned error: %v", err)
	}
	if publishChecks != 3 {
		t.Fatalf("expected initial, commit, and post-write publish checks, got %d", publishChecks)
	}
	assertFileContent(t, filepath.Join(parent, writeTestFileName), "hello")
}

func TestWriteRootPinnedParentPublishCheckRejectsParentSwapDuringCommitCheck(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory replacement semantics are covered on Unix")
	}

	rootDir := t.TempDir()
	originalParent := filepath.Join(rootDir, "reports")
	relocatedParent := filepath.Join(rootDir, "reports-relocated")
	if err := os.MkdirAll(originalParent, 0o750); err != nil {
		t.Fatalf("mkdir original parent: %v", err)
	}

	root := openTestWriteRoot(t, rootDir, OpenWriteRoot)
	publishChecks := 0
	err := root.WriteFileCreatingParentsAfterParentReadyWithPinnedParentPublishCheck(
		filepath.Join("reports", writeTestFileName),
		[]byte("hello"),
		0o640,
		0o750,
		func(parentPath string, parentIdentity fs.FileInfo) error {
			publishChecks++
			if publishChecks == 2 {
				if err := os.Rename(originalParent, relocatedParent); err != nil {
					return err
				}
				if err := os.Mkdir(originalParent, 0o750); err != nil {
					return err
				}
			}
			return VerifyDirectoryIdentity(parentPath, parentIdentity)
		},
	)
	if err == nil || !strings.Contains(err.Error(), "directory identity changed") {
		t.Fatalf("expected commit-time parent identity error, got %v", err)
	}
	if publishChecks != 2 {
		t.Fatalf("expected initial and commit publish checks, got %d", publishChecks)
	}
	if _, statErr := os.Stat(filepath.Join(relocatedParent, writeTestFileName)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("expected relocated parent target to remain absent, got %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(originalParent, writeTestFileName)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("expected replacement parent target to remain absent, got %v", statErr)
	}
}

func TestWriteRootPinnedParentPublishCheckRejectsParentSwapBetweenFinalCheckAndRename(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory replacement semantics are covered on Unix")
	}

	rootDir := t.TempDir()
	originalParent := filepath.Join(rootDir, "reports")
	relocatedParent := filepath.Join(rootDir, "reports-relocated")
	replacementParent := originalParent
	if err := os.MkdirAll(originalParent, 0o750); err != nil {
		t.Fatalf("mkdir original parent: %v", err)
	}

	renameReadyCalls := 0
	originalRenameReady := writeFileRenameReadyFn
	writeFileRenameReadyFn = func() error {
		renameReadyCalls++
		if err := os.Rename(originalParent, relocatedParent); err != nil {
			return err
		}
		return os.Mkdir(originalParent, 0o750)
	}
	t.Cleanup(func() {
		writeFileRenameReadyFn = originalRenameReady
	})

	root := openTestWriteRoot(t, rootDir, OpenWriteRoot)
	publishChecks := 0
	err := root.WriteFileCreatingParentsAfterParentReadyWithPinnedParentPublishCheck(
		filepath.Join("reports", writeTestFileName),
		[]byte("hello"),
		0o640,
		0o750,
		func(parentPath string, parentIdentity fs.FileInfo) error {
			publishChecks++
			return VerifyDirectoryIdentity(parentPath, parentIdentity)
		},
	)
	if err == nil || !strings.Contains(err.Error(), "directory identity changed") {
		t.Fatalf("expected post-rename parent identity error, got %v", err)
	}
	if publishChecks != 1 {
		t.Fatalf("expected only initial publish check before rename-ready swap, got %d", publishChecks)
	}
	if renameReadyCalls != 1 {
		t.Fatalf("expected one rename-ready hook call, got %d", renameReadyCalls)
	}
	if _, statErr := os.Stat(filepath.Join(relocatedParent, writeTestFileName)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("expected relocated parent target to remain absent, got %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(replacementParent, writeTestFileName)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("expected replacement parent target to remain absent, got %v", statErr)
	}
}

func TestRenameAtDirectoryPathRequiresDirectChildrenAndMapsRenameError(t *testing.T) {
	parent := t.TempDir()
	if err := os.WriteFile(filepath.Join(parent, "temp"), []byte("hello"), 0o600); err != nil {
		t.Fatalf("write temp: %v", err)
	}
	if err := renameAtDirectoryPath(parent, "temp", "target"); err != nil {
		t.Fatalf("renameAtDirectoryPath returned error: %v", err)
	}
	assertFileContent(t, filepath.Join(parent, "target"), "hello")

	if err := renameAtDirectoryPath(parent, filepath.Join("nested", "temp"), "target"); err == nil || !strings.Contains(err.Error(), "direct children") {
		t.Fatalf("expected direct-child validation error, got %v", err)
	}

	err := renameAtDirectoryPath(parent, "missing", "other")
	var linkErr *os.LinkError
	if !errors.As(err, &linkErr) {
		t.Fatalf("expected mapped link error, got %T %[1]v", err)
	}
	if linkErr.Op != "renameat" || linkErr.Old != "missing" || linkErr.New != "other" {
		t.Fatalf("mapped link error = %#v", linkErr)
	}
}

func TestRenameAtPinnedDirectoryUsesPinnedParentAfterPathReplacement(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory replacement semantics are covered on Unix")
	}

	rootDir := t.TempDir()
	originalParent := filepath.Join(rootDir, "reports")
	relocatedParent := filepath.Join(rootDir, "reports-relocated")
	if err := os.MkdirAll(originalParent, 0o750); err != nil {
		t.Fatalf("mkdir original parent: %v", err)
	}
	if err := os.WriteFile(filepath.Join(originalParent, "temp"), []byte("hello"), 0o600); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	parent, err := OpenRoot(originalParent)
	if err != nil {
		t.Fatalf("open original parent root: %v", err)
	}
	defer func() {
		if closeErr := parent.Close(); closeErr != nil && !errors.Is(closeErr, os.ErrClosed) {
			t.Errorf("close parent root: %v", closeErr)
		}
	}()
	parentIdentity, err := parent.Lstat(".")
	if err != nil {
		t.Fatalf("stat pinned parent: %v", err)
	}

	if err := os.Rename(originalParent, relocatedParent); err != nil {
		t.Fatalf("relocate original parent: %v", err)
	}
	if err := os.Mkdir(originalParent, 0o750); err != nil {
		t.Fatalf("mkdir replacement parent: %v", err)
	}

	if err := renameAtPinnedDirectory(parent, parentIdentity, "temp", "target"); err != nil {
		t.Fatalf("renameAtPinnedDirectory returned error: %v", err)
	}
	assertFileContent(t, filepath.Join(relocatedParent, "target"), "hello")
	if _, statErr := os.Stat(filepath.Join(originalParent, "target")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("expected replacement parent target to remain absent, got %v", statErr)
	}
}

func TestRenameAtPinnedDirectoryRejectsInvalidPinnedParent(t *testing.T) {
	expectedInfo, changedInfo := writePinnedTargetInfoPair(t)
	renameCalls := 0
	root := &fakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			if name != "." {
				t.Fatalf("unexpected lstat target %q", name)
			}
			return changedInfo, nil
		},
		rename: func(string, string) error {
			renameCalls++
			return nil
		},
	}

	err := renameAtPinnedDirectory(root, expectedInfo, "temp", "target")
	if err == nil || !strings.Contains(err.Error(), "pinned parent identity changed") {
		t.Fatalf("expected pinned parent identity error, got %v", err)
	}
	if renameCalls != 0 {
		t.Fatalf("expected no rename after pinned parent identity mismatch, got %d", renameCalls)
	}
}

func TestRenameAtPinnedDirectoryRejectsNestedPathsBeforeRootLookup(t *testing.T) {
	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) {
			t.Fatal("expected direct-child validation before root lstat")
			return nil, nil
		},
	}

	err := renameAtPinnedDirectory(root, newPinnedTargetInfo(t, "parent"), filepath.Join("nested", "temp"), "target")
	if err == nil || !strings.Contains(err.Error(), "direct children") {
		t.Fatalf("expected direct-child validation error, got %v", err)
	}
}

func TestRenameAtPinnedDirectoryPropagatesRootLookupError(t *testing.T) {
	expectedErr := errors.New("stat pinned parent failure")
	root := &fakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			if name != "." {
				t.Fatalf("unexpected lstat target %q", name)
			}
			return nil, expectedErr
		},
		rename: func(string, string) error {
			t.Fatal("rename should not run after root lookup error")
			return nil
		},
	}

	err := renameAtPinnedDirectory(root, newPinnedTargetInfo(t, "parent"), "temp", "target")
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected root lookup error, got %v", err)
	}
}

func TestWriteFileAtRootWithPostWriteCheckRunsAfterCommit(t *testing.T) {
	rootDir := t.TempDir()
	root, err := OpenRoot(rootDir)
	if err != nil {
		t.Fatalf("OpenRoot returned error: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Errorf("close root: %v", closeErr)
		}
	})
	target := rootedTarget{rootAbs: rootDir, rel: writeTestFileName, abs: filepath.Join(rootDir, writeTestFileName)}

	postWriteCalls := 0
	err = writeFileAtRootWithPostWriteCheck(root, target, []byte("hello"), 0o640, func() error {
		postWriteCalls++
		assertFileContent(t, filepath.Join(rootDir, writeTestFileName), "hello")
		return nil
	})
	if err != nil {
		t.Fatalf("writeFileAtRootWithPostWriteCheck returned error: %v", err)
	}
	if postWriteCalls != 1 {
		t.Fatalf("expected one post-write check, got %d", postWriteCalls)
	}
}

func TestWriteAtomicReplacementWithPinnedTargetAndPostWriteCheckRunsAfterCommit(t *testing.T) {
	tempInfo := newPinnedTargetInfo(t, "temp")
	tempClosed := false
	target, targetData := newPinnedFallbackTargetFile(t, tempInfo, "before")
	root := newCommittedTargetValidationRoot(
		t,
		tempInfo,
		func() (fs.FileInfo, error) { return tempInfo, nil },
		func() error { return nil },
		func(string) error { return nil },
		&tempClosed,
	)

	postWriteCalls := 0
	err := writeAtomicReplacementWithPinnedTargetAndPostWriteCheck(root, writeTestFileName, []byte("after"), 0o600, target, false, func() error {
		postWriteCalls++
		if string(*targetData) != "before" {
			t.Fatalf("atomic rename path should not overwrite pinned fallback target, got %q", string(*targetData))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("writeAtomicReplacementWithPinnedTargetAndPostWriteCheck returned error: %v", err)
	}
	if !tempClosed {
		t.Fatal("expected temp file close after commit")
	}
	if postWriteCalls != 1 {
		t.Fatalf("expected one post-write check, got %d", postWriteCalls)
	}
}

func TestVerifyOverwrittenTargetRejectsUnsafeTargetStates(t *testing.T) {
	infoDir := t.TempDir()
	regularPath := filepath.Join(infoDir, "regular")
	if err := os.WriteFile(regularPath, []byte("x"), 0o600); err != nil {
		t.Fatalf("write regular info target: %v", err)
	}
	regularInfo := statTestPath(t, regularPath)
	dirInfo := statTestPath(t, infoDir)
	symlinkPath := filepath.Join(infoDir, "link")
	if err := os.Symlink(regularPath, symlinkPath); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	symlinkInfo, err := os.Lstat(symlinkPath)
	if err != nil {
		t.Fatalf("lstat symlink: %v", err)
	}
	statErr := errors.New("opened stat failure")

	cases := []struct {
		name     string
		pathInfo fs.FileInfo
		fileInfo fs.FileInfo
		statErr  error
		want     string
	}{
		{name: "symlink", pathInfo: symlinkInfo, fileInfo: regularInfo, want: "symlink"},
		{name: "directory", pathInfo: dirInfo, fileInfo: regularInfo, want: "not a regular file"},
		{name: "opened stat error", pathInfo: regularInfo, statErr: statErr, want: statErr.Error()},
		{name: "opened mismatch", pathInfo: regularInfo, fileInfo: dirInfo, want: "changed before validation"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := &fakeRoot{lstat: func(string) (fs.FileInfo, error) { return tc.pathInfo, nil }}
			file := &fakeFile{stat: func() (fs.FileInfo, error) { return tc.fileInfo, tc.statErr }}

			err := verifyOverwrittenTarget(root, writeTestFileName, file)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q error, got %v", tc.want, err)
			}
		})
	}
}

func TestWriteFileAtomicallyIfAbsentReadinessErrorPreventsPublish(t *testing.T) {
	rootDir := t.TempDir()
	root, err := OpenRoot(rootDir)
	if err != nil {
		t.Fatalf("OpenRoot returned error: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Errorf("close root: %v", closeErr)
		}
	})

	expectedErr := errors.New("publish readiness failed")
	originalReady := writeFilePublishReadyFn
	writeFilePublishReadyFn = func() error { return expectedErr }
	t.Cleanup(func() {
		writeFilePublishReadyFn = originalReady
	})

	err = writeFileAtomicallyIfAbsentAtRoot(root, writeTestFileName, []byte("hello"), 0o640)
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected publish readiness error, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(rootDir, writeTestFileName)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("expected target to remain absent, got %v", statErr)
	}
}

func TestWriteFileAtomicallyIfAbsentPropagatesPublishFailures(t *testing.T) {
	tempInfo := newPinnedTargetInfo(t, "temp")
	for _, tc := range []struct {
		name string
		root *fakeRoot
		want error
	}{
		{
			name: "link",
			want: errors.New("link failure"),
		},
		{
			name: "remove",
			want: errors.New("remove failure"),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.root = &fakeRoot{
				openFile: openTargetOrTempFile(writeTestFileName, func() (File, error) {
					t.Fatalf("target should not be opened")
					return nil, nil
				}, tempInfo, nil),
				link: func(string, string) error {
					if tc.name == "link" {
						return tc.want
					}
					return nil
				},
				remove: func(string) error {
					if tc.name == "remove" {
						return tc.want
					}
					return nil
				},
				lstat: func(string) (fs.FileInfo, error) { return tempInfo, nil },
			}

			err := writeFileAtomicallyIfAbsentAtRoot(tc.root, writeTestFileName, []byte("hello"), 0o640)
			if !errors.Is(err, tc.want) {
				t.Fatalf("expected %v, got %v", tc.want, err)
			}
		})
	}
}

func TestWriteFileAtomicallyIfAbsentPropagatesTempPreparationFailures(t *testing.T) {
	for _, tc := range []struct {
		name     string
		openErr  error
		writeErr error
		statErr  error
		want     error
	}{
		{name: "open", openErr: errors.New("open temp failure")},
		{name: "write", writeErr: errors.New("write temp failure")},
		{name: "stat", statErr: errors.New("stat temp failure")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := tc.openErr
			if want == nil {
				want = tc.writeErr
			}
			if want == nil {
				want = tc.statErr
			}
			root := &fakeRoot{
				openFile: func(string, int, os.FileMode) (File, error) {
					if tc.openErr != nil {
						return nil, tc.openErr
					}
					return &fakeFile{
						stat:  func() (fs.FileInfo, error) { return newPinnedTargetInfo(t, "temp"), tc.statErr },
						write: func(p []byte) (int, error) { return len(p), tc.writeErr },
						chmod: chmodWithoutError,
						close: closeWithoutError,
					}, nil
				},
				remove: func(string) error { return nil },
			}

			err := writeFileAtomicallyIfAbsentAtRoot(root, writeTestFileName, []byte("hello"), 0o640)
			if !errors.Is(err, want) {
				t.Fatalf("expected %v, got %v", want, err)
			}
		})
	}
}

func TestWriteAtomicReplacementWithPinnedTargetPermissionFallbackRunsPostWrite(t *testing.T) {
	info := newPinnedTargetInfo(t, "target")
	target, targetData := newPinnedFallbackTargetFile(t, info, "before")
	root := &fakeRoot{
		openFile: func(string, int, os.FileMode) (File, error) {
			return nil, os.ErrPermission
		},
		lstat: func(string) (fs.FileInfo, error) {
			return info, nil
		},
	}

	postWriteCalls := 0
	err := writeAtomicReplacementWithPinnedTargetAndPostWriteCheck(root, writeTestFileName, []byte("after"), 0o600, target, true, func() error {
		postWriteCalls++
		if string(*targetData) != "after" {
			t.Fatalf("expected fallback overwrite before post-write check, got %q", string(*targetData))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("writeAtomicReplacementWithPinnedTargetAndPostWriteCheck returned error: %v", err)
	}
	if postWriteCalls != 1 {
		t.Fatalf("expected one post-write check, got %d", postWriteCalls)
	}
}

func TestWriteAtomicReplacementWithPinnedTargetPermissionFallbackRejectsRollbackRequiredBeforeMutation(t *testing.T) {
	info := newPinnedTargetInfo(t, "target")
	target, targetData := newPinnedFallbackTargetFile(t, info, "before")
	root := &fakeRoot{
		openFile: func(string, int, os.FileMode) (File, error) {
			return nil, os.ErrPermission
		},
		lstat: func(name string) (fs.FileInfo, error) {
			t.Fatalf("rollback-required permission fallback should not lstat %s", name)
			return nil, nil
		},
	}
	postWriteCalls := 0

	err := writeAtomicReplacementWithPinnedTargetCallbacks(root, writeTestFileName, []byte("after"), 0o600, target, true, pinnedReplacementChecks{
		postWrite: func() error {
			postWriteCalls++
			return nil
		},
		rollbackOnPostWriteFailure: true,
	})
	if !errors.Is(err, os.ErrPermission) {
		t.Fatalf("expected original permission error, got %v", err)
	}
	if err == nil || !strings.Contains(err.Error(), "fallback replacement cannot roll back post-write failure") {
		t.Fatalf("expected rollback-required fallback rejection, got %v", err)
	}
	if postWriteCalls != 0 {
		t.Fatalf("expected post-write check to be skipped after fallback rejection, got %d", postWriteCalls)
	}
	if string(*targetData) != "before" {
		t.Fatalf("expected fallback target data to remain unchanged, got %q", string(*targetData))
	}
}

func TestWriteAtomicReplacementCommitReadyErrorPreventsRename(t *testing.T) {
	tempInfo := newPinnedTargetInfo(t, "temp")
	expectedErr := errors.New("not ready to commit")
	root := &fakeRoot{
		openFile: openTargetOrTempFile(writeTestFileName, func() (File, error) {
			t.Fatalf("target should not be opened")
			return nil, nil
		}, tempInfo, nil),
		rename: func(string, string) error {
			t.Fatal("rename should not run after commit readiness error")
			return nil
		},
		remove: func(string) error { return nil },
	}

	err := writeAtomicReplacementWithChecks(root, writeTestFileName, []byte("hello"), 0o640, atomicReplacementOptions{
		commitReady: func() error {
			return expectedErr
		},
	})
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected commit readiness error, got %v", err)
	}
}

func TestWriteAtomicReplacementRenameReadyErrorPreventsRename(t *testing.T) {
	tempInfo := newPinnedTargetInfo(t, "temp")
	expectedErr := errors.New("not ready to rename")
	root := &fakeRoot{
		openFile: openTargetOrTempFile(writeTestFileName, func() (File, error) {
			t.Fatalf("target should not be opened")
			return nil, nil
		}, tempInfo, nil),
		rename: func(string, string) error {
			t.Fatal("rename should not run after rename readiness error")
			return nil
		},
		remove: func(string) error { return nil },
	}

	originalReady := writeFileRenameReadyFn
	writeFileRenameReadyFn = func() error { return expectedErr }
	t.Cleanup(func() {
		writeFileRenameReadyFn = originalReady
	})

	err := writeAtomicReplacementWithChecks(root, writeTestFileName, []byte("hello"), 0o640, atomicReplacementOptions{})
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected rename readiness error, got %v", err)
	}
}

func TestWriteAtomicReplacementWithPinnedTargetCommitPermissionFallbackRunsPostWrite(t *testing.T) {
	info := newPinnedTargetInfo(t, "target")
	tempInfo := newPinnedTargetInfo(t, "temp")
	target, targetData := newPinnedFallbackTargetFile(t, info, "before")
	root := newCommitPermissionFallbackRoot(info, tempInfo, target)
	postWriteCalls := 0

	err := writeAtomicReplacementWithPinnedTargetAndPostWriteCheck(root, writeTestFileName, []byte("after"), 0o600, target, true, func() error {
		postWriteCalls++
		if string(*targetData) != "after" {
			t.Fatalf("expected fallback overwrite before post-write check, got %q", string(*targetData))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("writeAtomicReplacementWithPinnedTargetAndPostWriteCheck returned error: %v", err)
	}
	if postWriteCalls != 1 {
		t.Fatalf("expected one post-write check, got %d", postWriteCalls)
	}
}

func TestWriteAtomicReplacementWithPinnedTargetCommitPermissionFallbackRejectsRollbackRequiredBeforeMutation(t *testing.T) {
	info := newPinnedTargetInfo(t, "target")
	tempInfo := newPinnedTargetInfo(t, "temp")
	target, targetData := newPinnedFallbackTargetFile(t, info, "before")
	root := newCommitPermissionFallbackRoot(info, tempInfo, target)
	postWriteCalls := 0

	err := writeAtomicReplacementWithPinnedTargetCallbacks(root, writeTestFileName, []byte("after"), 0o600, target, true, pinnedReplacementChecks{
		postWrite: func() error {
			postWriteCalls++
			return nil
		},
		rollbackOnPostWriteFailure: true,
	})
	if !errors.Is(err, os.ErrPermission) {
		t.Fatalf("expected original permission error, got %v", err)
	}
	if err == nil || !strings.Contains(err.Error(), "fallback replacement cannot roll back post-write failure") {
		t.Fatalf("expected rollback-required fallback rejection, got %v", err)
	}
	if postWriteCalls != 0 {
		t.Fatalf("expected post-write check to be skipped after fallback rejection, got %d", postWriteCalls)
	}
	if string(*targetData) != "before" {
		t.Fatalf("expected fallback target data to remain unchanged, got %q", string(*targetData))
	}
}

func newCommitPermissionFallbackRoot(info, tempInfo fs.FileInfo, target File) *fakeRoot {
	return &fakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			if name == writeTestFileName {
				return info, nil
			}
			return tempInfo, nil
		},
		openFile: openTargetOrTempFile(writeTestFileName, func() (File, error) {
			return target, nil
		}, tempInfo, nil),
		rename: func(string, string) error { return os.ErrPermission },
		remove: func(string) error { return nil },
	}
}

func TestWriteFileIfAbsentAtRootWithPostWriteCheckExistingAndNilPostWrite(t *testing.T) {
	rootDir := t.TempDir()
	targetPath := filepath.Join(rootDir, writeTestFileName)
	if err := os.WriteFile(targetPath, []byte("existing"), 0o640); err != nil {
		t.Fatalf("seed target: %v", err)
	}
	root, err := OpenRoot(rootDir)
	if err != nil {
		t.Fatalf("OpenRoot returned error: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Errorf("close root: %v", closeErr)
		}
	})
	target := rootedTarget{rootAbs: rootDir, rel: writeTestFileName, abs: targetPath}
	if err := writeFileIfAbsentAtRootWithPostWriteCheck(root, target, []byte("new"), 0o640, nil); !errors.Is(err, os.ErrExist) {
		t.Fatalf("expected existing target error, got %v", err)
	}
	if err := os.Remove(targetPath); err != nil {
		t.Fatalf("remove seeded target: %v", err)
	}
	if err := writeFileIfAbsentAtRootWithPostWriteCheck(root, target, []byte("new"), 0o640, nil); err != nil {
		t.Fatalf("writeFileIfAbsentAtRootWithPostWriteCheck returned error: %v", err)
	}
	assertFileContent(t, targetPath, "new")
}

func TestWriteFileIfAbsentAtRootWithPostWriteCheckReturnsPostWriteError(t *testing.T) {
	rootDir := t.TempDir()
	root, err := OpenRoot(rootDir)
	if err != nil {
		t.Fatalf("OpenRoot returned error: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Errorf("close root: %v", closeErr)
		}
	})

	targetPath := filepath.Join(rootDir, writeTestFileName)
	target := rootedTarget{rootAbs: rootDir, rel: writeTestFileName, abs: targetPath}
	expectedErr := errors.New("post-write check failed")
	postWriteCalls := 0
	err = writeFileIfAbsentAtRootWithPostWriteCheck(root, target, []byte("created"), 0o640, func() error {
		postWriteCalls++
		assertFileContent(t, targetPath, "created")
		return expectedErr
	})
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected post-write error, got %v", err)
	}
	if postWriteCalls != 1 {
		t.Fatalf("expected one post-write call, got %d", postWriteCalls)
	}
}

func TestWriteRootPreWriteReadinessErrorPreventsCallerPreWriteAndWrite(t *testing.T) {
	rootDir := t.TempDir()
	root := openTestWriteRoot(t, rootDir, OpenWriteRoot)
	expectedErr := errors.New("root no longer ready")
	originalReady := writeFilePreWriteReadyFn
	writeFilePreWriteReadyFn = func() error { return expectedErr }
	t.Cleanup(func() {
		writeFilePreWriteReadyFn = originalReady
	})
	target := filepath.Join("reports", writeTestFileName)

	err := root.WriteFileCreatingParentsAfterParentReadyWithPreWriteCheck(target, []byte("hello"), 0o640, 0o750, func() error {
		return nil
	}, func() error {
		t.Fatal("preWrite should not run after readiness error")
		return nil
	})
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected readiness error, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(rootDir, target)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("expected target to remain absent, got %v", statErr)
	}
}

func TestWriteRootPreWriteErrorPreventsWrite(t *testing.T) {
	rootDir := t.TempDir()
	root := openTestWriteRoot(t, rootDir, OpenWriteRoot)
	expectedErr := errors.New("target changed before write")
	target := filepath.Join("reports", writeTestFileName)

	err := root.WriteFileCreatingParentsAfterParentReadyWithPreWriteCheck(target, []byte("hello"), 0o640, 0o750, func() error {
		return nil
	}, func() error {
		return expectedErr
	})
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected preWrite error, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(rootDir, target)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("expected target to remain absent, got %v", statErr)
	}
}

func TestWriteRootPreWriteCheckRejectsNonRelativeTarget(t *testing.T) {
	root := openTestWriteRoot(t, t.TempDir(), OpenWriteRoot)

	err := root.WriteFileCreatingParentsAfterParentReadyWithPreWriteCheck(filepath.Join(t.TempDir(), writeTestFileName), []byte("hello"), 0o640, 0o750, func() error {
		t.Fatal("parentReady should not run for invalid target")
		return nil
	}, func() error {
		t.Fatal("preWrite should not run for invalid target")
		return nil
	})
	if err == nil {
		t.Fatal("expected absolute target to be rejected")
	}
}

func TestWriteRootLstatAndRemoveRejectNonRelativeTargets(t *testing.T) {
	root := openTestWriteRoot(t, t.TempDir(), OpenWriteRoot)
	absoluteTarget := filepath.Join(t.TempDir(), writeTestFileName)

	if _, err := root.Lstat(absoluteTarget); err == nil {
		t.Fatal("expected Lstat absolute target to be rejected")
	}
	if err := root.Remove(absoluteTarget); err == nil {
		t.Fatal("expected Remove absolute target to be rejected")
	}
}

func TestWriteRootLstatAndRemove(t *testing.T) {
	rootDir := t.TempDir()
	target := filepath.Join(rootDir, writeTestFileName)
	if err := os.WriteFile(target, []byte("hello"), 0o640); err != nil {
		t.Fatalf("write target: %v", err)
	}
	root := openTestWriteRoot(t, rootDir, OpenWriteRoot)

	info, err := root.Lstat(writeTestFileName)
	if err != nil {
		t.Fatalf("Lstat returned error: %v", err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("expected regular file info, got %v", info.Mode())
	}
	if err := root.Remove(writeTestFileName); err != nil {
		t.Fatalf("Remove returned error: %v", err)
	}
	if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected target to be removed, got %v", err)
	}
}

func TestWriteRootCreatesMissingParentsAndWritesIfAbsent(t *testing.T) {
	assertWriteRootCreatesMissingParentsAndWrites(t, "WriteFileCreatingParentsIfAbsent", (*WriteRoot).WriteFileCreatingParentsIfAbsent)
}

func TestWriteRootCreatesMissingParentsAndPublishesAtomicallyIfAbsent(t *testing.T) {
	rootDir := t.TempDir()
	root := openTestWriteRoot(t, rootDir, OpenWriteRoot)
	target := filepath.Join("reports", writeTestFileName)
	data := []byte("complete cache object")

	if err := root.WriteFileCreatingParentsAtomicallyIfAbsent(target, data, 0o640, 0o750); err != nil {
		t.Fatalf("WriteFileCreatingParentsAtomicallyIfAbsent returned error: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(rootDir, target)); err != nil {
		t.Fatalf("read published target: %v", err)
	} else if !bytes.Equal(got, data) {
		t.Fatalf("published target = %q, want %q", got, data)
	}
	if err := root.WriteFileCreatingParentsAtomicallyIfAbsent(target, []byte("replacement"), 0o640, 0o750); !errors.Is(err, os.ErrExist) {
		t.Fatalf("second atomic publish error = %v, want exist", err)
	}
	if got, err := os.ReadFile(filepath.Join(rootDir, target)); err != nil {
		t.Fatalf("read preserved target: %v", err)
	} else if !bytes.Equal(got, data) {
		t.Fatalf("preserved target = %q, want %q", got, data)
	}
}

func TestWriteRootVerifyIdentity(t *testing.T) {
	rootDir := t.TempDir()
	root := openTestWriteRoot(t, rootDir, OpenWriteRoot)
	expected, err := os.Stat(rootDir)
	if err != nil {
		t.Fatalf("stat root: %v", err)
	}
	if err := root.VerifyIdentity(expected); err != nil {
		t.Fatalf("VerifyIdentity returned error: %v", err)
	}
	other, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatalf("stat different root: %v", err)
	}
	if err := root.VerifyIdentity(other); err == nil {
		t.Fatal("expected different root identity to be rejected")
	}
	t.Run("propagates root lookup errors", func(t *testing.T) {
		expectedErr := errors.New("root lookup failure")
		root := &WriteRoot{root: &fakeRoot{
			lstat: func(string) (fs.FileInfo, error) { return nil, expectedErr },
		}}

		if err := root.VerifyIdentity(expected); !errors.Is(err, expectedErr) {
			t.Fatalf("VerifyIdentity error = %v, want %v", err, expectedErr)
		}
	})
}

func TestVerifyDirectoryIdentity(t *testing.T) {
	rootDir := t.TempDir()
	expected, err := os.Stat(rootDir)
	if err != nil {
		t.Fatalf("stat root: %v", err)
	}
	if err := VerifyDirectoryIdentity(rootDir, expected); err != nil {
		t.Fatalf("VerifyDirectoryIdentity returned error: %v", err)
	}
	if err := VerifyDirectoryIdentity(t.TempDir(), expected); err == nil {
		t.Fatal("expected different directory identity to be rejected")
	}
	if err := VerifyDirectoryIdentity(filepath.Join(rootDir, "missing"), expected); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing directory identity error = %v, want not exist", err)
	}
}

func TestOpenOrCreatePinnedDirectory(t *testing.T) {
	rootDir := t.TempDir()
	root, err := OpenRootNoFollow(rootDir)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	defer func() {
		if err := root.Close(); err != nil {
			t.Fatalf("close root: %v", err)
		}
	}()

	child, err := OpenOrCreatePinnedDirectory(root, rootDir, "cache", 0o750)
	if err != nil {
		t.Fatalf("create pinned child: %v", err)
	}
	if err := child.Close(); err != nil {
		t.Fatalf("close created child: %v", err)
	}
	existing, err := OpenOrCreatePinnedDirectory(root, rootDir, "cache", 0o750)
	if err != nil {
		t.Fatalf("open existing pinned child: %v", err)
	}
	if err := existing.Close(); err != nil {
		t.Fatalf("close existing child: %v", err)
	}
	if err := os.WriteFile(filepath.Join(rootDir, "file"), []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("write non-directory child: %v", err)
	}
	if _, err := OpenOrCreatePinnedDirectory(root, rootDir, "file", 0o750); err == nil {
		t.Fatal("expected non-directory child to be rejected")
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(rootDir, "link")); err != nil {
		t.Skipf("create directory symlink: %v", err)
	}
	if _, err := OpenOrCreatePinnedDirectory(root, rootDir, "link", 0o750); err == nil {
		t.Fatal("expected symlinked child to be rejected")
	}
}

func TestOpenOrCreatePinnedDirectoryPropagatesFailures(t *testing.T) {
	expectedErr := errors.New("directory operation failed")
	info, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatalf("stat directory: %v", err)
	}

	if _, err := OpenOrCreatePinnedDirectory(&fakeRoot{lstat: func(string) (fs.FileInfo, error) { return nil, expectedErr }}, "/root", "child", 0o750); !errors.Is(err, expectedErr) {
		t.Fatalf("lstat error = %v, want %v", err, expectedErr)
	}
	missingChildRoot := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return nil, os.ErrNotExist },
		mkdir: func(string, os.FileMode) error { return expectedErr },
	}
	if _, err := OpenOrCreatePinnedDirectory(missingChildRoot, "/root", "child", 0o750); !errors.Is(err, expectedErr) {
		t.Fatalf("mkdir error = %v, want %v", err, expectedErr)
	}
	openFailureRoot := &fakeRoot{
		lstat:    func(string) (fs.FileInfo, error) { return info, nil },
		openRoot: func(string) (Root, error) { return nil, expectedErr },
	}
	if _, err := OpenOrCreatePinnedDirectory(openFailureRoot, "/root", "child", 0o750); !errors.Is(err, expectedErr) {
		t.Fatalf("open child error = %v, want %v", err, expectedErr)
	}
	t.Run("rejects child replaced while opening", func(t *testing.T) {
		changedInfo, err := os.Stat(t.TempDir())
		if err != nil {
			t.Fatalf("stat changed directory: %v", err)
		}
		closed := false
		openedChild := &fakeRoot{
			lstat: func(string) (fs.FileInfo, error) { return changedInfo, nil },
			close: func() error { closed = true; return nil },
		}
		root := &fakeRoot{
			lstat:    func(string) (fs.FileInfo, error) { return info, nil },
			openRoot: func(string) (Root, error) { return openedChild, nil },
		}

		if _, err := OpenOrCreatePinnedDirectory(root, "/root", "child", 0o750); err == nil || !strings.Contains(err.Error(), "directory changed while opening") {
			t.Fatalf("changed child error = %v, want directory identity rejection", err)
		}
		if !closed {
			t.Fatal("expected changed child handle to close")
		}
	})
	t.Run("closes child when identity check fails", func(t *testing.T) {
		closed := false
		openedChild := &fakeRoot{
			lstat: func(string) (fs.FileInfo, error) { return nil, expectedErr },
			close: func() error { closed = true; return nil },
		}
		root := &fakeRoot{
			lstat:    func(string) (fs.FileInfo, error) { return info, nil },
			openRoot: func(string) (Root, error) { return openedChild, nil },
		}

		if _, err := OpenOrCreatePinnedDirectory(root, "/root", "child", 0o750); !errors.Is(err, expectedErr) {
			t.Fatalf("opened child lstat error = %v, want %v", err, expectedErr)
		}
		if !closed {
			t.Fatal("expected child handle to close after identity check failure")
		}
	})
}

func TestWriteRootIfAbsentRejectsExistingTarget(t *testing.T) {
	assertWriteRootRejectsExistingTarget(t, "WriteFileCreatingParentsIfAbsent", (*WriteRoot).WriteFileCreatingParentsIfAbsent)
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
	for _, write := range []func(*WriteRoot, string, []byte, os.FileMode, os.FileMode) error{
		(*WriteRoot).WriteFileCreatingParentsIfAbsent,
		(*WriteRoot).WriteFileCreatingParentsAtomicallyIfAbsent,
	} {
		assertWriteRootRejectsNonRelativeTargets(t, write)
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

	withRuntimeGOOS(t, "darwin")
	expectedTmpTarget := filepath.Join(string(os.PathSeparator), "private", "tmp")
	tmpTarget, tmpOK := trustedRootAliasTarget(filepath.Join(string(os.PathSeparator), "tmp"))
	if !tmpOK || tmpTarget != expectedTmpTarget {
		t.Fatalf("expected /tmp alias target %q, got target=%q ok=%v", expectedTmpTarget, tmpTarget, tmpOK)
	}
	expectedVarTarget := filepath.Join(string(os.PathSeparator), "private", "var")
	varTarget, varOK := trustedRootAliasTarget(filepath.Join(string(os.PathSeparator), "var"))
	if !varOK || varTarget != expectedVarTarget {
		t.Fatalf("expected /var alias target %q, got target=%q ok=%v", expectedVarTarget, varTarget, varOK)
	}

	withRuntimeGOOS(t, "linux")
	tmpTarget, tmpOK = trustedRootAliasTarget(filepath.Join(string(os.PathSeparator), "tmp"))
	if tmpOK || tmpTarget != "" {
		t.Fatalf("expected trusted aliases to be disabled on linux, got target=%q ok=%v", tmpTarget, tmpOK)
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

func TestWriteFileIfAbsentAtRootPropagatesExclusiveCreateError(t *testing.T) {
	expectedErr := errors.New("exclusive create failure")
	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return nil, os.ErrNotExist },
		openFile: func(name string, flag int, perm os.FileMode) (File, error) {
			if name != writeTestFileName {
				t.Fatalf("unexpected create path: %s", name)
			}
			if flag != os.O_RDWR|os.O_CREATE|os.O_EXCL {
				t.Fatalf("unexpected create flags: %#x", flag)
			}
			return nil, expectedErr
		},
	}

	err := writeFileIfAbsentAtRoot(root, rootedTarget{rel: writeTestFileName}, []byte("hello"), 0o600)
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected exclusive create error, got %v", err)
	}
}

func TestWriteFileIfAbsentAtRootCreatesTargetExclusivelyWithoutLinking(t *testing.T) {
	targetInfo := newPinnedTargetInfo(t, "target")
	root, state := newExclusiveCreateRoot(t, targetInfo)

	err := writeFileIfAbsentAtRoot(root, rootedTarget{rel: writeTestFileName}, []byte("hello"), 0o640)
	if err != nil {
		t.Fatalf("expected exclusive create success, got %v", err)
	}
	assertExclusiveCreateSuccess(t, state)
}

func TestWriteFileIfAbsentAtRootRejectsInvalidCreatedTarget(t *testing.T) {
	openedInfo, changedInfo := writePinnedTargetInfoPair(t)
	cases := []struct {
		exclusiveCreatedTargetCase
	}{
		{exclusiveCreatedTargetCase{name: "opened nonregular", fileInfo: &modeOverrideFileInfo{FileInfo: openedInfo, mode: os.ModeDir | 0o755}, wantError: "not a regular file", cleanup: true}},
		{exclusiveCreatedTargetCase{name: "target symlink", fileInfo: openedInfo, pathInfo: &modeOverrideFileInfo{FileInfo: openedInfo, mode: os.ModeSymlink | 0o777}, wantError: "became a symlink"}},
		{exclusiveCreatedTargetCase{name: "target replaced", fileInfo: openedInfo, pathInfo: changedInfo, wantError: "changed before validation"}},
		{exclusiveCreatedTargetCase{name: "target lookup error", fileInfo: openedInfo, lstatErr: errors.New("target lookup failure"), wantError: "target lookup failure"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertExclusiveCreatedTargetRejected(t, tc.exclusiveCreatedTargetCase)
		})
	}
}

func TestWriteFileIfAbsentAtRootCleansUpCreatedTargetPreparationErrors(t *testing.T) {
	targetInfo := newPinnedTargetInfo(t, "target")
	cases := []struct {
		name     string
		statErr  error
		chmodErr error
		wantErr  error
	}{
		{name: "stat", statErr: errors.New("target stat failure")},
		{name: "chmod", chmodErr: errors.New("target chmod failure")},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			closed := false
			removed := false
			root := &fakeRoot{
				openFile: func(string, int, os.FileMode) (File, error) {
					return &fakeFile{
						stat:  func() (fs.FileInfo, error) { return targetInfo, tc.statErr },
						write: func(p []byte) (int, error) { return len(p), nil },
						chmod: func(os.FileMode) error { return tc.chmodErr },
						close: func() error { closed = true; return nil },
					}, nil
				},
				lstat:  func(string) (fs.FileInfo, error) { return nil, os.ErrNotExist },
				remove: func(string) error { removed = true; return nil },
			}

			err := writeFileIfAbsentAtRoot(root, rootedTarget{rel: writeTestFileName}, []byte("hello"), 0o600)
			if !errors.Is(err, tc.statErr) && !errors.Is(err, tc.chmodErr) {
				t.Fatalf("expected preparation error, got %v", err)
			}
			if !closed || !removed {
				t.Fatalf("expected created target cleanup, closed=%t removed=%t", closed, removed)
			}
		})
	}
}

func TestWriteFileIfAbsentAtRootReturnsExistWhenTargetAppearsAfterLookup(t *testing.T) {
	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return nil, os.ErrNotExist },
	}
	root.openFile = func(name string, flag int, perm os.FileMode) (File, error) {
		if name == writeTestFileName {
			return nil, os.ErrExist
		}
		t.Fatalf("unexpected create path: %s", name)
		return nil, nil
	}

	err := writeFileIfAbsentAtRoot(root, rootedTarget{rel: writeTestFileName}, []byte("hello"), 0o600)
	if !errors.Is(err, os.ErrExist) {
		t.Fatalf("expected os.ErrExist from exclusive create, got %v", err)
	}
}

func TestWriteFileIfAbsentAtRootRemovesFallbackTargetOnWriteError(t *testing.T) {
	expectedErr := errors.New("write target failure")
	assertWriteIfAbsentFallbackCleanup(t, expectedErr, func(targetClosed *bool) File {
		return &fakeFile{
			write: func([]byte) (int, error) { return 0, expectedErr },
			close: func() error {
				*targetClosed = true
				return nil
			},
			chmod: func(os.FileMode) error { return nil },
		}
	})
}

func TestWriteFileIfAbsentAtRootRemovesFallbackTargetOnCloseError(t *testing.T) {
	expectedErr := errors.New("close target failure")
	assertWriteIfAbsentFallbackCleanup(t, expectedErr, func(targetClosed *bool) File {
		return &fakeFile{
			write: func(p []byte) (int, error) { return len(p), nil },
			close: func() error {
				*targetClosed = true
				return expectedErr
			},
			chmod: func(os.FileMode) error { return nil },
		}
	})
}

func TestWriteRootRejectsNonRelativeTargets(t *testing.T) {
	assertWriteRootRejectsNonRelativeTargets(t, (*WriteRoot).WriteFileCreatingParents)
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
	tempInfo := newPinnedTargetInfo(t, "temp")
	expectedErr := errors.New("existing target close failure")
	root := &fakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			if name == writeTestFileName {
				return fileInfo, nil
			}
			return tempInfo, nil
		},
		openFile: func(name string, flag int, perm os.FileMode) (File, error) {
			if name != writeTestFileName {
				return &fakeFile{
					stat:  func() (fs.FileInfo, error) { return tempInfo, nil },
					write: func(p []byte) (int, error) { return len(p), nil },
					chmod: chmodWithoutError,
					close: closeWithoutError,
				}, nil
			}
			return &fakeFile{
				stat:  func() (fs.FileInfo, error) { return fileInfo, nil },
				close: func() error { return expectedErr },
			}, nil
		},
		rename: func(string, string) error { return nil },
		remove: func(string) error { return nil },
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
	tempInfo := newPinnedTargetInfo(t, "temp")
	if fakeTempFile, ok := tempFile.(*fakeFile); ok && fakeTempFile.stat == nil {
		fakeTempFile.stat = func() (fs.FileInfo, error) {
			return tempInfo, nil
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
	tempInfo := newPinnedTargetInfo(t, "temp")
	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) {
			return nil, os.ErrNotExist
		},
		openFile: func(name string, flag int, perm os.FileMode) (File, error) {
			return &fakeFile{
				stat:  func() (fs.FileInfo, error) { return tempInfo, nil },
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
	tempInfo := newPinnedTargetInfo(t, "temp")

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
				stat:  func() (fs.FileInfo, error) { return tempInfo, nil },
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
	tempInfo := newPinnedTargetInfo(t, "temp")
	closeErr := errors.New("pinned target close failure")

	root := &fakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			if name != writeTestFileName {
				t.Fatalf("unexpected lstat path: %s", name)
			}
			return tempInfo, nil
		},
		openFile: func(name string, flag int, perm os.FileMode) (File, error) {
			if name == writeTestFileName {
				return &fakeFile{
					stat:  func() (fs.FileInfo, error) { return info, nil },
					close: func() error { return closeErr },
				}, nil
			}
			return &fakeFile{
				stat:  func() (fs.FileInfo, error) { return tempInfo, nil },
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
	openTarget := func() (File, error) { return nil, expectedErr }
	tempInfo := newPinnedTargetInfo(t, "temp")
	root := &fakeRoot{
		openFile: openTargetOrTempFile(writeTestFileName, openTarget, tempInfo, nil),
		lstat: func(name string) (fs.FileInfo, error) {
			if name != writeTestFileName {
				t.Fatalf("unexpected lstat path: %s", name)
			}
			return tempInfo, nil
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

func TestWriteAtomicReplacementVerifiesCommittedTarget(t *testing.T) {
	tempInfo := newPinnedTargetInfo(t, "temp")
	tempClosed := false
	renameCalls := 0
	removeCalls := 0
	lstatCalls := 0
	lstat := func(string) (fs.FileInfo, error) {
		lstatCalls++
		return tempInfo, nil
	}
	rename := func(string, string) error {
		renameCalls++
		return nil
	}
	remove := func(string) error {
		removeCalls++
		return nil
	}
	closeFn := func() error {
		tempClosed = true
		return nil
	}
	root := newCommittedTargetTestRoot(t, tempInfo, lstat, rename, remove, closeFn)

	err := writeAtomicReplacement(root, writeTestFileName, []byte("after"), 0o600, statTestPath(t, t.TempDir()))
	if err != nil {
		t.Fatalf("expected committed target validation success, got %v", err)
	}
	if renameCalls != 1 {
		t.Fatalf("expected one rename attempt, got %d", renameCalls)
	}
	if lstatCalls != 1 {
		t.Fatalf("expected one committed target lstat, got %d", lstatCalls)
	}
	if !tempClosed {
		t.Fatal("expected temp file close before rename validation")
	}
	if removeCalls != 0 {
		t.Fatalf("expected no temp cleanup remove after successful rename, got %d", removeCalls)
	}
}

func TestWriteAtomicReplacementRejectsChangedCommittedTarget(t *testing.T) {
	tempInfo, changedInfo := writePinnedTargetInfoPair(t)
	tempClosed := false
	renameCalls := 0
	removeCalls := 0
	lstat := func(string) (fs.FileInfo, error) {
		return changedInfo, nil
	}
	rename := func(string, string) error {
		renameCalls++
		return nil
	}
	remove := func(string) error {
		removeCalls++
		return nil
	}
	closeFn := func() error {
		tempClosed = true
		return nil
	}
	root := newCommittedTargetTestRoot(t, tempInfo, lstat, rename, remove, closeFn)

	err := writeAtomicReplacement(root, writeTestFileName, []byte("after"), 0o600, statTestPath(t, t.TempDir()))
	if err == nil || !strings.Contains(err.Error(), "committed target changed before validation") {
		t.Fatalf("expected committed target validation error, got %v", err)
	}
	if renameCalls != 1 {
		t.Fatalf("expected one rename attempt, got %d", renameCalls)
	}
	if !tempClosed {
		t.Fatal("expected temp file close before committed target mismatch")
	}
	if removeCalls != 0 {
		t.Fatalf("expected no cleanup remove after committed target mismatch, got %d", removeCalls)
	}
}

func TestWriteAtomicReplacementLeavesCommittedTargetAfterPostWriteFailure(t *testing.T) {
	tempInfo := newPinnedTargetInfo(t, "temp")
	postWriteErr := errors.New("post-write validation failure")
	lstatCalls := 0
	removeCalls := 0
	concurrentReplacementRemoved := false
	root := newCommittedTargetTestRoot(t,
		tempInfo,
		func(string) (fs.FileInfo, error) {
			lstatCalls++
			return tempInfo, nil
		},
		func(string, string) error {
			return nil
		},
		func(string) error {
			removeCalls++
			concurrentReplacementRemoved = true
			return nil
		},
		closeWithoutError,
	)

	err := writeAtomicReplacementWithPostWriteCheck(root, writeTestFileName, []byte("after"), 0o600, statTestPath(t, t.TempDir()), func() error {
		return postWriteErr
	})
	if !errors.Is(err, postWriteErr) {
		t.Fatalf("expected post-write error, got %v", err)
	}
	if lstatCalls != 1 {
		t.Fatalf("expected only committed target validation lstat, got %d", lstatCalls)
	}
	if removeCalls != 0 {
		t.Fatalf("expected no cleanup remove after post-write failure, got %d", removeCalls)
	}
	if concurrentReplacementRemoved {
		t.Fatal("post-write failure cleanup removed a concurrent replacement")
	}
}

func TestWriteAtomicReplacementRollbackRetainsOwnedTargetWithoutPathMutation(t *testing.T) {
	tempInfo := newPinnedTargetInfo(t, "temp")
	postWriteErr := errors.New("post-write validation failure")
	removeCalls := 0
	lstatCalls := 0
	renameCalls := 0
	root := &fakeRoot{
		openFile: openTargetOrTempFile(writeTestFileName, func() (File, error) {
			t.Fatalf("target should not be opened")
			return nil, nil
		}, tempInfo, nil),
		lstat: func(name string) (fs.FileInfo, error) {
			lstatCalls++
			if name == writeTestFileName {
				return tempInfo, nil
			}
			t.Fatalf("unexpected lstat path: %s", name)
			return nil, nil
		},
		rename: func(string, string) error {
			renameCalls++
			return nil
		},
		remove: func(name string) error {
			removeCalls++
			t.Fatalf("rollback must not remove %s after post-write failure", name)
			return nil
		},
	}

	err := writeAtomicReplacementWithChecks(root, writeTestFileName, []byte("after"), 0o600, atomicReplacementOptions{
		postWrite: func() error {
			return postWriteErr
		},
		rollbackOnPostWriteFailure: true,
	})
	if !errors.Is(err, postWriteErr) {
		t.Fatalf("expected post-write error, got %v", err)
	}
	if renameCalls != 1 {
		t.Fatalf("expected only commit rename, got %d", renameCalls)
	}
	if lstatCalls != 1 {
		t.Fatalf("expected only committed-target validation lstat, got %d", lstatCalls)
	}
	if removeCalls != 0 {
		t.Fatalf("expected no path removal during rollback, got %d", removeCalls)
	}
}

func TestWriteAtomicReplacementRollbackPreservesConcurrentReplacementAfterStaleCheck(t *testing.T) {
	tempInfo := newPinnedTargetInfo(t, "temp")
	postWriteErr := errors.New("post-write validation failure")
	concurrentReplacementReady := false
	renameCalls := 0
	root := &fakeRoot{
		openFile: openTargetOrTempFile(writeTestFileName, func() (File, error) {
			t.Fatalf("target should not be opened")
			return nil, nil
		}, tempInfo, nil),
		lstat: func(name string) (fs.FileInfo, error) {
			if name != writeTestFileName {
				t.Fatalf("unexpected lstat path: %s", name)
			}
			return tempInfo, nil
		},
		rename: func(oldName, newName string) error {
			renameCalls++
			if concurrentReplacementReady {
				t.Fatalf("rollback must not rename concurrent replacement %s to %s", oldName, newName)
			}
			return nil
		},
		remove: func(name string) error {
			t.Fatalf("rollback must not remove concurrent replacement %s", name)
			return nil
		},
	}

	err := writeAtomicReplacementWithChecks(root, writeTestFileName, []byte("after"), 0o600, atomicReplacementOptions{
		postWrite: func() error {
			concurrentReplacementReady = true
			return postWriteErr
		},
		rollbackOnPostWriteFailure: true,
	})
	if !errors.Is(err, postWriteErr) {
		t.Fatalf("expected post-write error, got %v", err)
	}
	if renameCalls != 1 {
		t.Fatalf("expected only commit rename, got %d", renameCalls)
	}
}

func TestAtomicWriteSessionRollbackCommittedTargetWithErrorDoesNotPathMutate(t *testing.T) {
	primaryErr := errors.New("post-write validation failure")
	session := &atomicWriteSession{
		root: &fakeRoot{
			lstat: func(name string) (fs.FileInfo, error) {
				t.Fatalf("rollback should not lstat %s", name)
				return nil, nil
			},
			rename: func(oldName, newName string) error {
				t.Fatalf("rollback should not rename %s to %s", oldName, newName)
				return nil
			},
			remove: func(name string) error {
				t.Fatalf("rollback should not remove %s", name)
				return nil
			},
		},
		targetRel: writeTestFileName,
		tempInfo:  newPinnedTargetInfo(t, "temp"),
	}

	err := session.rollbackCommittedTargetWithError(primaryErr)
	if !errors.Is(err, primaryErr) {
		t.Fatalf("expected primary error, got %v", err)
	}
}

func TestRunFallbackPostWriteCheckNonRollbackContract(t *testing.T) {
	if err := runFallbackPostWriteCheck(nil, true, writeTestFileName); err != nil {
		t.Fatalf("expected nil post-write fallback check to succeed, got %v", err)
	}

	postWriteErr := errors.New("post-write validation failure")
	err := runFallbackPostWriteCheck(func() error {
		return postWriteErr
	}, false, writeTestFileName)
	if !errors.Is(err, postWriteErr) {
		t.Fatalf("expected post-write error, got %v", err)
	}
	if strings.Contains(err.Error(), "committed target changed before rollback") {
		t.Fatalf("non-rollback fallback check should not add rollback error, got %v", err)
	}

	err = runFallbackPostWriteCheck(func() error {
		return postWriteErr
	}, true, writeTestFileName)
	if !errors.Is(err, postWriteErr) {
		t.Fatalf("expected post-write error with rollback safety failure, got %v", err)
	}
	if err == nil || !strings.Contains(err.Error(), "committed target changed before rollback") {
		t.Fatalf("expected rollback safety error, got %v", err)
	}
}

func TestWriteAtomicReplacementReturnsCloseErrorBeforeRename(t *testing.T) {
	expectedErr := errors.New("close temp failure")
	renameCalls := 0
	lstatCalls := 0
	removeCalls := 0
	lstat := func(string) (fs.FileInfo, error) {
		lstatCalls++
		return newPinnedTargetInfo(t, "target"), nil
	}
	rename := func(string, string) error {
		renameCalls++
		return nil
	}
	remove := func(string) error {
		removeCalls++
		return nil
	}
	closeFn := func() error { return expectedErr }
	root := newCommittedTargetTestRoot(t, newPinnedTargetInfo(t, "temp"), lstat, rename, remove, closeFn)

	err := writeAtomicReplacement(root, writeTestFileName, []byte("after"), 0o600, statTestPath(t, t.TempDir()))
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected temp close error, got %v", err)
	}
	if renameCalls != 0 {
		t.Fatalf("expected no rename after close failure, got %d", renameCalls)
	}
	if lstatCalls != 0 {
		t.Fatalf("expected no lstat after close failure, got %d", lstatCalls)
	}
	if removeCalls != 1 {
		t.Fatalf("expected temp cleanup after close failure, got %d removes", removeCalls)
	}
}

func TestWriteAtomicReplacementWithPinnedTargetKeepsCommittedTargetIdentity(t *testing.T) {
	tempInfo := newPinnedTargetInfo(t, "temp")
	targetInfo := newPinnedTargetInfo(t, "target")
	tempClosed := false
	renameCalls := 0
	removeCalls := 0
	lstatTarget := func() (fs.FileInfo, error) { return tempInfo, nil }
	rename := func() error {
		renameCalls++
		return nil
	}
	remove := func(string) error {
		removeCalls++
		return nil
	}
	root := newCommittedTargetValidationRoot(t, tempInfo, lstatTarget, rename, remove, &tempClosed)

	target := &fakeFile{
		stat:  func() (fs.FileInfo, error) { return targetInfo, nil },
		close: closeWithoutError,
	}
	if err := writeAtomicReplacementWithPinnedTarget(root, writeTestFileName, []byte("after"), 0o600, target, false); err != nil {
		t.Fatalf("expected committed target validation success, got %v", err)
	}
	if renameCalls != 1 {
		t.Fatalf("expected one rename attempt, got %d", renameCalls)
	}
	if !tempClosed {
		t.Fatal("expected committed temp file to be closed")
	}
	if removeCalls != 0 {
		t.Fatalf("expected no cleanup remove after successful rename, got %d", removeCalls)
	}
}

func TestWriteAtomicReplacementWithPinnedTargetRejectsChangedCommittedTarget(t *testing.T) {
	tempInfo, changedInfo := writePinnedTargetInfoPair(t)
	tempClosed := false
	renameCalls := 0
	removeCalls := 0
	target, targetData := newPinnedFallbackTargetFile(t, changedInfo, "before")
	lstatTarget := func() (fs.FileInfo, error) { return changedInfo, nil }
	rename := func() error {
		renameCalls++
		return nil
	}
	remove := func(string) error {
		removeCalls++
		return nil
	}
	root := newCommittedTargetValidationRoot(t, tempInfo, lstatTarget, rename, remove, &tempClosed)

	err := writeAtomicReplacementWithPinnedTarget(root, writeTestFileName, []byte("after"), 0o600, target, false)
	if err == nil || !strings.Contains(err.Error(), "committed target changed before validation") {
		t.Fatalf("expected committed target validation error, got %v", err)
	}
	if renameCalls != 1 {
		t.Fatalf("expected one rename attempt, got %d", renameCalls)
	}
	if !tempClosed {
		t.Fatal("expected temp file to be closed during cleanup")
	}
	if removeCalls != 0 {
		t.Fatalf("expected no cleanup remove after committed target mismatch, got %d", removeCalls)
	}
	if string(*targetData) != "before" {
		t.Fatalf("expected no fallback overwrite after committed target mismatch, got %q", string(*targetData))
	}
}

func TestWriteAtomicReplacementWithPinnedTargetReturnsCommittedTargetLstatError(t *testing.T) {
	tempInfo := newPinnedTargetInfo(t, "temp")
	expectedErr := errors.New("lstat failure")
	tempClosed := false
	root := newCommittedTargetLstatErrorRoot(t, tempInfo, expectedErr, &tempClosed)

	target := &fakeFile{
		stat:  func() (fs.FileInfo, error) { return newPinnedTargetInfo(t, "target"), nil },
		close: closeWithoutError,
	}
	err := writeAtomicReplacementWithPinnedTarget(root, writeTestFileName, []byte("after"), 0o600, target, false)
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected committed target lstat error, got %v", err)
	}
	if !tempClosed {
		t.Fatal("expected temp file close during cleanup after lstat error")
	}
}

func TestWriteAtomicReplacementWithPinnedTargetReturnsCommittedTargetStatError(t *testing.T) {
	expectedErr := errors.New("stat failure")
	tempClosed := false
	lstatCalls := 0
	root := &fakeRoot{
		openFile: func(name string, flag int, perm os.FileMode) (File, error) {
			if !strings.HasPrefix(name, atomicTempPrefix) {
				t.Fatalf("unexpected open path: %s", name)
			}
			return &fakeFile{
				stat:  func() (fs.FileInfo, error) { return nil, expectedErr },
				write: func(p []byte) (int, error) { return len(p), nil },
				chmod: chmodWithoutError,
				close: func() error {
					tempClosed = true
					return nil
				},
			}, nil
		},
		lstat: func(string) (fs.FileInfo, error) {
			lstatCalls++
			return newPinnedTargetInfo(t, "target"), nil
		},
		rename: func(string, string) error { return nil },
		remove: func(string) error { return nil },
	}

	target := &fakeFile{
		stat:  func() (fs.FileInfo, error) { return newPinnedTargetInfo(t, "target"), nil },
		close: closeWithoutError,
	}
	err := writeAtomicReplacementWithPinnedTarget(root, writeTestFileName, []byte("after"), 0o600, target, false)
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected committed target stat error, got %v", err)
	}
	if lstatCalls != 0 {
		t.Fatalf("expected no committed target lstat after stat error, got %d", lstatCalls)
	}
	if !tempClosed {
		t.Fatal("expected temp file close during cleanup after stat error")
	}
}

func TestWriteAtomicReplacementWithPinnedTargetReturnsCloseErrorBeforeRename(t *testing.T) {
	expectedErr := errors.New("close temp failure")
	renameCalls := 0
	lstatCalls := 0
	removeCalls := 0
	tempInfo := newPinnedTargetInfo(t, "temp")
	targetPath := filepath.Join(t.TempDir(), writeTestFileName)
	if err := os.WriteFile(targetPath, []byte("before"), 0o640); err != nil {
		t.Fatalf("seed target: %v", err)
	}
	targetInfo := statTestPath(t, targetPath)
	root := &fakeRoot{
		openFile: func(name string, flag int, perm os.FileMode) (File, error) {
			if !strings.HasPrefix(name, atomicTempPrefix) {
				t.Fatalf("unexpected open path: %s", name)
			}
			return &fakeFile{
				stat:  func() (fs.FileInfo, error) { return tempInfo, nil },
				write: func(p []byte) (int, error) { return len(p), nil },
				chmod: chmodWithoutError,
				close: func() error { return expectedErr },
			}, nil
		},
		lstat: func(string) (fs.FileInfo, error) {
			lstatCalls++
			return targetInfo, nil
		},
		rename: func(string, string) error {
			renameCalls++
			return nil
		},
		remove: func(string) error {
			removeCalls++
			return nil
		},
	}
	target := &fakeFile{
		stat:  func() (fs.FileInfo, error) { return targetInfo, nil },
		close: closeWithoutError,
	}

	err := writeAtomicReplacementWithPinnedTarget(root, writeTestFileName, []byte("after"), 0o600, target, false)
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected temp close error, got %v", err)
	}
	if renameCalls != 0 {
		t.Fatalf("expected no rename after close failure, got %d", renameCalls)
	}
	if lstatCalls != 0 {
		t.Fatalf("expected no committed target lstat after close failure, got %d", lstatCalls)
	}
	if removeCalls != 1 {
		t.Fatalf("expected temp cleanup after close failure, got %d removes", removeCalls)
	}
	assertFileContent(t, targetPath, "before")
}

func TestAtomicWriteSessionVerifyCommittedTargetRejectsMissingSnapshot(t *testing.T) {
	session := &atomicWriteSession{
		root:      &fakeRoot{},
		targetRel: writeTestFileName,
	}

	err := session.verifyCommittedTarget()
	if err == nil || !strings.Contains(err.Error(), "temporary file info unavailable after commit") {
		t.Fatalf("expected missing temp snapshot error, got %v", err)
	}
}

func TestAtomicWriteSessionSnapshotAndCloseTempFileRejectsNonRegularTemp(t *testing.T) {
	session := &atomicWriteSession{
		targetRel: writeTestFileName,
		tempFile: &fakeFile{
			stat: func() (fs.FileInfo, error) {
				return &modeOverrideFileInfo{
					FileInfo: newPinnedTargetInfo(t, "temp"),
					mode:     os.ModeDir | 0o755,
				}, nil
			},
			close: func() error {
				t.Fatal("expected non-regular temp file to abort before close")
				return nil
			},
		},
	}

	err := session.snapshotAndCloseTempFile()
	if err == nil || !strings.Contains(err.Error(), "temporary file is not regular after commit") {
		t.Fatalf("expected non-regular temp file error, got %v", err)
	}
	if session.tempInfo != nil {
		t.Fatal("expected no temp snapshot after non-regular temp rejection")
	}
}

func TestAtomicWriteSessionVerifyCommittedTargetRejectsNonRegularSnapshot(t *testing.T) {
	session := &atomicWriteSession{
		targetRel: writeTestFileName,
		tempInfo: &modeOverrideFileInfo{
			FileInfo: newPinnedTargetInfo(t, "temp"),
			mode:     os.ModeDir | 0o755,
		},
	}

	err := session.verifyCommittedTarget()
	if err == nil || !strings.Contains(err.Error(), "temporary file is not regular after commit") {
		t.Fatalf("expected non-regular temp snapshot error, got %v", err)
	}
}

func TestAtomicWriteSessionVerifyCommittedTargetRejectsNonRegularCommittedTarget(t *testing.T) {
	tempInfo := newPinnedTargetInfo(t, "temp")
	session := &atomicWriteSession{
		root: &fakeRoot{
			lstat: func(string) (fs.FileInfo, error) {
				return &modeOverrideFileInfo{
					FileInfo: tempInfo,
					mode:     os.ModeDir | 0o755,
				}, nil
			},
		},
		targetRel: writeTestFileName,
		tempInfo:  tempInfo,
	}

	err := session.verifyCommittedTarget()
	if err == nil || !strings.Contains(err.Error(), "committed target changed before validation") {
		t.Fatalf("expected non-regular committed target error, got %v", err)
	}
}

func TestAtomicWriteSessionSnapshotAndCloseTempFileAllowsMissingTempFile(t *testing.T) {
	session := &atomicWriteSession{}

	if err := session.snapshotAndCloseTempFile(); err != nil {
		t.Fatalf("expected nil snapshotAndCloseTempFile error, got %v", err)
	}
}

func TestAtomicWriteSessionWriteAndCloseReturnsWriteError(t *testing.T) {
	expectedErr := errors.New("write failure")
	session := &atomicWriteSession{
		tempFile: &fakeFile{
			write: func([]byte) (int, error) { return 0, expectedErr },
			close: func() error {
				t.Fatal("expected writeAndClose to abort before close on write error")
				return nil
			},
		},
	}

	err := session.writeAndClose([]byte("after"), 0o600)
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected writeAndClose write error, got %v", err)
	}
	if session.tempFile == nil {
		t.Fatal("expected temp file handle to remain when writeAndClose aborts before close")
	}
}

func TestAtomicWriteSessionWriteAndClose(t *testing.T) {
	tempClosed := false
	session := &atomicWriteSession{
		tempFile: &fakeFile{
			write: func(p []byte) (int, error) { return len(p), nil },
			chmod: chmodWithoutError,
			close: func() error {
				tempClosed = true
				return nil
			},
		},
	}

	if err := session.writeAndClose([]byte("after"), 0o600); err != nil {
		t.Fatalf("expected writeAndClose success, got %v", err)
	}
	if !tempClosed {
		t.Fatal("expected writeAndClose to close the temp file")
	}
	if session.tempFile != nil {
		t.Fatal("expected writeAndClose to clear the temp file handle")
	}
}

func TestWriteAtomicReplacementReturnsCommittedTargetLstatError(t *testing.T) {
	expectedErr := errors.New("lstat failure")
	tempInfo := newPinnedTargetInfo(t, "temp")
	tempClosed := false
	root := newCommittedTargetLstatErrorRoot(t, tempInfo, expectedErr, &tempClosed)

	err := writeAtomicReplacement(root, writeTestFileName, []byte("after"), 0o600, statTestPath(t, t.TempDir()))
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected committed target lstat error, got %v", err)
	}
	if !tempClosed {
		t.Fatal("expected temp file close during committed target lstat failure")
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

	for _, tc := range unsafeTargetModeCases() {
		tt := struct {
			name     string
			pathInfo fs.FileInfo
			want     string
		}{
			name:     tc.name,
			pathInfo: &modeOverrideFileInfo{FileInfo: originalInfo, mode: tc.mode},
			want:     tc.want,
		}
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
	info := newPinnedTargetInfo(t, "before")
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
	info := newPinnedTargetInfo(t, "before")
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
	assertMovedFileResult(t, sourcePath, targetPath, "hello", "be moved away")
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
	assertMovedFileResult(t, sourcePath, targetPath, "copied", "be removed after copy fallback")
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
	for _, tc := range moveFallbackFailureCases() {
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
