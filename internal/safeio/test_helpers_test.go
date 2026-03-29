package safeio

import (
	"os"
	"path/filepath"
	"testing"
)

const (
	unexpectedErrFmt = "unexpected error: %v"
	escapesRootErr   = "path escapes root"
	getwdErrFmt      = "getwd: %v"
	restoreWDErrFmt  = "restore wd %s: %v"
	mkdirDeadDirFmt  = "mkdir deadDir: %v"
	chdirDeadDirFmt  = "chdir deadDir: %v"
	removeDeadDirFmt = "remove deadDir: %v"
)

func withWorkingDir(t *testing.T, dir string) {
	t.Helper()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf(getwdErrFmt, err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(originalWD); err != nil {
			t.Fatalf(restoreWDErrFmt, originalWD, err)
		}
	})
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir %s: %v", dir, err)
	}
}

func withRemovedWorkingDir(t *testing.T, deadDirName string) {
	t.Helper()
	deadDir := filepath.Join(t.TempDir(), deadDirName)
	if err := os.MkdirAll(deadDir, 0o755); err != nil {
		t.Fatalf(mkdirDeadDirFmt, err)
	}
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf(getwdErrFmt, err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(originalWD); err != nil {
			t.Fatalf(restoreWDErrFmt, originalWD, err)
		}
	})
	if err := os.Chdir(deadDir); err != nil {
		t.Fatalf(chdirDeadDirFmt, err)
	}
	if err := os.RemoveAll(deadDir); err != nil {
		t.Fatalf(removeDeadDirFmt, err)
	}
}
