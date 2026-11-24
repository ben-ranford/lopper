package safeio

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestRegularParserReadsUsePositiveLimits(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "manifest")
	if err := os.WriteFile(file, []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := ReadRegularFileUnderLimit(root, file, 4)
	if err != nil || string(data) != "safe" {
		t.Fatalf("expected complete input at the bound, got %q, %v", data, err)
	}
	if _, err := ReadRegularFileLimit(file, 3); !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("expected exact-file byte limit, got %v", err)
	}
	if _, err := ReadRegularFileLimit(file, 0); !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("expected zero limit to be rejected, got %v", err)
	}
	if _, err := ReadRegularFileUnderLimit(root, filepath.Join(root, "missing"), 4); !os.IsNotExist(err) {
		t.Fatalf("expected missing file error, got %v", err)
	}
	if _, err := ReadRegularFileLimit(root, 4); !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("expected directory to be rejected, got %v", err)
	}
}
