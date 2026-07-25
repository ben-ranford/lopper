//go:build windows

package safeio

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestOpenAtomicTestFileSharesDeleteOnWindows(t *testing.T) {
	rootDir := t.TempDir()
	targetPath := filepath.Join(rootDir, "trace.ndjson")
	prepareAtomicRegularFile(t, rootDir, targetPath)

	file := openAtomicTestFile(t, targetPath)
	removeAtomicTestTarget(t, file, rootDir, targetPath)

	if _, err := os.Stat(targetPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected original path to be absent, got %v", err)
	}
	info, err := file.Stat()
	if err != nil {
		t.Fatalf("stat renamed open handle: %v", err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("expected regular open handle, got %v", info.Mode())
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close renamed open handle: %v", err)
	}
	assertAtomicTestFileClosed(t, file)
}

func openAtomicTestFile(t *testing.T, path string) *os.File {
	t.Helper()
	pathUTF16, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatalf("encode %q: %v", path, err)
	}
	handle, err := windows.CreateFile(
		pathUTF16,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		atomicTestFileAttributes(t, path),
		0,
	)
	if err != nil {
		t.Fatalf("open %q with delete sharing: %v", path, err)
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		if closeErr := windows.CloseHandle(handle); closeErr != nil {
			t.Fatalf("wrap %q handle; close handle: %v", path, closeErr)
		}
		t.Fatalf("wrap %q handle: os.NewFile returned nil", path)
	}
	return trackAtomicTestFile(t, file)
}

func atomicTestFileAttributes(t *testing.T, path string) uint32 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %q before open: %v", path, err)
	}
	if info.IsDir() {
		return windows.FILE_FLAG_BACKUP_SEMANTICS
	}
	return windows.FILE_ATTRIBUTE_NORMAL
}

func removeAtomicTestTarget(t *testing.T, file *os.File, rootDir, targetPath string) {
	t.Helper()
	if file == nil {
		if err := os.Remove(targetPath); err != nil {
			t.Fatalf("remove target: %v", err)
		}
		return
	}
	movedPath := filepath.Join(rootDir, "opened-target.ndjson")
	if err := os.Rename(targetPath, movedPath); err != nil {
		t.Fatalf("rename open target out of namespace: %v", err)
	}
}
