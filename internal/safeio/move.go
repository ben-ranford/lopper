package safeio

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// MoveFileUnder atomically places sourcePath at targetPath only if both resolve under rootDir.
// It preserves atomic final placement by renaming within root, and falls back to copy-then-rename
// when the direct rename cannot be completed.
func MoveFileUnder(rootDir, sourcePath, targetPath string, dirPerm, filePerm os.FileMode) (returnErr error) {
	source, err := resolveRootedTarget(rootDir, sourcePath, rejectRootTarget)
	if err != nil {
		return err
	}
	target, err := resolveRootedTarget(rootDir, targetPath, rejectRootTarget)
	if err != nil {
		return err
	}

	root, err := fileSystem.OpenRoot(target.rootAbs)
	if err != nil {
		return fmt.Errorf("open root: %w", err)
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			returnErr = errors.Join(returnErr, closeErr)
		}
	}()

	return MoveFileWithinRoot(root, source.rel, target.rel, dirPerm, filePerm)
}

// MoveFileWithinRoot atomically places sourceRel at targetRel within root.
// It preserves atomic final placement by renaming within root, and falls back to copy-then-rename
// only when the direct rename fails with EXDEV.
func MoveFileWithinRoot(root Root, sourceRel, targetRel string, dirPerm, filePerm os.FileMode) (returnErr error) {
	if err := root.MkdirAll(filepath.Dir(targetRel), dirPerm); err != nil {
		return err
	}

	cleanupSource := false
	defer func() {
		if !cleanupSource {
			return
		}
		if removeErr := root.Remove(sourceRel); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			returnErr = errors.Join(returnErr, removeErr)
		}
	}()

	renameErr := prepareAndRenameWithinRoot(root, sourceRel, targetRel, filePerm)
	if renameErr == nil {
		return nil
	}
	if !errors.Is(renameErr, syscall.EXDEV) {
		return renameErr
	}

	if copyErr := copyFileWithinRoot(root, sourceRel, targetRel, filePerm); copyErr != nil {
		return errors.Join(renameErr, copyErr)
	}

	cleanupSource = true
	if err := root.Remove(sourceRel); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	cleanupSource = false
	return nil
}

func prepareAndRenameWithinRoot(root Root, sourceRel, targetRel string, filePerm os.FileMode) error {
	if err := root.Chmod(sourceRel, filePerm); err != nil {
		return err
	}
	return root.Rename(sourceRel, targetRel)
}

func copyFileWithinRoot(root Root, sourceRel, targetRel string, filePerm os.FileMode) (returnErr error) {
	source, err := root.Open(sourceRel)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := source.Close(); closeErr != nil {
			returnErr = errors.Join(returnErr, closeErr)
		}
	}()

	session, err := newAtomicWriteSession(root, targetRel, filePerm)
	if err != nil {
		return err
	}
	defer func() {
		if cleanupErr := session.cleanup(); cleanupErr != nil {
			returnErr = errors.Join(returnErr, cleanupErr)
		}
	}()

	if _, err := io.Copy(session.tempFile, source); err != nil {
		return err
	}
	if err := session.tempFile.Chmod(filePerm); err != nil {
		return err
	}
	if err := session.closeTempFile(); err != nil {
		return err
	}
	if err := root.Rename(session.tempRel, targetRel); err != nil {
		return err
	}
	session.tempRel = ""
	return nil
}
