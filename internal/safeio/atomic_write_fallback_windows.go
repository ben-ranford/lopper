//go:build windows

package safeio

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"syscall"

	win "golang.org/x/sys/windows"
)

func fallbackAtomicReplacement(root Root, fallback atomicReplacementFallback) (returnErr error) {
	if !windowsReplaceExistingRenameFallback(fallback.renameErr, atomicRenameSourceRel(fallback.renameErr, fallback.oldName), fallback.newName) {
		return fallback.renameErr
	}

	replacementFile, closeReplacementFile, err := replacementFileForWindowsFallback(root, fallback.newName, fallback.replacementFile)
	if err != nil {
		return errors.Join(fallback.renameErr, err)
	}
	defer func() {
		if closeErr := closeReplacementFile(); closeErr != nil {
			returnErr = errors.Join(returnErr, closeErr)
		}
	}()

	// The ownership check that guards the eventual rollback (below) reads
	// the target's current content, then a later, separate step trusts that
	// read; an identity check adjacent to the rollback write (see
	// restoreWindowsFallbackTarget) catches a concurrent writer that
	// replaces the target's identity in between, but not one that reuses
	// this same still-open inode the way this exact fallback path itself
	// does. Hold an exclusive lock across the whole transaction -- the
	// overwrite, its post-write check, and any rollback -- so a second,
	// concurrent same-key writer taking this same fallback path for this
	// same target blocks until this one fully finishes, rather than
	// interleaving with it.
	unlockReplacementFile, err := lockPinnedReplacementFile(replacementFile)
	if err != nil {
		return errors.Join(fallback.renameErr, err)
	}
	defer func() {
		if unlockErr := unlockReplacementFile(); unlockErr != nil {
			returnErr = errors.Join(returnErr, unlockErr)
		}
	}()

	var rollbackData []byte
	if fallback.rollbackOnPostWriteFailure {
		rollbackData, err = snapshotPinnedWindowsFallbackTarget(root, fallback.newName, replacementFile)
		if err != nil {
			return errors.Join(fallback.renameErr, err)
		}
	}

	fallbackErr := overwritePinnedFile(root, fallback.newName, replacementFile, fallback.data, nil)
	if fallbackErr != nil {
		return errors.Join(fallback.renameErr, fallbackErr)
	}
	if err := verifyOverwrittenTarget(root, fallback.newName, replacementFile); err != nil {
		return errors.Join(fallback.renameErr, err)
	}
	if err := runPostWriteCheck(fallback.postWrite); err != nil {
		if fallback.rollbackOnPostWriteFailure {
			return restoreWindowsFallbackTarget(root, fallback.newName, replacementFile, rollbackData, fallback.data, err)
		}
		return err
	}
	return nil
}

// lockPinnedReplacementFile takes a whole-file exclusive byte-range lock on
// replacementFile via LockFileEx, blocking until any other holder (this same
// fallback path running for a concurrent same-key writer) releases it. Only
// a genuine *os.File exposes the underlying handle this requires; test
// doubles and any other File implementation skip locking entirely rather
// than fail, since they never run concurrently with a second real writer.
// The lock is released automatically by the OS if the process exits before
// calling the returned unlock, so a crash mid-transaction cannot leave it
// held forever.
func lockPinnedReplacementFile(file File) (unlock func() error, returnErr error) {
	osFile, ok := file.(*os.File)
	if !ok {
		return func() error { return nil }, nil
	}
	handle := win.Handle(osFile.Fd())
	overlapped := new(win.Overlapped)
	if err := win.LockFileEx(handle, win.LOCKFILE_EXCLUSIVE_LOCK, 0, ^uint32(0), ^uint32(0), overlapped); err != nil {
		return nil, fmt.Errorf("lock pinned replacement file: %w", err)
	}
	return func() error {
		if err := win.UnlockFileEx(handle, 0, ^uint32(0), ^uint32(0), overlapped); err != nil {
			return fmt.Errorf("unlock pinned replacement file: %w", err)
		}
		return nil
	}, nil
}

func atomicRenameSourceRel(err error, fallbackRel string) string {
	var publishErr *publishRenameError
	if errors.As(err, &publishErr) && publishErr.sourceRel != "" {
		return publishErr.sourceRel
	}
	return fallbackRel
}

