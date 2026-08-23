package safeio

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

type atomicWriteSession struct {
	root      Root
	targetRel string
	tempRel   string
	tempFile  File
	tempInfo  fs.FileInfo
}

type truncatingFile interface {
	File
	Truncate(size int64) error
}

var (
	_ = writeAtomicReplacementWithPinnedTargetAndPostWriteCheck
	_ = writeFileAtRootWithPostWriteCheck
)

type pinnedReplacementChecks struct {
	commitReady                func() error
	postWrite                  func() error
	commitRename               atomicRenameFunc
	rollbackOnPostWriteFailure bool
}

type atomicReplacementOptions struct {
	replacementInfo            fs.FileInfo
	commitReady                func() error
	postWrite                  func() error
	commitRename               atomicRenameFunc
	rollbackOnPostWriteFailure bool
}

type atomicReplacementFallback struct {
	oldName                    string
	newName                    string
	replacementFile            File
	data                       []byte
	renameErr                  error
	postWrite                  func() error
	rollbackOnPostWriteFailure bool
}

func newPinnedReplacementChecks(checks []func() error) pinnedReplacementChecks {
	callbacks := pinnedReplacementChecks{}
	if len(checks) > 0 {
		callbacks.commitReady = checks[0]
	}
	if len(checks) > 1 {
		callbacks.postWrite = checks[1]
	}
	return callbacks
}

func newAtomicWriteSession(root Root, targetRel string, perm os.FileMode) (*atomicWriteSession, error) {
	tempRel, tempFile, err := createAtomicTempFile(root, filepath.Dir(targetRel), perm)
	if err != nil {
		return nil, err
	}

	return &atomicWriteSession{
		root:      root,
		targetRel: targetRel,
		tempRel:   tempRel,
		tempFile:  tempFile,
	}, nil
}

func (s *atomicWriteSession) writeAndClose(data []byte, perm os.FileMode) error {
	if err := s.writeAndPrepare(data, perm); err != nil {
		return err
	}
	return s.closeTempFile()
}

func (s *atomicWriteSession) writeAndPrepare(data []byte, perm os.FileMode) error {
	if _, err := s.tempFile.Write(data); err != nil {
		return err
	}
	return s.tempFile.Chmod(perm)
}

type atomicRenameFunc func(oldName, newName string) error

func (s *atomicWriteSession) commit(commitReady func() error, rename atomicRenameFunc) error {
	if err := writeFilePublishReadyFn(); err != nil {
		return err
	}
	if commitReady != nil {
		if err := commitReady(); err != nil {
			return err
		}
	}
	if rename == nil {
		if err := writeFileRenameReadyFn(); err != nil {
			return err
		}
		rename = s.root.Rename
	}
	if err := rename(s.tempRel, s.targetRel); err != nil {
		return err
	}
	s.tempRel = ""
	return nil
}

func (s *atomicWriteSession) verifyCommittedTarget() error {
	if s.tempInfo == nil {
		return fmt.Errorf("temporary file info unavailable after commit: %s", s.targetRel)
	}
	if !s.tempInfo.Mode().IsRegular() {
		return fmt.Errorf("temporary file is not regular after commit: %s", s.targetRel)
	}
	pathInfo, err := s.root.Lstat(s.targetRel)
	if err != nil {
		return fmt.Errorf("committed target changed before validation: %w", err)
	}
	if !pathInfo.Mode().IsRegular() || !os.SameFile(s.tempInfo, pathInfo) {
		return fmt.Errorf("committed target changed before validation: %s", s.targetRel)
	}
	return nil
}

func (s *atomicWriteSession) verifyCommittedTargetAndPostWrite(postWrite func() error, rollbackOnPostWriteFailure bool) error {
	if err := s.verifyCommittedTarget(); err != nil {
		return err
	}
	if postWrite == nil {
		return nil
	}
	if err := postWrite(); err != nil {
		if rollbackOnPostWriteFailure {
			return s.rollbackCommittedTargetWithError(err)
		}
		return err
	}
	return nil
}

func (s *atomicWriteSession) rollbackCommittedTargetWithError(primaryErr error) error {
	// Root cleanup is path-based. A check-then-Remove or check-then-Rename can
	// unlink or move a concurrent same-key replacement, so retain the committed
	// file as an orphan when rollback cannot be identity-atomic.
	return primaryErr
}

func rollbackRequiredFallbackError(targetRel string) error {
	return fmt.Errorf("fallback replacement cannot roll back post-write failure: %s", targetRel)
}

func (s *atomicWriteSession) snapshotAndCloseTempFile() error {
	if s.tempFile == nil {
		return nil
	}
	tempInfo, err := s.tempFile.Stat()
	if err != nil {
		return err
	}
	if !tempInfo.Mode().IsRegular() {
		return fmt.Errorf("temporary file is not regular after commit: %s", s.targetRel)
	}
	if err := s.closeTempFile(); err != nil {
		return err
	}
	s.tempInfo = tempInfo
	return nil
}

