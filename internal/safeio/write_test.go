package safeio

import (
	"bytes"
	"errors"
	"fmt"
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
	sourceChangedMsg    = "source changed"
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

func replaceFileAtPathWithDistinctIdentity(t *testing.T, path string, expected fs.FileInfo, replacement string) {
	t.Helper()

	dir := filepath.Dir(path)
	base := filepath.Base(path)
	replacementPath := filepath.Join(dir, "."+base+".replacement")
	originalPath := filepath.Join(dir, "."+base+".original")

	if err := os.WriteFile(replacementPath, []byte(replacement), 0o600); err != nil {
		t.Fatalf("write replacement file: %v", err)
	}
	if err := os.Rename(path, originalPath); err != nil {
		t.Fatalf("move original aside: %v", err)
	}
	if err := os.Rename(replacementPath, path); err != nil {
		t.Fatalf("publish replacement file: %v", err)
	}
	if err := os.Remove(originalPath); err != nil {
		t.Fatalf("remove displaced original: %v", err)
	}

	replacementInfo := statTestPath(t, path)
	if os.SameFile(expected, replacementInfo) {
		t.Fatalf("expected replacement at %s to change file identity", path)
	}
}

func newExclusiveCreateRoot(t *testing.T, targetInfo fs.FileInfo) (*fakeRoot, *exclusiveCreateState) {
	t.Helper()

	state := &exclusiveCreateState{}
	return &fakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			if strings.HasPrefix(filepath.Base(name), atomicTempPrefix) {
				return targetInfo, nil
			}
			if name != writeTestFileName {
				t.Fatalf("unexpected target lookup: %s", name)
			}
			state.lstatCalls++
			if state.lstatCalls == 1 {
				return nil, os.ErrNotExist
			}
			return targetInfo, nil
		},
		openFile: exclusiveCreateOpenFile(t, targetInfo, state),
		link:     atomicTempPublishLink(t, writeTestFileName),
		remove:   removeOnlyTempPath(t),
	}, state
}

func assertExclusiveCreateOpen(t *testing.T, name string, flag int, perm os.FileMode) {
	t.Helper()
	if !strings.HasPrefix(filepath.Base(name), atomicTempPrefix) {
		t.Fatalf("unexpected temp create path: %s", name)
	}
	if flag != os.O_RDWR|os.O_CREATE|os.O_EXCL {
		t.Fatalf("unexpected temp open flags: %#x", flag)
	}
	if perm != 0o640 {
		t.Fatalf("unexpected target perm: %#o", perm)
	}
}

func atomicTempPublishLink(t *testing.T, finalName string) func(string, string) error {
	t.Helper()
	return func(oldName, newName string) error {
		if !strings.HasPrefix(filepath.Base(oldName), atomicTempPrefix) {
			t.Fatalf("unexpected temp publish source: %s", oldName)
		}
		if newName != finalName && !strings.HasPrefix(filepath.Base(newName), atomicTempPrefix) {
			t.Fatalf("unexpected temp publish target: %s", newName)
		}
		return nil
	}
}

func exclusiveCreateOpenFile(t *testing.T, targetInfo fs.FileInfo, state *exclusiveCreateState) func(string, int, os.FileMode) (File, error) {
	t.Helper()
	return func(name string, flag int, perm os.FileMode) (File, error) {
		assertExclusiveCreateOpen(t, name, flag, perm)
		return exclusiveCreateFile(targetInfo, state), nil
	}
}

func exclusiveCreateFile(targetInfo fs.FileInfo, state *exclusiveCreateState) File {
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
		t.Fatalf("expected initial target lookup and post-publish revalidation, got %d lookups", state.lstatCalls)
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
		lstat: func(name string) (fs.FileInfo, error) {
			if strings.HasPrefix(filepath.Base(name), atomicTempPrefix) {
				return tc.fileInfo, nil
			}
			lstatCalls++
			if lstatCalls == 1 {
				return nil, os.ErrNotExist
			}
			return tc.pathInfo, tc.lstatErr
		},
		link:   atomicTempPublishLink(t, writeTestFileName),
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
			if strings.HasPrefix(filepath.Base(name), atomicTempPrefix) {
				return tempInfo, nil
			}
			if name != writeTestFileName {
				t.Fatalf("unexpected lstat path: %s", name)
			}
			return lstat(name)
		},
		link: func(oldName, newName string) error {
			if !strings.HasPrefix(filepath.Base(oldName), atomicTempPrefix) || !strings.HasPrefix(filepath.Base(newName), atomicTempPrefix) {
				t.Fatalf("unexpected identity-bound link %q -> %q", oldName, newName)
			}
			return nil
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
			if strings.HasPrefix(filepath.Base(name), atomicTempPrefix) {
				return targetFile(), nil
			}
			if name == writeTestFileName {
				return nil, errors.New("write-if-absent must not open final target for writing")
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
		t.Fatalf("expected cleanup for incomplete temp target, got %v", removed)
	}
	if !strings.HasPrefix(filepath.Base(removed[0]), atomicTempPrefix) {
		t.Fatalf("expected temp cleanup path, got %v", removed)
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

func TestWriteFileIfAbsentAtRootPropagatesTempCreateError(t *testing.T) {
	expectedErr := errors.New("temp create failure")
	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return nil, os.ErrNotExist },
		openFile: func(name string, flag int, perm os.FileMode) (File, error) {
			if !strings.HasPrefix(filepath.Base(name), atomicTempPrefix) {
				t.Fatalf("unexpected temp create path: %s", name)
			}
			if flag != os.O_RDWR|os.O_CREATE|os.O_EXCL {
				t.Fatalf("unexpected create flags: %#x", flag)
			}
			return nil, expectedErr
		},
	}

	err := writeFileIfAbsentAtRoot(root, rootedTarget{rel: writeTestFileName}, []byte("hello"), 0o600)
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected temp create error, got %v", err)
	}
}

func TestWriteFileIfAbsentAtRootCreatesTargetWithPreparedTempLink(t *testing.T) {
	targetInfo := newPinnedTargetInfo(t, "target")
	root, state := newExclusiveCreateRoot(t, targetInfo)

	err := writeFileIfAbsentAtRoot(root, rootedTarget{rel: writeTestFileName}, []byte("hello"), 0o640)
	if err != nil {
		t.Fatalf("expected exclusive create success, got %v", err)
	}
	assertExclusiveCreateSuccess(t, state)
}

func TestWriteFileAtomicallyIfAbsentAtRootPublishesCompletedTemp(t *testing.T) {
	tempInfo := newPinnedTargetInfo(t, "target")
	var events []string
	root := &fakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			if strings.HasPrefix(filepath.Base(name), atomicTempPrefix) {
				events = append(events, "lstat-temp")
				return tempInfo, nil
			}
			if name != writeTestFileName {
				t.Fatalf("unexpected target lookup: %s", name)
			}
			events = append(events, "lstat-target")
			return tempInfo, nil
		},
		openFile: func(name string, flag int, perm os.FileMode) (File, error) {
			if !strings.HasPrefix(filepath.Base(name), atomicTempPrefix) {
				t.Fatalf("unexpected temp create path: %s", name)
			}
			if flag != os.O_RDWR|os.O_CREATE|os.O_EXCL {
				t.Fatalf("unexpected temp open flags: %#x", flag)
			}
			events = append(events, "open-temp")
			return &fakeFile{
				stat: func() (fs.FileInfo, error) {
					events = append(events, "stat-temp")
					return tempInfo, nil
				},
				write: func(p []byte) (int, error) {
					events = append(events, "write-temp:"+string(p))
					return len(p), nil
				},
				chmod: func(mode os.FileMode) error {
					events = append(events, "chmod-temp")
					return nil
				},
				close: func() error {
					events = append(events, "close-temp")
					return nil
				},
			}, nil
		},
		link: func(oldName, newName string) error {
			if !strings.HasPrefix(filepath.Base(oldName), atomicTempPrefix) {
				t.Fatalf("unexpected publish link source %q -> %q", oldName, newName)
			}
			switch {
			case newName == writeTestFileName:
				events = append(events, "link-target")
			case strings.HasPrefix(filepath.Base(newName), atomicTempPrefix):
				events = append(events, "link-cleanup")
			default:
				t.Fatalf("unexpected publish link target %q -> %q", oldName, newName)
			}
			return nil
		},
		remove: func(name string) error {
			if !strings.HasPrefix(filepath.Base(name), atomicTempPrefix) {
				t.Fatalf("unexpected temp cleanup path: %s", name)
			}
			events = append(events, "remove-temp")
			return nil
		},
	}

	err := writeFileAtomicallyIfAbsentAtRoot(root, writeTestFileName, []byte("hello"), 0o640)
	if err != nil {
		t.Fatalf("expected atomic-if-absent publish success, got %v", err)
	}
	want := []string{"open-temp", "write-temp:hello", "chmod-temp", "stat-temp", "close-temp", "lstat-temp", "link-cleanup", "lstat-temp", "lstat-temp", "link-target", "lstat-target", "lstat-temp", "lstat-temp", "link-cleanup", "lstat-temp", "lstat-temp", "remove-temp", "lstat-temp", "remove-temp", "lstat-temp", "lstat-temp", "link-cleanup", "lstat-temp", "lstat-temp", "remove-temp", "lstat-temp", "remove-temp"}
	if strings.Join(events, ",") != strings.Join(want, ",") {
		t.Fatalf("unexpected publish order: got %v want %v", events, want)
	}
}

