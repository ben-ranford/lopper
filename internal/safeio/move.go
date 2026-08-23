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

const (
	moveSourceChangedBeforeCleanup  = "move source changed before cleanup"
	moveSourceChangedBeforeRename   = "move source changed before rename"
	moveSourceChangedBeforeFallback = "move source changed before fallback copy"
	moveTargetChangedBeforeValidate = "move target changed before validation"
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

	fallbackSourceRel, sourceWasQuarantined := moveFallbackCopySourceState(renameErr, sourceRel)
	fallbackSourceInfo := publishRenameSourceInfo(renameErr, sourceInfo)
	_, copyErr := copyFileWithinRoot(root, fallbackSourceRel, targetRel, filePerm, fallbackSourceInfo)
	if errors.Is(copyErr, os.ErrNotExist) && fallbackCopySourceIsAbsent(root, sourceRel, fallbackSourceRel) {
		fallbackSourceInfo = sourceInfo
		_, copyErr = copyFileWithinRoot(root, sourceRel, targetRel, filePerm, sourceInfo)
		sourceWasQuarantined = false
	}
	if copyErr != nil {
		return errors.Join(
			renameErr,
			copyErr,
			restoreQuarantinedMoveSourceAfterFallbackFailure(root, sourceRel, fallbackSourceRel, sourceInfo, sourceWasQuarantined),
		)
	}

	return errors.Join(
		publishRenameCleanup(renameErr),
		removeCopiedMoveSource(root, sourceRel, sourceInfo),
		cleanupCopiedMoveFallbackSource(root, sourceRel, fallbackSourceRel, fallbackSourceInfo),
		cleanupMoveSourceStagingDir(root, sourceRel, fallbackSourceRel),
	)
}

type moveLinklessRenameError struct {
	err error
}

func (e *moveLinklessRenameError) Error() string {
	return e.err.Error()
}

func (e *moveLinklessRenameError) Unwrap() error {
	return e.err
}

func moveFallbackCopySource(err error, sourceRel string) string {
	fallbackSourceRel, _ := moveFallbackCopySourceState(err, sourceRel)
	return fallbackSourceRel
}

func moveFallbackCopySourceState(err error, sourceRel string) (fallbackSourceRel string, sourceWasQuarantined bool) {
	var linklessErr *moveLinklessRenameError
	if errors.As(err, &linklessErr) {
		return publishRenameSource(linklessErr.err, sourceRel), true
	}
	fallbackSourceRel = publishRenameSource(err, sourceRel)
	if isMoveSourceStagingEntry(sourceRel, fallbackSourceRel) {
		return fallbackSourceRel, false
	}
	return sourceRel, false
}

func fallbackCopySourceIsAbsent(root Root, sourceRel, fallbackSourceRel string) bool {
	if filepath.Clean(fallbackSourceRel) == filepath.Clean(sourceRel) {
		return false
	}
	_, err := root.Lstat(fallbackSourceRel)
	return errors.Is(err, os.ErrNotExist)
}

func prepareAndRenameWithinRoot(root Root, sourceRel, targetRel string, filePerm os.FileMode) (fs.FileInfo, error) {
	sourceInfo, err := chmodAndSnapshotMoveSource(root, sourceRel, filePerm)
	if err != nil {
		return nil, err
	}
	aliasesTarget, err := targetAliasesSource(root, sourceRel, targetRel, sourceInfo)
	if readDirFileUnsupportedWithoutCleanupError(err) {
		// Root only promises File from Open. When two differently-spelled
		// paths may address one directory entry, do the identity-checked
		// quarantine rename instead of guessing from incomplete observations.
		return sourceInfo, renameLinklessMoveSource(root, sourceRel, targetRel, sourceInfo)
	}
	if err != nil {
		return sourceInfo, err
	}
	_, err = publishIdentityBoundReplacingWithSourceState(root, sourceRel, targetRel, sourceInfo, moveSourceChangedBeforeRename, moveTargetChangedBeforeValidate)
	if err != nil {
		if errors.Is(err, errIdentityBoundReplacementUnsupported) {
			return sourceInfo, renameLinklessMoveSource(root, sourceRel, targetRel, sourceInfo)
		}
		return sourceInfo, err
	}
	if aliasesTarget {
		return sourceInfo, nil
	}
	if err := removeIdentityBound(root, sourceRel, sourceInfo, moveSourceChangedBeforeCleanup); err != nil {
		return sourceInfo, err
	}
	return sourceInfo, nil
}

func renameLinklessMoveSource(root Root, sourceRel, targetRel string, sourceInfo fs.FileInfo) error {
	sourceConsumed, err := renameFileIfMatches(root, sourceRel, targetRel, sourceInfo, moveSourceChangedBeforeRename)
	if err != nil {
		return &moveLinklessRenameError{err: err}
	}
	if sourceConsumed {
		return verifyPublishedPathMatchesInfo(root, targetRel, sourceInfo, moveTargetChangedBeforeValidate)
	}
	return errors.Join(
		verifyPublishedPathMatchesInfo(root, targetRel, sourceInfo, moveTargetChangedBeforeValidate),
		removeIdentityBound(root, sourceRel, sourceInfo, moveSourceChangedBeforeCleanup),
	)
}

