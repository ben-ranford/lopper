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
}

type truncatingFile interface {
	File
	Truncate(size int64) error
}

type atomicReplacementOptions struct {
	forceReplacementPerm bool
	allowInPlaceFallback bool
}

type atomicReplacementFallbackRequest struct {
	root                 Root
	tempName             string
	targetName           string
	replacementFile      File
	data                 []byte
	perm                 os.FileMode
	forceReplacementPerm bool
	renameErr            error
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

func (s *atomicWriteSession) writeAndCommit(data []byte, perm os.FileMode) error {
	if _, err := s.tempFile.Write(data); err != nil {
		return err
	}
	if err := s.tempFile.Chmod(perm); err != nil {
		return err
	}
	if err := s.tempFile.Sync(); err != nil {
		return err
	}
	if err := s.closeTempFile(); err != nil {
		return err
	}
	if err := s.root.Rename(s.tempRel, s.targetRel); err != nil {
		return err
	}
	s.tempRel = ""
	return syncRootDirectory(s.root)
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

func writeAtomicReplacement(root Root, target rootedTarget, data []byte, perm os.FileMode, replacementInfo fs.FileInfo, options atomicReplacementOptions) (returnErr error) {
	targetRel, err := resolveRelativeTarget(target.rel, rejectRootTarget)
	if err != nil {
		return err
	}
	parent, closeParent, err := openDirectoryWithinRoot(root, target.rootAbs, filepath.Dir(targetRel), false, 0)
	if err != nil {
		return err
	}
	if closeParent {
		defer func() {
			returnErr = errors.Join(returnErr, parent.Close())
		}()
	}
	return writeAtomicReplacementInParent(parent, filepath.Base(targetRel), data, perm, replacementInfo, options)
}

func writeAtomicReplacementInParent(root Root, targetName string, data []byte, perm os.FileMode, replacementInfo fs.FileInfo, options atomicReplacementOptions) (returnErr error) {
	var replacementFile File
	closeReplacementFile := func() error { return nil }
	if options.allowInPlaceFallback {
		var err error
		replacementFile, closeReplacementFile, err = openPinnedReplacementTargetIfNeeded(root, targetName, replacementInfo)
		if err != nil {
			return err
		}
	}
	defer func() {
		returnErr = errors.Join(returnErr, closeReplacementFile())
	}()

	session, err := newAtomicWriteSession(root, targetName, perm)
	if err != nil {
		return err
	}
	defer func() {
		returnErr = errors.Join(returnErr, session.cleanup())
	}()

	if err := session.writeAndCommit(data, perm); err != nil {
		if session.tempRel == "" || session.tempFile != nil {
			return err
		}
		if !options.allowInPlaceFallback {
			return err
		}
		return fallbackAtomicReplacement(atomicReplacementFallbackRequest{
			root:                 root,
			tempName:             session.tempRel,
			targetName:           targetName,
			replacementFile:      replacementFile,
			data:                 data,
			perm:                 perm,
			forceReplacementPerm: options.forceReplacementPerm,
			renameErr:            err,
		})
	}
	return nil
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
