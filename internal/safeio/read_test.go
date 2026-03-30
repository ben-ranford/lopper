package safeio

import (
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	unexpectedContentFmt = "unexpected content: got %q"
	writeFileErrFmt      = "write file: %v"
	missingFileName      = "missing.txt"
	resolveTargetPathErr = "resolve target path"
	rootCloseErrFmt      = "expected root close error, got %v"
)

func TestReadFileUnderReadsFileInsideRoot(t *testing.T) {
	rootDir := t.TempDir()
	targetPath := filepath.Join(rootDir, "nested", writeTestFileName)
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		t.Fatalf("create parent dir: %v", err)
	}
	if err := os.WriteFile(targetPath, []byte("hello"), 0o600); err != nil {
		t.Fatalf(writeFileErrFmt, err)
	}

	data, err := ReadFileUnder(rootDir, targetPath)
	if err != nil {
		t.Fatalf("ReadFileUnder returned error: %v", err)
	}
	if got := string(data); got != "hello" {
		t.Fatalf(unexpectedContentFmt, got)
	}
}

func TestReadFileUnderLimitReadsFileInsideRoot(t *testing.T) {
	rootDir := t.TempDir()
	targetPath := filepath.Join(rootDir, "nested", writeTestFileName)
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		t.Fatalf("create parent dir: %v", err)
	}
	if err := os.WriteFile(targetPath, []byte("hello"), 0o600); err != nil {
		t.Fatalf(writeFileErrFmt, err)
	}

	data, err := ReadFileUnderLimit(rootDir, targetPath, 5)
	if err != nil {
		t.Fatalf("ReadFileUnderLimit returned error: %v", err)
	}
	if got := string(data); got != "hello" {
		t.Fatalf(unexpectedContentFmt, got)
	}
}

func TestReadFileUnderLimitRejectsOversizedFile(t *testing.T) {
	rootDir := t.TempDir()
	targetPath := filepath.Join(rootDir, "large.txt")
	if err := os.WriteFile(targetPath, []byte("hello"), 0o600); err != nil {
		t.Fatalf(writeFileErrFmt, err)
	}

	_, err := ReadFileUnderLimit(rootDir, targetPath, 4)
	if !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("expected ErrFileTooLarge, got %v", err)
	}
}

func TestReadOpenedFileRejectsOversizedPipeContent(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := reader.Close(); closeErr != nil {
			t.Fatalf("close pipe reader: %v", closeErr)
		}
	})

	done := make(chan error, 1)
	go func() {
		_, writeErr := writer.Write([]byte("hello"))
		closeErr := writer.Close()
		if writeErr != nil {
			done <- writeErr
			return
		}
		done <- closeErr
	}()

	_, err = readOpenedFile(reader, 4)
	if !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("expected ErrFileTooLarge for pipe content, got %v", err)
	}
	if writeErr := <-done; writeErr != nil {
		t.Fatalf("pipe writer error: %v", writeErr)
	}
}

func TestReadOpenedFileAllowsMaxInt64Limit(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := reader.Close(); closeErr != nil {
			t.Fatalf("close pipe reader: %v", closeErr)
		}
	})

	done := make(chan error, 1)
	go func() {
		_, writeErr := writer.Write([]byte("ok"))
		closeErr := writer.Close()
		if writeErr != nil {
			done <- writeErr
			return
		}
		done <- closeErr
	}()

	data, err := readOpenedFile(reader, math.MaxInt64)
	if err != nil {
		t.Fatalf("readOpenedFile returned error: %v", err)
	}
	if string(data) != "ok" {
		t.Fatalf(unexpectedContentFmt, string(data))
	}
	if writeErr := <-done; writeErr != nil {
		t.Fatalf("pipe writer error: %v", writeErr)
	}
}

func TestReadOpenedFileDirectoryReadError(t *testing.T) {
	dirFile, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open temp dir: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := dirFile.Close(); closeErr != nil {
			t.Fatalf("close temp dir file: %v", closeErr)
		}
	})

	if _, err := readOpenedFile(dirFile, 1); err == nil {
		t.Fatalf("expected readOpenedFile to fail when reading a directory")
	}
}

