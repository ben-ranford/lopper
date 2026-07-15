package safeio

import (
	"errors"
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
	rootDir := t.TempDir()
	targetPath := filepath.Join(rootDir, writeTestFileName)
	if err := os.WriteFile(targetPath, []byte("before"), 0o644); err != nil {
		t.Fatalf("seed target file: %v", err)
	}
	if err := os.Chmod(targetPath, 0o644); err != nil {
		t.Fatalf("chmod target file: %v", err)
	}

	if err := WriteFileUnder(rootDir, targetPath, []byte("after"), 0o600); err != nil {
		t.Fatalf("WriteFileUnder returned error: %v", err)
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

func TestWriteFileUnderRejectsPathTraversalOutsideRoot(t *testing.T) {
	parentDir := t.TempDir()
	rootDir := filepath.Join(parentDir, "root")
	if err := os.MkdirAll(rootDir, 0o755); err != nil {
		t.Fatalf("create root dir: %v", err)
	}

	outsidePath := filepath.Join(parentDir, "secret.txt")
	err := WriteFileUnder(rootDir, outsidePath, []byte("secret"), 0o600)
	if err == nil {
		t.Fatal("expected error for outside path, got nil")
	}
	if !strings.Contains(err.Error(), escapesRootErr) {
		t.Fatalf(unexpectedErrFmt, err)
	}
}

func TestWriteFileUnderRejectsSymlinkedParentEscapingRoot(t *testing.T) {
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
	err := WriteFileUnder(rootDir, targetPath, []byte("secret"), 0o600)
	if err == nil {
		t.Fatal("expected symlink escape write to fail")
	}
	if _, statErr := os.Stat(filepath.Join(outsideDir, writeTestFileName)); !os.IsNotExist(statErr) {
		t.Fatalf("expected outside file to remain absent, got err=%v", statErr)
	}
}

func TestWriteFileUnderRejectsNonDirectoryRoot(t *testing.T) {
	rootDir := t.TempDir()
	rootFile := filepath.Join(rootDir, "root-file")
	if err := os.WriteFile(rootFile, []byte("not-a-dir"), 0o600); err != nil {
		t.Fatalf("write root file: %v", err)
	}

	err := WriteFileUnder(rootFile, filepath.Join(rootFile, "child.txt"), []byte("hello"), 0o600)
	if err == nil {
		t.Fatal("expected error when root is not a directory")
	}
	if !strings.Contains(err.Error(), "open root") && !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf(unexpectedErrFmt, err)
	}
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
	root, err := OpenWriteRoot(rootDir)
	if err != nil {
		t.Fatalf("OpenWriteRoot returned error: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := root.Close(); closeErr != nil && !errors.Is(closeErr, os.ErrClosed) {
			t.Errorf("close write root: %v", closeErr)
		}
	})

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

func TestWriteRootPropagatesParentLookupError(t *testing.T) {
	expectedErr := errors.New("parent lookup failure")
	withFileSystem(t, &fakeFileSystem{openRoot: func(string) (Root, error) {
		return &fakeRoot{
			lstat: func(string) (fs.FileInfo, error) {
				return nil, expectedErr
			},
			close: func() error {
				return nil
			},
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

	err = root.WriteFileCreatingParents(filepath.Join("reports", writeTestFileName), []byte("hello"), 0o600, 0o750)
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected parent lookup error, got %v", err)
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
	if err == nil || !strings.Contains(err.Error(), "output parent contains symlink") {
		t.Fatalf("expected symlinked parent rejection, got %v", err)
	}
	data, readErr := os.ReadFile(outsideTarget)
	if readErr != nil {
		t.Fatalf("read outside target: %v", readErr)
	}
	if string(data) != "outside-before" {
		t.Fatalf("unexpected outside content: %q", string(data))
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

func TestWriteFileUnderKeepsPrimaryErrorWhenCleanupFails(t *testing.T) {
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
	if errors.Is(err, cleanupErr) {
		t.Fatalf("expected cleanup error to stay secondary, got %v", err)
	}
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
