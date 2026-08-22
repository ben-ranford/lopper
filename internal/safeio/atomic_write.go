package safeio

import (
	"errors"
	"fmt"
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
	if err := writeFilePublishReadyFn(); err != nil {
		return err
	}
	if err := s.root.Rename(s.tempRel, s.targetRel); err != nil {
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

func (s *atomicWriteSession) verifyCommittedTargetAndPostWrite(postWrite func() error) error {
	if err := s.verifyCommittedTarget(); err != nil {
		return err
	}
	if postWrite == nil {
		return nil
	}
	return postWrite()
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

func writeAtomicReplacement(root Root, targetRel string, data []byte, perm os.FileMode, replacementInfo fs.FileInfo) error {
	return writeAtomicReplacementWithPostWriteCheck(root, targetRel, data, perm, replacementInfo, nil)
}

func writeAtomicReplacementWithPostWriteCheck(root Root, targetRel string, data []byte, perm os.FileMode, replacementInfo fs.FileInfo, postWrite func() error) (returnErr error) {
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
	return session.verifyCommittedTargetAndPostWrite(postWrite)
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
	return writeAtomicReplacementWithPinnedTargetAndPostWriteCheck(root, targetRel, data, perm, replacementFile, allowPermissionFallback, nil)
}

func writeAtomicReplacementWithPinnedTargetAndPostWriteCheck(root Root, targetRel string, data []byte, perm os.FileMode, replacementFile File, allowPermissionFallback bool, postWrite func() error) (returnErr error) {
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
	return session.verifyCommittedTargetAndPostWrite(postWrite)
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
