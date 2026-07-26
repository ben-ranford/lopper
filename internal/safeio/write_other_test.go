//go:build !windows

package safeio

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteFileReplacingUnderReplacesReadOnlyExistingRegularFileOnNonWindows(t *testing.T) {
	rootDir := t.TempDir()
	targetPath := filepath.Join(rootDir, writeTestFileName)
	if err := os.WriteFile(targetPath, []byte("before"), 0o444); err != nil {
		t.Fatalf("seed target file: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(targetPath, 0o600); err != nil && !os.IsNotExist(err) {
			t.Errorf("restore target file permissions: %v", err)
		}
	})

	probe, probeErr := os.OpenFile(targetPath, os.O_WRONLY, 0)
	if probeErr == nil {
		if err := probe.Close(); err != nil {
			t.Fatalf("close writability probe: %v", err)
		}
		t.Skip("effective privileges bypass read-only file permissions")
	}
	if !os.IsPermission(probeErr) {
		t.Skipf("read-only file semantics are not testable: %v", probeErr)
	}

	if err := WriteFileReplacingUnder(rootDir, targetPath, []byte("after"), 0o600); err != nil {
		t.Fatalf("WriteFileReplacingUnder returned error: %v", err)
	}

	assertFileContent(t, targetPath, "after")
	info, err := os.Stat(targetPath)
	if err != nil {
		t.Fatalf("stat replaced file: %v", err)
	}
	if info.Mode().Perm() != 0o444 {
		t.Fatalf("expected existing read-only mode 0444 to be preserved, got %#o", info.Mode().Perm())
	}
}
