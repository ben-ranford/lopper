//go:build windows

package safeio

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

func fallbackAtomicReplacement(root Root, oldName, newName string, replacementFile File, data []byte, renameErr error) (returnErr error) {
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

	fallbackErr := overwritePinnedFile(root, newName, replacementFile, data, nil)
	if fallbackErr != nil {
		return errors.Join(renameErr, fallbackErr)
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