func (s *atomicWriteSession) closeTempFile() error {
	if s.tempFile == nil {
		return nil
	}
	if err := s.tempFile.Close(); err != nil {
		return err
	}
	s.tempFile = nil
	return nil
}

func (s *atomicWriteSession) cleanup() error {
	return cleanupAtomicTempFile(s.root, s.tempRel, s.tempFile)
}

func verifyOverwrittenTarget(root Root, targetRel string, file File) error {
	pathInfo, err := root.Lstat(targetRel)
	if err != nil {
		return fmt.Errorf("overwritten target changed before validation: %w", err)
	}
	if pathInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("overwritten target became a symlink before validation: %s", targetRel)
	}
	if !pathInfo.Mode().IsRegular() {
		return fmt.Errorf("overwritten target is not a regular file before validation: %s", targetRel)
	}
	openedInfo, err := file.Stat()
	if err != nil {
		return err
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(pathInfo, openedInfo) {
		return fmt.Errorf("overwritten target changed before validation: %s", targetRel)
	}
	return nil
}

func writeAtomicReplacement(root Root, targetRel string, data []byte, perm os.FileMode, replacementInfo fs.FileInfo) error {
	return writeAtomicReplacementWithChecks(root, targetRel, data, perm, atomicReplacementOptions{replacementInfo: replacementInfo})
}

func writeAtomicReplacementWithPostWriteCheck(root Root, targetRel string, data []byte, perm os.FileMode, replacementInfo fs.FileInfo, postWrite func() error) (returnErr error) {
	return writeAtomicReplacementWithChecks(root, targetRel, data, perm, atomicReplacementOptions{replacementInfo: replacementInfo, postWrite: postWrite})
}

func writeAtomicReplacementWithChecks(root Root, targetRel string, data []byte, perm os.FileMode, options atomicReplacementOptions) (returnErr error) {
	replacementFile, closeReplacementFile, err := openPinnedReplacementTargetIfNeeded(root, targetRel, options.replacementInfo)
	if err != nil {
		return err
	}
	defer func() {
		returnErr = errors.Join(returnErr, closeReplacementFile())
	}()

	session, err := newAtomicWriteSession(root, targetRel, perm)
	if err != nil {
		return err
	}
	defer func() {
		returnErr = errors.Join(returnErr, session.cleanup())
	}()

	if err := session.writeAndPrepare(data, perm); err != nil {
		return err
	}
	if err := session.snapshotAndCloseTempFile(); err != nil {
		return err
	}
	if err := session.commit(options.commitReady, options.commitRename); err != nil {
		return fallbackAtomicReplacement(root, atomicReplacementFallback{
			oldName:                    session.tempRel,
			newName:                    targetRel,
			replacementFile:            replacementFile,
			data:                       data,
			renameErr:                  err,
			postWrite:                  options.postWrite,
			rollbackOnPostWriteFailure: options.rollbackOnPostWriteFailure,
		})
	}
	return session.verifyCommittedTargetAndPostWrite(options.postWrite, options.rollbackOnPostWriteFailure)
}

func writeFileAtomicallyIfAbsentAtRoot(root Root, targetRel string, data []byte, perm os.FileMode) (returnErr error) {
	session, err := newAtomicWriteSession(root, targetRel, perm)
	if err != nil {
		return err
	}
	defer func() {
		returnErr = errors.Join(returnErr, session.cleanup())
	}()

	if err := session.writeAndPrepare(data, perm); err != nil {
		return err
	}
	if err := session.snapshotAndCloseTempFile(); err != nil {
		return err
	}
	if err := writeFilePublishReadyFn(); err != nil {
		return err
	}
	if err := root.Link(session.tempRel, targetRel); err != nil {
		return err
	}
	if err := root.Remove(session.tempRel); err != nil {
		return err
	}
	session.tempRel = ""
	return session.verifyCommittedTarget()
}

func writeAtomicReplacementWithPinnedTarget(root Root, targetRel string, data []byte, perm os.FileMode, replacementFile File, allowPermissionFallback bool) (returnErr error) {
	return writeAtomicReplacementWithPinnedTargetAndChecks(root, targetRel, data, perm, replacementFile, allowPermissionFallback, nil, nil)
}

func writeAtomicReplacementWithPinnedTargetAndPostWriteCheck(root Root, targetRel string, data []byte, perm os.FileMode, replacementFile File, allowPermissionFallback bool, postWrite func() error) (returnErr error) {
	return writeAtomicReplacementWithPinnedTargetAndChecks(root, targetRel, data, perm, replacementFile, allowPermissionFallback, nil, postWrite)
}

func writeAtomicReplacementWithPinnedTargetAndChecks(root Root, targetRel string, data []byte, perm os.FileMode, replacementFile File, allowPermissionFallback bool, checks ...func() error) (returnErr error) {
	return writeAtomicReplacementWithPinnedTargetCallbacks(root, targetRel, data, perm, replacementFile, allowPermissionFallback, newPinnedReplacementChecks(checks))
}