func TestReadFileUnderRejectsPathTraversalOutsideRoot(t *testing.T) {
	parentDir := t.TempDir()
	rootDir := filepath.Join(parentDir, "root")
	if err := os.MkdirAll(rootDir, 0o755); err != nil {
		t.Fatalf("create root dir: %v", err)
	}

	outsidePath := filepath.Join(parentDir, "secret.txt")
	if err := os.WriteFile(outsidePath, []byte("secret"), 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}

	_, err := ReadFileUnder(rootDir, outsidePath)
	if err == nil {
		t.Fatal("expected error for outside path, got nil")
	}
	if !strings.Contains(err.Error(), escapesRootErr) {
		t.Fatalf(unexpectedErrFmt, err)
	}
}

func TestReadFileUnderRejectsParentDirectoryTarget(t *testing.T) {
	parentDir := t.TempDir()
	rootDir := filepath.Join(parentDir, "root")
	if err := os.MkdirAll(rootDir, 0o755); err != nil {
		t.Fatalf("create root dir: %v", err)
	}

	_, err := ReadFileUnder(rootDir, parentDir)
	if err == nil {
		t.Fatal("expected error for parent directory target")
	}
	if !strings.Contains(err.Error(), escapesRootErr) {
		t.Fatalf(unexpectedErrFmt, err)
	}
}

