//go:build darwin || windows

package safeio

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type atomicRootOpenTestPreparer func(t *testing.T, rootDir, targetPath string)
type atomicRootOpenTestOpener func(t *testing.T, rootDir, targetPath string) (*os.File, error)

type atomicRootOpenFailureCase struct {
	name        string
	targetName  string
	prepare     atomicRootOpenTestPreparer
	open        atomicRootOpenTestOpener
	wantErrors  []error
	wantMessage string
	wantClosed  bool
}

func TestOpenRootFileNoFollowAtomicFailureModes(t *testing.T) {
	for _, tc := range atomicRootOpenFailureCases() {
		t.Run(tc.name, func(t *testing.T) {
			assertAtomicRootOpenFailure(t, tc)
		})
	}
}

func atomicRootOpenFailureCases() []atomicRootOpenFailureCase {
	return []atomicRootOpenFailureCase{
		{name: "invalid name", targetName: "../trace.ndjson", open: rejectUnexpectedAtomicOpen, wantErrors: []error{os.ErrInvalid}},
		{name: "missing before open", open: rejectUnexpectedAtomicOpen, wantErrors: []error{os.ErrNotExist}},
		{name: "symlink before open", prepare: prepareAtomicSymlink, open: rejectUnexpectedAtomicOpen, wantErrors: []error{ErrNoFollowFinalComponent}},
		{name: "directory before open", prepare: prepareAtomicDirectory, open: rejectUnexpectedAtomicOpen, wantErrors: []error{ErrNoFollowFinalComponent}},
		{name: "opened handle stat fails", prepare: prepareAtomicRegularFile, open: openClosedAtomicTarget, wantErrors: []error{os.ErrClosed}, wantClosed: true},
		{name: "opened handle is directory", prepare: prepareAtomicRegularFile, open: openAtomicRootDirectory, wantErrors: []error{ErrNoFollowFinalComponent}, wantClosed: true},
		{name: "target removed after open", prepare: prepareAtomicRegularFile, open: openAndRemoveAtomicTarget, wantErrors: []error{os.ErrNotExist}, wantClosed: true},
		{name: "target becomes symlink", prepare: prepareAtomicRegularFile, open: openAndSymlinkAtomicTarget, wantErrors: []error{ErrNoFollowFinalComponent}, wantClosed: true},
		{name: "target becomes directory", prepare: prepareAtomicRegularFile, open: openAndMakeAtomicTargetDirectory, wantErrors: []error{ErrNoFollowFinalComponent}, wantClosed: true},
		{name: "opened handle identity differs", prepare: prepareAtomicRegularFile, open: openDifferentAtomicTarget, wantMessage: "changed while opening", wantClosed: true},
		{name: "open reports missing", prepare: prepareAtomicRegularFile, open: returnAtomicOpenError(os.ErrNotExist), wantErrors: []error{os.ErrNotExist}},
		{name: "open reports permission", prepare: prepareAtomicRegularFile, open: returnAtomicOpenError(os.ErrPermission), wantErrors: []error{os.ErrPermission}},
		{name: "open error after symlink swap", prepare: prepareAtomicRegularFile, open: failAfterSymlinkingAtomicTarget, wantErrors: []error{ErrNoFollowFinalComponent, os.ErrPermission}},
	}
}

