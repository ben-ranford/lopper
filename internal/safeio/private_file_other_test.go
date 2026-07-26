//go:build !windows

package safeio

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRegularFilePrivateToOwnerRejectsGroupReadableMode(t *testing.T) {
	rootDir := t.TempDir()
	targetPath := filepath.Join(rootDir, "auth.key")
	if err := os.WriteFile(targetPath, []byte("key"), 0o640); err != nil {
		t.Fatalf("seed group-readable key: %v", err)
	}
	if err := os.Chmod(targetPath, 0o640); err != nil {
		t.Fatalf("set group-readable key mode: %v", err)
	}
	root, err := OpenWriteRoot(rootDir)
	if err != nil {
		t.Fatalf("open key root: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Errorf("close key root: %v", closeErr)
		}
	})
	info, err := root.Lstat("auth.key")
	if err != nil {
		t.Fatalf("inspect group-readable key: %v", err)
	}

	private, err := root.RegularFilePrivateToOwner("auth.key", info)
	if err != nil {
		t.Fatalf("validate group-readable key: %v", err)
	}
	if private {
		t.Fatal("expected group-readable key to fail owner-only validation")
	}
}
