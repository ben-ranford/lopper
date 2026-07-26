//go:build windows

package runtime

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestWindowsPinnedRuntimeSourceHandleBlocksWritesAndRename(t *testing.T) {
	sourcePath := filepath.Join(t.TempDir(), "node.exe")
	writeRuntimeTestExecutable(t, sourcePath, "@echo off\r\n")

	expected, err := os.Stat(sourcePath)
	if err != nil {
		t.Fatalf("stat runtime source: %v", err)
	}
	file, _, err := openWindowsPinnedRuntimePath(sourcePath, syscall.FILE_ATTRIBUTE_NORMAL, expected)
	if err != nil {
		t.Fatalf("pin runtime source: %v", err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			t.Errorf("close pinned runtime source: %v", closeErr)
		}
	}()

	if writable, err := os.OpenFile(sourcePath, os.O_WRONLY|os.O_APPEND, 0); err == nil {
		_ = writable.Close()
		t.Fatal("expected pinned source handle to deny write-open sharing")
	}
	if err := os.Rename(sourcePath, sourcePath+".moved"); err == nil {
		t.Fatal("expected pinned source handle to deny rename sharing")
	}
}

func TestWindowsStagedRuntimePinBlocksWritesAndRenameUntilCleanup(t *testing.T) {
	sourcePath := filepath.Join(t.TempDir(), "node.exe")
	writeRuntimeTestExecutable(t, sourcePath, "@echo off\r\n")

	source, err := openTrustedRuntimeExecutableSource(sourcePath)
	if err != nil {
		t.Fatalf("open pinned runtime source: %v", err)
	}
	stage, err := stageRuntimeExecutable(source)
	if err != nil {
		t.Fatalf("stage runtime executable: %v", err)
	}

	launchPath := stage.launchPath()
	if writable, err := os.OpenFile(launchPath, os.O_WRONLY|os.O_APPEND, 0); err == nil {
		_ = writable.Close()
		t.Fatal("expected staged runtime pin to deny write-open sharing")
	}
	if err := os.Rename(launchPath, launchPath+".moved"); err == nil {
		t.Fatal("expected staged runtime pin to deny rename sharing")
	}

	if err := stage.cleanup(); err != nil {
		t.Fatalf("cleanup staged runtime executable: %v", err)
	}
	if _, err := os.Stat(launchPath); !os.IsNotExist(err) {
		t.Fatalf("expected staged runtime cleanup to remove pinned image, stat err=%v", err)
	}
}