func assertAtomicRootOpenFailure(t *testing.T, tc atomicRootOpenFailureCase) {
	t.Helper()

	rootDir := t.TempDir()
	targetName := tc.targetName
	if targetName == "" {
		targetName = "trace.ndjson"
	}
	targetPath := filepath.Join(rootDir, targetName)
	if tc.prepare != nil {
		tc.prepare(t, rootDir, targetPath)
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

	var openedFile *os.File
	file, err := openRootFileNoFollowAtomic(root, targetName, func(*os.Root, string) (*os.File, error) {
		file, openErr := tc.open(t, rootDir, targetPath)
		openedFile = file
		return file, openErr
	})
	if file != nil {
		if closeErr := file.Close(); closeErr != nil {
			t.Fatalf("close unexpected file: %v", closeErr)
		}
		t.Fatal("expected failed atomic open to return no file")
	}
	assertAtomicRootOpenError(t, tc, err)
	if tc.wantClosed {
		assertAtomicTestFileClosed(t, openedFile)
	}
}

func assertAtomicRootOpenError(t *testing.T, tc atomicRootOpenFailureCase, err error) {
	t.Helper()

	for _, wantErr := range tc.wantErrors {
		if !errors.Is(err, wantErr) {
			t.Fatalf("expected error matching %v, got %v", wantErr, err)
		}
	}
	if tc.wantMessage != "" && (err == nil || !strings.Contains(err.Error(), tc.wantMessage)) {
		t.Fatalf("expected error containing %q, got %v", tc.wantMessage, err)
	}
}

func prepareAtomicRegularFile(t *testing.T, _ string, targetPath string) {
	t.Helper()
	if err := os.WriteFile(targetPath, []byte("trace\n"), 0o600); err != nil {
		t.Fatalf("write trace: %v", err)
	}
}

func prepareAtomicDirectory(t *testing.T, _ string, targetPath string) {
	t.Helper()
	if err := os.Mkdir(targetPath, 0o700); err != nil {
		t.Fatalf("mkdir target: %v", err)
	}
}

func prepareAtomicSymlink(t *testing.T, rootDir, targetPath string) {
	t.Helper()
	replacementPath := filepath.Join(rootDir, "trace.real")
	prepareAtomicRegularFile(t, rootDir, replacementPath)
	if err := os.Symlink(filepath.Base(replacementPath), targetPath); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
}

func rejectUnexpectedAtomicOpen(t *testing.T, _, _ string) (*os.File, error) {
	t.Helper()
	t.Fatal("atomic opener called after an earlier rejection")
	return nil, nil
}

func returnAtomicOpenError(err error) atomicRootOpenTestOpener {
	return func(*testing.T, string, string) (*os.File, error) {
		return nil, err
	}
}

func openClosedAtomicTarget(t *testing.T, _, targetPath string) (*os.File, error) {
	t.Helper()
	file := openAtomicTestFile(t, targetPath)
	if err := file.Close(); err != nil {
		t.Fatalf("close target before return: %v", err)
	}
	return file, nil
}

func openAtomicRootDirectory(t *testing.T, rootDir, _ string) (*os.File, error) {
	t.Helper()
	return openAtomicTestFile(t, rootDir), nil
}

func openAndRemoveAtomicTarget(t *testing.T, rootDir, targetPath string) (*os.File, error) {
	t.Helper()
	file := openAtomicTestFile(t, targetPath)
	removeAtomicTestTarget(t, file, rootDir, targetPath)
	return file, nil
}

func openAndSymlinkAtomicTarget(t *testing.T, rootDir, targetPath string) (*os.File, error) {
	t.Helper()
	file := openAtomicTestFile(t, targetPath)
	replaceAtomicTargetWithSymlink(t, file, rootDir, targetPath)
	return file, nil
}

func openAndMakeAtomicTargetDirectory(t *testing.T, rootDir, targetPath string) (*os.File, error) {
	t.Helper()
	file := openAtomicTestFile(t, targetPath)
	removeAtomicTestTarget(t, file, rootDir, targetPath)
	prepareAtomicDirectory(t, "", targetPath)
	return file, nil
}

func openDifferentAtomicTarget(t *testing.T, rootDir, _ string) (*os.File, error) {
	t.Helper()
	otherPath := filepath.Join(rootDir, "other.ndjson")
	prepareAtomicRegularFile(t, rootDir, otherPath)
	return openAtomicTestFile(t, otherPath), nil
}

func failAfterSymlinkingAtomicTarget(t *testing.T, rootDir, targetPath string) (*os.File, error) {
	t.Helper()
	replaceAtomicTargetWithSymlink(t, nil, rootDir, targetPath)
	return nil, os.ErrPermission
}

func replaceAtomicTargetWithSymlink(t *testing.T, file *os.File, rootDir, targetPath string) {
	t.Helper()
	replacementPath := filepath.Join(rootDir, "replacement.ndjson")
	prepareAtomicRegularFile(t, rootDir, replacementPath)
	removeAtomicTestTarget(t, file, rootDir, targetPath)
	if err := os.Symlink(filepath.Base(replacementPath), targetPath); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
}

func trackAtomicTestFile(t *testing.T, file *os.File) *os.File {
	t.Helper()
	t.Cleanup(func() {
		if closeErr := file.Close(); closeErr != nil && !errors.Is(closeErr, os.ErrClosed) {
			t.Fatalf("close test file: %v", closeErr)
		}
	})
	return file
}

func assertAtomicTestFileClosed(t *testing.T, file *os.File) {
	t.Helper()
	if file == nil {
		t.Fatal("expected atomic opener to return a file")
	}
	if _, err := file.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("expected rejected atomic handle to be closed, got %v", err)
	}
}