// replacementFileForWindowsFallback always opens its own fresh, read/write
// handle rather than trusting a caller-supplied replacementFile: this
// fallback both writes the overwrite and, for a rollback-eligible caller,
// later reads the target back through that exact same handle (see
// snapshotPinnedWindowsFallbackTarget) -- reading through a second handle
// would deadlock against this one's own lock. A caller-supplied
// replacementFile is pinned write-only (see openPinnedReplacementTarget,
// which must stay write-only for callers whose target permits writing but
// not reading), so it can't serve that read; when one is supplied, its
// already-verified identity (via Stat, not a fresh path lookup) is reused
// as the expected identity for this handle instead of re-resolving by
// path, preserving the same anti-TOCTOU guarantee early pinning exists
// for. The caller-supplied handle itself is left for its own owner to
// close, as established by every caller of this function.
func replacementFileForWindowsFallback(root Root, targetRel string, replacementFile File) (File, func() error, error) {
	var info fs.FileInfo
	var err error
	if replacementFile != nil {
		info, err = replacementFile.Stat()
	} else {
		info, err = root.Lstat(targetRel)
	}
	if err != nil {
		return nil, nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, nil, fmt.Errorf("target path became a symlink before replacement: %s", targetRel)
	}
	if !info.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("target path is not a regular file before replacement: %s", targetRel)
	}

	file, err := openFlaggedPinnedReplacementTarget(root, targetRel, info, os.O_RDWR)
	if err != nil {
		return nil, nil, err
	}
	return file, file.Close, nil
}

// snapshotPinnedWindowsFallbackTarget reads the target's current content
// through the already-open, already-locked replacementFile handle rather
// than opening a second handle via root.Open. Windows byte-range locks
// (lockPinnedReplacementFile) block overlapping access through *any* other
// handle in the locking process, not just other processes -- a second
// handle here would deadlock against our own lock instead of racing a
// concurrent writer.
func snapshotPinnedWindowsFallbackTarget(root Root, targetRel string, replacementFile File) ([]byte, error) {
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

	seeker, ok := replacementFile.(io.Seeker)
	if !ok {
		return nil, fmt.Errorf("target does not support seeking for rollback snapshot: %s", targetRel)
	}
	if _, err := seeker.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	return io.ReadAll(replacementFile)
}

// restoreWindowsFallbackTarget rolls the fallback overwrite back to
// rollbackData. It reuses overwritePinnedFile -- the same identity-check-then-write
// primitive the primary fallback overwrite itself uses -- passing the
// ownership content check in as its beforeRevalidate hook instead of running
// it as a separate, independently returning step. That keeps the ownership
// check and the identity check that immediately precedes the write adjacent
// within one call, rather than letting a caller resume between them: a
// concurrent same-key writer that replaces the target's identity after the
// ownership check reads matching content, but before the rollback writes,
// is still caught by overwritePinnedFile's own Lstat/Stat/SameFile check
// immediately before it truncates and writes.
func restoreWindowsFallbackTarget(root Root, targetRel string, replacementFile File, rollbackData, fallbackData []byte, primaryErr error) error {
	verifyOwnership := func() error {
		return verifyWindowsFallbackWriteOwnership(root, targetRel, replacementFile, fallbackData)
	}
	if err := overwritePinnedFile(root, targetRel, replacementFile, rollbackData, verifyOwnership); err != nil {
		return errors.Join(primaryErr, fmt.Errorf("rollback Windows fallback replacement: %w", err))
	}
	if err := verifyOverwrittenTarget(root, targetRel, replacementFile); err != nil {
		return errors.Join(primaryErr, fmt.Errorf("validate Windows fallback rollback: %w", err))
	}
	return primaryErr
}

func verifyWindowsFallbackWriteOwnership(root Root, targetRel string, replacementFile File, fallbackData []byte) error {
	currentData, err := snapshotPinnedWindowsFallbackTarget(root, targetRel, replacementFile)
	if err != nil {
		return err
	}
	if !bytes.Equal(currentData, fallbackData) {
		return fmt.Errorf("target changed after fallback overwrite: %s", targetRel)
	}
	return nil
}

func windowsReplaceExistingRenameFallback(err error, oldName, newName string) bool {
	return onlyWindowsReplaceExistingRename(publishRenameCause(err), oldName, newName)
}

func onlyWindowsReplaceExistingRename(err error, oldName, newName string) bool {
	if linkErr, ok := err.(*os.LinkError); ok {
		return linkErr.Op == "renameat" &&
			linkErr.Old == oldName &&
			linkErr.New == newName &&
			isWindowsReplaceExistingError(linkErr.Err)
	}
	if joined, ok := err.(UnwrapAller); ok {
		causes := joined.Unwrap()
		return len(causes) == 1 && causes[0] != nil && onlyWindowsReplaceExistingRename(causes[0], oldName, newName)
	}
	if wrapped, ok := err.(Unwrapper); ok {
		return wrapped.Unwrap() != nil && onlyWindowsReplaceExistingRename(wrapped.Unwrap(), oldName, newName)
	}
	return false
}

func isWindowsReplaceExistingError(err error) bool {
	errno, ok := err.(syscall.Errno)
	return ok &&
		(errno == syscall.ERROR_ALREADY_EXISTS || errno == syscall.ERROR_FILE_EXISTS)
}