func targetAliasesSource(root Root, sourceRel, targetRel string, sourceInfo fs.FileInfo) (bool, error) {
	sourceClean := filepath.Clean(sourceRel)
	targetClean := filepath.Clean(targetRel)
	if !pathSpellingCanAlias(sourceClean, targetClean) {
		return false, nil
	}
	targetInfo, err := root.Lstat(targetClean)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !targetInfo.Mode().IsRegular() || !os.SameFile(sourceInfo, targetInfo) {
		return false, nil
	}
	if sourceClean == targetClean {
		return true, nil
	}
	sourceParent := filepath.Dir(sourceClean)
	targetParent := filepath.Dir(targetClean)
	sourceParentInfo, err := root.Lstat(sourceParent)
	if err != nil {
		return false, err
	}
	targetParentInfo, err := root.Lstat(targetParent)
	if err != nil {
		return false, err
	}
	if !sourceParentInfo.IsDir() || !targetParentInfo.IsDir() || !os.SameFile(sourceParentInfo, targetParentInfo) {
		return false, nil
	}
	aliases, err := countDirectoryEntriesAddressingBothNames(root, sourceParent, filepath.Base(sourceClean), filepath.Base(targetClean), sourceInfo)
	if err != nil {
		return false, err
	}
	return aliases == 1, nil
}

func pathSpellingCanAlias(sourceRel, targetRel string) bool {
	return sourceRel == targetRel || strings.EqualFold(sourceRel, targetRel) || containsNonASCII(sourceRel) || containsNonASCII(targetRel)
}

func containsNonASCII(value string) bool {
	for _, r := range value {
		if r > 0x7f {
			return true
		}
	}
	return false
}

func countDirectoryEntriesAddressingBothNames(root Root, parentRel, sourceBase, targetBase string, sourceInfo fs.FileInfo) (_ int, returnErr error) {
	dir, err := OpenPinnedDirectory(root, parentRel)
	if err != nil {
		return 0, err
	}
	defer func() {
		returnErr = errors.Join(returnErr, dir.Close())
	}()
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, entry := range entries {
		name := entry.Name()
		if name != sourceBase && name != targetBase && !strings.EqualFold(name, sourceBase) && !strings.EqualFold(name, targetBase) {
			continue
		}
		entryInfo, err := root.Lstat(filepath.Join(parentRel, name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return 0, err
		}
		if entryInfo.Mode().IsRegular() && os.SameFile(sourceInfo, entryInfo) {
			count++
		}
	}
	return count, nil
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
		return nil, fmt.Errorf("%s: %s", moveSourceChangedBeforeRename, sourceRel)
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
	if expectedSourceInfo != nil && !sameRegularFile(expectedSourceInfo, sourceInfo) {
		return nil, fmt.Errorf("%s: %s", moveSourceChangedBeforeFallback, sourceRel)
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
	if err := verifyPublishedPathMatchesInfo(root, sourceRel, sourceInfo, moveSourceChangedBeforeFallback); err != nil {
		return nil, err
	}
	if err := session.commit(); err != nil {
		return nil, err
	}
	return sourceInfo, nil
}

func removeCopiedMoveSource(root Root, sourceRel string, sourceInfo os.FileInfo) error {
	return removeIdentityBound(root, sourceRel, sourceInfo, moveSourceChangedBeforeCleanup)
}

func cleanupCopiedMoveFallbackSource(root Root, sourceRel, fallbackSourceRel string, sourceInfo os.FileInfo) error {
	if filepath.Clean(sourceRel) == filepath.Clean(fallbackSourceRel) {
		return nil
	}
	return cleanupAtomicTempFileIfMatches(root, fallbackSourceRel, sourceInfo)
}

func restoreQuarantinedMoveSourceAfterFallbackFailure(root Root, sourceRel, fallbackSourceRel string, sourceInfo fs.FileInfo, sourceWasQuarantined bool) error {
	if !sourceWasQuarantined || !isMoveSourceStagingEntry(sourceRel, fallbackSourceRel) {
		return nil
	}
	restored, err := restoreQuarantinedPathNoReplace(root, fallbackSourceRel, sourceRel, moveSourceChangedBeforeFallback, sourceInfo)
	if !restored {
		return err
	}
	return errors.Join(
		err,
		cleanupCopiedMoveFallbackSource(root, sourceRel, fallbackSourceRel, sourceInfo),
		cleanupMoveSourceStagingDir(root, sourceRel, fallbackSourceRel),
	)
}

func cleanupMoveSourceStagingDir(root Root, sourceRel, fallbackSourceRel string) error {
	if filepath.Clean(sourceRel) == filepath.Clean(fallbackSourceRel) {
		return nil
	}
	if !isMoveSourceStagingEntry(sourceRel, fallbackSourceRel) {
		return nil
	}
	dir := filepath.Dir(fallbackSourceRel)
	info, err := root.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return nil
	}
	return ignoreRemoveNotExist(root.Remove(dir))
}

func isMoveSourceStagingEntry(sourceRel, fallbackSourceRel string) bool {
	if filepath.Clean(sourceRel) == filepath.Clean(fallbackSourceRel) {
		return false
	}
	dir := filepath.Dir(fallbackSourceRel)
	return dir != "." && filepath.Base(fallbackSourceRel) == "entry" && strings.HasPrefix(filepath.Base(dir), atomicTempPrefix)
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
		if errors.Is(err, errIdentityBoundLinkUnavailable) || identityBoundLinkUnsupported(err) {
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