func TestReadFileUnderReturnsErrorForMissingFile(t *testing.T) {
	rootDir := t.TempDir()
	missingPath := filepath.Join(rootDir, missingFileName)

	_, err := ReadFileUnder(rootDir, missingPath)
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

func TestReadFileUnderRejectsNonDirectoryRoot(t *testing.T) {
	rootDir := t.TempDir()
	rootFile := filepath.Join(rootDir, "root-file")
	if err := os.WriteFile(rootFile, []byte("not-a-dir"), 0o600); err != nil {
		t.Fatalf("write root file: %v", err)
	}

	_, err := ReadFileUnder(rootFile, rootFile)
	if err == nil {
		t.Fatal("expected error when root is not a directory")
	}
	if !strings.Contains(err.Error(), "open root") {
		t.Fatalf(unexpectedErrFmt, err)
	}
}

func TestReadFileUnderRootAbsFailureWhenCWDRemoved(t *testing.T) {
	withRemovedWorkingDir(t, "dead")

	_, err := ReadFileUnder(".", "x")
	if err == nil {
		t.Fatal("expected root path resolution error")
	}
	if !strings.Contains(err.Error(), "resolve root path") && !strings.Contains(err.Error(), "open root") {
		t.Fatalf(unexpectedErrFmt, err)
	}
}

func TestReadFileUnderTargetAbsFailureWhenCWDRemoved(t *testing.T) {
	rootDir := t.TempDir()
	withRemovedWorkingDir(t, "dead-target")

	_, err := ReadFileUnder(rootDir, "relative-target.txt")
	if err == nil {
		t.Fatal("expected target path resolution error")
	}
	if !strings.Contains(err.Error(), resolveTargetPathErr) && !strings.Contains(err.Error(), escapesRootErr) {
		t.Fatalf(unexpectedErrFmt, err)
	}
}

func TestReadFileUnderDirectoryTargetReturnsReadError(t *testing.T) {
	rootDir := t.TempDir()
	dirTarget := filepath.Join(rootDir, "nested")
	if err := os.MkdirAll(dirTarget, 0o755); err != nil {
		t.Fatalf("create dir target: %v", err)
	}

	_, err := ReadFileUnder(rootDir, dirTarget)
	if err == nil {
		t.Fatal("expected error when reading a directory target")
	}
}

func TestReadFileUnderRootPathAsTargetReturnsError(t *testing.T) {
	rootDir := t.TempDir()
	_, err := ReadFileUnder(rootDir, rootDir)
	if err == nil {
		t.Fatal("expected error when target is root directory")
	}
}

func TestPathReadersReadAbsoluteAndRelativePaths(t *testing.T) {
	rootDir := t.TempDir()
	targetPath := filepath.Join(rootDir, "target.txt")
	if err := os.WriteFile(targetPath, []byte("content"), 0o600); err != nil {
		t.Fatalf("write target file: %v", err)
	}

	for _, reader := range pathReaders() {
		t.Run(reader.name, func(t *testing.T) {
			assertReadContent(t, reader.read, targetPath, "content")
			withWorkingDir(t, rootDir)
			assertReadContent(t, reader.read, "target.txt", "content")
		})
	}
}

func TestPathReadersReturnErrorForMissingFile(t *testing.T) {
	missingPath := filepath.Join(t.TempDir(), missingFileName)
	for _, reader := range pathReaders() {
		t.Run(reader.name, func(t *testing.T) {
			if _, err := reader.read(missingPath); err == nil {
				t.Fatal("expected error for missing file")
			}
		})
	}
}

func TestReadFileReturnsErrorWhenParentIsNotDirectory(t *testing.T) {
	rootDir := t.TempDir()
	parentFile := filepath.Join(rootDir, "not-a-dir")
	if err := os.WriteFile(parentFile, []byte("x"), 0o600); err != nil {
		t.Fatalf("write parent file: %v", err)
	}

	_, err := ReadFile(filepath.Join(parentFile, "child.txt"))
	if err == nil {
		t.Fatal("expected error when parent path is a file")
	}
	if !strings.Contains(err.Error(), "open parent root") {
		t.Fatalf(unexpectedErrFmt, err)
	}
}

func TestReadFileTargetAbsFailureWhenCWDRemoved(t *testing.T) {
	withRemovedWorkingDir(t, "dead-readfile")

	_, err := ReadFile("relative.txt")
	if err == nil {
		t.Fatal("expected target path resolution error")
	}
	if !strings.Contains(err.Error(), resolveTargetPathErr) && !strings.Contains(err.Error(), "open parent root") {
		t.Fatalf(unexpectedErrFmt, err)
	}
}

func TestReadFileUnderRootAbsFailureViaHook(t *testing.T) {
	rootDir := t.TempDir()
	targetPath := filepath.Join(rootDir, writeTestFileName)

	originalAbs := absPathFn
	absPathFn = func(path string) (string, error) {
		if path == rootDir {
			return "", errors.New("root abs failure")
		}
		return originalAbs(path)
	}
	t.Cleanup(func() {
		absPathFn = originalAbs
	})

	_, err := ReadFileUnder(rootDir, targetPath)
	if err == nil {
		t.Fatal("expected root path absolute resolution error")
	}
	if !strings.Contains(err.Error(), "resolve root path") {
		t.Fatalf(unexpectedErrFmt, err)
	}
}

func TestReadFileUnderTargetAbsFailureViaHook(t *testing.T) {
	rootDir := t.TempDir()
	targetPath := filepath.Join(rootDir, writeTestFileName)

	originalAbs := absPathFn
	absPathFn = func(path string) (string, error) {
		if path == targetPath {
			return "", errors.New("target abs failure")
		}
		return originalAbs(path)
	}
	t.Cleanup(func() {
		absPathFn = originalAbs
	})

	_, err := ReadFileUnder(rootDir, targetPath)
	if err == nil {
		t.Fatal("expected target path absolute resolution error")
	}
	if !strings.Contains(err.Error(), resolveTargetPathErr) {
		t.Fatalf(unexpectedErrFmt, err)
	}
}

func TestReadFileUnderRelFailureViaHook(t *testing.T) {
	rootDir := t.TempDir()
	targetPath := filepath.Join(rootDir, writeTestFileName)
	if err := os.WriteFile(targetPath, []byte("hi"), 0o600); err != nil {
		t.Fatalf(writeFileErrFmt, err)
	}

	originalRel := relPathFn
	relPathFn = func(_, _ string) (string, error) {
		return "", errors.New("rel failure")
	}
	t.Cleanup(func() {
		relPathFn = originalRel
	})

	_, err := ReadFileUnder(rootDir, targetPath)
	if err == nil {
		t.Fatal("expected relative path resolution error")
	}
	if !strings.Contains(err.Error(), "compute relative path") {
		t.Fatalf(unexpectedErrFmt, err)
	}
}

func TestReadFileUnderCloseRootError(t *testing.T) {
	rootDir := t.TempDir()
	targetPath := filepath.Join(rootDir, writeTestFileName)
	if err := os.WriteFile(targetPath, []byte("hi"), 0o600); err != nil {
		t.Fatalf(writeFileErrFmt, err)
	}

	expectedErr := errors.New("root close failure")
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

	_, err := ReadFileUnder(rootDir, targetPath)
	if err == nil {
		t.Fatal("expected root close error to be returned")
	}
	if !errors.Is(err, expectedErr) {
		t.Fatalf(rootCloseErrFmt, err)
	}
}

func TestReadFileUnderCloseFileError(t *testing.T) {
	rootDir := t.TempDir()
	targetPath := filepath.Join(rootDir, writeTestFileName)
	if err := os.WriteFile(targetPath, []byte("hi"), 0o600); err != nil {
		t.Fatalf(writeFileErrFmt, err)
	}

	expectedErr := errors.New("file close failure")
	originalCloseFile := closeFileFn
	closeFileFn = func(file *os.File) error {
		err := originalCloseFile(file)
		if err != nil {
			return err
		}
		return expectedErr
	}
	t.Cleanup(func() {
		closeFileFn = originalCloseFile
	})

	_, err := ReadFileUnder(rootDir, targetPath)
	if err == nil {
		t.Fatal("expected file close error to be returned")
	}
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected file close error, got %v", err)
	}
}

