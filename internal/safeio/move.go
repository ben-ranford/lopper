package safeio

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
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
	if filepath.Clean(sourceRel) == filepath.Clean(targetRel) {
		_, err := chmodAndSnapshotMoveSource(root, sourceRel, filePerm)
		return err
	}

	sourceInfo, renameErr := prepareAndRenameWithinRoot(root, sourceRel, targetRel, filePerm)
	if renameErr == nil {
		return nil
	}
	if !errors.Is(renameErr, syscall.EXDEV) {
		return renameErr
	}

	sourceInfo, copyErr := copyFileWithinRoot(root, sourceRel, targetRel, filePerm, sourceInfo)
	if copyErr != nil {
		return errors.Join(renameErr, copyErr)
	}

	return errors.Join(publishRenameCleanup(renameErr), removeCopiedMoveSource(root, sourceRel, sourceInfo))
}

func prepareAndRenameWithinRoot(root Root, sourceRel, targetRel string, filePerm os.FileMode) (fs.FileInfo, error) {
	sourceInfo, err := chmodAndSnapshotMoveSource(root, sourceRel, filePerm)
	if err != nil {
		return nil, err
	}
	aliasesTarget, err := targetAliasesSource(root, sourceRel, targetRel, sourceInfo)
	if err != nil {
		return sourceInfo, err
	}
	sourceConsumed, err := publishIdentityBoundReplacingWithSourceState(root, sourceRel, targetRel, sourceInfo, "move source changed before rename", "move target changed before validation")
	if err != nil {
		return sourceInfo, err
	}
	if sourceConsumed || aliasesTarget {
		return sourceInfo, nil
	}
	if err := removeIdentityBound(root, sourceRel, sourceInfo, "move source changed before cleanup"); err != nil {
		return sourceInfo, err
	}
	return sourceInfo, nil
}

func targetAliasesSource(root Root, sourceRel, targetRel string, sourceInfo fs.FileInfo) (bool, error) {
	if !strings.EqualFold(filepath.Clean(sourceRel), filepath.Clean(targetRel)) {
		return false, nil
	}
	targetInfo, err := root.Lstat(targetRel)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return targetInfo.Mode().IsRegular() && os.SameFile(sourceInfo, targetInfo), nil
}

func chmodAndSnapshotMoveSource(root Root, sourceRel string, filePerm os.FileMode) (os.FileInfo, error) {
	sourceInfo, err := root.Lstat(sourceRel)
	if err != nil {
		return nil, err
	}
	if !sourceInfo.Mode().IsRegular() {
		return nil, fmt.Errorf("move source is not a regular file: %s", sourceRel)
	}
	if err := root.Chmod(sourceRel, filePerm); err != nil {
		return nil, err
	}
	updatedSourceInfo, err := root.Lstat(sourceRel)
	if err != nil {
		return nil, err
	}
	if !updatedSourceInfo.Mode().IsRegular() || !os.SameFile(sourceInfo, updatedSourceInfo) {
		return nil, fmt.Errorf("move source changed before rename: %s", sourceRel)
	}
	return updatedSourceInfo, nil
}

func copyFileWithinRoot(root Root, sourceRel, targetRel string, filePerm os.FileMode, expectedSourceInfos ...fs.FileInfo) (_ os.FileInfo, returnErr error) {
	var expectedSourceInfo fs.FileInfo
	if len(expectedSourceInfos) > 0 {
		expectedSourceInfo = expectedSourceInfos[0]
	}
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
	if expectedSourceInfo != nil && !os.SameFile(expectedSourceInfo, sourceInfo) {
		return nil, fmt.Errorf("move source changed before fallback copy: %s", sourceRel)
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
	return sourceInfo, nil
}

func removeCopiedMoveSource(root Root, sourceRel string, sourceInfo os.FileInfo) error {
	return removeIdentityBound(root, sourceRel, sourceInfo, "move source changed before cleanup")
}

func removeIdentityBound(root Root, rel string, expected fs.FileInfo, message string) error {
	if expected == nil {
		return fmt.Errorf("%s: %s", message, rel)
	}
	cleanupRel, err := stageIdentityBoundLink(root, rel, expected, message)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if identityBoundLinkUnsupported(err) {
			return removeFileIfMatches(root, rel, expected, message)
		}
		return err
	}
	if err := removeFileIfMatches(root, rel, expected, message); err != nil {
		cleanupErr := cleanupAtomicTempFileIfMatches(root, cleanupRel, expected)
		if errors.Is(err, os.ErrNotExist) {
			return cleanupErr
		}
		return errors.Join(err, cleanupErr)
	}
	return cleanupAtomicTempFileIfMatches(root, cleanupRel, expected)
}
