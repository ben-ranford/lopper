package safeio

import (
	"errors"
	"fmt"
	"io"
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

type RenameIfMatchesStater interface {
	RenameIfMatchesState(oldName, newName string, expected fs.FileInfo, message string) (bool, error)
}

type publishRenameError struct {
	sourceRel  string
	err        error
	cleanupErr error
}

const (
	committedTargetChangedBeforeValidation = "committed target changed before validation"
	temporaryFileChangedBeforeCommit       = "temporary file changed before commit"
)

var errIdentityBoundReplacementUnsupported = errors.New("identity-bound atomic replacement unsupported")
var errIdentityBoundLinkUnavailable = errors.New("identity-bound link unavailable")

func (e *publishRenameError) Error() string {
	if e.cleanupErr == nil {
		return e.err.Error()
	}
	return errors.Join(e.err, e.cleanupErr).Error()
}

func (e *publishRenameError) Unwrap() []error {
	if e.cleanupErr == nil {
		return []error{e.err}
	}
	return []error{e.err, e.cleanupErr}
}

func publishRenameCause(err error) error {
	var publishErr *publishRenameError
	if errors.As(err, &publishErr) && publishErr.err != nil {
		return publishErr.err
	}
	return err
}

func publishRenameCleanup(err error) error {
	var publishErr *publishRenameError
	if errors.As(err, &publishErr) {
		return publishErr.cleanupErr
	}
	return nil
}

func publishRenameSource(err error, fallback string) string {
	var publishErr *publishRenameError
	if errors.As(err, &publishErr) && publishErr.sourceRel != "" {
		return publishErr.sourceRel
	}
	return fallback
}

func withPublishRenameSource(err error, sourceRel string) error {
	if err == nil {
		return nil
	}
	var publishErr *publishRenameError
	if errors.As(err, &publishErr) {
		if publishErr.sourceRel != "" {
			sourceRel = publishErr.sourceRel
		}
		return &publishRenameError{
			sourceRel:  sourceRel,
			err:        publishErr.err,
			cleanupErr: publishErr.cleanupErr,
		}
	}
	return &publishRenameError{sourceRel: sourceRel, err: err}
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
	return s.commitPreparedSource(temporaryFileChangedBeforeCommit, committedTargetChangedBeforeValidation)
}

func (s *atomicWriteSession) commitPreparedSource(sourceMessage, targetMessage string) (returnErr error) {
	stagedRel, stagedInfo, err := stageIdentityBoundFileKeepingSourceLive(s.root, s.tempRel, s.tempInfo, sourceMessage, s.tempFile)
	if err != nil {
		return err
	}
	defer func() {
		if cleanupErr := cleanupAtomicTempFileIfMatches(s.root, stagedRel, stagedInfo); cleanupErr != nil {
			var publishErr *publishRenameError
			if errors.As(returnErr, &publishErr) {
				publishErr.cleanupErr = errors.Join(publishErr.cleanupErr, cleanupErr)
			} else {
				returnErr = errors.Join(returnErr, cleanupErr)
			}
		}
	}()
	if err := s.closeTempFile(); err != nil {
		return err
	}
	if err := verifyPublishedPathMatchesInfo(s.root, stagedRel, stagedInfo, sourceMessage); err != nil {
		return err
	}
	stagedConsumed, err := renameFileIfMatches(s.root, stagedRel, s.targetRel, stagedInfo, sourceMessage)
	if err != nil {
		return withPublishRenameSource(err, stagedRel)
	}
	if !stagedConsumed {
		return errors.Join(
			verifyPublishedPathMatchesInfo(s.root, s.targetRel, stagedInfo, targetMessage),
			cleanupAtomicTempFileIfMatches(s.root, stagedRel, stagedInfo),
		)
	}
	return verifyPublishedPathMatchesInfo(s.root, s.targetRel, stagedInfo, targetMessage)
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
	if !sameRegularFile(expected, pathInfo) {
		return fmt.Errorf("%s: %s", message, rel)
	}
	return nil
}

func publishIdentityBoundReplacing(root Root, sourceRel, targetRel string, expected fs.FileInfo, sourceMessage, targetMessage string) (returnErr error) {
	_, err := publishIdentityBoundReplacingWithSourceState(root, sourceRel, targetRel, expected, sourceMessage, targetMessage)
	return err
}

func publishIdentityBoundReplacingWithSourceState(root Root, sourceRel, targetRel string, expected fs.FileInfo, sourceMessage, targetMessage string) (_ bool, returnErr error) {
	stagedRel, stagedInfo, err := stageIdentityBoundFileKeepingSourceLive(root, sourceRel, expected, sourceMessage, nil)
	if err != nil {
		return false, err
	}
	defer func() {
		if cleanupErr := cleanupAtomicTempFileIfMatches(root, stagedRel, stagedInfo); cleanupErr != nil {
			var publishErr *publishRenameError
			if errors.As(returnErr, &publishErr) {
				publishErr.cleanupErr = errors.Join(publishErr.cleanupErr, cleanupErr)
			} else {
				returnErr = errors.Join(returnErr, cleanupErr)
			}
		}
	}()
	if err := verifyPublishedPathMatchesInfo(root, stagedRel, stagedInfo, sourceMessage); err != nil {
		return false, err
	}
	stagedConsumed, err := renameFileIfMatches(root, stagedRel, targetRel, stagedInfo, sourceMessage)
	if err != nil {
		return false, withPublishRenameSource(err, stagedRel)
	}
	return !stagedConsumed, verifyPublishedPathMatchesInfo(root, targetRel, stagedInfo, targetMessage)
}

func renameFileIfMatches(root Root, oldName, newName string, expected fs.FileInfo, message string) (bool, error) {
	if guardedRoot, ok := root.(RenameIfMatchesStater); ok {
		return guardedRoot.RenameIfMatchesState(oldName, newName, expected, message)
	}
	if guardedRoot, ok := root.(identityBoundOperationsRoot); ok {
		return true, guardedRoot.RenameIfMatches(oldName, newName, expected, message)
	}
	return renameFileIfMatchesUsingBasicRoot(root, oldName, newName, expected, message)
}

func linkFileIfMatches(root Root, oldName, newName string, expected fs.FileInfo, message string) error {
	if guardedRoot, ok := root.(identityBoundOperationsRoot); ok {
		return guardedRoot.LinkIfMatches(oldName, newName, expected, message)
	}
	return linkFileIfMatchesUsingBasicRoot(root, oldName, newName, expected, message)
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

func stageIdentityBoundFile(root Root, sourceRel string, expected fs.FileInfo, message string) (string, fs.FileInfo, error) {
	return stageIdentityBoundFileKeepingSourceLive(root, sourceRel, expected, message, nil)
}

func stageIdentityBoundFileKeepingSourceLive(root Root, sourceRel string, expected fs.FileInfo, message string, liveSource File) (string, fs.FileInfo, error) {
	expected, err := validateLiveSourceInfo(liveSource, sourceRel, expected, message)
	if err != nil {
		return "", nil, err
	}
	stagedRel, err := stageIdentityBoundLink(root, sourceRel, expected, message)
	if err == nil {
		return stagedRel, expected, nil
	}
	if !errors.Is(err, errIdentityBoundLinkUnavailable) {
		return "", nil, err
	}
	stagedRel, stagedInfo, copyErr := stageIdentityBoundCopy(root, sourceRel, expected, message, liveSource)
	if copyErr != nil {
		return "", nil, fmt.Errorf("%w: %s: %w", errIdentityBoundReplacementUnsupported, sourceRel, copyErr)
	}
	return stagedRel, stagedInfo, nil
}

func validateLiveSourceInfo(liveSource File, sourceRel string, expected fs.FileInfo, message string) (fs.FileInfo, error) {
	if liveSource == nil {
		return expected, nil
	}
	liveInfo, err := liveSource.Stat()
	if err != nil {
		return nil, err
	}
	if !sameRegularFile(expected, liveInfo) {
		return nil, fmt.Errorf("%s: %s", message, sourceRel)
	}
	return liveInfo, nil
}

func stageIdentityBoundCopy(root Root, sourceRel string, expected fs.FileInfo, message string, liveSource File) (_ string, _ fs.FileInfo, returnErr error) {
	source, closeSource, err := openStagingCopySource(root, sourceRel, expected, message, liveSource)
	if err != nil {
		return "", nil, err
	}
	defer func() {
		if closeSource {
			closeErr := source.Close()
			returnErr = errors.Join(returnErr, closeErr)
		}
	}()

	for range 10 {
		stagedRel, err := identityBoundStagingPath(sourceRel)
		if err != nil {
			return "", nil, err
		}
		stagedMode := chmodSupportedMode(expected.Mode())
		staged, err := root.OpenFile(stagedRel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, stagedMode)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", nil, err
		}
		stagedInfo, err := staged.Stat()
		if err != nil {
			return "", nil, closeCreatedFileWithoutIdentity(staged, err)
		}
		if !stagedInfo.Mode().IsRegular() {
			return "", nil, closeFilePreservingPrimary(staged, fmt.Errorf("%s: %s", message, stagedRel))
		}
		stagedReady := false
		cleanupStagedInfo := stagedInfo
		defer func() {
			if !stagedReady {
				returnErr = errors.Join(returnErr, cleanupAtomicTempFileIfMatches(root, stagedRel, cleanupStagedInfo))
			}
		}()
		if _, err := io.Copy(staged, source); err != nil {
			return "", nil, closeFilePreservingPrimary(staged, err)
		}
		if err := staged.Chmod(stagedMode); err != nil {
			return "", nil, closeFilePreservingPrimary(staged, err)
		}
		stagedInfo, err = staged.Stat()
		if err != nil {
			return "", nil, closeFilePreservingPrimary(staged, err)
		}
		cleanupStagedInfo = stagedInfo
		if err := staged.Close(); err != nil {
			return "", nil, err
		}
		openStagedInfo := stagedInfo
		publishedStagedInfo, err := publishedRegularFileInfo(root, stagedRel, message)
		if err != nil {
			return "", nil, err
		}
		if !sameRegularFile(openStagedInfo, publishedStagedInfo) {
			return "", nil, fmt.Errorf("%s: %s", message, stagedRel)
		}
		stagedInfo = publishedStagedInfo
		cleanupStagedInfo = stagedInfo
		if _, err := validateLiveSourceInfo(source, sourceRel, expected, message); err != nil {
			return "", nil, err
		}
		if closeSource {
			if err := source.Close(); err != nil {
				closeSource = false
				return "", nil, err
			}
			closeSource = false
		}
		stagedReady = true
		return stagedRel, stagedInfo, nil
	}
	return "", nil, fmt.Errorf("create identity-bound staging copy: too many collisions")
}

func openStagingCopySource(root Root, sourceRel string, expected fs.FileInfo, message string, liveSource File) (File, bool, error) {
	if liveSource != nil {
		_, err := validateLiveSourceInfo(liveSource, sourceRel, expected, message)
		if err != nil {
			return nil, false, err
		}
		if seeker, ok := liveSource.(io.Seeker); ok {
			if _, err := seeker.Seek(0, io.SeekStart); err != nil {
				return nil, false, err
			}
			return liveSource, false, nil
		} else {
			source, err := root.Open(sourceRel)
			if err != nil {
				return nil, false, err
			}
			if _, err := validateLiveSourceInfo(source, sourceRel, expected, message); err != nil {
				return nil, false, errors.Join(err, source.Close())
			}
			return source, true, nil
		}
	}

	source, err := OpenPinnedFile(root, sourceRel)
	if err != nil {
		return nil, false, err
	}
	sourceInfo, err := source.Stat()
	if err != nil {
		return nil, false, closeFilePreservingPrimary(source, err)
	}
	if !sameRegularFile(expected, sourceInfo) {
		return nil, false, closeFilePreservingPrimary(source, fmt.Errorf("%s: %s", message, sourceRel))
	}
	return source, true, nil
}

func identityBoundLinkUnsupported(err error) bool {
	return errors.Is(err, errors.ErrUnsupported) ||
		errors.Is(err, syscall.EOPNOTSUPP) ||
		errors.Is(err, syscall.ENOSYS) ||
		errors.Is(err, syscall.EXDEV) ||
		errors.Is(err, syscall.EPERM)
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

func publishedRegularFileInfo(root Root, rel, message string) (fs.FileInfo, error) {
	info, err := root.Lstat(rel)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", message, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s: %s", message, rel)
	}
	return info, nil
}

