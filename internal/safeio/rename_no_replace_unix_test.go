//go:build darwin || linux

package safeio

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type closeErrorRenameNoReplaceDir struct {
	*os.File
	err      error
	closeCnt *int
}

func (d *closeErrorRenameNoReplaceDir) Close() error {
	(*d.closeCnt)++
	return errors.Join(d.File.Close(), d.err)
}

func TestRenameNoReplaceIgnoresParentDirCloseFailureAfterSuccessfulMove(t *testing.T) {
	rootDir := t.TempDir()
	sourcePath := filepath.Join(rootDir, "source")
	targetPath := filepath.Join(rootDir, "target")
	if err := os.Mkdir(sourcePath, 0o750); err != nil {
		t.Fatalf("create source: %v", err)
	}
	sourceInfo := statTestPath(t, sourcePath)
	root := openTestRoot(t, rootDir)

	closeErr := errors.New("close parent after successful rename")
	closeCnt := hookRenameNoReplaceDirCloseError(t, closeErr)

	if err := RenameNoReplace(root, "source", "target"); err != nil {
		t.Fatalf("rename no-replace should keep successful move despite close error, got %v", err)
	}

	if *closeCnt != 2 {
		t.Fatalf("expected both parent directory handles to close, got %d closes", *closeCnt)
	}
	assertRenameNoReplacePathAbsent(t, sourcePath)
	assertRenameNoReplaceSameFile(t, targetPath, sourceInfo)
}

func TestRenameNoReplacePreservesRenameAndCloseErrorsOnFailedMove(t *testing.T) {
	rootDir := t.TempDir()
	sourcePath := filepath.Join(rootDir, "source")
	targetPath := filepath.Join(rootDir, "target")
	if err := os.Mkdir(sourcePath, 0o750); err != nil {
		t.Fatalf("create source: %v", err)
	}
	if err := os.Mkdir(targetPath, 0o750); err != nil {
		t.Fatalf("create target: %v", err)
	}
	sourceInfo := statTestPath(t, sourcePath)
	targetInfo := statTestPath(t, targetPath)
	root := openTestRoot(t, rootDir)

	closeErr := errors.New("close parent after failed rename")
	closeCnt := hookRenameNoReplaceDirCloseError(t, closeErr)

	err := RenameNoReplace(root, "source", "target")
	if !errors.Is(err, os.ErrExist) {
		t.Fatalf("rename no-replace error = %v, want existing target", err)
	}
	if !errors.Is(err, closeErr) {
		t.Fatalf("rename no-replace error = %v, want joined close error", err)
	}
	if *closeCnt != 2 {
		t.Fatalf("expected both parent directory handles to close, got %d closes", *closeCnt)
	}
	assertRenameNoReplaceSameFile(t, sourcePath, sourceInfo)
	assertRenameNoReplaceSameFile(t, targetPath, targetInfo)
}

func hookRenameNoReplaceDirCloseError(t *testing.T, err error) *int {
	t.Helper()
	originalOpen := openRenameNoReplaceDir
	closeCnt := 0
	openRenameNoReplaceDir = func(root *osRoot) (renameNoReplaceDir, error) {
		dir, openErr := originalOpen(root)
		if openErr != nil {
			return nil, openErr
		}
		file, ok := dir.(*os.File)
		if !ok {
			t.Fatalf("unexpected rename no-replace dir type %T", dir)
		}
		return &closeErrorRenameNoReplaceDir{
			File:     file,
			err:      err,
			closeCnt: &closeCnt,
		}, nil
	}
	t.Cleanup(func() {
		openRenameNoReplaceDir = originalOpen
	})
	return &closeCnt
}
