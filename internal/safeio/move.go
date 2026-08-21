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

	renameErr := prepareAndRenameWithinRoot(root, sourceRel, targetRel, filePerm)
	if renameErr == nil {
		return nil
	}
	if !errors.Is(renameErr, syscall.EXDEV) {
		return renameErr
	}

	sourceInfo, copyErr := copyFileWithinRoot(root, sourceRel, targetRel, filePerm)
	if copyErr != nil {
		return errors.Join(renameErr, copyErr)
	}

	return removeCopiedMoveSource(root, sourceRel, sourceInfo)
}

func prepareAndRenameWithinRoot(root Root, sourceRel, targetRel string, filePerm os.FileMode) error {
	source, err := OpenPinnedFile(root, sourceRel)
	if err != nil {
		return err
	}
	sourceInfo, err := chmodSnapshotAndCloseMoveSource(source, sourceRel, filePerm)
	if err != nil {
		return err
	}
	if err := verifyPublishedPathMatchesInfo(root, sourceRel, sourceInfo, "move source changed before rename"); err != nil {
		return err
	}
	if err := root.Rename(sourceRel, targetRel); err != nil {
		return err
	}
	return verifyPublishedPathMatchesInfo(root, targetRel, sourceInfo, "move target changed before validation")
}

func chmodSnapshotAndCloseMoveSource(source File, sourceRel string, filePerm os.FileMode) (info os.FileInfo, returnErr error) {
	defer func() {
		if closeErr := source.Close(); closeErr != nil {
			returnErr = errors.Join(returnErr, closeErr)
		}
	}()
	if err := source.Chmod(filePerm); err != nil {
		return nil, err
	}
	info, err := source.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("move source is not a regular file: %s", sourceRel)
	}
	return info, nil
}

func copyFileWithinRoot(root Root, sourceRel, targetRel string, filePerm os.FileMode) (_ os.FileInfo, returnErr error) {
	source, err := OpenPinnedFile(root, sourceRel)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := source.Close(); closeErr != nil {
			returnErr = errors.Join(returnErr, closeErr)
		}
	}()
	sourceInfo, err := source.Stat()
	if err != nil {
		return nil, err
	}
	if !sourceInfo.Mode().IsRegular() {
		return nil, fmt.Errorf("move source is not a regular file: %s", sourceRel)
	}

	session, err := newAtomicWriteSession(root, targetRel, filePerm)
	if err != nil {
		return nil, err
	}
	defer func() {
		if cleanupErr := session.cleanup(); cleanupErr != nil {
			returnErr = errors.Join(returnErr, cleanupErr)
		}
	}()

	if _, err := io.Copy(session.tempFile, source); err != nil {
		return nil, err
	}
	if err := session.tempFile.Chmod(filePerm); err != nil {
		return nil, err
	}
	if err := session.snapshotAndCloseTempFile(); err != nil {
		return nil, err
	}
	if err := session.commit(); err != nil {
		return nil, err
	}
	if err := session.verifyCommittedTarget(); err != nil {
		return nil, err
	}
	return sourceInfo, nil
}

func removeCopiedMoveSource(root Root, sourceRel string, sourceInfo os.FileInfo) error {
	if err := verifyPublishedPathMatchesInfo(root, sourceRel, sourceInfo, "move source changed before cleanup"); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if err := root.Remove(sourceRel); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
