//go:build !windows

package safeio

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
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

func TestWriteRootFallsBackToPinnedOverwriteWhenParentLacksWritePermissionOnNonWindows(t *testing.T) {
	if syscall.Geteuid() == 0 {
		t.Skip("effective privileges bypass parent write permission checks")
	}

	rootDir := t.TempDir()
	parentDir := filepath.Join(rootDir, "reports")
	if err := os.MkdirAll(parentDir, 0o755); err != nil {
		t.Fatalf("mkdir reports: %v", err)
	}
	targetPath := filepath.Join(parentDir, writeTestFileName)
	if err := os.WriteFile(targetPath, []byte("before"), 0o640); err != nil {
		t.Fatalf("seed target file: %v", err)
	}
	if err := os.Chmod(parentDir, 0o555); err != nil {
		t.Fatalf("chmod reports read-only: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(parentDir, 0o755); err != nil && !os.IsNotExist(err) {
			t.Errorf("restore reports permissions: %v", err)
		}
	})

	probePath := filepath.Join(parentDir, ".safeio-write-probe")
	if err := os.WriteFile(probePath, []byte("probe"), 0o600); err == nil {
		if removeErr := os.Remove(probePath); removeErr != nil {
			t.Fatalf("remove write probe: %v", removeErr)
		}
		t.Skip("effective privileges bypass missing parent write permission")
	} else if !os.IsPermission(err) {
		t.Skipf("parent write permission semantics are not testable: %v", err)
	}

	probe, err := os.OpenFile(targetPath, os.O_WRONLY, 0)
	if err != nil {
		t.Skipf("existing writable target cannot be reopened without parent write permission: %v", err)
	}
	if err := probe.Close(); err != nil {
		t.Fatalf("close writable target probe: %v", err)
	}

	root := openTestWriteRoot(t, rootDir, OpenWriteRoot)
	if err := root.WriteFileCreatingParents(filepath.Join("reports", writeTestFileName), []byte("after"), 0o600, 0o750); err != nil {
		t.Fatalf("WriteFileCreatingParents returned error: %v", err)
	}

	assertFileContent(t, targetPath, "after")
	info, err := os.Stat(targetPath)
	if err != nil {
		t.Fatalf("stat overwritten target: %v", err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("expected target mode 0640 to be preserved, got %#o", info.Mode().Perm())
	}
	if entries, err := os.ReadDir(parentDir); err != nil {
		t.Fatalf("read reports dir: %v", err)
	} else if len(entries) != 1 || entries[0].Name() != writeTestFileName {
		t.Fatalf("expected only target file to remain, got %v", entries)
	}
}

func TestWriteAtomicReplacementWithPinnedTargetFallsBackWhenTempCreationDeniedOnNonWindows(t *testing.T) {
	targetPath := filepath.Join(t.TempDir(), writeTestFileName)
	if err := os.WriteFile(targetPath, []byte("before"), 0o640); err != nil {
		t.Fatalf("seed target info path: %v", err)
	}
	info := statTestPath(t, targetPath)
	targetData := []byte("before")
	targetFile := &truncatingFakeFile{
		fakeFile: &fakeFile{
			stat: func() (fs.FileInfo, error) { return info, nil },
			write: func(p []byte) (int, error) {
				targetData = append(targetData, p...)
				return len(p), nil
			},
			close: closeWithoutError,
		},
		truncate: func(size int64) error {
			if size != 0 {
				t.Fatalf("unexpected truncate size: %d", size)
			}
			targetData = targetData[:0]
			return nil
		},
	}
	root := &fakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			if name != writeTestFileName {
				return nil, os.ErrNotExist
			}
			return info, nil
		},
		openFile: func(name string, flag int, perm os.FileMode) (File, error) {
			if name == writeTestFileName {
				return targetFile, nil
			}
			return nil, os.ErrPermission
		},
	}

	if err := writeAtomicReplacementWithPinnedTarget(root, writeTestFileName, []byte("after"), 0o600, targetFile); err != nil {
		t.Fatalf("writeAtomicReplacementWithPinnedTarget returned error: %v", err)
	}
	if string(targetData) != "after" {
		t.Fatalf("expected fallback overwrite data, got %q", string(targetData))
	}
}

func TestWriteAtomicReplacementWithPinnedTargetFallsBackWhenRenameDeniedOnNonWindows(t *testing.T) {
	targetPath := filepath.Join(t.TempDir(), writeTestFileName)
	if err := os.WriteFile(targetPath, []byte("before"), 0o640); err != nil {
		t.Fatalf("seed target info path: %v", err)
	}
	info := statTestPath(t, targetPath)
	targetData := []byte("before")
	targetFile := &truncatingFakeFile{
		fakeFile: &fakeFile{
			stat: func() (fs.FileInfo, error) { return info, nil },
			write: func(p []byte) (int, error) {
				targetData = append(targetData, p...)
				return len(p), nil
			},
			close: closeWithoutError,
		},
		truncate: func(size int64) error {
			if size != 0 {
				t.Fatalf("unexpected truncate size: %d", size)
			}
			targetData = targetData[:0]
			return nil
		},
	}
	removeCalls := 0
	root := &fakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			if name != writeTestFileName {
				return nil, os.ErrNotExist
			}
			return info, nil
		},
		openFile: func(name string, flag int, perm os.FileMode) (File, error) {
			if name == writeTestFileName {
				return targetFile, nil
			}
			return &fakeFile{
				write: func(p []byte) (int, error) { return len(p), nil },
				chmod: chmodWithoutError,
				close: closeWithoutError,
			}, nil
		},
		rename: func(string, string) error { return os.ErrPermission },
		remove: func(string) error {
			removeCalls++
			return nil
		},
	}

	if err := writeAtomicReplacementWithPinnedTarget(root, writeTestFileName, []byte("after"), 0o600, targetFile); err != nil {
		t.Fatalf("writeAtomicReplacementWithPinnedTarget returned error: %v", err)
	}
	if removeCalls != 1 {
		t.Fatalf("expected temp cleanup after rename fallback, got %d removes", removeCalls)
	}
	if string(targetData) != "after" {
		t.Fatalf("expected fallback overwrite data, got %q", string(targetData))
	}
}
