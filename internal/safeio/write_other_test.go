//go:build !windows

package safeio

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenPinnedReplacementTargetIfNeededSkipsPinnedTargetOnNonWindows(t *testing.T) {
	openCalls := 0
	root := &fakeRoot{
		openFile: func(string, int, os.FileMode) (File, error) {
			openCalls++
			return nil, errors.New("unexpected pinned open")
		},
	}

	file, closeFile, err := openPinnedReplacementTargetIfNeeded(root, writeTestFileName, statTestPath(t, t.TempDir()))
	if err != nil {
		t.Fatalf("expected pinned target open to be skipped, got %v", err)
	}
	if file != nil {
		t.Fatal("expected no pinned target file on non-Windows")
	}
	if err := closeFile(); err != nil {
		t.Fatalf("expected no-op pinned target close, got %v", err)
	}
	if openCalls != 0 {
		t.Fatalf("expected no pinned target open calls, got %d", openCalls)
	}
}

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

func TestWriteRootReplacingParentsReplacesReadOnlyExistingRegularFileOnNonWindows(t *testing.T) {
	rootDir := t.TempDir()
	root := openTestWriteRoot(t, rootDir, OpenWriteRoot)
	targetPath := filepath.Join("nested", writeTestFileName)
	targetAbs := filepath.Join(rootDir, targetPath)
	if err := os.MkdirAll(filepath.Dir(targetAbs), 0o750); err != nil {
		t.Fatalf("mkdir target parent: %v", err)
	}
	if err := os.WriteFile(targetAbs, []byte("before"), 0o444); err != nil {
		t.Fatalf("seed target file: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(targetAbs, 0o600); err != nil && !os.IsNotExist(err) {
			t.Errorf("restore target file permissions: %v", err)
		}
	})

	probe, probeErr := os.OpenFile(targetAbs, os.O_WRONLY, 0)
	if probeErr == nil {
		if err := probe.Close(); err != nil {
			t.Fatalf("close writability probe: %v", err)
		}
		t.Skip("effective privileges bypass read-only file permissions")
	}
	if !os.IsPermission(probeErr) {
		t.Skipf("read-only file semantics are not testable: %v", probeErr)
	}

	if err := root.WriteFileReplacingParents(targetPath, []byte("after"), 0o600, 0o750); err != nil {
		t.Fatalf("WriteFileReplacingParents returned error: %v", err)
	}

	assertFileContent(t, targetAbs, "after")
	info, err := os.Stat(targetAbs)
	if err != nil {
		t.Fatalf("stat replaced file: %v", err)
	}
	if info.Mode().Perm() != 0o444 {
		t.Fatalf("expected existing read-only mode 0444 to be preserved, got %#o", info.Mode().Perm())
	}
}
