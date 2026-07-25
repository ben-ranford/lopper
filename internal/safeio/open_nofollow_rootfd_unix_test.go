//go:build linux

package safeio

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOSRootFDRejectsNilRoot(t *testing.T) {
	var root *os.Root
	_, err := osRootFD(root)
	if err == nil || !strings.Contains(err.Error(), "nil root") {
		t.Fatalf("expected nil root rejection, got %v", err)
	}
}

func TestOSRootFDRejectsZeroValueRoot(t *testing.T) {
	root := new(os.Root)
	_, err := osRootFD(root)
	if err == nil || !strings.Contains(err.Error(), "missing root state") {
		t.Fatalf("expected zero-value root rejection, got %v", err)
	}
}

func TestOSRootFDReturnsUnderlyingDescriptor(t *testing.T) {
	rootDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(rootDir, "trace.ndjson"), []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write probe file: %v", err)
	}

	root, err := os.OpenRoot(rootDir)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Fatalf("close root: %v", closeErr)
		}
	}()

	fd, err := osRootFD(root)
	if err != nil {
		t.Fatalf("osRootFD returned error: %v", err)
	}
	if fd < 0 {
		t.Fatalf("expected non-negative root fd, got %d", fd)
	}
}
