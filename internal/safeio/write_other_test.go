//go:build !windows

package safeio

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
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
	targetPath := seedReadOnlyTargetFile(t, rootDir, 0o444)
	requireReadOnlyTarget(t, targetPath)

	if err := WriteFileReplacingUnder(rootDir, targetPath, []byte("after"), 0o600); err != nil {
		t.Fatalf("WriteFileReplacingUnder returned error: %v", err)
	}

	assertTargetContentAndMode(t, targetPath, "after", 0o444, "replaced")
}

func TestWriteRootPermissionFallbackCreatesMissingParentsOnNonWindows(t *testing.T) {
	assertWriteRootCreatesMissingParentsAndWrites(t, "WriteFileCreatingParentsWithPermissionFallback", (*WriteRoot).WriteFileCreatingParentsWithPermissionFallback)
}

func TestWriteRootPermissionFallbackRejectsNonRelativeTargetsOnNonWindows(t *testing.T) {
	assertWriteRootRejectsNonRelativeTargets(t, (*WriteRoot).WriteFileCreatingParentsWithPermissionFallback)
}

func TestWriteRootPermissionFallbackRejectsExistingDirectoryTargetOnNonWindows(t *testing.T) {
	rootDir := t.TempDir()
	root := openTestWriteRoot(t, rootDir, OpenWriteRoot)
	targetPath := filepath.Join("reports", writeTestFileName)
	if err := os.MkdirAll(filepath.Join(rootDir, targetPath), 0o755); err != nil {
		t.Fatalf("mkdir target directory: %v", err)
	}

	err := root.WriteFileCreatingParentsWithPermissionFallback(targetPath, []byte("hello"), 0o600, 0o750)
	if err == nil {
		t.Fatal("expected existing directory target to be rejected")
	}
	if !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("expected non-regular target error, got %v", err)
	}
}

func TestWriteRootReturnsErrorWithoutMutationWhenParentLacksWritePermissionOnNonWindows(t *testing.T) {
	if syscall.Geteuid() == 0 {
		t.Skip("effective privileges bypass parent write permission checks")
	}

	rootDir, parentDir, targetPath := setupReadOnlyWriteParent(t)
	requireParentWriteDeniedOnNonWindows(t, parentDir, ".safeio-write-probe")
	requireWritableTargetReopenableOnNonWindows(t, targetPath)

	root := openTestWriteRoot(t, rootDir, OpenWriteRoot)
	err := root.WriteFileCreatingParents(filepath.Join("reports", writeTestFileName), []byte("after"), 0o600, 0o750)
	if !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("expected permission error, got %v", err)
	}

	assertTargetContentAndMode(t, targetPath, "before", 0o640, "existing")
	if entries, err := os.ReadDir(parentDir); err != nil {
		t.Fatalf("read reports dir: %v", err)
	} else if len(entries) != 1 || entries[0].Name() != writeTestFileName {
		t.Fatalf("expected only target file to remain, got %v", entries)
	}
}

func TestWriteRootPermissionFallbackOverwritesWhenParentLacksWritePermissionOnNonWindows(t *testing.T) {
	if syscall.Geteuid() == 0 {
		t.Skip("effective privileges bypass parent write permission checks")
	}

	rootDir, parentDir, targetPath := setupReadOnlyWriteParent(t)
	requireParentWriteDeniedOnNonWindows(t, parentDir, ".safeio-write-probe")
	requireWritableTargetReopenableOnNonWindows(t, targetPath)

	root := openTestWriteRoot(t, rootDir, OpenWriteRoot)
	if err := root.WriteFileCreatingParentsWithPermissionFallback(filepath.Join("reports", writeTestFileName), []byte("after"), 0o600, 0o750); err != nil {
		t.Fatalf("WriteFileCreatingParentsWithPermissionFallback returned error: %v", err)
	}

	assertTargetContentAndMode(t, targetPath, "after", 0o640, "overwritten")
	if entries, err := os.ReadDir(parentDir); err != nil {
		t.Fatalf("read reports dir: %v", err)
	} else if len(entries) != 1 || entries[0].Name() != writeTestFileName {
		t.Fatalf("expected only target file to remain, got %v", entries)
	}
}

func TestWriteAtomicReplacementWithPinnedTargetFallsBackWhenTempCreationDeniedOnNonWindows(t *testing.T) {
	root, targetFile, targetData := newFallbackDeniedWriteRoot(t, os.ErrPermission, nil)

	if err := writeAtomicReplacementWithPinnedTarget(root, writeTestFileName, []byte("after"), 0o600, targetFile, true); err != nil {
		t.Fatalf("writeAtomicReplacementWithPinnedTarget returned error: %v", err)
	}
	assertFallbackTargetData(t, targetData, "after")
}