func TestWriteFileIfAbsentAtRootRejectsInvalidCreatedTarget(t *testing.T) {
	openedInfo, changedInfo := writePinnedTargetInfoPair(t)
	cases := []struct {
		exclusiveCreatedTargetCase
	}{
		{exclusiveCreatedTargetCase{name: "opened nonregular", fileInfo: &modeOverrideFileInfo{FileInfo: openedInfo, mode: os.ModeDir | 0o755}, wantError: "temporary file is not regular", cleanup: true}},
		{exclusiveCreatedTargetCase{name: "target symlink", fileInfo: openedInfo, pathInfo: &modeOverrideFileInfo{FileInfo: openedInfo, mode: os.ModeSymlink | 0o777}, wantError: committedTargetChangedBeforeValidation, cleanup: true}},
		{exclusiveCreatedTargetCase{name: "target replaced", fileInfo: openedInfo, pathInfo: changedInfo, wantError: "changed before validation", cleanup: true}},
		{exclusiveCreatedTargetCase{name: "target lookup error", fileInfo: openedInfo, lstatErr: errors.New("target lookup failure"), wantError: "target lookup failure", cleanup: true}},
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
	tempInfo := newPinnedTargetInfo(t, "temp")
	root := &fakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			if strings.HasPrefix(filepath.Base(name), atomicTempPrefix) {
				return tempInfo, nil
			}
			return nil, os.ErrNotExist
		},
	}
	root.openFile = func(name string, flag int, perm os.FileMode) (File, error) {
		if !strings.HasPrefix(filepath.Base(name), atomicTempPrefix) {
			t.Fatalf("unexpected temp create path: %s", name)
		}
		return &fakeFile{
			stat:  func() (fs.FileInfo, error) { return tempInfo, nil },
			write: func(p []byte) (int, error) { return len(p), nil },
			chmod: chmodWithoutError,
			close: closeWithoutError,
		}, nil
	}
	root.link = func(oldName, newName string) error {
		if !strings.HasPrefix(filepath.Base(oldName), atomicTempPrefix) {
			t.Fatalf("unexpected publish link source: %s", oldName)
		}
		if newName == writeTestFileName {
			return os.ErrExist
		}
		return nil
	}

	err := writeFileIfAbsentAtRoot(root, rootedTarget{rel: writeTestFileName}, []byte("hello"), 0o600)
	if !errors.Is(err, os.ErrExist) {
		t.Fatalf("expected os.ErrExist from publish link, got %v", err)
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
		link: func(oldName, newName string) error {
			if !strings.HasPrefix(filepath.Base(oldName), atomicTempPrefix) || !strings.HasPrefix(filepath.Base(newName), atomicTempPrefix) {
				t.Fatalf("unexpected identity-bound link %q -> %q", oldName, newName)
			}
			return nil
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
		lstat: func(name string) (fs.FileInfo, error) {
			if strings.HasPrefix(filepath.Base(name), atomicTempPrefix) {
				return tempInfo, nil
			}
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
		link: func(oldName, newName string) error {
			if !strings.HasPrefix(filepath.Base(oldName), atomicTempPrefix) || !strings.HasPrefix(filepath.Base(newName), atomicTempPrefix) {
				t.Fatalf("unexpected identity-bound link %q -> %q", oldName, newName)
			}
			return nil
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
			if strings.HasPrefix(filepath.Base(name), atomicTempPrefix) {
				return tempInfo, nil
			}
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
		link: func(oldName, newName string) error {
			if !strings.HasPrefix(filepath.Base(oldName), atomicTempPrefix) || !strings.HasPrefix(filepath.Base(newName), atomicTempPrefix) {
				t.Fatalf("unexpected identity-bound link %q -> %q", oldName, newName)
			}
			return nil
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
			if strings.HasPrefix(filepath.Base(name), atomicTempPrefix) {
				return tempInfo, nil
			}
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
		link: func(oldName, newName string) error {
			if !strings.HasPrefix(filepath.Base(oldName), atomicTempPrefix) || !strings.HasPrefix(filepath.Base(newName), atomicTempPrefix) {
				t.Fatalf("unexpected identity-bound link %q -> %q", oldName, newName)
			}
			return nil
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
			if strings.HasPrefix(filepath.Base(name), atomicTempPrefix) {
				return tempInfo, nil
			}
			if name != writeTestFileName {
				t.Fatalf("unexpected lstat path: %s", name)
			}
			return tempInfo, nil
		},
		link: func(oldName, newName string) error {
			if !strings.HasPrefix(filepath.Base(oldName), atomicTempPrefix) || !strings.HasPrefix(filepath.Base(newName), atomicTempPrefix) {
				t.Fatalf("unexpected identity-bound link %q -> %q", oldName, newName)
			}
			return nil
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
		t.Fatalf("expected publish rename only, got %d", renameCalls)
	}
	if lstatCalls != 1 {
		t.Fatalf("expected one committed target lstat, got %d", lstatCalls)
	}
	if !tempClosed {
		t.Fatal("expected temp file close before rename validation")
	}
	if removeCalls != 4 {
		t.Fatalf("expected identity-bound cleanup for original temp and staged link, got %d removes", removeCalls)
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
	if err == nil || !strings.Contains(err.Error(), committedTargetChangedBeforeValidation) {
		t.Fatalf("expected committed target validation error, got %v", err)
	}
	if renameCalls != 1 {
		t.Fatalf("expected publish rename only, got %d", renameCalls)
	}
	if !tempClosed {
		t.Fatal("expected temp file close before committed target mismatch")
	}
	if removeCalls != 4 {
		t.Fatalf("expected identity-bound cleanup without removing mismatched target, got %d removes", removeCalls)
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
		t.Fatalf("expected publish rename only, got %d", renameCalls)
	}
	if !tempClosed {
		t.Fatal("expected committed temp file to be closed")
	}
	if removeCalls != 4 {
		t.Fatalf("expected identity-bound cleanup for original temp and staged link, got %d removes", removeCalls)
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
	if err == nil || !strings.Contains(err.Error(), committedTargetChangedBeforeValidation) {
		t.Fatalf("expected committed target validation error, got %v", err)
	}
	if renameCalls != 1 {
		t.Fatalf("expected publish rename only, got %d", renameCalls)
	}
	if !tempClosed {
		t.Fatal("expected temp file to be closed during cleanup")
	}
	if removeCalls != 4 {
		t.Fatalf("expected identity-bound cleanup without removing mismatched target, got %d removes", removeCalls)
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

func TestAtomicWriteSessionCommitRejectsChangedTempPathBeforePublish(t *testing.T) {
	tempInfo, changedInfo := writePinnedTargetInfoPair(t)
	renameCalls := 0
	session := &atomicWriteSession{
		root: &fakeRoot{
			lstat: func(name string) (fs.FileInfo, error) {
				if !strings.HasPrefix(filepath.Base(name), atomicTempPrefix) {
					t.Fatalf("unexpected lstat path: %s", name)
				}
				return changedInfo, nil
			},
			link: func(oldName, newName string) error {
				if oldName != ".safeio-atomic-test" || !strings.HasPrefix(filepath.Base(newName), atomicTempPrefix) {
					t.Fatalf("unexpected identity-bound link %q -> %q", oldName, newName)
				}
				return nil
			},
			rename: func(string, string) error {
				renameCalls++
				return nil
			},
			remove: func(name string) error {
				if !strings.HasPrefix(filepath.Base(name), atomicTempPrefix) {
					t.Fatalf("unexpected cleanup path: %s", name)
				}
				return nil
			},
		},
		targetRel: writeTestFileName,
		tempRel:   ".safeio-atomic-test",
		tempInfo:  tempInfo,
	}

	err := session.commit()
	if err == nil || !strings.Contains(err.Error(), temporaryFileChangedBeforeCommit) {
		t.Fatalf("expected changed temp path rejection, got %v", err)
	}
	if renameCalls != 0 {
		t.Fatalf("expected no rename after temp substitution, got %d", renameCalls)
	}
}

func TestAtomicWriteSessionCommitRejectsHardLinksUnsupported(t *testing.T) {
	tempInfo := newPinnedTargetInfo(t, "temp")
	renameCalls := 0
	removeCalls := 0
	session := &atomicWriteSession{
		root: &rootWithoutIdentity{Root: &fakeRoot{
			mkdir: func(string, os.FileMode) error { return nil },
			lstat: func(name string) (fs.FileInfo, error) {
				switch name {
				case ".safeio-atomic-test", writeTestFileName:
					return tempInfo, nil
				default:
					t.Fatalf("unexpected lstat path: %s", name)
					return nil, os.ErrNotExist
				}
			},
			link: func(string, string) error {
				return errors.ErrUnsupported
			},
			open: func(string) (File, error) {
				return nil, errors.ErrUnsupported
			},
			rename: func(oldName, newName string) error {
				if oldName != ".safeio-atomic-test" || newName != writeTestFileName {
					t.Fatalf("unexpected direct rename %q -> %q", oldName, newName)
				}
				renameCalls++
				return nil
			},
			remove: func(string) error {
				removeCalls++
				return nil
			},
		}},
		targetRel: writeTestFileName,
		tempRel:   ".safeio-atomic-test",
		tempInfo:  tempInfo,
	}

	err := session.commit()
	if err == nil || !errors.Is(err, errIdentityBoundReplacementUnsupported) {
		t.Fatalf("expected identity-bound replacement unsupported error, got %v", err)
	}
	if renameCalls != 0 {
		t.Fatalf("expected no direct rename fallback, got %d", renameCalls)
	}
	if removeCalls != 1 {
		t.Fatalf("expected only quarantine directory cleanup, got %d removes", removeCalls)
	}
}

func TestAtomicWriteSessionCommitPreservesMismatchedTargetAfterRename(t *testing.T) {
	tempInfo, changedInfo := writePinnedTargetInfoPair(t)
	fixture := newAtomicCommitTargetMismatchFixture(t, tempInfo, changedInfo)
	session := fixture.session()

	err := session.commit()
	if err == nil || !strings.Contains(err.Error(), committedTargetChangedBeforeValidation) {
		t.Fatalf("expected committed target validation error, got %v", err)
	}
	fixture.assertRenameCalls(t, 1)
	fixture.assertRemoveCalls(t, 2)
	if session.tempRel != ".safeio-atomic-test" {
		t.Fatalf("expected original temp path to remain cleanup-owned, got %q", session.tempRel)
	}
}

func TestAtomicWriteSessionCommitPreservesConcurrentReplacementAfterRename(t *testing.T) {
	tempInfo, replacementInfo := writePinnedTargetInfoPair(t)
	fixture := newAtomicCommitTargetMismatchFixture(t, tempInfo, tempInfo)
	fixture.afterRename = func() { fixture.targetInfo = replacementInfo }
	fixture.remove = func(name string) error {
		if !strings.HasPrefix(filepath.Base(name), atomicTempPrefix) {
			t.Fatalf("must not remove concurrent replacement by pathname: %s", name)
		}
		fixture.removeCalls++
		return nil
	}
	session := fixture.session()

	err := session.commit()
	if err == nil || !strings.Contains(err.Error(), committedTargetChangedBeforeValidation) {
		t.Fatalf("expected committed target validation error, got %v", err)
	}
	fixture.assertRenameCalls(t, 1)
	fixture.assertRemoveCalls(t, 2)
	if !os.SameFile(fixture.targetInfo, replacementInfo) {
		t.Fatalf("expected concurrent replacement to remain published, got %v", fixture.targetInfo)
	}
	if session.tempRel != ".safeio-atomic-test" {
		t.Fatalf("expected original temp path to remain cleanup-owned, got %q", session.tempRel)
	}
}

type atomicCommitTargetMismatchFixture struct {
	t           *testing.T
	tempInfo    fs.FileInfo
	targetInfo  fs.FileInfo
	renameCalls int
	removeCalls int
	afterRename func()
	remove      func(string) error
}

func newAtomicCommitTargetMismatchFixture(t *testing.T, tempInfo, targetInfo fs.FileInfo) *atomicCommitTargetMismatchFixture {
	t.Helper()
	fixture := &atomicCommitTargetMismatchFixture{t: t, tempInfo: tempInfo, targetInfo: targetInfo}
	fixture.remove = func(string) error {
		fixture.removeCalls++
		return nil
	}
	return fixture
}

func (f *atomicCommitTargetMismatchFixture) session() *atomicWriteSession {
	return &atomicWriteSession{
		root: &fakeRoot{
			lstat:  f.lstat,
			link:   f.link,
			rename: f.rename,
			remove: f.remove,
		},
		targetRel: writeTestFileName,
		tempRel:   ".safeio-atomic-test",
		tempInfo:  f.tempInfo,
	}
}

func (f *atomicCommitTargetMismatchFixture) lstat(name string) (fs.FileInfo, error) {
	switch name {
	case ".safeio-atomic-test":
		return f.tempInfo, nil
	case writeTestFileName:
		return f.targetInfo, nil
	default:
		return f.lstatStagedTemp(name)
	}
}

func (f *atomicCommitTargetMismatchFixture) lstatStagedTemp(name string) (fs.FileInfo, error) {
	if strings.HasPrefix(filepath.Base(name), atomicTempPrefix) {
		return f.tempInfo, nil
	}
	f.t.Fatalf("unexpected lstat path: %s", name)
	return nil, os.ErrNotExist
}

func (f *atomicCommitTargetMismatchFixture) link(oldName, newName string) error {
	if !strings.HasPrefix(filepath.Base(newName), atomicTempPrefix) {
		f.t.Fatalf("unexpected identity-bound link %q -> %q", oldName, newName)
	}
	if oldName == ".safeio-atomic-test" || strings.HasPrefix(filepath.Base(oldName), atomicTempPrefix) {
		return nil
	}
	f.t.Fatalf("unexpected identity-bound link %q -> %q", oldName, newName)
	return nil
}

func (f *atomicCommitTargetMismatchFixture) rename(oldName, newName string) error {
	if strings.HasPrefix(filepath.Base(oldName), atomicTempPrefix) && strings.HasPrefix(filepath.Base(newName), atomicTempPrefix) {
		return nil
	}
	if !strings.HasPrefix(filepath.Base(oldName), atomicTempPrefix) || newName != writeTestFileName {
		f.t.Fatalf("unexpected rename %q -> %q", oldName, newName)
	}
	f.renameCalls++
	if f.afterRename != nil {
		f.afterRename()
	}
	return nil
}

func (f *atomicCommitTargetMismatchFixture) assertRenameCalls(t *testing.T, want int) {
	t.Helper()
	if f.renameCalls != want {
		t.Fatalf("expected %d rename attempts, got %d", want, f.renameCalls)
	}
}

func (f *atomicCommitTargetMismatchFixture) assertRemoveCalls(t *testing.T, want int) {
	t.Helper()
	if f.removeCalls != want {
		t.Fatalf("expected %d removes, got %d", want, f.removeCalls)
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
	if err == nil || !strings.Contains(err.Error(), committedTargetChangedBeforeValidation) {
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

func chmodNameWithoutError(t *testing.T, wantName string) func(string, os.FileMode) error {
	t.Helper()
	return func(name string, _ os.FileMode) error {
		if name != wantName {
			t.Fatalf("unexpected chmod path: %s", name)
		}
		return nil
	}
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

	withFileSystem(t, moveFallbackCopyFileSystem())

	if err := MoveFileUnder(rootDir, sourcePath, targetPath, 0o750, 0o640); err != nil {
		t.Fatalf("MoveFileUnder returned error: %v", err)
	}
	assertMovedFileResult(t, sourcePath, targetPath, "copied", "be removed after copy fallback")
}

func moveFallbackCopyFileSystem() FileSystem {
	return &fakeFileSystem{openRoot: func(name string) (Root, error) {
		root, err := (&osFileSystem{}).OpenRoot(name)
		if err != nil {
			return nil, err
		}
		return moveFallbackCopyRoot(root), nil
	}}
}

func moveFallbackCopyRoot(root Root) Root {
	directPublishAttempts := 0
	fallbackPublished := false
	return &fakeRoot{
		Root: root,
		rename: func(oldName, newName string) error {
			return moveFallbackCopyRename(root, oldName, newName, &directPublishAttempts, &fallbackPublished)
		},
	}
}

func moveFallbackCopyRename(root Root, oldName, newName string, attempts *int, fallbackPublished *bool) error {
	if strings.Contains(oldName, atomicTempPrefix) && strings.Contains(newName, atomicTempPrefix) {
		return root.Rename(oldName, newName)
	}
	if strings.Contains(oldName, atomicTempPrefix) && newName == "snapshots/final.json" {
		(*attempts)++
		if *attempts == 1 {
			return syscall.EXDEV
		}
		*fallbackPublished = true
		return root.Rename(oldName, newName)
	}
	if oldName == "snapshots/temp.json" && strings.Contains(newName, atomicTempPrefix) && *fallbackPublished {
		return root.Rename(oldName, newName)
	}
	return syscall.EXDEV
}

func lstatSourceOrTemp(t *testing.T, sourceInfo fs.FileInfo) func(string) (fs.FileInfo, error) {
	t.Helper()
	return func(name string) (fs.FileInfo, error) {
		if name != "source" && !strings.HasPrefix(filepath.Base(name), atomicTempPrefix) {
			t.Fatalf("unexpected lstat path: %s", name)
		}
		return sourceInfo, nil
	}
}

func assertIdentityBoundSourceLink(t *testing.T, oldName, newName string) {
	t.Helper()
	if (oldName != "source" && !strings.HasPrefix(filepath.Base(oldName), atomicTempPrefix)) || !strings.HasPrefix(filepath.Base(newName), atomicTempPrefix) {
		t.Fatalf("unexpected identity-bound link %q -> %q", oldName, newName)
	}
}

func identityBoundSourceLink(t *testing.T, linkErr error, calls *int) func(string, string) error {
	t.Helper()
	return func(oldName, newName string) error {
		(*calls)++
		assertIdentityBoundSourceLink(t, oldName, newName)
		return linkErr
	}
}

func TestMoveFileUnderReturnsRenameErrorWithoutFallback(t *testing.T) {
	rootDir := t.TempDir()
	sourcePath := filepath.Join(rootDir, "temp.json")
	targetPath := filepath.Join(rootDir, "final.json")
	renameErr := errors.New("rename failure")
	sourceInfo := processFileInfo()

	withFileSystem(t, &fakeFileSystem{openRoot: func(string) (Root, error) {
		return &fakeRoot{
			mkdirAll: func(string, os.FileMode) error {
				return nil
			},
			lstat: func(string) (fs.FileInfo, error) {
				return sourceInfo, nil
			},
			chmod: func(string, os.FileMode) error { return nil },
			link: func(oldName, newName string) error {
				if (oldName != "temp.json" && !strings.HasPrefix(filepath.Base(oldName), atomicTempPrefix)) || !strings.HasPrefix(filepath.Base(newName), atomicTempPrefix) {
					t.Fatalf("unexpected identity-bound link %q -> %q", oldName, newName)
				}
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
			if strings.Contains(oldName, atomicTempPrefix) && strings.Contains(newName, atomicTempPrefix) {
				return root.Rename(oldName, newName)
			}
			if !strings.Contains(oldName, atomicTempPrefix) || newName != filepath.Join("snapshots", "final.json") {
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
	chmodErr := errors.New("chmod failure")
	sourceInfo := processFileInfo()
	failingRoot := &fakeRoot{
		mkdirAll: func(string, os.FileMode) error { return nil },
		lstat: func(string) (fs.FileInfo, error) {
			return sourceInfo, nil
		},
		chmod: func(_ string, perm os.FileMode) error {
			if perm != 0o640 {
				t.Fatalf("unexpected chmod perm %#o", perm)
			}
			return chmodErr
		},
		rename: func(oldName, newName string) error {
			if oldName != "source" || newName != "target" {
				t.Fatalf("unexpected rename %q -> %q", oldName, newName)
			}
			t.Fatalf("unexpected rename %q -> %q", oldName, newName)
			return nil
		},
	}

	err := MoveFileWithinRoot(failingRoot, "source", "target", 0o750, 0o640)
	if !errors.Is(err, chmodErr) {
		t.Fatalf("expected chmod error without fallback copy, got %v", err)
	}
}

func TestMoveFileUnderRenamesUnreadableSource(t *testing.T) {
	if runtimeGOOS == "windows" {
		t.Skip("POSIX mode bits required")
	}

	rootDir := t.TempDir()
	sourcePath := filepath.Join(rootDir, "snapshots", "temp.json")
	targetPath := filepath.Join(rootDir, "snapshots", "final.json")
	if err := os.MkdirAll(filepath.Dir(sourcePath), 0o755); err != nil {
		t.Fatalf("create source dir: %v", err)
	}
	if err := os.WriteFile(sourcePath, []byte("hello"), 0o200); err != nil {
		t.Fatalf("write source file: %v", err)
	}
	if err := os.Chmod(sourcePath, 0o200); err != nil {
		t.Fatalf("make source unreadable: %v", err)
	}

	if err := MoveFileUnder(rootDir, sourcePath, targetPath, 0o750, 0o640); err != nil {
		t.Fatalf("MoveFileUnder returned error: %v", err)
	}
	assertMovedFileResult(t, sourcePath, targetPath, "hello", "be renamed away")
}

func TestMoveFileWithinRootSameSourceAndTargetPreservesFile(t *testing.T) {
	rootDir := t.TempDir()
	path := filepath.Join(rootDir, "same.txt")
	if err := os.WriteFile(path, []byte("same"), 0o600); err != nil {
		t.Fatalf("seed same-path source: %v", err)
	}
	root := openTestRoot(t, rootDir)

	if err := MoveFileWithinRoot(root, "same.txt", "same.txt", 0o750, 0o640); err != nil {
		t.Fatalf("MoveFileWithinRoot returned error: %v", err)
	}
	assertFileContent(t, path, "same")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat same-path file: %v", err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("expected same-path move to chmod to 0640, got %#o", info.Mode().Perm())
	}
	matches, err := filepath.Glob(filepath.Join(rootDir, atomicTempPrefix+"*"))
	if err != nil {
		t.Fatalf("glob temp files: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("same-path move leaked staging entries: %v", matches)
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
	sourcePath := filepath.Join(rootDir, "source")
	targetPath := filepath.Join(rootDir, "target")

	withMoveFallbackFileSystem(t, moveFallbackConfig{
		sourcePath:      "source",
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

func TestMoveFileWithinRootPreservesReplacedSourceAfterCopyFallback(t *testing.T) {
	state := newReplacedSourceMoveFallbackState(t)

	root := newReplacedSourceMoveFallbackRoot(t, state)
	err := MoveFileWithinRoot(root, "source", "target", 0o750, 0o640)
	if err == nil || !strings.Contains(err.Error(), "move source changed before cleanup") {
		t.Fatalf("expected changed source cleanup rejection, got %v", err)
	}
	if state.published != "original" {
		t.Fatalf("expected pinned original data to be published, got %q", state.published)
	}
	if !os.SameFile(state.sourceInfo, state.replacementInfo) {
		t.Fatalf("expected replacement source to remain published at source path, got %v", state.sourceInfo)
	}
	if state.removeCalls != 0 {
		t.Fatalf("expected replacement source not to be removed, got %d remove calls", state.removeCalls)
	}
}

func TestMoveFileWithinRootRejectsFallbackSourceReplacementAfterEXDEV(t *testing.T) {
	originalInfo, replacementInfo := writePinnedTargetInfoPair(t)
	fixture := newFallbackSourceReplacementAfterEXDEVFixture(t, originalInfo, replacementInfo)

	err := MoveFileWithinRoot(fixture.root(), "source", "target", 0o750, 0o640)
	if err == nil || !errors.Is(err, syscall.EXDEV) || !strings.Contains(err.Error(), "move source changed before fallback copy") {
		t.Fatalf("expected EXDEV plus fallback source replacement error, got %v", err)
	}
	fixture.assertFallbackSourceOpens(t, 1)
	fixture.assertTempOpens(t, 0)
}

type fallbackSourceReplacementAfterEXDEVFixture struct {
	t               *testing.T
	originalInfo    fs.FileInfo
	replacementInfo fs.FileInfo
	fallbackStarted bool
	openCalls       int
	tempOpenCalls   int
}

func newFallbackSourceReplacementAfterEXDEVFixture(t *testing.T, originalInfo, replacementInfo fs.FileInfo) *fallbackSourceReplacementAfterEXDEVFixture {
	t.Helper()
	return &fallbackSourceReplacementAfterEXDEVFixture{
		t:               t,
		originalInfo:    originalInfo,
		replacementInfo: replacementInfo,
	}
}

func (f *fallbackSourceReplacementAfterEXDEVFixture) root() *fakeRoot {
	return &fakeRoot{
		mkdirAll: func(string, os.FileMode) error { return nil },
		lstat:    f.lstat,
		chmod:    chmodNameWithoutError(f.t, "source"),
		link:     f.link,
		rename:   f.rename,
		remove:   f.remove,
		open:     f.open,
		openFile: f.openFile,
	}
}

func (f *fallbackSourceReplacementAfterEXDEVFixture) lstat(name string) (fs.FileInfo, error) {
	switch {
	case name == "source" && f.fallbackStarted:
		return f.replacementInfo, nil
	case name == "source":
		return f.originalInfo, nil
	case strings.HasPrefix(filepath.Base(name), atomicTempPrefix):
		return f.originalInfo, nil
	default:
		return nil, os.ErrNotExist
	}
}

func (f *fallbackSourceReplacementAfterEXDEVFixture) link(oldName, newName string) error {
	assertIdentityBoundSourceLink(f.t, oldName, newName)
	return nil
}

func (f *fallbackSourceReplacementAfterEXDEVFixture) rename(oldName, newName string) error {
	if strings.HasPrefix(filepath.Base(oldName), atomicTempPrefix) && strings.HasPrefix(filepath.Base(newName), atomicTempPrefix) {
		return nil
	}
	if !strings.HasPrefix(filepath.Base(oldName), atomicTempPrefix) || newName != "target" {
		f.t.Fatalf("unexpected rename %q -> %q", oldName, newName)
	}
	f.fallbackStarted = true
	return syscall.EXDEV
}

func (f *fallbackSourceReplacementAfterEXDEVFixture) remove(name string) error {
	if !strings.HasPrefix(filepath.Base(name), atomicTempPrefix) {
		f.t.Fatalf("unexpected cleanup path: %s", name)
	}
	return nil
}

func (f *fallbackSourceReplacementAfterEXDEVFixture) open(name string) (File, error) {
	if name != "source" {
		f.t.Fatalf("unexpected open path: %s", name)
	}
	f.openCalls++
	return f.replacedSourceFile(), nil
}

func (f *fallbackSourceReplacementAfterEXDEVFixture) replacedSourceFile() File {
	return &fakeFile{
		stat:  func() (fs.FileInfo, error) { return f.replacementInfo, nil },
		read:  f.failIfRead,
		close: closeWithoutError,
	}
}

func (f *fallbackSourceReplacementAfterEXDEVFixture) failIfRead([]byte) (int, error) {
	f.t.Fatal("fallback copy must stop before reading replaced source")
	return 0, nil
}

func (f *fallbackSourceReplacementAfterEXDEVFixture) openFile(string, int, os.FileMode) (File, error) {
	f.tempOpenCalls++
	return nil, errors.New("unexpected temp creation after fallback source replacement")
}

func (f *fallbackSourceReplacementAfterEXDEVFixture) assertFallbackSourceOpens(t *testing.T, want int) {
	t.Helper()
	if f.openCalls != want {
		t.Fatalf("expected %d fallback source opens, got %d", want, f.openCalls)
	}
}

func (f *fallbackSourceReplacementAfterEXDEVFixture) assertTempOpens(t *testing.T, want int) {
	t.Helper()
	if f.tempOpenCalls != want {
		t.Fatalf("expected %d temp opens, got %d", want, f.tempOpenCalls)
	}
}

type replacedSourceMoveFallbackState struct {
	originalInfo    fs.FileInfo
	replacementInfo fs.FileInfo
	tempInfo        fs.FileInfo
	sourceInfo      fs.FileInfo
	sourceStage     bool
	tempExists      bool
	tempStage       bool
	targetExists    bool
	removeCalls     int
	published       string
}

func newReplacedSourceMoveFallbackState(t *testing.T) *replacedSourceMoveFallbackState {
	t.Helper()

	originalInfo, replacementInfo := writePinnedTargetInfoPair(t)
	return &replacedSourceMoveFallbackState{
		originalInfo:    originalInfo,
		replacementInfo: replacementInfo,
		tempInfo:        newPinnedTargetInfo(t, "temp"),
		sourceInfo:      originalInfo,
	}
}

func newReplacedSourceMoveFallbackRoot(t *testing.T, state *replacedSourceMoveFallbackState) *fakeRoot {
	t.Helper()

	return &fakeRoot{
		mkdirAll: func(string, os.FileMode) error { return nil },
		lstat:    state.lstat,
		open:     state.openSource(t),
		chmod:    chmodNameWithoutError(t, "source"),
		openFile: func(name string, _ int, _ os.FileMode) (File, error) {
			return state.openTemp(t, name)
		},
		link:   state.link(t),
		rename: state.rename(t),
		remove: state.remove(t),
	}
}

func (s *replacedSourceMoveFallbackState) lstat(name string) (fs.FileInfo, error) {
	if name == "source" {
		return s.sourceInfo, nil
	}
	if isMoveFallbackTempPath(name) {
		return s.tempPathInfo()
	}
	if name == "target" && s.targetExists {
		return s.tempInfo, nil
	}
	return nil, os.ErrNotExist
}

func (s *replacedSourceMoveFallbackState) tempPathInfo() (fs.FileInfo, error) {
	if s.sourceStage {
		return s.sourceInfo, nil
	}
	if s.tempStage || s.tempExists {
		return s.tempInfo, nil
	}
	return nil, os.ErrNotExist
}

func (s *replacedSourceMoveFallbackState) openSource(t *testing.T) func(string) (File, error) {
	t.Helper()

	return func(name string) (File, error) {
		if name != "source" {
			t.Fatalf("unexpected source open %q", name)
		}
		reader := strings.NewReader("original")
		return &fakeFile{
			read: func(p []byte) (int, error) {
				s.sourceInfo = s.replacementInfo
				return reader.Read(p)
			},
			stat:  func() (fs.FileInfo, error) { return s.originalInfo, nil },
			chmod: chmodWithoutError,
			close: closeWithoutError,
		}, nil
	}
}

func (s *replacedSourceMoveFallbackState) openTemp(t *testing.T, name string) (File, error) {
	t.Helper()

	if !isMoveFallbackTempPath(name) {
		t.Fatalf("unexpected temp open %q", name)
	}
	s.tempExists = true
	return &fakeFile{
		write: func(p []byte) (int, error) {
			s.published += string(p)
			return len(p), nil
		},
		stat:  func() (fs.FileInfo, error) { return s.tempInfo, nil },
		chmod: chmodWithoutError,
		close: closeWithoutError,
	}, nil
}

func (s *replacedSourceMoveFallbackState) link(t *testing.T) func(string, string) error {
	t.Helper()

	return func(oldName, newName string) error {
		return s.linkReplacedSourceMoveFallback(t, oldName, newName)
	}
}

func (s *replacedSourceMoveFallbackState) linkReplacedSourceMoveFallback(t *testing.T, oldName, newName string) error {
	t.Helper()
	if isMoveFallbackTempPath(oldName) && s.sourceStage && newName == "source" {
		s.sourceStage = false
		return nil
	}
	if !isMoveFallbackTempPath(newName) {
		t.Fatalf("unexpected identity-bound link target %q", newName)
	}
	return s.linkReplacedSourceMoveFallbackTempTarget(t, oldName)
}

func (s *replacedSourceMoveFallbackState) linkReplacedSourceMoveFallbackTempTarget(t *testing.T, oldName string) error {
	t.Helper()
	if s.linkReplacedSourceFromSource(oldName) {
		return nil
	}
	if s.linkReplacedSourceFromStagedSource(oldName) {
		return nil
	}
	if s.linkReplacedSourceFromTemp(oldName) {
		return nil
	}
	t.Fatalf("unexpected identity-bound link source %q", oldName)
	return nil
}

func (s *replacedSourceMoveFallbackState) linkReplacedSourceFromSource(oldName string) bool {
	if oldName != "source" {
		return false
	}
	s.sourceStage = true
	return true
}

func (s *replacedSourceMoveFallbackState) linkReplacedSourceFromStagedSource(oldName string) bool {
	return isMoveFallbackTempPath(oldName) && s.sourceStage
}

func (s *replacedSourceMoveFallbackState) linkReplacedSourceFromTemp(oldName string) bool {
	if !isMoveFallbackTempPath(oldName) || !s.tempExists {
		return false
	}
	s.tempStage = true
	return true
}

func (s *replacedSourceMoveFallbackState) rename(t *testing.T) func(string, string) error {
	t.Helper()

	return func(oldName, newName string) error {
		return s.renameReplacedSourceMoveFallback(t, oldName, newName)
	}
}

func (s *replacedSourceMoveFallbackState) renameReplacedSourceMoveFallback(t *testing.T, oldName, newName string) error {
	t.Helper()
	if oldName == "source" && isMoveFallbackTempPath(newName) {
		s.sourceStage = true
		return nil
	}
	if isMoveFallbackTempPath(oldName) && isMoveFallbackTempPath(newName) {
		return s.renameReplacedSourceStage()
	}
	if isMoveFallbackTempPath(oldName) && newName == "source" {
		s.sourceStage = false
		return nil
	}
	return s.renameReplacedSourceTarget(t, oldName, newName)
}

func (s *replacedSourceMoveFallbackState) renameReplacedSourceStage() error {
	if s.sourceStage {
		return nil
	}
	if s.tempStage {
		s.tempStage = false
	}
	return nil
}

func (s *replacedSourceMoveFallbackState) renameReplacedSourceTarget(t *testing.T, oldName, newName string) error {
	t.Helper()
	if isMoveFallbackTempPath(oldName) && s.sourceStage && newName == "target" {
		return syscall.EXDEV
	}
	if !isMoveFallbackTempPath(oldName) || !s.tempStage || newName != "target" {
		t.Fatalf("unexpected rename %q -> %q", oldName, newName)
	}
	s.tempExists = false
	s.tempStage = false
	s.targetExists = true
	return nil
}

func (s *replacedSourceMoveFallbackState) remove(t *testing.T) func(string) error {
	t.Helper()

	return func(name string) error {
		return s.removeReplacedSourceMoveFallback(t, name)
	}
}

func (s *replacedSourceMoveFallbackState) removeReplacedSourceMoveFallback(t *testing.T, name string) error {
	t.Helper()
	if isMoveFallbackTempPath(name) {
		return s.removeReplacedSourceTempPath()
	}
	if name != "source" {
		t.Fatalf("unexpected removal %q", name)
	}
	s.removeCalls++
	return nil
}

func (s *replacedSourceMoveFallbackState) removeReplacedSourceTempPath() error {
	if s.sourceStage {
		s.sourceStage = false
		return nil
	}
	s.tempExists = false
	s.tempStage = false
	return nil
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
	sourceInfo      fs.FileInfo
	tempInfo        fs.FileInfo
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
	sourceData        string
	sourceExists      bool
	sourceStageExists bool
	sourceStagePath   string
	sourceCleanupPath string
	sourceOpenCalls   int
	sourceCloseCalls  int
	tempData          string
	tempExists        bool
	tempStageExists   bool
	targetData        string
	targetExists      bool
}

func withMoveFallbackFileSystem(t *testing.T, cfg moveFallbackConfig) {
	t.Helper()
	withFileSystem(t, &fakeFileSystem{openRoot: func(string) (Root, error) {
		return newMoveFallbackRoot(cfg), nil
	}})
}

func newMoveFallbackRoot(cfg moveFallbackConfig) *fakeRoot {
	cfg = withMoveFallbackDefaultInfos(cfg)
	state := newMoveFallbackState(cfg)
	return &fakeRoot{
		open:     newMoveFallbackOpenHook(cfg, state),
		openFile: newMoveFallbackOpenFileHook(cfg, state),
		lstat:    newMoveFallbackLstatHook(cfg, state),
		chmod: func(string, os.FileMode) error {
			return nil
		},
		mkdirAll: func(string, os.FileMode) error { return nil },
		link:     newMoveFallbackLinkHook(cfg, state),
		rename:   newMoveFallbackRenameHook(cfg, state),
		remove:   newMoveFallbackRemoveHook(cfg, state),
		close:    func() error { return cfg.rootCloseErr },
	}
}

func withMoveFallbackDefaultInfos(cfg moveFallbackConfig) moveFallbackConfig {
	if cfg.sourceInfo == nil {
		cfg.sourceInfo = processFileInfo()
	}
	if cfg.tempInfo == nil {
		cfg.tempInfo = processFileInfo()
	}
	return cfg
}

func processFileInfo() fs.FileInfo {
	info, err := os.Stat(os.Args[0])
	if err != nil {
		return nil
	}
	return info
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

func newMoveFallbackLstatHook(cfg moveFallbackConfig, state *moveFallbackState) func(string) (fs.FileInfo, error) {
	return func(name string) (fs.FileInfo, error) {
		switch {
		case name == cfg.sourcePath && state.sourceExists:
			return cfg.sourceInfo, nil
		case name == state.sourceStagePath || name == state.sourceCleanupPath:
			return cfg.sourceInfo, nil
		case isMoveFallbackTempPath(name) && (state.tempExists || state.tempStageExists):
			return cfg.tempInfo, nil
		case state.targetExists:
			return cfg.tempInfo, nil
		default:
			return nil, os.ErrNotExist
		}
	}
}

func newMoveFallbackOpenHook(cfg moveFallbackConfig, state *moveFallbackState) func(string) (File, error) {
	return func(name string) (File, error) {
		if name != cfg.sourcePath {
			return nil, errors.New("unexpected source open path")
		}
		state.sourceOpenCalls++
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
		close: func() error {
			state.sourceCloseCalls++
			if state.sourceCloseCalls > 0 {
				return cfg.sourceCloseErr
			}
			return nil
		},
		stat:  func() (fs.FileInfo, error) { return cfg.sourceInfo, nil },
		chmod: chmodWithoutError,
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
		stat:  func() (fs.FileInfo, error) { return cfg.tempInfo, nil },
	}
}

func newMoveFallbackLinkHook(cfg moveFallbackConfig, state *moveFallbackState) func(string, string) error {
	return func(oldName, newName string) error {
		return state.linkMoveFallbackName(cfg, oldName, newName)
	}
}

func (s *moveFallbackState) linkMoveFallbackName(cfg moveFallbackConfig, oldName, newName string) error {
	if oldName == s.sourceStagePath && newName == cfg.sourcePath {
		s.sourceExists = true
		return nil
	}
	if !isMoveFallbackTempPath(newName) {
		return errors.New("unexpected identity-bound link target")
	}
	return s.linkMoveFallbackTempTarget(cfg, oldName, newName)
}

func (s *moveFallbackState) linkMoveFallbackTempTarget(cfg moveFallbackConfig, oldName, newName string) error {
	switch {
	case oldName == cfg.sourcePath:
		s.sourceStageExists = true
		s.sourceStagePath = newName
	case oldName == s.sourceStagePath && s.sourceStageExists:
		s.sourceCleanupPath = newName
	case isMoveFallbackTempPath(oldName) && s.tempExists:
		s.tempStageExists = true
	case isMoveFallbackTempPath(oldName):
	default:
		return errors.New("unexpected identity-bound link source")
	}
	return nil
}

func newMoveFallbackRenameHook(cfg moveFallbackConfig, state *moveFallbackState) func(string, string) error {
	return func(oldName, newName string) error {
		return state.renameMoveFallbackName(cfg, oldName, newName)
	}
}

func (s *moveFallbackState) renameMoveFallbackName(cfg moveFallbackConfig, oldName, newName string) error {
	if oldName == cfg.sourcePath && isMoveFallbackTempPath(newName) {
		s.stageSourceForMove(newName)
		return nil
	}
	if oldName == s.sourceStagePath && isMoveFallbackTempPath(newName) {
		s.sourceStageExists = false
		s.sourceStagePath = ""
		s.sourceCleanupPath = newName
		return nil
	}
	if isMoveFallbackTempPath(oldName) && isMoveFallbackTempPath(newName) {
		s.tempStageExists = false
		return nil
	}
	if isMoveFallbackTempPath(oldName) && newName == cfg.sourcePath {
		s.restoreSourceAfterMoveCleanup()
		return nil
	}
	return s.renameMoveFallbackTarget(cfg, oldName, newName)
}

func (s *moveFallbackState) stageSourceForMove(newName string) {
	s.sourceExists = false
	s.sourceStageExists = true
	s.sourceStagePath = newName
	s.sourceCleanupPath = newName
}

func (s *moveFallbackState) restoreSourceAfterMoveCleanup() {
	s.sourceExists = true
	s.sourceStageExists = false
	s.sourceStagePath = ""
	s.sourceCleanupPath = ""
}

func (s *moveFallbackState) renameMoveFallbackTarget(cfg moveFallbackConfig, oldName, newName string) error {
	if oldName == s.sourceStagePath && s.sourceStageExists {
		return syscall.EXDEV
	}
	if !isMoveFallbackTempPath(oldName) || !s.tempStageExists {
		return nil
	}
	if err := cfg.failure(moveFallbackFailTempRename); err != nil {
		return err
	}
	s.publishMoveFallbackTemp()
	return nil
}

func (s *moveFallbackState) publishMoveFallbackTemp() {
	s.targetData = s.tempData
	s.targetExists = true
	s.tempData = ""
	s.tempExists = false
	s.tempStageExists = false
}

func newMoveFallbackRemoveHook(cfg moveFallbackConfig, state *moveFallbackState) func(string) error {
	return func(name string) error {
		return state.removeMoveFallbackName(cfg, name)
	}
}

func (s *moveFallbackState) removeMoveFallbackName(cfg moveFallbackConfig, name string) error {
	switch {
	case name == cfg.sourcePath && s.sourceExists:
		return s.removeMoveFallbackSource(cfg.sourceRemoveErr)
	case name == s.sourceCleanupPath && s.sourceCleanupPath != "":
		s.sourceCleanupPath = ""
		return nil
	case name == s.sourceStagePath && s.sourceStageExists:
		return s.removeMoveFallbackSourceStage(cfg.sourceRemoveErr)
	case isMoveFallbackTempPath(name):
		s.tempData = ""
		s.tempExists = false
		s.tempStageExists = false
		return cfg.tempRemoveErr
	default:
		return nil
	}
}

func (s *moveFallbackState) removeMoveFallbackSource(sourceRemoveErr error) error {
	if sourceRemoveErr == nil || errors.Is(sourceRemoveErr, os.ErrNotExist) {
		s.sourceExists = false
	}
	return sourceRemoveErr
}

func (s *moveFallbackState) removeMoveFallbackSourceStage(sourceRemoveErr error) error {
	if s.sourceStagePath != s.sourceCleanupPath {
		s.sourceStageExists = false
		s.sourceStagePath = ""
		return nil
	}
	if sourceRemoveErr == nil || errors.Is(sourceRemoveErr, os.ErrNotExist) {
		s.sourceStageExists = false
		s.sourceStagePath = ""
		s.sourceCleanupPath = ""
	}
	return sourceRemoveErr
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
	if state.sourceStageExists || state.sourceStagePath != "" || state.sourceCleanupPath != "" || state.tempStageExists {
		t.Fatalf("expected staged links cleaned after fallback failure, got sourceStage=%t sourceStagePath=%q cleanupPath=%q tempStage=%t", state.sourceStageExists, state.sourceStagePath, state.sourceCleanupPath, state.tempStageExists)
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
	sourcePath := filepath.Join(rootDir, "source")
	targetPath := filepath.Join(rootDir, "target")
	primaryErr := errors.New("copy read failure")
	sourceCloseErr := errors.New("close source failure")
	tempRemoveErr := errors.New("remove temp failure")
	rootCloseErr := errors.New("close root failure")

	withMoveFallbackFileSystem(t, moveFallbackConfig{
		sourcePath:     "source",
		sourceReadErr:  primaryErr,
		sourceCloseErr: sourceCloseErr,
		tempRemoveErr:  tempRemoveErr,
		rootCloseErr:   rootCloseErr,
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
}

func TestPrepareAndRenameWithinRootErrors(t *testing.T) {
	chmodErr := errors.New("chmod failure")
	sourceInfo := processFileInfo()
	chmodRoot := &fakeRoot{
		mkdirAll: func(string, os.FileMode) error { return nil },
		lstat:    func(string) (fs.FileInfo, error) { return sourceInfo, nil },
		chmod: func(string, os.FileMode) error {
			return chmodErr
		},
	}
	if err := MoveFileWithinRoot(chmodRoot, "source", "target", 0o750, 0o640); !errors.Is(err, chmodErr) {
		t.Fatalf("expected chmod error, got %v", err)
	}

	renameErr := errors.New("rename failure")
	renameRoot := &fakeRoot{
		mkdirAll: func(string, os.FileMode) error { return nil },
		lstat:    func(string) (fs.FileInfo, error) { return sourceInfo, nil },
		chmod:    chmodNameWithoutError(t, "source"),
		link: func(oldName, newName string) error {
			if (oldName != "source" && !strings.HasPrefix(filepath.Base(oldName), atomicTempPrefix)) || !strings.HasPrefix(filepath.Base(newName), atomicTempPrefix) {
				t.Fatalf("unexpected identity-bound link %q -> %q", oldName, newName)
			}
			return nil
		},
		rename: func(string, string) error {
			return renameErr
		},
		remove: func(name string) error {
			if !strings.HasPrefix(filepath.Base(name), atomicTempPrefix) {
				t.Fatalf("unexpected cleanup path %q", name)
			}
			return nil
		},
	}
	if err := MoveFileWithinRoot(renameRoot, "source", "target", 0o750, 0o640); !errors.Is(err, renameErr) {
		t.Fatalf("expected rename error, got %v", err)
	}
}

func TestPrepareAndRenameWithinRootRejectsChangedSourceBeforeRename(t *testing.T) {
	sourceInfo, changedInfo := writePinnedTargetInfoPair(t)
	lstatCalls := 0
	renameCalls := 0
	root := &rootWithoutIdentity{Root: &fakeRoot{
		mkdirAll: func(string, os.FileMode) error { return nil },
		mkdir:    func(string, os.FileMode) error { return nil },
		lstat: func(name string) (fs.FileInfo, error) {
			if name != "source" {
				t.Fatalf("unexpected lstat path: %s", name)
			}
			lstatCalls++
			if lstatCalls == 1 {
				return sourceInfo, nil
			}
			return changedInfo, nil
		},
		chmod: chmodNameWithoutError(t, "source"),
		rename: func(string, string) error {
			renameCalls++
			return nil
		},
	}}

	err := MoveFileWithinRoot(root, "source", "target", 0o750, 0o640)
	if err == nil || !strings.Contains(err.Error(), "move source changed before rename") {
		t.Fatalf("expected changed source rejection, got %v", err)
	}
	if renameCalls != 0 {
		t.Fatalf("expected no rename after source substitution, got %d", renameCalls)
	}
}

func TestPrepareAndRenameWithinRootRejectsSubstitutedStagedSourceBeforePublish(t *testing.T) {
	sourceInfo, changedInfo := writePinnedTargetInfoPair(t)
	renameCalls := 0
	removeCalls := 0
	root := &fakeRoot{
		mkdirAll: func(string, os.FileMode) error { return nil },
		lstat: func(name string) (fs.FileInfo, error) {
			switch {
			case name == "source":
				return sourceInfo, nil
			case strings.HasPrefix(filepath.Base(name), atomicTempPrefix):
				return changedInfo, nil
			default:
				t.Fatalf("unexpected lstat path: %s", name)
				return nil, os.ErrNotExist
			}
		},
		chmod: chmodNameWithoutError(t, "source"),
		link: func(oldName, newName string) error {
			if (oldName != "source" && !strings.HasPrefix(filepath.Base(oldName), atomicTempPrefix)) || !strings.HasPrefix(filepath.Base(newName), atomicTempPrefix) {
				t.Fatalf("unexpected identity-bound link %q -> %q", oldName, newName)
			}
			return nil
		},
		rename: func(string, string) error {
			renameCalls++
			return nil
		},
		remove: func(name string) error {
			if !strings.HasPrefix(filepath.Base(name), atomicTempPrefix) {
				t.Fatalf("unexpected cleanup path: %s", name)
			}
			removeCalls++
			return nil
		},
	}

	err := MoveFileWithinRoot(root, "source", "target", 0o750, 0o640)
	if err == nil || !strings.Contains(err.Error(), "move source changed before rename") {
		t.Fatalf("expected staged source substitution rejection, got %v", err)
	}
	if renameCalls != 0 {
		t.Fatalf("expected no target publish after staged source substitution, got %d renames", renameCalls)
	}
	if removeCalls != 0 {
		t.Fatalf("expected substituted staged link to remain untouched, got %d removes", removeCalls)
	}
}

func TestPrepareAndRenameWithinRootRejectsStagedSourceSwapAfterValidation(t *testing.T) {
	sourceInfo, changedInfo := writePinnedTargetInfoPair(t)
	stagedValidated := false
	renameCalls := 0
	removeCalls := 0
	root := &fakeRoot{
		mkdirAll: func(string, os.FileMode) error { return nil },
		lstat: func(name string) (fs.FileInfo, error) {
			switch {
			case name == "source":
				return sourceInfo, nil
			case strings.HasPrefix(filepath.Base(name), atomicTempPrefix):
				if stagedValidated {
					return changedInfo, nil
				}
				stagedValidated = true
				return sourceInfo, nil
			default:
				t.Fatalf("unexpected lstat path: %s", name)
				return nil, os.ErrNotExist
			}
		},
		chmod: chmodNameWithoutError(t, "source"),
		link: func(oldName, newName string) error {
			if (oldName != "source" && !strings.HasPrefix(filepath.Base(oldName), atomicTempPrefix)) || !strings.HasPrefix(filepath.Base(newName), atomicTempPrefix) {
				t.Fatalf("unexpected identity-bound link %q -> %q", oldName, newName)
			}
			return nil
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

	err := MoveFileWithinRoot(root, "source", "target", 0o750, 0o640)
	if err == nil || !strings.Contains(err.Error(), "move source changed before rename") {
		t.Fatalf("expected staged source swap rejection, got %v", err)
	}
	if renameCalls != 0 {
		t.Fatalf("expected no target publish after staged source swap, got %d renames", renameCalls)
	}
	if removeCalls != 0 {
		t.Fatalf("expected substituted staged path not to be removed, got %d removes", removeCalls)
	}
}

func TestPrepareAndRenameWithinRootRejectsStagedSourceSwapAtPublish(t *testing.T) {
	sourceInfo, changedInfo := writePinnedTargetInfoPair(t)
	renameCalls := 0
	root := &fakeRoot{
		mkdirAll:        func(string, os.FileMode) error { return nil },
		lstat:           lstatSourceOrAtomicTemp(t, sourceInfo),
		chmod:           chmodNameWithoutError(t, "source"),
		link:            linkSourceOrAtomicTempToAtomicTemp(t),
		rename:          failRawRename(t, "publish must use identity-bound rename"),
		renameIfMatches: rejectStagedSourceSwapAtPublish(t, sourceInfo, changedInfo, &renameCalls),
		removeIfMatches: acceptAtomicTempCleanupForIdentity(t, sourceInfo),
	}

	err := MoveFileWithinRoot(root, "source", "target", 0o750, 0o640)
	if err == nil || !strings.Contains(err.Error(), "move source changed before rename") {
		t.Fatalf("expected operation-time staged source swap rejection, got %v", err)
	}
	if renameCalls != 1 {
		t.Fatalf("expected one identity-bound rename attempt, got %d", renameCalls)
	}
}

func lstatSourceOrAtomicTemp(t *testing.T, info fs.FileInfo) func(string) (fs.FileInfo, error) {
	t.Helper()
	return func(name string) (fs.FileInfo, error) {
		if name == "source" || strings.HasPrefix(filepath.Base(name), atomicTempPrefix) {
			return info, nil
		}
		t.Fatalf("unexpected lstat path: %s", name)
		return nil, os.ErrNotExist
	}
}

func linkSourceOrAtomicTempToAtomicTemp(t *testing.T) func(string, string) error {
	t.Helper()
	return func(oldName, newName string) error {
		oldOK := oldName == "source" || strings.HasPrefix(filepath.Base(oldName), atomicTempPrefix)
		newOK := strings.HasPrefix(filepath.Base(newName), atomicTempPrefix)
		if !oldOK || !newOK {
			t.Fatalf("unexpected identity-bound link %q -> %q", oldName, newName)
		}
		return nil
	}
}

func failRawRename(t *testing.T, message string) func(string, string) error {
	t.Helper()
	return func(string, string) error {
		t.Fatal(message)
		return nil
	}
}

func rejectStagedSourceSwapAtPublish(t *testing.T, sourceInfo, changedInfo fs.FileInfo, calls *int) func(string, string, fs.FileInfo, string) error {
	t.Helper()
	return func(oldName, newName string, expected fs.FileInfo, message string) error {
		if !strings.HasPrefix(filepath.Base(oldName), atomicTempPrefix) || newName != "target" {
			t.Fatalf("unexpected identity-bound rename %q -> %q", oldName, newName)
		}
		(*calls)++
		requireSameFileInfo(t, expected, sourceInfo, "source identity")
		return identityMismatchError(message, oldName, changedInfo, sourceInfo)
	}
}

func acceptAtomicTempCleanupForIdentity(t *testing.T, expectedInfo fs.FileInfo) func(string, fs.FileInfo, string) error {
	t.Helper()
	return func(name string, expected fs.FileInfo, _ string) error {
		if !strings.HasPrefix(filepath.Base(name), atomicTempPrefix) {
			t.Fatalf("unexpected cleanup path: %s", name)
		}
		requireSameFileInfo(t, expected, expectedInfo, "cleanup source identity")
		return nil
	}
}

func requireSameFileInfo(t *testing.T, got, want fs.FileInfo, label string) {
	t.Helper()
	if !os.SameFile(got, want) {
		t.Fatalf("expected %s, got %v", label, got)
	}
}

func identityMismatchError(message, name string, replacementInfo, originalInfo fs.FileInfo) error {
	if os.SameFile(replacementInfo, originalInfo) {
		return nil
	}
	return fmt.Errorf("%s: %s", message, name)
}

func useRandomTempNames(t *testing.T, names ...string) *int {
	t.Helper()
	calls := 0
	originalRandomTempNameFn := randomTempNameFn
	randomTempNameFn = func() (string, error) {
		if calls >= len(names) {
			return "", fmt.Errorf("unexpected random temp name call %d", calls+1)
		}
		name := names[calls]
		calls++
		return name, nil
	}
	t.Cleanup(func() {
		randomTempNameFn = originalRandomTempNameFn
	})
	return &calls
}

func lstatOriginalForNames(t *testing.T, info fs.FileInfo, names ...string) func(string) (fs.FileInfo, error) {
	t.Helper()
	allowed := make(map[string]struct{}, len(names))
	for _, name := range names {
		allowed[name] = struct{}{}
	}
	return func(name string) (fs.FileInfo, error) {
		if _, ok := allowed[name]; ok {
			return info, nil
		}
		t.Fatalf("unexpected lstat path: %s", name)
		return nil, os.ErrNotExist
	}
}

func requireExactLinks(t *testing.T, pairs ...[2]string) func(string, string) error {
	t.Helper()
	return func(oldName, newName string) error {
		for _, pair := range pairs {
			if oldName == pair[0] && newName == pair[1] {
				return nil
			}
		}
		t.Fatalf("unexpected identity-bound link %q -> %q", oldName, newName)
		return nil
	}
}

func countAndFailRawRemove(t *testing.T, calls *int, message string) func(string) error {
	t.Helper()
	return func(name string) error {
		(*calls)++
		t.Fatalf("%s, got raw remove of %s", message, name)
		return nil
	}
}

func rejectSourceSwapAtRemoval(t *testing.T, originalInfo, replacementInfo fs.FileInfo, cleanupNames []string, sourceChecks *int) func(string, fs.FileInfo, string) error {
	t.Helper()
	return func(name string, expected fs.FileInfo, message string) error {
		requireSameFileInfo(t, expected, originalInfo, name)
		if name == "source" {
			(*sourceChecks)++
			return identityMismatchError(message, name, replacementInfo, originalInfo)
		}
		requireOneOfNames(t, name, cleanupNames...)
		return nil
	}
}

func rejectTempSwapAtRemoval(t *testing.T, originalInfo, replacementInfo fs.FileInfo, tempRel, quarantineRel string, removeChecks *int) func(string, fs.FileInfo, string) error {
	t.Helper()
	return func(name string, expected fs.FileInfo, message string) error {
		requireSameFileInfo(t, expected, originalInfo, name)
		if name == tempRel {
			(*removeChecks)++
			return identityMismatchError(message, name, replacementInfo, originalInfo)
		}
		requireOneOfNames(t, name, quarantineRel)
		return nil
	}
}

func requireOneOfNames(t *testing.T, got string, names ...string) {
	t.Helper()
	for _, name := range names {
		if got == name {
			return
		}
	}
	t.Fatalf("unexpected identity-bound cleanup path: %s", got)
}

type rootWithoutIdentity struct {
	Root
}

func TestPathOperationIfMatchesSupportsPlainRoot(t *testing.T) {
	t.Run("link", func(t *testing.T) {
		rootDir := t.TempDir()
		sourcePath := filepath.Join(rootDir, "source")
		if err := os.WriteFile(sourcePath, []byte("source"), 0o600); err != nil {
			t.Fatalf("seed source: %v", err)
		}
		expected := statTestPath(t, sourcePath)
		root := openPlainRoot(t, rootDir)

		if err := linkFileIfMatches(root, "source", "target", expected, sourceChangedMsg); err != nil {
			t.Fatalf("linkFileIfMatches returned error: %v", err)
		}
		assertFileContent(t, filepath.Join(rootDir, "target"), "source")
		assertNoAtomicStagingEntries(t, rootDir)
	})

	t.Run("rename", func(t *testing.T) {
		rootDir := t.TempDir()
		sourcePath := filepath.Join(rootDir, "source")
		if err := os.WriteFile(sourcePath, []byte("source"), 0o000); err != nil {
			t.Fatalf("seed source: %v", err)
		}
		expected := statTestPath(t, sourcePath)
		root := openPlainRoot(t, rootDir)

		consumed, err := renameFileIfMatches(root, "source", "target", expected, sourceChangedMsg)
		if err != nil {
			t.Fatalf("renameFileIfMatches returned error: %v", err)
		}
		if !consumed {
			t.Fatal("expected plain-root rename to consume the source")
		}
		assertPathAbsent(t, sourcePath)
		targetPath := filepath.Join(rootDir, "target")
		if err := os.Chmod(targetPath, 0o600); err != nil {
			t.Fatalf("chmod target: %v", err)
		}
		assertFileContent(t, targetPath, "source")
		assertNoAtomicStagingEntries(t, rootDir)
	})
}

func TestPublishIdentityBoundIfAbsentSupportsPlainRoot(t *testing.T) {
	rootDir := t.TempDir()
	sourcePath := filepath.Join(rootDir, "source")
	if err := os.WriteFile(sourcePath, []byte("source"), 0o600); err != nil {
		t.Fatalf("seed source: %v", err)
	}
	expected := statTestPath(t, sourcePath)
	root := openPlainRoot(t, rootDir)

	if err := publishIdentityBoundIfAbsent(root, "source", "target", expected); err != nil {
		t.Fatalf("publishIdentityBoundIfAbsent returned error: %v", err)
	}
	assertFileContent(t, filepath.Join(rootDir, "source"), "source")
	assertFileContent(t, filepath.Join(rootDir, "target"), "source")
	assertNoAtomicStagingEntries(t, rootDir)
}

func TestPublishIdentityBoundReplacingSupportsPlainRoot(t *testing.T) {
	rootDir := t.TempDir()
	sourcePath := filepath.Join(rootDir, "source")
	if err := os.WriteFile(sourcePath, []byte("source"), 0o000); err != nil {
		t.Fatalf("seed source: %v", err)
	}
	expected := statTestPath(t, sourcePath)
	root := openPlainRoot(t, rootDir)

	consumed, err := publishIdentityBoundReplacingWithSourceState(root, "source", "target", expected, sourceChangedMsg, "target changed")
	if err != nil {
		t.Fatalf("publishIdentityBoundReplacingWithSourceState returned error: %v", err)
	}
	if consumed {
		t.Fatal("expected plain-root replacement to leave the staged source for outer cleanup")
	}
	targetPath := filepath.Join(rootDir, "target")
	if err := os.Chmod(targetPath, 0o600); err != nil {
		t.Fatalf("chmod target: %v", err)
	}
	assertFileContent(t, targetPath, "source")
	assertNoAtomicStagingEntries(t, rootDir)
}

func TestRemoveFileIfMatchesSupportsPlainRoot(t *testing.T) {
	rootDir := t.TempDir()
	sourcePath := filepath.Join(rootDir, "source")
	if err := os.WriteFile(sourcePath, []byte("source"), 0o600); err != nil {
		t.Fatalf("seed source: %v", err)
	}
	expected := statTestPath(t, sourcePath)
	root := openPlainRoot(t, rootDir)

	if err := removeFileIfMatches(root, "source", expected, sourceChangedMsg); err != nil {
		t.Fatalf("removeFileIfMatches returned error: %v", err)
	}
	assertPathAbsent(t, sourcePath)
	assertNoAtomicStagingEntries(t, rootDir)
}

func TestRemoveFileIfMatchesRejectsEmptyAndMissingIdentity(t *testing.T) {
	root := &fakeRoot{}
	if err := removeFileIfMatches(root, "", nil, sourceChangedMsg); err != nil {
		t.Fatalf("empty path should be a no-op, got %v", err)
	}
	if err := removeFileIfMatches(root, "source", nil, sourceChangedMsg); err == nil {
		t.Fatal("expected missing identity rejection")
	}
}

func TestIdentityBoundQuarantinePathUsesSourceDirectory(t *testing.T) {
	useRandomTempNames(t, atomicTempPrefix+"quarantine")
	var mkdirPath string
	root := &fakeRoot{
		mkdir: func(name string, perm os.FileMode) error {
			mkdirPath = name
			return nil
		},
	}

	quarantineDir, quarantineRel, err := identityBoundQuarantinePath(root, filepath.Join("nested", "source"))
	if err != nil {
		t.Fatalf("identityBoundQuarantinePath returned error: %v", err)
	}
	wantDir := filepath.Join("nested", atomicTempPrefix+"quarantine")
	if quarantineDir != wantDir {
		t.Fatalf("unexpected quarantine dir: got %q want %q", quarantineDir, wantDir)
	}
	if quarantineRel != filepath.Join(wantDir, "entry") {
		t.Fatalf("unexpected quarantine path: %q", quarantineRel)
	}
	if mkdirPath != wantDir {
		t.Fatalf("expected quarantine mkdir in source directory, got %q", mkdirPath)
	}
}

func TestCloseAndCleanupCreatedFileBindsCleanupToObservedInode(t *testing.T) {
	const tempRel = ".safeio-atomic-created"
	const cleanupRel = ".safeio-atomic-cleanup"
	createdInfo := newPinnedTargetInfo(t, "created")
	statErr := errors.New("staged stat failure")
	removeChecks := 0
	rawRemoveCalls := 0
	useRandomTempNames(t, cleanupRel)

	root := &fakeRoot{
		lstat: lstatOriginalForNames(t, createdInfo, tempRel, cleanupRel),
		linkIfMatches: func(oldName, newName string, expected fs.FileInfo, message string) error {
			requireSameFileInfo(t, expected, createdInfo, oldName)
			if oldName != tempRel || newName != cleanupRel {
				t.Fatalf("unexpected cleanup link %q -> %q", oldName, newName)
			}
			return nil
		},
		remove: countAndFailRawRemove(t, &rawRemoveCalls, "created-file cleanup must stay identity-bound"),
		removeIfMatches: func(name string, expected fs.FileInfo, message string) error {
			removeChecks++
			requireSameFileInfo(t, expected, createdInfo, name)
			requireOneOfNames(t, name, tempRel, cleanupRel)
			return nil
		},
	}

	err := closeAndCleanupCreatedFile(root, &fakeFile{close: closeWithoutError}, tempRel, statErr)
	if !errors.Is(err, statErr) {
		t.Fatalf("expected staged stat failure, got %v", err)
	}
	if removeChecks != 2 {
		t.Fatalf("expected temp and cleanup removals, got %d", removeChecks)
	}
	if rawRemoveCalls != 0 {
		t.Fatalf("expected no raw removals, got %d", rawRemoveCalls)
	}
}

func TestStageIdentityBoundFileDoesNotTreatPostLinkCleanupErrorAsLinklessFallback(t *testing.T) {
	postLinkCleanupErr := errors.Join(syscall.EPERM, errors.New("post-link cleanup failed"))
	openCalls := 0
	root := &fakeRoot{
		linkIfMatches: func(string, string, fs.FileInfo, string) error {
			return postLinkCleanupErr
		},
		open: func(string) (File, error) {
			openCalls++
			return nil, nil
		},
	}

	_, _, err := stageIdentityBoundFile(root, "source", newPinnedTargetInfo(t, "source"), sourceChangedMsg)
	if !errors.Is(err, postLinkCleanupErr) {
		t.Fatalf("expected post-link cleanup error, got %v", err)
	}
	if errors.Is(err, errIdentityBoundReplacementUnsupported) {
		t.Fatalf("post-link cleanup error must not trigger linkless fallback: %v", err)
	}
	if openCalls != 0 {
		t.Fatalf("expected no copy fallback open, got %d opens", openCalls)
	}
}

func TestCleanupAtomicTempFileIfMatchesRejectsMissingIdentity(t *testing.T) {
	err := cleanupAtomicTempFileIfMatches(&fakeRoot{}, ".safeio-atomic-temp", nil)
	if err == nil || !strings.Contains(err.Error(), "cleanup file identity unavailable") {
		t.Fatalf("expected missing cleanup identity rejection, got %v", err)
	}
}

func TestVerifyPublishedPathMatchesInfoRejectsNilAndNonRegularExpected(t *testing.T) {
	root := &fakeRoot{}
	if err := verifyPublishedPathMatchesInfo(root, "source", nil, sourceChangedMsg); err == nil {
		t.Fatal("expected nil identity rejection")
	}
	dirInfo := &modeOverrideFileInfo{FileInfo: newPinnedTargetInfo(t, "source"), mode: os.ModeDir | 0o755}
	if err := verifyPublishedPathMatchesInfo(root, "source", dirInfo, sourceChangedMsg); err == nil {
		t.Fatal("expected non-regular identity rejection")
	}
}

func TestStageIdentityBoundLinkRejectsNilAndNonRegularExpected(t *testing.T) {
	root := &fakeRoot{}
	if _, err := stageIdentityBoundLink(root, "source", nil, sourceChangedMsg); err == nil {
		t.Fatal("expected nil identity rejection")
	}
	dirInfo := &modeOverrideFileInfo{FileInfo: newPinnedTargetInfo(t, "source"), mode: os.ModeDir | 0o755}
	if _, err := stageIdentityBoundLink(root, "source", dirInfo, sourceChangedMsg); err == nil {
		t.Fatal("expected non-regular identity rejection")
	}
}

func TestStageIdentityBoundLinkFailsAfterNameCollisions(t *testing.T) {
	info := newPinnedTargetInfo(t, "source")
	root := &fakeRoot{
		linkIfMatches: func(string, string, fs.FileInfo, string) error {
			return os.ErrExist
		},
	}

	_, err := stageIdentityBoundLink(root, "source", info, sourceChangedMsg)
	if err == nil || !strings.Contains(err.Error(), "too many collisions") {
		t.Fatalf("expected staging collision exhaustion, got %v", err)
	}
}

func TestPublishIdentityBoundIfAbsentHandlesExistUnsupportedAndVerifyFailure(t *testing.T) {
	for _, tc := range publishIdentityBoundIfAbsentCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			assertPublishIdentityBoundIfAbsentOutcome(t, tc)
		})
	}
}

type publishIdentityBoundIfAbsentCase struct {
	name     string
	info     fs.FileInfo
	linkErr  error
	lstat    func(string) (fs.FileInfo, error)
	wantErr  error
	wantText string
}

func publishIdentityBoundIfAbsentCases(t *testing.T) []publishIdentityBoundIfAbsentCase {
	t.Helper()

	info, replacementInfo := writePinnedTargetInfoPair(t)
	return []publishIdentityBoundIfAbsentCase{
		{name: "exists", info: info, linkErr: os.ErrExist, wantErr: os.ErrExist},
		{name: "unsupported", info: info, linkErr: syscall.EXDEV, wantErr: errIdentityBoundReplacementUnsupported},
		{name: "verify", info: info, lstat: lstatOriginalForNames(t, replacementInfo, "target"), wantText: committedTargetChangedBeforeValidation},
	}
}

func assertPublishIdentityBoundIfAbsentOutcome(t *testing.T, tc publishIdentityBoundIfAbsentCase) {
	t.Helper()

	err := publishIdentityBoundIfAbsent(publishIdentityBoundIfAbsentRoot(tc), "source", "target", tc.info)
	if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
		t.Fatalf("expected %v, got %v", tc.wantErr, err)
	}
	if tc.wantText != "" && (err == nil || !strings.Contains(err.Error(), tc.wantText)) {
		t.Fatalf("expected %q, got %v", tc.wantText, err)
	}
}

func publishIdentityBoundIfAbsentRoot(tc publishIdentityBoundIfAbsentCase) *fakeRoot {
	linkCalls := 0
	return &fakeRoot{
		linkIfMatches: func(string, string, fs.FileInfo, string) error {
			linkCalls++
			if linkCalls == 2 {
				return tc.linkErr
			}
			return nil
		},
		lstat: func(name string) (fs.FileInfo, error) {
			if name == "target" && tc.lstat != nil {
				return tc.lstat(name)
			}
			return tc.info, nil
		},
	}
}

func TestPublishIdentityBoundIfAbsentVerifiesLinkedTarget(t *testing.T) {
	info := newPinnedTargetInfo(t, "source")
	root := &fakeRoot{
		linkIfMatches: func(string, string, fs.FileInfo, string) error {
			return nil
		},
		lstat: func(name string) (fs.FileInfo, error) {
			if name == "target" || strings.HasPrefix(filepath.Base(name), atomicTempPrefix) {
				return info, nil
			}
			t.Fatalf("unexpected lstat path: %s", name)
			return nil, os.ErrNotExist
		},
	}

	if err := publishIdentityBoundIfAbsent(root, "source", "target", info); err != nil {
		t.Fatalf("expected publish-if-absent success, got %v", err)
	}
}

func TestRemoveIdentityBoundRejectsMissingIdentityAndFallsBackWithoutLinks(t *testing.T) {
	info := newPinnedTargetInfo(t, "source")
	if err := removeIdentityBound(&fakeRoot{}, "source", nil, sourceChangedMsg); err == nil {
		t.Fatal("expected missing source identity rejection")
	}
	removeCalls := 0
	root := &fakeRoot{
		linkIfMatches: func(string, string, fs.FileInfo, string) error {
			return syscall.EXDEV
		},
		lstat: lstatOriginalForNames(t, info, "source"),
		remove: func(string) error {
			removeCalls++
			return nil
		},
	}
	err := removeIdentityBound(root, "source", info, sourceChangedMsg)
	if err != nil {
		t.Fatalf("expected direct identity-bound removal fallback, got %v", err)
	}
	if removeCalls != 1 {
		t.Fatalf("expected one direct removal fallback, got %d", removeCalls)
	}
}

func TestPrepareAndRenameWithinRootRejectsReplacementWhenHardLinksUnsupported(t *testing.T) {
	sourceInfo := newPinnedTargetInfo(t, "source")
	renameCalls := 0
	removeCalls := 0
	root := &fakeRoot{
		mkdirAll: func(string, os.FileMode) error { return nil },
		lstat: func(name string) (fs.FileInfo, error) {
			switch name {
			case "source", "target":
				return sourceInfo, nil
			default:
				t.Fatalf("unexpected lstat path: %s", name)
				return nil, os.ErrNotExist
			}
		},
		chmod: chmodNameWithoutError(t, "source"),
		link: func(string, string) error {
			return errors.ErrUnsupported
		},
		open: func(string) (File, error) {
			return nil, errors.ErrUnsupported
		},
		rename: func(oldName, newName string) error {
			if oldName != "source" || newName != "target" {
				t.Fatalf("unexpected direct rename %q -> %q", oldName, newName)
			}
			renameCalls++
			return nil
		},
		remove: func(string) error {
			removeCalls++
			return nil
		},
	}

	err := MoveFileWithinRoot(root, "source", "target", 0o750, 0o640)
	if err == nil || (!errors.Is(err, errIdentityBoundReplacementUnsupported) && !errors.Is(err, errors.ErrUnsupported)) {
		t.Fatalf("expected unsupported replacement error, got %v", err)
	}
	if renameCalls != 0 {
		t.Fatalf("expected no direct rename fallback, got %d", renameCalls)
	}
	if removeCalls != 0 {
		t.Fatalf("unsupported identity-bound replacement should not remove staging links, got %d removes", removeCalls)
	}
}

func TestMoveFileWithinRootIdentityBoundStagingErrors(t *testing.T) {
	sourceInfo := newPinnedTargetInfo(t, "source")
	for _, tc := range []struct {
		name     string
		random   func() (string, error)
		linkErr  error
		wantErr  string
		wantLink bool
	}{
		{
			name:    "random name error",
			random:  func() (string, error) { return "", errors.New("random failure") },
			wantErr: "random failure",
		},
		{
			name:     "link failure",
			random:   func() (string, error) { return atomicTempPrefix + "stage", nil },
			linkErr:  errors.New("link failure"),
			wantErr:  "link failure",
			wantLink: true,
		},
		{
			name:     "collision exhaustion",
			random:   func() (string, error) { return atomicTempPrefix + "stage", nil },
			linkErr:  os.ErrExist,
			wantErr:  "too many collisions",
			wantLink: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertMoveFileWithinRootIdentityBoundStagingError(t, sourceInfo, tc.random, tc.linkErr, tc.wantErr, tc.wantLink)
		})
	}
}

func assertMoveFileWithinRootIdentityBoundStagingError(t *testing.T, sourceInfo fs.FileInfo, random func() (string, error), linkErr error, wantErr string, wantLink bool) {
	t.Helper()
	originalRandomTempNameFn := randomTempNameFn
	randomTempNameFn = random
	defer func() {
		randomTempNameFn = originalRandomTempNameFn
	}()

	linkCalls := 0
	root := &fakeRoot{
		mkdirAll: func(string, os.FileMode) error { return nil },
		lstat:    lstatSourceOrTemp(t, sourceInfo),
		chmod:    chmodNameWithoutError(t, "source"),
		link:     identityBoundSourceLink(t, linkErr, &linkCalls),
		rename: func(string, string) error {
			t.Fatal("staging failure must abort before rename")
			return nil
		},
	}

	err := MoveFileWithinRoot(root, "source", "target", 0o750, 0o640)
	if err == nil || !strings.Contains(err.Error(), wantErr) {
		t.Fatalf("expected %q error, got %v", wantErr, err)
	}
	if (linkCalls > 0) != wantLink {
		t.Fatalf("expected link called=%t, got %d calls", wantLink, linkCalls)
	}
}

func TestRemoveIdentityBoundDoesNotOverwriteNewerSourceDuringCleanup(t *testing.T) {
	originalInfo, replacementInfo := writePinnedTargetInfoPair(t)
	stagedRel := ".safeio-atomic-stage"
	randomCalls := 0
	removeCalls := 0
	originalRandomTempNameFn := randomTempNameFn
	randomTempNameFn = func() (string, error) {
		randomCalls++
		return stagedRel, nil
	}
	defer func() {
		randomTempNameFn = originalRandomTempNameFn
	}()

	root := &fakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			switch name {
			case stagedRel:
				return replacementInfo, nil
			case "source":
				return originalInfo, nil
			default:
				t.Fatalf("unexpected lstat path: %s", name)
				return nil, os.ErrNotExist
			}
		},
		link: func(oldName, newName string) error {
			if oldName != "source" || newName != stagedRel {
				t.Fatalf("cleanup must identity-link source first, got link %q -> %q", oldName, newName)
			}
			return nil
		},
		remove: func(name string) error {
			removeCalls++
			t.Fatalf("must not remove substituted staged source: %s", name)
			return nil
		},
	}

	err := removeIdentityBound(root, "source", originalInfo, "move source changed before cleanup")
	if err == nil || !strings.Contains(err.Error(), "move source changed before cleanup") {
		t.Fatalf("expected identity mismatch rejection, got %v", err)
	}
	if randomCalls != 1 {
		t.Fatalf("expected one staging name, got %d", randomCalls)
	}
	if removeCalls != 0 {
		t.Fatalf("expected no removal after cleanup identity mismatch, got %d removes", removeCalls)
	}
}

func TestRemoveIdentityBoundRejectsSourceSwapAtRemoval(t *testing.T) {
	originalInfo, replacementInfo := writePinnedTargetInfoPair(t)
	stageNames := []string{".safeio-atomic-source-stage", ".safeio-atomic-cleanup-stage"}
	randomCalls := useRandomTempNames(t, stageNames...)
	sourceRemoveChecks := 0
	rawRemoveCalls := 0

	root := &fakeRoot{
		lstat:           lstatOriginalForNames(t, originalInfo, "source", stageNames[0], stageNames[1]),
		link:            requireExactLinks(t, [2]string{"source", stageNames[0]}, [2]string{stageNames[0], stageNames[1]}),
		remove:          countAndFailRawRemove(t, &rawRemoveCalls, "removal must be identity-conditional"),
		removeIfMatches: rejectSourceSwapAtRemoval(t, originalInfo, replacementInfo, stageNames, &sourceRemoveChecks),
	}

	err := removeIdentityBound(root, "source", originalInfo, "move source changed before cleanup")
	if err == nil || !strings.Contains(err.Error(), "move source changed before cleanup") {
		t.Fatalf("expected source cleanup identity rejection, got %v", err)
	}
	if sourceRemoveChecks != 1 {
		t.Fatalf("expected one source removal identity check, got %d", sourceRemoveChecks)
	}
	if *randomCalls != 2 {
		t.Fatalf("expected two staging names, got %d", *randomCalls)
	}
	if rawRemoveCalls != 0 {
		t.Fatalf("expected no raw removals, got %d", rawRemoveCalls)
	}
}

func TestCleanupAtomicTempFileIfMatchesDoesNotRemoveQuarantinedSwap(t *testing.T) {
	originalInfo, changedInfo := writePinnedTargetInfoPair(t)
	tempRel := ".safeio-atomic-source"
	quarantineRel := ".safeio-atomic-quarantine"
	removeCalls := 0
	originalRandomTempNameFn := randomTempNameFn
	randomTempNameFn = func() (string, error) {
		return quarantineRel, nil
	}
	defer func() {
		randomTempNameFn = originalRandomTempNameFn
	}()

	root := &fakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			switch name {
			case tempRel:
				return originalInfo, nil
			case quarantineRel:
				return changedInfo, nil
			default:
				t.Fatalf("unexpected lstat path: %s", name)
			}
			return nil, os.ErrNotExist
		},
		link: func(oldName, newName string) error {
			if oldName != tempRel || newName != quarantineRel {
				t.Fatalf("unexpected cleanup link %q -> %q", oldName, newName)
			}
			return nil
		},
		remove: func(name string) error {
			removeCalls++
			t.Fatalf("must not remove substituted quarantined path: %s", name)
			return nil
		},
	}

	if err := cleanupAtomicTempFileIfMatches(root, tempRel, originalInfo); err == nil || !strings.Contains(err.Error(), "cleanup file changed before removal") {
		t.Fatalf("expected substituted cleanup identity mismatch, got %v", err)
	}
	if removeCalls != 0 {
		t.Fatalf("expected no removal after quarantine identity mismatch, got %d removes", removeCalls)
	}
}

func TestCleanupAtomicTempFileIfMatchesRejectsTempSwapAtRemoval(t *testing.T) {
	originalInfo, replacementInfo := writePinnedTargetInfoPair(t)
	tempRel := ".safeio-atomic-source"
	quarantineRel := ".safeio-atomic-quarantine"
	removeChecks := 0
	rawRemoveCalls := 0
	useRandomTempNames(t, quarantineRel)

	root := &fakeRoot{
		lstat:           lstatOriginalForNames(t, originalInfo, tempRel, quarantineRel),
		link:            requireExactLinks(t, [2]string{tempRel, quarantineRel}),
		remove:          countAndFailRawRemove(t, &rawRemoveCalls, "cleanup must be identity-conditional"),
		removeIfMatches: rejectTempSwapAtRemoval(t, originalInfo, replacementInfo, tempRel, quarantineRel, &removeChecks),
	}

	if err := cleanupAtomicTempFileIfMatches(root, tempRel, originalInfo); err == nil || !strings.Contains(err.Error(), "cleanup file changed before removal") {
		t.Fatalf("expected operation-time cleanup identity rejection, got %v", err)
	}
	if removeChecks != 1 {
		t.Fatalf("expected one temp removal identity check, got %d", removeChecks)
	}
	if rawRemoveCalls != 0 {
		t.Fatalf("expected no raw removals, got %d", rawRemoveCalls)
	}
}

func TestPrepareAndRenameWithinRootRejectsChangedTargetAfterRename(t *testing.T) {
	sourceInfo, changedInfo := writePinnedTargetInfoPair(t)
	fixture := newChangedTargetAfterRenameFixture(t, sourceInfo, changedInfo)

	err := MoveFileWithinRoot(fixture.root(), "source", "target", 0o750, 0o640)
	if err == nil || !strings.Contains(err.Error(), "move target changed before validation") {
		t.Fatalf("expected changed target rejection, got %v", err)
	}
	fixture.assertSourceLstats(t, 3)
	fixture.assertRenameCalls(t, 1)
}

type changedTargetAfterRenameFixture struct {
	t                *testing.T
	sourceInfo       fs.FileInfo
	changedInfo      fs.FileInfo
	sourceLstatCalls int
	renameCalls      int
}

func newChangedTargetAfterRenameFixture(t *testing.T, sourceInfo, changedInfo fs.FileInfo) *changedTargetAfterRenameFixture {
	t.Helper()
	return &changedTargetAfterRenameFixture{t: t, sourceInfo: sourceInfo, changedInfo: changedInfo}
}

func (f *changedTargetAfterRenameFixture) root() *fakeRoot {
	return &fakeRoot{
		mkdirAll: func(string, os.FileMode) error { return nil },
		lstat:    f.lstat,
		chmod:    chmodNameWithoutError(f.t, "source"),
		link:     f.link,
		rename:   f.rename,
	}
}

func (f *changedTargetAfterRenameFixture) lstat(name string) (fs.FileInfo, error) {
	switch {
	case name == "source":
		f.sourceLstatCalls++
		return f.sourceInfo, nil
	case strings.HasPrefix(filepath.Base(name), atomicTempPrefix):
		return f.sourceInfo, nil
	case name == "target":
		return f.changedInfo, nil
	default:
		f.t.Fatalf("unexpected lstat path: %s", name)
		return nil, os.ErrNotExist
	}
}

func (f *changedTargetAfterRenameFixture) link(oldName, newName string) error {
	assertIdentityBoundSourceLink(f.t, oldName, newName)
	return nil
}

func (f *changedTargetAfterRenameFixture) rename(oldName, newName string) error {
	if strings.HasPrefix(filepath.Base(oldName), atomicTempPrefix) && strings.HasPrefix(filepath.Base(newName), atomicTempPrefix) {
		return nil
	}
	if !strings.HasPrefix(filepath.Base(oldName), atomicTempPrefix) || newName != "target" {
		f.t.Fatalf("unexpected rename %q -> %q", oldName, newName)
	}
	f.renameCalls++
	return nil
}

func (f *changedTargetAfterRenameFixture) assertSourceLstats(t *testing.T, want int) {
	t.Helper()
	if f.sourceLstatCalls != want {
		t.Fatalf("expected %d source lstats, got %d", want, f.sourceLstatCalls)
	}
}

func (f *changedTargetAfterRenameFixture) assertRenameCalls(t *testing.T, want int) {
	t.Helper()
	if f.renameCalls != want {
		t.Fatalf("expected %d renames, got %d", want, f.renameCalls)
	}
}

func TestPrepareAndRenameWithinRootReturnsSourceCleanupErrorAfterPublish(t *testing.T) {
	sourceInfo := newPinnedTargetInfo(t, "source")
	cleanupErr := errors.New("cleanup remove failure")
	renameCalls := 0
	root := &fakeRoot{
		mkdirAll: func(string, os.FileMode) error { return nil },
		lstat: func(name string) (fs.FileInfo, error) {
			switch {
			case name == "source", strings.HasPrefix(filepath.Base(name), atomicTempPrefix), name == "target":
				return sourceInfo, nil
			default:
				t.Fatalf("unexpected lstat path: %s", name)
				return nil, os.ErrNotExist
			}
		},
		chmod: chmodNameWithoutError(t, "source"),
		link: func(oldName, newName string) error {
			if (oldName != "source" && !strings.HasPrefix(filepath.Base(oldName), atomicTempPrefix)) || !strings.HasPrefix(filepath.Base(newName), atomicTempPrefix) {
				t.Fatalf("unexpected identity-bound link %q -> %q", oldName, newName)
			}
			return nil
		},
		rename: func(oldName, newName string) error {
			renameCalls++
			if strings.HasPrefix(filepath.Base(oldName), atomicTempPrefix) && newName == "target" {
				return nil
			}
			t.Fatalf("unexpected rename %q -> %q", oldName, newName)
			return nil
		},
		remove: func(name string) error {
			if name == "source" {
				return cleanupErr
			}
			return nil
		},
	}

	err := MoveFileWithinRoot(root, "source", "target", 0o750, 0o640)
	if !errors.Is(err, cleanupErr) {
		t.Fatalf("expected source cleanup error, got %v", err)
	}
	if renameCalls != 1 {
		t.Fatalf("expected publish rename only, got %d", renameCalls)
	}
}

func TestChmodAndSnapshotMoveSourceErrorBranches(t *testing.T) {
	lstatErr := errors.New("lstat failure")
	regularInfo := newPinnedTargetInfo(t, "source")
	for _, tc := range []struct {
		name    string
		root    Root
		wantErr string
		wantIs  error
	}{
		{
			name: "initial lstat error",
			root: &fakeRoot{
				mkdirAll: func(string, os.FileMode) error { return nil },
				lstat: func(string) (fs.FileInfo, error) {
					return nil, lstatErr
				},
			},
			wantIs: lstatErr,
		},
		{
			name: "non regular source",
			root: &fakeRoot{
				mkdirAll: func(string, os.FileMode) error { return nil },
				lstat: func(string) (fs.FileInfo, error) {
					return &modeOverrideFileInfo{FileInfo: regularInfo, mode: os.ModeDir | 0o755}, nil
				},
			},
			wantErr: "move source is not a regular file",
		},
		{
			name: "updated lstat error",
			root: func() Root {
				lstatCalls := 0
				return &fakeRoot{
					mkdirAll: func(string, os.FileMode) error { return nil },
					lstat: func(string) (fs.FileInfo, error) {
						lstatCalls++
						if lstatCalls == 1 {
							return regularInfo, nil
						}
						return nil, lstatErr
					},
					chmod: chmodNameWithoutError(t, "source"),
				}
			}(),
			wantIs: lstatErr,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := MoveFileWithinRoot(tc.root, "source", "target", 0o750, 0o640)
			assertMoveFileWithinRootError(t, err, tc.wantIs, tc.wantErr)
		})
	}
}

func TestCopyFileWithinRootRejectsInvalidSourceStat(t *testing.T) {
	statErr := errors.New("stat failure")
	regularInfo := newPinnedTargetInfo(t, "source")
	for _, tc := range []copyInvalidSourceStatCase{
		{
			name:   "stat error",
			stat:   func() (fs.FileInfo, error) { return nil, statErr },
			wantIs: statErr,
		},
		{
			name: "non regular source",
			stat: func() (fs.FileInfo, error) {
				return &modeOverrideFileInfo{FileInfo: regularInfo, mode: os.ModeDir | 0o755}, nil
			},
			wantErr: "move source is not a regular file",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertCopyFileWithinRootRejectsInvalidSourceStat(t, regularInfo, tc)
		})
	}
}

type copyInvalidSourceStatCase struct {
	name    string
	stat    func() (fs.FileInfo, error)
	wantErr string
	wantIs  error
}

func assertCopyFileWithinRootRejectsInvalidSourceStat(t *testing.T, regularInfo fs.FileInfo, tc copyInvalidSourceStatCase) {
	t.Helper()
	root := copyInvalidSourceStatRoot(t, regularInfo, tc.stat)
	err := MoveFileWithinRoot(root, "source", "target", 0o750, 0o640)
	assertMoveFileWithinRootError(t, err, tc.wantIs, tc.wantErr)
}

func assertMoveFileWithinRootError(t *testing.T, err, wantIs error, wantErr string) {
	t.Helper()
	switch {
	case wantIs != nil && !errors.Is(err, wantIs):
		t.Fatalf("expected %v, got %v", wantIs, err)
	case wantErr != "" && (err == nil || !strings.Contains(err.Error(), wantErr)):
		t.Fatalf("expected %q error, got %v", wantErr, err)
	}
}

func copyInvalidSourceStatRoot(t *testing.T, regularInfo fs.FileInfo, invalidStat func() (fs.FileInfo, error)) Root {
	t.Helper()
	return &fakeRoot{
		mkdirAll: func(string, os.FileMode) error { return nil },
		lstat:    lstatSourceOrTemp(t, regularInfo),
		chmod:    chmodNameWithoutError(t, "source"),
		link:     identityBoundSourceLink(t, nil, new(int)),
		rename:   exdevSourceOrTempToTarget(t),
		remove:   removeOnlyTempPath(t),
		open:     invalidSourceStatOpen(t, regularInfo, invalidStat),
	}
}

func exdevSourceOrTempToTarget(t *testing.T) func(string, string) error {
	t.Helper()
	return func(oldName, newName string) error {
		if strings.HasPrefix(filepath.Base(oldName), atomicTempPrefix) && strings.HasPrefix(filepath.Base(newName), atomicTempPrefix) {
			return nil
		}
		if (oldName == "source" || strings.HasPrefix(filepath.Base(oldName), atomicTempPrefix)) && newName == "target" {
			return syscall.EXDEV
		}
		t.Fatalf("unexpected rename %q -> %q", oldName, newName)
		return nil
	}
}

func removeOnlyTempPath(t *testing.T) func(string) error {
	t.Helper()
	return func(name string) error {
		if !strings.HasPrefix(filepath.Base(name), atomicTempPrefix) {
			t.Fatalf("unexpected cleanup path: %s", name)
		}
		return nil
	}
}

func invalidSourceStatOpen(t *testing.T, regularInfo fs.FileInfo, invalidStat func() (fs.FileInfo, error)) func(string) (File, error) {
	t.Helper()
	statCalls := 0
	return func(name string) (File, error) {
		if name != "source" {
			t.Fatalf("unexpected source open path: %s", name)
		}
		return &fakeFile{
			stat:  invalidSourceStatSequence(regularInfo, invalidStat, &statCalls),
			close: closeWithoutError,
		}, nil
	}
}

func invalidSourceStatSequence(regularInfo fs.FileInfo, invalidStat func() (fs.FileInfo, error), calls *int) func() (fs.FileInfo, error) {
	return func() (fs.FileInfo, error) {
		(*calls)++
		if *calls == 1 {
			return regularInfo, nil
		}
		return invalidStat()
	}
}

func TestIdentityBoundPublishRejectsSubstitutedSourceWithoutPublishingIt(t *testing.T) {
	rootDir := t.TempDir()
	sourcePath := filepath.Join(rootDir, "source")
	targetPath := filepath.Join(rootDir, "target")
	if err := os.WriteFile(sourcePath, []byte("original"), 0o600); err != nil {
		t.Fatalf("seed source: %v", err)
	}
	expected := statTestPath(t, sourcePath)
	replaceFileAtPathWithDistinctIdentity(t, sourcePath, expected, "replacement")
	root := openTestRoot(t, rootDir)

	err := publishIdentityBoundReplacing(root, "source", "target", expected, sourceChangedMsg, "target changed")
	if err == nil || !strings.Contains(err.Error(), sourceChangedMsg) {
		t.Fatalf("expected substituted-source rejection, got %v", err)
	}
	assertFileContent(t, sourcePath, "replacement")
	if _, err := os.Lstat(targetPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("substituted source was published at target: %v", err)
	}
	assertNoAtomicStagingEntries(t, rootDir)
}

func TestIdentityBoundIfAbsentRejectsSubstitutedSourceWithoutPartialTarget(t *testing.T) {
	rootDir := t.TempDir()
	sourcePath := filepath.Join(rootDir, "source")
	targetPath := filepath.Join(rootDir, "target")
	if err := os.WriteFile(sourcePath, []byte("original"), 0o600); err != nil {
		t.Fatalf("seed source: %v", err)
	}
	expected := statTestPath(t, sourcePath)
	replaceFileAtPathWithDistinctIdentity(t, sourcePath, expected, "replacement")
	root := openTestRoot(t, rootDir)

	err := publishIdentityBoundIfAbsent(root, "source", "target", expected)
	if err == nil || !strings.Contains(err.Error(), temporaryFileChangedBeforeCommit) {
		t.Fatalf("expected substituted-source rejection, got %v", err)
	}
	assertFileContent(t, sourcePath, "replacement")
	if _, err := os.Lstat(targetPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("if-absent publish left a target: %v", err)
	}
	assertNoAtomicStagingEntries(t, rootDir)
}

func TestOSRootRenameIfMatchesQuarantinesSubstitution(t *testing.T) {
	rootDir := t.TempDir()
	sourcePath := filepath.Join(rootDir, "source")
	targetPath := filepath.Join(rootDir, "target")
	if err := os.WriteFile(sourcePath, []byte("original"), 0o600); err != nil {
		t.Fatalf("seed source: %v", err)
	}
	expected := statTestPath(t, sourcePath)
	replaceFileAtPathWithDistinctIdentity(t, sourcePath, expected, "replacement")
	root := openTestRoot(t, rootDir)
	osRoot, ok := root.(*osRoot)
	if !ok {
		t.Fatal("expected production osRoot")
	}

	consumed, err := osRoot.RenameIfMatchesState("source", "target", expected, sourceChangedMsg)
	if err == nil || consumed {
		t.Fatalf("expected quarantined substitution rejection, consumed=%t err=%v", consumed, err)
	}
	assertPathAbsent(t, sourcePath)
	assertPathAbsent(t, targetPath)
	assertFileContent(t, filepath.Join(findAtomicEntryPath(t, rootDir), "entry"), "replacement")
}

func TestOSRootRemoveIfMatchesQuarantinesSubstitution(t *testing.T) {
	rootDir := t.TempDir()
	sourcePath := filepath.Join(rootDir, "source")
	if err := os.WriteFile(sourcePath, []byte("original"), 0o600); err != nil {
		t.Fatalf("seed source: %v", err)
	}
	expected := statTestPath(t, sourcePath)
	replaceFileAtPathWithDistinctIdentity(t, sourcePath, expected, "replacement")
	root := openTestRoot(t, rootDir)
	osRoot, ok := root.(*osRoot)
	if !ok {
		t.Fatal("expected production osRoot")
	}

	err := osRoot.RemoveIfMatches("source", expected, sourceChangedMsg)
	if err == nil {
		t.Fatal("expected substituted-source removal rejection")
	}
	assertPathAbsent(t, sourcePath)
	assertFileContent(t, filepath.Join(findAtomicEntryPath(t, rootDir), "entry"), "replacement")
}

func TestRestoreQuarantinedPathNoReplaceRetainsStagedEntryWhenOriginalReappears(t *testing.T) {
	rootDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(rootDir, "quarantine"), 0o700); err != nil {
		t.Fatalf("create quarantine: %v", err)
	}
	if err := os.WriteFile(filepath.Join(rootDir, "quarantine", "entry"), []byte("displaced"), 0o600); err != nil {
		t.Fatalf("seed quarantined entry: %v", err)
	}
	if err := os.WriteFile(filepath.Join(rootDir, "source"), []byte("newer"), 0o600); err != nil {
		t.Fatalf("seed concurrent source: %v", err)
	}
	root := openTestRoot(t, rootDir)
	stagedInfo := statTestPath(t, filepath.Join(rootDir, "quarantine", "entry"))

	restored, err := restoreQuarantinedPathNoReplace(root, filepath.Join("quarantine", "entry"), "source", sourceChangedMsg, stagedInfo)
	if restored || !errors.Is(err, os.ErrExist) {
		t.Fatalf("expected no-replace restore conflict, restored=%t err=%v", restored, err)
	}
	assertFileContent(t, filepath.Join(rootDir, "source"), "newer")
	assertFileContent(t, filepath.Join(rootDir, "quarantine", "entry"), "displaced")
}

func TestAtomicReplacementFallsBackToPreparedCopyWhenLinksUnsupported(t *testing.T) {
	rootDir := t.TempDir()
	root := &rootWithoutIdentity{Root: &fakeRoot{
		Root: openTestRoot(t, rootDir),
		link: func(string, string) error {
			return syscall.EPERM
		},
	}}

	if err := WriteFileReplacingWithinRoot(root, "target", []byte("completed"), 0o600); err != nil {
		t.Fatalf("linkless replacement returned error: %v", err)
	}
	assertFileContent(t, filepath.Join(rootDir, "target"), "completed")
	assertNoAtomicStagingEntries(t, rootDir)
}

func TestMoveFallsBackToPreparedCopyWhenLinksUnsupported(t *testing.T) {
	rootDir := t.TempDir()
	sourcePath := filepath.Join(rootDir, "source")
	if err := os.WriteFile(sourcePath, []byte("completed"), 0o600); err != nil {
		t.Fatalf("seed source: %v", err)
	}
	root := &rootWithoutIdentity{Root: &fakeRoot{
		Root: openTestRoot(t, rootDir),
		link: func(string, string) error {
			return syscall.EPERM
		},
	}}

	if err := MoveFileWithinRoot(root, "source", "target", 0o750, 0o640); err != nil {
		t.Fatalf("linkless move returned error: %v", err)
	}
	if _, err := os.Lstat(sourcePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("linkless move left source: %v", err)
	}
	assertFileContent(t, filepath.Join(rootDir, "target"), "completed")
	assertNoAtomicStagingEntries(t, rootDir)
}

func TestWriteRootIfAbsentFallsBackToExclusiveCreateWhenLinksUnsupported(t *testing.T) {
	rootDir := t.TempDir()
	base := openTestRoot(t, rootDir)
	root := &WriteRoot{
		root: &rootWithoutIdentity{Root: &fakeRoot{
			Root: base,
			link: func(string, string) error {
				return syscall.EPERM
			},
		}},
		rootAbs: rootDir,
	}

	targetRel := filepath.Join("reports", "target")
	if err := root.WriteFileCreatingParentsIfAbsent(targetRel, []byte("completed"), 0o640, 0o750); err != nil {
		t.Fatalf("linkless if-absent create returned error: %v", err)
	}
	assertFileContent(t, filepath.Join(rootDir, targetRel), "completed")
	assertNoAtomicStagingEntries(t, rootDir)
}

func TestWriteRootIfAbsentLinklessFallbackRejectsExistingTarget(t *testing.T) {
	rootDir := t.TempDir()
	targetRel := filepath.Join("reports", "target")
	if err := os.MkdirAll(filepath.Join(rootDir, "reports"), 0o750); err != nil {
		t.Fatalf("create target parent: %v", err)
	}
	targetPath := filepath.Join(rootDir, targetRel)
	if err := os.WriteFile(targetPath, []byte("before"), 0o600); err != nil {
		t.Fatalf("seed target: %v", err)
	}

	base := openTestRoot(t, rootDir)
	root := &WriteRoot{
		root: &rootWithoutIdentity{Root: &fakeRoot{
			Root: base,
			link: func(string, string) error {
				return syscall.EPERM
			},
		}},
		rootAbs: rootDir,
	}

	err := root.WriteFileCreatingParentsIfAbsent(targetRel, []byte("after"), 0o640, 0o750)
	if !errors.Is(err, os.ErrExist) {
		t.Fatalf("expected existing target rejection on linkless root, got %v", err)
	}
	assertFileContent(t, targetPath, "before")
	assertNoAtomicStagingEntries(t, rootDir)
}

func TestAtomicIfAbsentLinklessFallbackLeavesNoPartialTarget(t *testing.T) {
	rootDir := t.TempDir()
	root := &rootWithoutIdentity{Root: &fakeRoot{
		Root: openTestRoot(t, rootDir),
		link: func(string, string) error {
			return syscall.EPERM
		},
	}}

	err := writeFileAtomicallyIfAbsentAtRoot(root, "target", []byte("completed"), 0o600)
	if err == nil || !errors.Is(err, errIdentityBoundReplacementUnsupported) {
		t.Fatalf("expected atomic linkless-if-absent rejection, got %v", err)
	}
	if _, err := os.Lstat(filepath.Join(rootDir, "target")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("linkless if-absent fallback exposed a target: %v", err)
	}
	assertNoAtomicStagingEntries(t, rootDir)
}

func TestWriteRootIfAbsentLinklessFallbackLeavesNoPartialTargetOnCloseError(t *testing.T) {
	rootDir := t.TempDir()
	base := openTestRoot(t, rootDir)
	closeErr := errors.New("close target failure")
	root := &WriteRoot{
		root: &rootWithoutIdentity{Root: &fakeRoot{
			Root: base,
			link: func(string, string) error {
				return syscall.EPERM
			},
			openFile: func(name string, flag int, perm os.FileMode) (File, error) {
				file, err := base.OpenFile(name, flag, perm)
				if err != nil {
					return nil, err
				}
				if name != "target" {
					return file, nil
				}
				return &fakeFile{
					File: file,
					close: func() error {
						if err := file.Close(); err != nil {
							return err
						}
						return closeErr
					},
				}, nil
			},
		}},
		rootAbs: rootDir,
	}

	err := root.WriteFileCreatingParentsIfAbsent("target", []byte("completed"), 0o600, 0o750)
	if !errors.Is(err, closeErr) {
		t.Fatalf("expected linkless close failure, got %v", err)
	}
	if _, err := os.Lstat(filepath.Join(rootDir, "target")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("linkless fallback left a partial target: %v", err)
	}
	assertNoAtomicStagingEntries(t, rootDir)
}

func TestLinklessStagingCopyFailureCleansUpPrivateCandidate(t *testing.T) {
	rootDir := t.TempDir()
	sourcePath := filepath.Join(rootDir, "source")
	if err := os.WriteFile(sourcePath, []byte("source"), 0o600); err != nil {
		t.Fatalf("seed source: %v", err)
	}
	expected := statTestPath(t, sourcePath)
	readErr := errors.New("copy failed")
	base := openTestRoot(t, rootDir)
	root := &rootWithoutIdentity{Root: &fakeRoot{
		Root: base,
		link: func(string, string) error {
			return syscall.EPERM
		},
		open: func(name string) (File, error) {
			file, err := base.Open(name)
			if err != nil {
				return nil, err
			}
			return &fakeFile{
				File: file,
				read: func([]byte) (int, error) {
					return 0, readErr
				},
			}, nil
		},
	}}

	_, _, err := stageIdentityBoundFile(root, "source", expected, sourceChangedMsg)
	if err == nil || !errors.Is(err, readErr) {
		t.Fatalf("expected linkless copy failure, got %v", err)
	}
	assertFileContent(t, sourcePath, "source")
	assertNoAtomicStagingEntries(t, rootDir)
}

func TestMoveFileWithinRootCaseOnlyAliasPreservesFile(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("case-insensitive alias regression requires the default macOS filesystem")
	}

	rootDir := t.TempDir()
	sourcePath := filepath.Join(rootDir, "File")
	if err := os.WriteFile(sourcePath, []byte("same"), 0o600); err != nil {
		t.Fatalf("seed source: %v", err)
	}
	sourceInfo := statTestPath(t, sourcePath)
	targetInfo, err := os.Lstat(filepath.Join(rootDir, "file"))
	if err != nil || !os.SameFile(sourceInfo, targetInfo) {
		t.Skip("temporary directory is case-sensitive")
	}
	root := openTestRoot(t, rootDir)

	if err := MoveFileWithinRoot(root, "File", "file", 0o750, 0o640); err != nil {
		t.Fatalf("case-only move returned error: %v", err)
	}
	assertFileContent(t, filepath.Join(rootDir, "file"), "same")
	assertNoAtomicStagingEntries(t, rootDir)
}

func TestPublishRenameSourcePrefersStagedName(t *testing.T) {
	err := &publishRenameError{sourceRel: ".safeio-atomic-stage", err: errors.New("rename failed")}
	if got := publishRenameSource(err, ".safeio-atomic-original"); got != ".safeio-atomic-stage" {
		t.Fatalf("expected staged fallback source, got %q", got)
	}
}

func assertNoAtomicStagingEntries(t *testing.T, rootDir string) {
	t.Helper()
	entries, err := os.ReadDir(rootDir)
	if err != nil {
		t.Fatalf("read root entries: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), atomicTempPrefix) {
			t.Fatalf("leaked atomic staging entry: %s", entry.Name())
		}
	}
}
