//go:build windows

package safeio

import (
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
)

func fallbackAtomicReplacement(root Root, oldName, newName string, replacementFile File, data []byte, renameErr error, postWrite func() error, rollbackOnPostWriteFailure bool) (returnErr error) {
	if !windowsReplaceExistingRenameFallback(renameErr, oldName, newName) {
		return renameErr
	}

	replacementFile, closeReplacementFile, err := replacementFileForWindowsFallback(root, newName, replacementFile)
	if err != nil {
		return errors.Join(renameErr, err)
	}
	defer func() {
		if closeErr := closeReplacementFile(); closeErr != nil {
			returnErr = errors.Join(returnErr, closeErr)
		}
	}()

	var rollbackData []byte
	if rollbackOnPostWriteFailure {
		rollbackData, err = snapshotPinnedWindowsFallbackTarget(root, newName, replacementFile)
		if err != nil {
			return errors.Join(renameErr, err)
		}
	}

	fallbackErr := overwritePinnedFile(root, newName, replacementFile, data, nil)
	if fallbackErr != nil {
		return errors.Join(renameErr, fallbackErr)
	}
	if err := verifyOverwrittenTarget(root, newName, replacementFile); err != nil {
		return errors.Join(renameErr, err)
	}
	if err := runPostWriteCheck(postWrite); err != nil {
		if rollbackOnPostWriteFailure {
			return restoreWindowsFallbackTarget(root, newName, replacementFile, rollbackData, err)
		}
		return err
	}
	return nil
}

func replacementFileForWindowsFallback(root Root, targetRel string, replacementFile File) (File, func() error, error) {
	if replacementFile != nil {
		return replacementFile, func() error { return nil }, nil
	}

	info, err := root.Lstat(targetRel)
	if err != nil {
		return nil, nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, nil, fmt.Errorf("target path became a symlink before replacement: %s", targetRel)
	}
	if !info.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("target path is not a regular file before replacement: %s", targetRel)
	}

	file, err := openPinnedReplacementTarget(root, targetRel, info)
	if err != nil {
		return nil, nil, err
	}
	return file, file.Close, nil
}

func snapshotPinnedWindowsFallbackTarget(root Root, targetRel string, replacementFile File) (_ []byte, returnErr error) {
	pathInfo, err := root.Lstat(targetRel)
	if err != nil {
		return nil, err
	}
	if pathInfo.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("target path became a symlink before rollback snapshot: %s", targetRel)
	}
	if !pathInfo.Mode().IsRegular() {
		return nil, fmt.Errorf("target path is not a regular file before rollback snapshot: %s", targetRel)
	}

	openedInfo, err := replacementFile.Stat()
	if err != nil {
		return nil, err
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(pathInfo, openedInfo) {
		return nil, fmt.Errorf("target changed before rollback snapshot: %s", targetRel)
	}

	reader, err := root.Open(targetRel)
	if err != nil {
		return nil, err
	}
	defer func() {
		returnErr = errors.Join(returnErr, reader.Close())
	}()

	readerInfo, err := reader.Stat()
	if err != nil {
		return nil, err
	}
	if !readerInfo.Mode().IsRegular() || !os.SameFile(openedInfo, readerInfo) {
		return nil, fmt.Errorf("target changed while opening rollback snapshot: %s", targetRel)
	}
	return io.ReadAll(reader)
}

func restoreWindowsFallbackTarget(root Root, targetRel string, replacementFile File, rollbackData []byte, primaryErr error) error {
	if err := truncateAndWritePinnedFile(targetRel, replacementFile, rollbackData); err != nil {
		return errors.Join(primaryErr, fmt.Errorf("rollback Windows fallback replacement: %w", err))
	}
	if err := verifyOverwrittenTarget(root, targetRel, replacementFile); err != nil {
		return errors.Join(primaryErr, fmt.Errorf("validate Windows fallback rollback: %w", err))
	}
	return primaryErr
}

func windowsReplaceExistingRenameFallback(err error, oldName, newName string) bool {
	var linkErr *os.LinkError
	if !errors.As(err, &linkErr) ||
		linkErr.Op != "renameat" ||
		linkErr.Old != oldName ||
		linkErr.New != newName {
		return false
	}
	errno, ok := linkErr.Err.(syscall.Errno)
	return ok &&
		(errno == syscall.ERROR_ALREADY_EXISTS || errno == syscall.ERROR_FILE_EXISTS)
}