func TestWriteAtomicReplacementWithPinnedTargetFallsBackWhenRenameDeniedOnNonWindows(t *testing.T) {
	removeCalls := 0
	root, targetFile, targetData := newFallbackRenameRoot(t, func(string) error {
		removeCalls++
		return nil
	})

	if err := writeAtomicReplacementWithPinnedTarget(root, writeTestFileName, []byte("after"), 0o600, targetFile, true); err != nil {
		t.Fatalf("writeAtomicReplacementWithPinnedTarget returned error: %v", err)
	}
	if removeCalls != 4 {
		t.Fatalf("expected temp cleanup after rename fallback, got %d removes", removeCalls)
	}
	assertFallbackTargetData(t, targetData, "after")
}

func seedReadOnlyTargetFile(t *testing.T, rootDir string, perm os.FileMode) string {
	t.Helper()

	targetPath := filepath.Join(rootDir, writeTestFileName)
	if err := os.WriteFile(targetPath, []byte("before"), perm); err != nil {
		t.Fatalf("seed target file: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(targetPath, 0o600); err != nil && !os.IsNotExist(err) {
			t.Errorf("restore target file permissions: %v", err)
		}
	})
	return targetPath
}

func requireReadOnlyTarget(t *testing.T, targetPath string) {
	t.Helper()

	probe, err := os.OpenFile(targetPath, os.O_WRONLY, 0)
	if err == nil {
		if closeErr := probe.Close(); closeErr != nil {
			t.Fatalf("close writability probe: %v", closeErr)
		}
		t.Skip("effective privileges bypass read-only file permissions")
	}
	if !os.IsPermission(err) {
		t.Skipf("read-only file semantics are not testable: %v", err)
	}
}

func assertTargetContentAndMode(t *testing.T, targetPath, want string, wantPerm os.FileMode, label string) {
	t.Helper()

	assertFileContent(t, targetPath, want)
	info, err := os.Stat(targetPath)
	if err != nil {
		t.Fatalf("stat %s file: %v", label, err)
	}
	if info.Mode().Perm() != wantPerm {
		t.Fatalf("expected existing %s mode %#o to be preserved, got %#o", label, wantPerm, info.Mode().Perm())
	}
}

func setupReadOnlyWriteParent(t *testing.T) (string, string, string) {
	t.Helper()

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

	return rootDir, parentDir, targetPath
}

func requireParentWriteDeniedOnNonWindows(t *testing.T, parentDir, probeName string) {
	t.Helper()

	probePath := filepath.Join(parentDir, probeName)
	if err := os.WriteFile(probePath, []byte("probe"), 0o600); err == nil {
		if removeErr := os.Remove(probePath); removeErr != nil {
			t.Fatalf("remove write probe: %v", removeErr)
		}
		t.Skip("effective privileges bypass missing parent write permission")
	} else if !os.IsPermission(err) {
		t.Skipf("parent write permission semantics are not testable: %v", err)
	}
}

func newFallbackPinnedTarget(t *testing.T) (fs.FileInfo, File, *[]byte) {
	t.Helper()

	targetPath := filepath.Join(t.TempDir(), writeTestFileName)
	if err := os.WriteFile(targetPath, []byte("before"), 0o640); err != nil {
		t.Fatalf("seed target info path: %v", err)
	}
	info := statTestPath(t, targetPath)
	targetFile, targetData := newPinnedFallbackTargetFile(t, info, "before")
	return info, targetFile, targetData
}

func newFallbackDeniedWriteRoot(t *testing.T, tempOpenErr error, rename func(string, string) error) (*fakeRoot, File, *[]byte) {
	t.Helper()

	info, targetFile, targetData := newFallbackPinnedTarget(t)
	tempInfo := newPinnedTargetInfo(t, "temp")
	openTarget := func() (File, error) { return targetFile, nil }
	root := &fakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			if name == writeTestFileName {
				return info, nil
			}
			return tempInfo, nil
		},
		openFile: openTargetOrTempFile(writeTestFileName, openTarget, tempInfo, tempOpenErr),
		link: func(oldName, newName string) error {
			if !strings.HasPrefix(filepath.Base(oldName), atomicTempPrefix) || !strings.HasPrefix(filepath.Base(newName), atomicTempPrefix) {
				t.Fatalf("unexpected identity-bound link %q -> %q", oldName, newName)
			}
			return nil
		},
		rename: rename,
	}
	return root, targetFile, targetData
}

func newFallbackRenameRoot(t *testing.T, remove func(string) error) (*fakeRoot, File, *[]byte) {
	t.Helper()

	root, targetFile, targetData := newFallbackDeniedWriteRoot(t, nil, func(string, string) error { return os.ErrPermission })
	root.remove = remove
	return root, targetFile, targetData
}

func assertFallbackTargetData(t *testing.T, targetData *[]byte, want string) {
	t.Helper()

	if string(*targetData) != want {
		t.Fatalf("expected fallback overwrite data, got %q", string(*targetData))
	}
}

func requireWritableTargetReopenableOnNonWindows(t *testing.T, targetPath string) {
	t.Helper()

	probe, err := os.OpenFile(targetPath, os.O_WRONLY, 0)
	if err != nil {
		t.Skipf("existing writable target cannot be reopened without parent write permission: %v", err)
	}
	if err := probe.Close(); err != nil {
		t.Fatalf("close writable target probe: %v", err)
	}
}
