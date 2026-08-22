package safeio

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
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

type identityBoundOperationsRoot interface {
	LinkIfMatches(oldName, newName string, expected fs.FileInfo, message string) error
	RenameIfMatches(oldName, newName string, expected fs.FileInfo, message string) error
	RemoveIfMatches(name string, expected fs.FileInfo, message string) error
}

type publishRenameError struct {
	sourceRel string
	err       error
}

const (
	committedTargetChangedBeforeValidation = "committed target changed before validation"
	temporaryFileChangedBeforeCommit       = "temporary file changed before commit"
)

var errIdentityBoundReplacementUnsupported = errors.New("identity-bound atomic replacement unsupported")

func (e *publishRenameError) Error() string {
	return e.err.Error()
}

func (e *publishRenameError) Unwrap() error {
	return e.err
}

func publishRenameCause(err error) error {
	var publishErr *publishRenameError
	if errors.As(err, &publishErr) && publishErr.err != nil {
		return publishErr.err
	}
	return err
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

func (s *atomicWriteSession) commit() error {
	return publishIdentityBoundReplacing(
		s.root,
		s.tempRel,
		s.targetRel,
		s.tempInfo,
		temporaryFileChangedBeforeCommit,
		committedTargetChangedBeforeValidation,
	)
}

func (s *atomicWriteSession) verifyCommittedTarget() error {
	if s.tempInfo == nil {
		return fmt.Errorf("temporary file info unavailable after commit: %s", s.targetRel)
	}
	if !s.tempInfo.Mode().IsRegular() {
		return fmt.Errorf("temporary file is not regular after commit: %s", s.targetRel)
	}
	return verifyPublishedPathMatchesInfo(s.root, s.targetRel, s.tempInfo, committedTargetChangedBeforeValidation)
}

func verifyPublishedPathMatchesInfo(root Root, rel string, expected fs.FileInfo, message string) error {
	if expected == nil {
		return fmt.Errorf("%s: %s", message, rel)
	}
	if !expected.Mode().IsRegular() {
		return fmt.Errorf("%s: %s", message, rel)
	}
	pathInfo, err := root.Lstat(rel)
	if err != nil {
		return fmt.Errorf("%s: %w", message, err)
	}
	if !pathInfo.Mode().IsRegular() || !os.SameFile(expected, pathInfo) {
		return fmt.Errorf("%s: %s", message, rel)
	}
	return nil
}

func publishIdentityBoundReplacing(root Root, sourceRel, targetRel string, expected fs.FileInfo, sourceMessage, targetMessage string) (returnErr error) {
	_, err := publishIdentityBoundReplacingWithSourceState(root, sourceRel, targetRel, expected, sourceMessage, targetMessage)
	return err
}

func publishIdentityBoundReplacingWithSourceState(root Root, sourceRel, targetRel string, expected fs.FileInfo, sourceMessage, targetMessage string) (_ bool, returnErr error) {
	stagedRel, err := stageIdentityBoundLink(root, sourceRel, expected, sourceMessage)
	if err != nil {
		if !identityBoundLinkUnsupported(err) {
			return false, err
		}
		return false, fmt.Errorf("%w: %s: %w", errIdentityBoundReplacementUnsupported, sourceRel, err)
	}
	defer func() {
		if cleanupErr := cleanupAtomicTempFileIfMatches(root, stagedRel, expected); cleanupErr != nil {
			returnErr = errors.Join(returnErr, cleanupErr)
		}
	}()
	if err := verifyPublishedPathMatchesInfo(root, stagedRel, expected, sourceMessage); err != nil {
		return false, err
	}
	if err := renameFileIfMatches(root, stagedRel, targetRel, expected, sourceMessage); err != nil {
		return false, &publishRenameError{sourceRel: stagedRel, err: err}
	}
	return false, verifyPublishedPathMatchesInfo(root, targetRel, expected, targetMessage)
}

func renameFileIfMatches(root Root, oldName, newName string, expected fs.FileInfo, message string) error {
	if guardedRoot, ok := root.(identityBoundOperationsRoot); ok {
		return guardedRoot.RenameIfMatches(oldName, newName, expected, message)
	}
	return fmt.Errorf("%w: %s: %s", errIdentityBoundReplacementUnsupported, oldName, message)
}

func linkFileIfMatches(root Root, oldName, newName string, expected fs.FileInfo, message string) error {
	if guardedRoot, ok := root.(identityBoundOperationsRoot); ok {
		return guardedRoot.LinkIfMatches(oldName, newName, expected, message)
	}
	return fmt.Errorf("%w: %s: %s", errIdentityBoundReplacementUnsupported, oldName, message)
}

func stageIdentityBoundLink(root Root, sourceRel string, expected fs.FileInfo, message string) (string, error) {
	if expected == nil {
		return "", fmt.Errorf("%s: %s", message, sourceRel)
	}
	if !expected.Mode().IsRegular() {
		return "", fmt.Errorf("%s: %s", message, sourceRel)
	}
	for range 10 {
		stagedRel, err := identityBoundStagingPath(sourceRel)
		if err != nil {
			return "", err
		}
		if err := linkFileIfMatches(root, sourceRel, stagedRel, expected, message); errors.Is(err, os.ErrExist) {
			continue
		} else if err != nil {
			return "", err
		}
		if err := verifyPublishedPathMatchesInfo(root, stagedRel, expected, message); err != nil {
			return "", errors.Join(err, cleanupAtomicTempFileIfMatches(root, stagedRel, expected))
		}
		return stagedRel, nil
	}
	return "", fmt.Errorf("create identity-bound staging link: too many collisions")
}

func identityBoundLinkUnsupported(err error) bool {
	return errors.Is(err, errors.ErrUnsupported) ||
		errors.Is(err, syscall.EOPNOTSUPP) ||
		errors.Is(err, syscall.ENOSYS) ||
		errors.Is(err, syscall.EXDEV)
}

func identityBoundStagingPath(sourceRel string) (string, error) {
	name, err := randomTempNameFn()
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(sourceRel)
	if dir == "." {
		return name, nil
	}
	return filepath.Join(dir, name), nil
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
	if s.tempInfo != nil {
		return cleanupAtomicTempFileIfMatches(s.root, s.tempRel, s.tempInfo)
	}
	return cleanupAtomicTempFile(s.root, s.tempRel, s.tempFile)
}

func writeAtomicReplacement(root Root, targetRel string, data []byte, perm os.FileMode, replacementInfo fs.FileInfo) (returnErr error) {
	replacementFile, closeReplacementFile, err := openPinnedReplacementTargetIfNeeded(root, targetRel, replacementInfo)
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
	if err := session.commit(); err != nil {
		return fallbackAtomicReplacement(root, session.tempRel, targetRel, replacementFile, data, err)
	}
	return nil
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
	return publishIdentityBoundIfAbsent(root, session.tempRel, targetRel, session.tempInfo)
}

func publishIdentityBoundIfAbsent(root Root, sourceRel, targetRel string, expected fs.FileInfo) error {
	if err := linkFileIfMatches(root, sourceRel, targetRel, expected, temporaryFileChangedBeforeCommit); err != nil {
		if errors.Is(err, os.ErrExist) {
			return os.ErrExist
		}
		if identityBoundLinkUnsupported(err) {
			return fmt.Errorf("%w: %s: %w", errIdentityBoundReplacementUnsupported, sourceRel, err)
		}
		return err
	}
	return verifyPublishedPathMatchesInfo(root, targetRel, expected, committedTargetChangedBeforeValidation)
}

func writeAtomicReplacementWithPinnedTarget(root Root, targetRel string, data []byte, perm os.FileMode, replacementFile File, allowPermissionFallback bool) (returnErr error) {
	session, err := newAtomicWriteSession(root, targetRel, perm)
	if err != nil {
		if pinnedOverwritePermissionFallbackAllowed(err, replacementFile, allowPermissionFallback) {
			return overwritePinnedFile(root, targetRel, replacementFile, data, nil)
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
	if err := session.commit(); err != nil {
		fallbackErr := fallbackAtomicReplacement(root, session.tempRel, targetRel, replacementFile, data, err)
		if fallbackErr == nil {
			return nil
		}
		if pinnedOverwritePermissionFallbackAllowed(err, replacementFile, allowPermissionFallback) {
			return overwritePinnedFile(root, targetRel, replacementFile, data, nil)
		}
		return fallbackErr
	}
	return nil
}

func pinnedOverwritePermissionFallbackAllowed(err error, replacementFile File, allowPermissionFallback bool) bool {
	return allowPermissionFallback && replacementFile != nil && os.IsPermission(publishRenameCause(err))
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

	targetFile, ok := file.(truncatingFile)
	if !ok {
		return fmt.Errorf("target does not support truncation: %s", targetRel)
	}
	if err := targetFile.Truncate(0); err != nil {
		return err
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