func writeAtomicReplacementWithPinnedTargetCallbacks(root Root, targetRel string, data []byte, perm os.FileMode, replacementFile File, allowPermissionFallback bool, callbacks pinnedReplacementChecks) (returnErr error) {
	session, err := newAtomicWriteSession(root, targetRel, perm)
	if err != nil {
		if pinnedOverwritePermissionFallbackAllowed(err, replacementFile, allowPermissionFallback) {
			return runPinnedOverwriteFallback(root, targetRel, replacementFile, data, err, callbacks)
		}
		return err
	}
	defer func() {
		returnErr = errors.Join(returnErr, session.cleanup())
	}()

	if err := session.writeAndPrepare(data, perm); err != nil {
		return err
	}
	if err := session.snapshotAndCloseTempFile(); err != nil {
		return err
	}
	if err := session.commit(callbacks.commitReady, callbacks.commitRename); err != nil {
		fallbackErr := fallbackAtomicReplacement(root, atomicReplacementFallback{
			oldName:                    session.tempRel,
			newName:                    targetRel,
			replacementFile:            replacementFile,
			data:                       data,
			renameErr:                  err,
			postWrite:                  callbacks.postWrite,
			rollbackOnPostWriteFailure: callbacks.rollbackOnPostWriteFailure,
		})
		if fallbackErr == nil {
			return nil
		}
		if pinnedOverwritePermissionFallbackAllowed(err, replacementFile, allowPermissionFallback) {
			return runPinnedOverwriteFallback(root, targetRel, replacementFile, data, err, callbacks)
		}
		return fallbackErr
	}
	return session.verifyCommittedTargetAndPostWrite(callbacks.postWrite, callbacks.rollbackOnPostWriteFailure)
}

func runPinnedOverwriteFallback(root Root, targetRel string, replacementFile File, data []byte, primaryErr error, callbacks pinnedReplacementChecks) error {
	if callbacks.rollbackOnPostWriteFailure {
		return errors.Join(primaryErr, rollbackRequiredFallbackError(targetRel))
	}
	if err := overwritePinnedFile(root, targetRel, replacementFile, data, nil); err != nil {
		return err
	}
	if err := verifyOverwrittenTarget(root, targetRel, replacementFile); err != nil {
		return err
	}
	return runFallbackPostWriteCheck(callbacks.postWrite, false, targetRel)
}

func runPostWriteCheck(postWrite func() error) error {
	if postWrite == nil {
		return nil
	}
	return postWrite()
}

func runFallbackPostWriteCheck(postWrite func() error, rollbackOnPostWriteFailure bool, targetRel string) error {
	err := runPostWriteCheck(postWrite)
	if err != nil && rollbackOnPostWriteFailure {
		return errors.Join(err, fmt.Errorf("committed target changed before rollback: %s", targetRel))
	}
	return err
}

func pinnedOverwritePermissionFallbackAllowed(err error, replacementFile File, allowPermissionFallback bool) bool {
	return allowPermissionFallback && replacementFile != nil && os.IsPermission(err)
}

func openPinnedReplacementTarget(root Root, targetRel string, expectedInfo fs.FileInfo) (File, error) {
	file, err := root.OpenFile(targetRel, os.O_WRONLY, 0)
	if err != nil {
		return nil, err
	}
	openedInfo, err := file.Stat()
	if err != nil {
		return nil, closeFilePreservingPrimary(file, err)
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(expectedInfo, openedInfo) {
		err := fmt.Errorf("target changed while opening for replacement: %s", targetRel)
		return nil, closeFilePreservingPrimary(file, err)
	}
	return file, nil
}

func overwritePinnedFile(root Root, targetRel string, file File, data []byte, beforeRevalidate func() error) error {
	if beforeRevalidate != nil {
		if err := beforeRevalidate(); err != nil {
			return err
		}
	}

	pathInfo, err := root.Lstat(targetRel)
	if err != nil {
		return err
	}
	if pathInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("target path became a symlink before replacement: %s", targetRel)
	}
	if !pathInfo.Mode().IsRegular() {
		return fmt.Errorf("target path is not a regular file before replacement: %s", targetRel)
	}

	openedInfo, err := file.Stat()
	if err != nil {
		return err
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(pathInfo, openedInfo) {
		return fmt.Errorf("target changed before replacement: %s", targetRel)
	}

	return truncateAndWritePinnedFile(targetRel, file, data)
}

func truncateAndWritePinnedFile(targetRel string, file File, data []byte) error {
	targetFile, ok := file.(truncatingFile)
	if !ok {
		return fmt.Errorf("target does not support truncation: %s", targetRel)
	}
	if err := targetFile.Truncate(0); err != nil {
		return err
	}
	if seeker, ok := file.(io.Seeker); ok {
		if _, err := seeker.Seek(0, io.SeekStart); err != nil {
			return err
		}
	}
	if _, err := file.Write(data); err != nil {
		return err
	}
	return nil
}

func closeFilePreservingPrimary(file File, primaryErr error) error {
	closeErr := file.Close()
	if primaryErr != nil {
		return primaryErr
	}
	return closeErr
}
