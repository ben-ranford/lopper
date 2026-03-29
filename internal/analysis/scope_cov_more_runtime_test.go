package analysis

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const scopeKeepJS = "keep.js"

func TestScopeCopyFileAdditionalErrorBranches(t *testing.T) {
	repo := t.TempDir()
	scopedRoot := t.TempDir()
	sourcePath := filepath.Join(repo, "src", scopeKeepJS)
	writeScopeFile(t, sourcePath, "export const keep = true\n")

	targetDir := filepath.Join(scopedRoot, "src", scopeKeepJS)
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatalf("mkdir target dir: %v", err)
	}
	if err := copyFile(repo, scopedRoot, filepath.Join("src", scopeKeepJS)); err == nil {
		t.Fatalf("expected copyFile to fail when target path is a directory")
	}

	sourceDir := filepath.Join(repo, "src", "nested")
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatalf("mkdir source dir: %v", err)
	}
	if err := copyFile(repo, scopedRoot, filepath.Join("src", "nested")); err == nil {
		t.Fatalf("expected copyFile to fail when source path is a directory")
	}

	var err error
	joinCloseError(&err, func() error { return errors.New("close failed") })
	if err == nil || !strings.Contains(err.Error(), "close failed") {
		t.Fatalf("expected joinCloseError to propagate close failure, got %v", err)
	}
}