func sameRegularFile(expected, actual fs.FileInfo) bool {
	if expected == nil || actual == nil {
		return false
	}
	if !expected.Mode().IsRegular() || !actual.Mode().IsRegular() {
		return false
	}
	return os.SameFile(expected, actual) &&
		expected.Size() == actual.Size() &&
		expected.Mode() == actual.Mode() &&
		expected.ModTime().Equal(actual.ModTime())
}

func chmodSupportedMode(mode os.FileMode) os.FileMode {
	return mode & (os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky)
}

func closeCreatedFileWithoutIdentity(file File, primaryErr error) error {
	closeErr := file.Close()
	return errors.Join(primaryErr, closeErr)
}

func (s *atomicWriteSession) snapshotAndCloseTempFile() error {
	if err := s.snapshotTempFile(); err != nil {
		return err
	}
	return s.closeTempFile()
}

func (s *atomicWriteSession) snapshotTempFile() error {
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
		var closeErr error
		if s.tempFile != nil {
			closeErr = s.tempFile.Close()
			s.tempFile = nil
		}
		return errors.Join(closeErr, cleanupAtomicTempFileIfMatches(s.root, s.tempRel, s.tempInfo))
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
	if err := session.snapshotTempFile(); err != nil {
		return err
	}
	if err := session.commit(); err != nil {
		fallbackErr := fallbackAtomicReplacement(root, publishRenameSource(err, session.tempRel), targetRel, replacementFile, data, err)
		if fallbackErr != nil {
			return fallbackErr
		}
		return publishRenameCleanup(err)
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
	if err := session.snapshotTempFile(); err != nil {
		return err
	}
	return session.publishIfAbsent()
}

func publishIdentityBoundIfAbsent(root Root, sourceRel, targetRel string, expected fs.FileInfo) (returnErr error) {
	stagedRel, stagedInfo, err := stageIdentityBoundFileKeepingSourceLive(root, sourceRel, expected, temporaryFileChangedBeforeCommit, nil)
	if err != nil {
		return err
	}
	return publishStagedIdentityBoundIfAbsent(root, sourceRel, stagedRel, targetRel, stagedInfo)
}

func (s *atomicWriteSession) publishIfAbsent() error {
	stagedRel, stagedInfo, err := stageIdentityBoundFileKeepingSourceLive(s.root, s.tempRel, s.tempInfo, temporaryFileChangedBeforeCommit, s.tempFile)
	if err != nil {
		return err
	}
	if err := s.closeTempFile(); err != nil {
		return errors.Join(err, cleanupAtomicTempFileIfMatches(s.root, stagedRel, stagedInfo))
	}
	return publishStagedIdentityBoundIfAbsent(s.root, s.tempRel, stagedRel, s.targetRel, stagedInfo)
}

func publishStagedIdentityBoundIfAbsent(root Root, sourceRel, stagedRel, targetRel string, stagedInfo fs.FileInfo) (returnErr error) {
	defer func() {
		returnErr = errors.Join(returnErr, cleanupAtomicTempFileIfMatches(root, stagedRel, stagedInfo))
	}()
	if err := linkFileIfMatches(root, stagedRel, targetRel, stagedInfo, temporaryFileChangedBeforeCommit); err != nil {
		if errors.Is(err, os.ErrExist) {
			return os.ErrExist
		}
		if identityBoundLinkUnsupported(err) {
			fallbackErr := fallbackAtomicIfAbsent(root, stagedRel, targetRel, stagedInfo, err)
			if fallbackErr == nil {
				return verifyPublishedPathMatchesInfo(root, targetRel, stagedInfo, committedTargetChangedBeforeValidation)
			}
			return fmt.Errorf("%w: %s: %w", errIdentityBoundReplacementUnsupported, sourceRel, fallbackErr)
		}
		return err
	}
	return verifyPublishedPathMatchesInfo(root, targetRel, stagedInfo, committedTargetChangedBeforeValidation)
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
	if err := session.snapshotTempFile(); err != nil {
		return err
	}
	if err := session.commit(); err != nil {
		fallbackErr := fallbackAtomicReplacement(root, publishRenameSource(err, session.tempRel), targetRel, replacementFile, data, err)
		if fallbackErr == nil {
			return publishRenameCleanup(err)
		}
		if pinnedOverwritePermissionFallbackAllowed(err, replacementFile, allowPermissionFallback) {
			return errors.Join(publishRenameCleanup(err), overwritePinnedFile(root, targetRel, replacementFile, data, nil))
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
