package safeio

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSafeIOAdditionalReadBranches(t *testing.T) {
	rootDir := t.TempDir()
	if resolvedRoot, err := filepath.EvalSymlinks(rootDir); err == nil && resolvedRoot != "" {
		rootDir = resolvedRoot
	}
	targetPath := filepath.Join(rootDir, "nested", "file.txt")
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	if err := os.WriteFile(targetPath, []byte("hello"), 0o600); err != nil {
		t.Fatalf("write target file: %v", err)
	}

	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(originalWD); err != nil {
			t.Fatalf("restore wd %s: %v", originalWD, err)
		}
	})
	if err := os.Chdir(rootDir); err != nil {
		t.Fatalf("chdir root dir: %v", err)
	}

	data, err := ReadFileUnder(rootDir, filepath.Join("nested", ".", "file.txt"))
	if err != nil {
		t.Fatalf("read file under relative path: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("unexpected relative read content: %q", string(data))
	}

	if _, err := ReadFile(rootDir); err == nil {
		t.Fatalf("expected directory reads to fail")
	}
}
