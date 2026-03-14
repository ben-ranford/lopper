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
	unexpectedErrFmt     = "unexpected error: %v"
	unexpectedContentFmt = "unexpected content: got %q"
	escapesRootErr       = "path escapes root"
	getwdErrFmt          = "getwd: %v"
	restoreWDErrFmt      = "restore wd %s: %v"
	mkdirDeadDirFmt      = "mkdir deadDir: %v"
	chdirDeadDirFmt      = "chdir deadDir: %v"
	removeDeadDirFmt     = "remove deadDir: %v"
	writeFileErrFmt      = "write file: %v"
)

func TestReadFileUnderReadsFileInsideRoot(t *testing.T) {
	rootDir := t.TempDir()
	targetPath := filepath.Join(rootDir, "nested", "file.txt")
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
	targetPath := filepath.Join(rootDir, "nested", "file.txt")
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
	defer reader.Close()

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
	defer reader.Close()

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
	missingPath := filepath.Join(rootDir, "missing.txt")

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
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf(getwdErrFmt, err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(originalWD); err != nil {
			t.Fatalf(restoreWDErrFmt, originalWD, err)
		}
	})

	deadDir := filepath.Join(t.TempDir(), "dead")
	if err := os.MkdirAll(deadDir, 0o755); err != nil {
		t.Fatalf(mkdirDeadDirFmt, err)
	}
	if err := os.Chdir(deadDir); err != nil {
		t.Fatalf(chdirDeadDirFmt, err)
	}
	if err := os.RemoveAll(deadDir); err != nil {
		t.Fatalf(removeDeadDirFmt, err)
	}

	_, err = ReadFileUnder(".", "x")
	if err == nil {
		t.Fatal("expected root path resolution error")
	}
	if !strings.Contains(err.Error(), "resolve root path") && !strings.Contains(err.Error(), "open root") {
		t.Fatalf(unexpectedErrFmt, err)
	}
}

func TestReadFileUnderTargetAbsFailureWhenCWDRemoved(t *testing.T) {
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf(getwdErrFmt, err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(originalWD); err != nil {
			t.Fatalf(restoreWDErrFmt, originalWD, err)
		}
	})

	rootDir := t.TempDir()
	deadDir := filepath.Join(t.TempDir(), "dead-target")
	if err := os.MkdirAll(deadDir, 0o755); err != nil {
		t.Fatalf(mkdirDeadDirFmt, err)
	}
	if err := os.Chdir(deadDir); err != nil {
		t.Fatalf(chdirDeadDirFmt, err)
	}
	if err := os.RemoveAll(deadDir); err != nil {
		t.Fatalf(removeDeadDirFmt, err)
	}

	_, err = ReadFileUnder(rootDir, "relative-target.txt")
	if err == nil {
		t.Fatal("expected target path resolution error")
	}
	if !strings.Contains(err.Error(), "resolve target path") && !strings.Contains(err.Error(), escapesRootErr) {
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
	missingPath := filepath.Join(t.TempDir(), "missing.txt")
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
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf(getwdErrFmt, err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(originalWD); err != nil {
			t.Fatalf(restoreWDErrFmt, originalWD, err)
		}
	})

	deadDir := filepath.Join(t.TempDir(), "dead-readfile")
	if err := os.MkdirAll(deadDir, 0o755); err != nil {
		t.Fatalf("mkdir deadDir: %v", err)
	}
	if err := os.Chdir(deadDir); err != nil {
		t.Fatalf("chdir deadDir: %v", err)
	}
	if err := os.RemoveAll(deadDir); err != nil {
		t.Fatalf("remove deadDir: %v", err)
	}

	_, err = ReadFile("relative.txt")
	if err == nil {
		t.Fatal("expected target path resolution error")
	}
	if !strings.Contains(err.Error(), "resolve target path") && !strings.Contains(err.Error(), "open parent root") {
		t.Fatalf(unexpectedErrFmt, err)
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

func withWorkingDir(t *testing.T, dir string) {
	t.Helper()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf(getwdErrFmt, err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(originalWD); err != nil {
			t.Fatalf(restoreWDErrFmt, originalWD, err)
		}
	})
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir %s: %v", dir, err)
	}
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
