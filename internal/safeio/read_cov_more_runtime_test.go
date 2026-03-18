package safeio

import (
	"path/filepath"
	"testing"
)

func TestReadFileUnderAndOpenFileInvalidPathBranches(t *testing.T) {
	rootDir := t.TempDir()
	badPath := filepath.Join(rootDir, "bad\x00name")

	if _, err := ReadFileUnder(rootDir, badPath); err == nil {
		t.Fatalf("expected ReadFileUnder to fail for invalid rooted path")
	}

	if _, err := OpenFile(badPath); err == nil {
		t.Fatalf("expected OpenFile to fail for invalid file name")
	}
}
