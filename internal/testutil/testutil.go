package testutil

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func CanceledContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func MustWriteFile(t *testing.T, path string, content string) {
	MustWriteFileMode(t, path, content, 0o600)
}

func MustWriteFileMode(t *testing.T, path string, content string, perm os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), perm); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func WriteNumberedTextFiles(t *testing.T, dir string, count int) {
	t.Helper()
	for i := 0; i < count; i++ {
		MustWriteFile(t, filepath.Join(dir, fmt.Sprintf("f-%d.txt", i)), "x")
	}
}

func WriteTempFile(t *testing.T, filename string, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), filename)
	MustWriteFileMode(t, path, content, 0o644)
	return path
}

func Chdir(t *testing.T, dir string) {
	t.Helper()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir %s: %v", dir, err)
	}
	t.Cleanup(func() { _ = os.Chdir(originalWD) })
}

func MustFirstFileEntry(t *testing.T, dir string) fs.DirEntry {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir %s: %v", dir, err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			return entry
		}
	}
	t.Fatalf("expected file entry in %s", dir)
	return nil
}
