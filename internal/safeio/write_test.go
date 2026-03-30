package safeio

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
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
	if !strings.Contains(err.Error(), "open root") {
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

func TestCreateAtomicTempFileInRootDir(t *testing.T) {
	rootDir := t.TempDir()
	root, err := os.OpenRoot(rootDir)
	if err != nil {
		t.Fatalf(openRootErrFmt, err)
	}
	t.Cleanup(func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Fatalf(closeRootErrFmt, closeErr)
		}
	})

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
	root, err := os.OpenRoot(rootDir)
	if err != nil {
		t.Fatalf(openRootErrFmt, err)
	}
	t.Cleanup(func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Fatalf(closeRootErrFmt, closeErr)
		}
	})

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
	root, err := os.OpenRoot(rootDir)
	if err != nil {
		t.Fatalf(openRootErrFmt, err)
	}
	t.Cleanup(func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Fatalf(closeRootErrFmt, closeErr)
		}
	})

	originalRandomTempNameFn := randomTempNameFn
	randomTempNameFn = func() (string, error) { return "", errors.New("boom") }
	defer func() {
		randomTempNameFn = originalRandomTempNameFn
	}()

	_, _, err = createAtomicTempFile(root, ".", 0o600)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected random temp name error, got %v", err)
	}
}

func TestCreateAtomicTempFileFailsAfterRepeatedCollisions(t *testing.T) {
	rootDir := t.TempDir()
	root, err := os.OpenRoot(rootDir)
	if err != nil {
		t.Fatalf(openRootErrFmt, err)
	}
	t.Cleanup(func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Fatalf(closeRootErrFmt, closeErr)
		}
	})
	if err := root.WriteFile("fixed", []byte("x"), 0o600); err != nil {
		t.Fatalf("seed colliding temp file: %v", err)
	}

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
	root, err := os.OpenRoot(rootDir)
	if err != nil {
		t.Fatalf(openRootErrFmt, err)
	}
	t.Cleanup(func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Fatalf(closeRootErrFmt, closeErr)
		}
	})

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
	root, err := os.OpenRoot(rootDir)
	if err != nil {
		t.Fatalf(openRootErrFmt, err)
	}

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
	root, err := os.OpenRoot(rootDir)
	if err != nil {
		t.Fatalf(openRootErrFmt, err)
	}
	if err := root.Close(); err != nil {
		t.Fatalf("close root: %v", err)
	}

	err = cleanupAtomicTempFile(root, "temp", &os.File{})
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

func TestWriteFileUnderCloseRootError(t *testing.T) {
	rootDir := t.TempDir()
	targetPath := filepath.Join(rootDir, writeTestFileName)

	expectedErr := errors.New("close root failure")
	originalCloseRoot := closeRootFn
	closeRootFn = func(root *os.Root) error {
		err := originalCloseRoot(root)
		if err != nil {
			return err
		}
		return expectedErr
	}
	t.Cleanup(func() {
		closeRootFn = originalCloseRoot
	})

	err := WriteFileUnder(rootDir, targetPath, []byte("hello"), 0o600)
	if err == nil {
		t.Fatal("expected root close failure to be returned")
	}
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected root close error, got %v", err)
	}
}

func TestWriteFileUnderCleanupError(t *testing.T) {
	rootDir := t.TempDir()
	targetPath := filepath.Join(rootDir, writeTestFileName)
	if err := os.MkdirAll(rootDir, 0o755); err != nil {
		t.Fatalf(openRootErrFmt, err)
	}

	cleanupErr := errors.New("cleanup failure")
	originalCleanup := cleanupTempFileFn
	cleanupTempFileFn = func(*os.Root, string, *os.File) error {
		return cleanupErr
	}
	t.Cleanup(func() {
		cleanupTempFileFn = originalCleanup
	})

	err := WriteFileUnder(rootDir, targetPath, []byte("hello"), 0o600)
	if err == nil {
		t.Fatal("expected cleanup error")
	}
	if !errors.Is(err, cleanupErr) {
		t.Fatalf("expected cleanup error, got %v", err)
	}
}

func TestWriteFileUnderWriteError(t *testing.T) {
	rootDir := t.TempDir()
	targetPath := filepath.Join(rootDir, writeTestFileName)

	writeErr := errors.New("write failure")
	originalWriteFn := writeFileFn
	writeFileFn = func(*os.File, []byte) (int, error) {
		return 0, writeErr
	}
	t.Cleanup(func() {
		writeFileFn = originalWriteFn
	})

	err := WriteFileUnder(rootDir, targetPath, []byte("hello"), 0o600)
	if err == nil {
		t.Fatal("expected write error")
	}
	if !errors.Is(err, writeErr) {
		t.Fatalf("expected write error, got %v", err)
	}
}

func TestWriteFileUnderChmodError(t *testing.T) {
	rootDir := t.TempDir()
	targetPath := filepath.Join(rootDir, writeTestFileName)

	chmodErr := errors.New("chmod failure")
	originalChmodFn := chmodFileFn
	chmodFileFn = func(*os.File, os.FileMode) error {
		return chmodErr
	}
	t.Cleanup(func() {
		chmodFileFn = originalChmodFn
	})

	err := WriteFileUnder(rootDir, targetPath, []byte("hello"), 0o600)
	if err == nil {
		t.Fatal("expected chmod error")
	}
	if !errors.Is(err, chmodErr) {
		t.Fatalf("expected chmod error, got %v", err)
	}
}

func TestWriteFileUnderTempCloseError(t *testing.T) {
	rootDir := t.TempDir()
	targetPath := filepath.Join(rootDir, writeTestFileName)

	closeErr := errors.New("temp close failure")
	originalCloseFn := closeFileFn
	closeFileFn = func(*os.File) error {
		return closeErr
	}
	t.Cleanup(func() {
		closeFileFn = originalCloseFn
	})

	err := WriteFileUnder(rootDir, targetPath, []byte("hello"), 0o600)
	if err == nil {
		t.Fatal("expected temp close error")
	}
	if !errors.Is(err, closeErr) {
		t.Fatalf("expected temp close error, got %v", err)
	}
}

func TestResolveWriteTargetAbsFailuresViaHook(t *testing.T) {
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

			originalAbs := absPathFn
			absPathFn = func(path string) (string, error) {
				if path == tc.hookPath(rootDir, targetPath) {
					return "", tc.hookErr
				}
				return originalAbs(path)
			}
			t.Cleanup(func() {
				absPathFn = originalAbs
			})

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

func TestResolveWriteTargetRelFailureViaHook(t *testing.T) {
	rootDir := t.TempDir()
	targetPath := filepath.Join(rootDir, writeTestFileName)

	originalRel := relPathFn
	relPathFn = func(_, _ string) (string, error) {
		return "", errors.New("rel failure")
	}
	t.Cleanup(func() {
		relPathFn = originalRel
	})

	err := WriteFileUnder(rootDir, targetPath, []byte("hello"), 0o600)
	if err == nil {
		t.Fatal("expected relative path computation error")
	}
	if !strings.Contains(err.Error(), "compute relative path") {
		t.Fatalf(unexpectedErrFmt, err)
	}
}