func TestReadFileCloseError(t *testing.T) {
	rootDir := t.TempDir()
	targetPath := filepath.Join(rootDir, writeTestFileName)
	if err := os.WriteFile(targetPath, []byte("hi"), 0o600); err != nil {
		t.Fatalf(writeFileErrFmt, err)
	}

	expectedErr := errors.New("read closer close failure")
	originalCloseReadCloser := closeReadCloserFn
	closeReadCloserFn = func(reader io.ReadCloser) error {
		err := originalCloseReadCloser(reader)
		if err != nil {
			return err
		}
		return expectedErr
	}
	t.Cleanup(func() {
		closeReadCloserFn = originalCloseReadCloser
	})

	_, err := ReadFile(targetPath)
	if err == nil {
		t.Fatal("expected read closer close error to be returned")
	}
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected close error, got %v", err)
	}
}

func TestOpenFileTargetAbsFailureViaHook(t *testing.T) {
	targetPath := filepath.Join(t.TempDir(), writeTestFileName)

	originalAbs := absPathFn
	absPathFn = func(path string) (string, error) {
		if path == targetPath {
			return "", errors.New("openfile target abs failure")
		}
		return originalAbs(path)
	}
	t.Cleanup(func() {
		absPathFn = originalAbs
	})

	_, err := OpenFile(targetPath)
	if err == nil {
		t.Fatal("expected target path absolute resolution error")
	}
	if !strings.Contains(err.Error(), resolveTargetPathErr) {
		t.Fatalf(unexpectedErrFmt, err)
	}
}

func TestOpenFileMissingFileCloseRootError(t *testing.T) {
	targetPath := filepath.Join(t.TempDir(), missingFileName)

	originalRootOpen := openRootOpenFn
	openRootOpenFn = func(_ *os.Root, _ string) (*os.File, error) {
		return nil, os.ErrNotExist
	}
	t.Cleanup(func() {
		openRootOpenFn = originalRootOpen
	})

	expectedErr := errors.New("open parent root close failure")
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

	_, err := OpenFile(targetPath)
	if err == nil {
		t.Fatal("expected fs.ErrNotExist on missing file with root close error")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected wrapped ErrNotExist, got %v", err)
	}
	if !errors.Is(err, expectedErr) {
		t.Fatalf(rootCloseErrFmt, err)
	}
}

func TestOpenFileOpenErrorCloseRootError(t *testing.T) {
	targetDir := t.TempDir()
	targetPath := filepath.Join(targetDir, "child.txt")
	if err := os.WriteFile(filepath.Join(targetDir, "marker"), []byte("x"), 0o600); err != nil {
		t.Fatalf(writeFileErrFmt, err)
	}

	originalRootOpen := openRootOpenFn
	openErr := errors.New("open child failure")
	openRootOpenFn = func(_ *os.Root, _ string) (*os.File, error) {
		return nil, openErr
	}
	t.Cleanup(func() {
		openRootOpenFn = originalRootOpen
	})

	expectedErr := errors.New("open root close failure")
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

	_, err := OpenFile(targetPath)
	if err == nil {
		t.Fatal("expected open error joined with root close error")
	}
	if !errors.Is(err, openErr) {
		t.Fatalf("expected original open error, got %v", err)
	}
	if !errors.Is(err, expectedErr) {
		t.Fatalf(rootCloseErrFmt, err)
	}
}

type pathReaderCase struct {
	name string
	read func(string) (string, error)
}

func pathReaders() []pathReaderCase {
	return []pathReaderCase{
		{name: "read-file", read: readFileContent},
		{name: "open-file", read: openFileContent},
	}
}

func readFileContent(path string) (string, error) {
	content, err := ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(content), nil
}

func openFileContent(path string) (string, error) {
	file, err := OpenFile(path)
	if err != nil {
		return "", err
	}
	content, readErr := io.ReadAll(file)
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		return "", errors.Join(readErr, closeErr)
	}
	return string(content), nil
}

func assertReadContent(t *testing.T, read func(string) (string, error), path, want string) {
	t.Helper()
	content, err := read(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if content != want {
		t.Fatalf("unexpected content from %s: %q", path, content)
	}
}
