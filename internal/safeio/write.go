package safeio

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// WriteRoot pins a filesystem root for path-confined atomic writes.
type WriteRoot struct {
	root    Root
	rootAbs string
}

// OpenWriteRoot opens rootDir once for subsequent root-relative writes.
func OpenWriteRoot(rootDir string) (*WriteRoot, error) {
	rootAbs, err := resolveAbsolutePath("root", rootDir)
	if err != nil {
		return nil, err
	}
	return openWriteRoot(rootAbs)
}

// OpenCanonicalWriteRoot pins a canonical root without following symlinks in
// any component of rootDir.
func OpenCanonicalWriteRoot(rootDir string) (*WriteRoot, error) {
	rootAbs, err := resolveAbsolutePath("root", rootDir)
	if err != nil {
		return nil, err
	}
	root, err := fileSystem.OpenRootNoFollow(rootAbs)
	if err != nil {
		return nil, fmt.Errorf("open canonical root: %w", err)
	}
	return &WriteRoot{root: root, rootAbs: rootAbs}, nil
}

func openWriteRoot(rootAbs string) (*WriteRoot, error) {
	root, err := fileSystem.OpenRoot(rootAbs)
	if err != nil {
		return nil, fmt.Errorf("open root: %w", err)
	}
	return &WriteRoot{root: root, rootAbs: rootAbs}, nil
}

// Close releases the pinned filesystem root.
func (r *WriteRoot) Close() error {
	return r.root.Close()
}

// RootInfo returns identity information for the pinned root directory.
func (r *WriteRoot) RootInfo() (fs.FileInfo, error) {
	return r.root.Lstat(".")
}

// VerifyIdentity reports whether the pinned root retains the expected identity.
func (r *WriteRoot) VerifyIdentity(expected fs.FileInfo) error {
	actual, err := r.RootInfo()
	if err != nil {
		return err
	}
	if !os.SameFile(expected, actual) {
		return fmt.Errorf("pinned root identity changed")
	}
	return nil
}

// WriteFileCreatingParents atomically writes a root-relative file, creating
// missing parent directories inside the pinned root.
func (r *WriteRoot) WriteFileCreatingParents(targetPath string, data []byte, perm, parentPerm os.FileMode) error {
	target, err := r.resolveTarget(targetPath)
	if err != nil {
		return err
	}
	return r.writeFileToTargetParent(target, data, perm, true, parentPerm, writeFileAtRoot)
}

// WriteFileCreatingParentsWithPermissionFallback atomically writes a
// root-relative file, creating missing parent directories inside the pinned
// root. When an existing regular target is already open and writable, callers
// may opt into an in-place overwrite fallback if parent directory permissions
// reject the atomic temp-file path.
func (r *WriteRoot) WriteFileCreatingParentsWithPermissionFallback(targetPath string, data []byte, perm, parentPerm os.FileMode) error {
	target, err := r.resolveTarget(targetPath)
	if err != nil {
		return err
	}
	return r.withTargetParent(target, true, parentPerm, func(parent Root, parentTarget rootedTarget) error {
		return writeFileAtRootWithPermissionFallback(parent, parentTarget, data, perm)
	})
}

// WriteFileCreatingParentsIfAbsent writes a root-relative file only when the
// target path does not already exist, creating missing parent directories
// inside the pinned root without following symlinks.
func (r *WriteRoot) WriteFileCreatingParentsIfAbsent(targetPath string, data []byte, perm, parentPerm os.FileMode) error {
	target, err := r.resolveTarget(targetPath)
	if err != nil {
		return err
	}
	return r.writeFileToTargetParent(target, data, perm, true, parentPerm, writeFileIfAbsentAtRoot)
}

// WriteFileCreatingParentsAtomicallyIfAbsent atomically publishes a
// root-relative file only if its target is absent, creating missing parent
// directories inside the pinned root without following symlinks.
func (r *WriteRoot) WriteFileCreatingParentsAtomicallyIfAbsent(targetPath string, data []byte, perm, parentPerm os.FileMode) error {
	target, err := r.resolveTarget(targetPath)
	if err != nil {
		return err
	}
	return r.withTargetParent(target, true, parentPerm, func(parent Root, parentTarget rootedTarget) error {
		return writeFileAtomicallyIfAbsentAtRoot(parent, parentTarget.rel, data, perm)
	})
}

func (r *WriteRoot) resolveTarget(targetPath string) (rootedTarget, error) {
	if filepath.IsAbs(targetPath) {
		return rootedTarget{}, fmt.Errorf("target path must be relative to root: %s", targetPath)
	}
	rel, err := normalizeRootedTarget(targetPath, filepath.Clean(targetPath), rejectRootTarget)
	if err != nil {
		return rootedTarget{}, err
	}
	return rootedTarget{rootAbs: r.rootAbs, rel: rel, abs: filepath.Join(r.rootAbs, rel)}, nil
}

func (r *WriteRoot) writeFileAtTarget(target rootedTarget, data []byte, perm os.FileMode, createParents bool, parentPerm os.FileMode) error {
	return r.writeFileToTargetParent(target, data, perm, createParents, parentPerm, writeFileAtRoot)
}

func (r *WriteRoot) writeFileToTargetParent(target rootedTarget, data []byte, perm os.FileMode, createParents bool, parentPerm os.FileMode, write func(root Root, target rootedTarget, data []byte, perm os.FileMode) error) (returnErr error) {
	return r.withTargetParent(target, createParents, parentPerm, func(parent Root, parentTarget rootedTarget) error {
		return write(parent, parentTarget, data, perm)
	})
}

func (r *WriteRoot) withTargetParent(target rootedTarget, createParents bool, parentPerm os.FileMode, write func(parent Root, parentTarget rootedTarget) error) (returnErr error) {
	parent, closeParent, err := r.openTargetParent(target, createParents, parentPerm)
	if err != nil {
		return err
	}
	if closeParent {
		defer func() {
			if closeErr := parent.Close(); closeErr != nil {
				returnErr = errors.Join(returnErr, closeErr)
			}
		}()
	}
	if err := writeFileParentReadyFn(); err != nil {
		return err
	}
	parentTarget := target
	parentTarget.rel = filepath.Base(target.rel)
	return write(parent, parentTarget)
}

func (r *WriteRoot) openTargetParent(target rootedTarget, create bool, perm os.FileMode) (Root, bool, error) {
	parentRel := filepath.Dir(target.rel)
	if parentRel == "." {
		return r.root, false, nil
	}

	current := r.root
	currentOwned := false
	currentAbs := r.rootAbs
	for _, part := range strings.Split(parentRel, string(os.PathSeparator)) {
		if part == "" || part == "." {
			continue
		}
		partAbs := filepath.Join(currentAbs, part)
		next, err := openTargetParentChild(current, part, partAbs, create, perm)
		if err != nil {
			return nil, false, closeOwnedRootWithError(current, currentOwned, err)
		}
		if currentOwned {
			if err := current.Close(); err != nil {
				return nil, false, closeRootWithError(next, err)
			}
		}
		current = next
		currentOwned = true
		currentAbs = partAbs
	}
	return current, currentOwned, nil
}

func openTargetParentChild(root Root, name, path string, create bool, perm os.FileMode) (Root, error) {
	return openValidatedChildRoot(root, name, path, func() (fs.FileInfo, error) { return lstatOrCreateDirectory(root, name, create, perm) }, "output parent contains symlink", "output parent is not a directory", "output parent changed while opening")
}

func lstatOrCreateDirectory(root Root, name string, create bool, perm os.FileMode) (fs.FileInfo, error) {
	info, err := root.Lstat(name)
	if !os.IsNotExist(err) || !create {
		return info, err
	}
	if mkdirErr := root.Mkdir(name, perm); mkdirErr != nil && !errors.Is(mkdirErr, fs.ErrExist) {
		return nil, mkdirErr
	}
	return root.Lstat(name)
}

func closeOwnedRootWithError(root Root, owned bool, err error) error {
	if !owned {
		return err
	}
	return closeRootWithError(root, err)
}

const atomicTempPrefix = ".safeio-atomic-"

var (
	randomTempNameFn       = randomTempName
	randReadFn             = rand.Read
	writeFileParentReadyFn = func() error { return nil }
)

// CreateTempFileWithinRoot creates a temporary file under dir within root.
func CreateTempFileWithinRoot(root Root, dir string, perm os.FileMode) (string, File, error) {
	return createAtomicTempFile(root, dir, perm)
}

// CleanupTempFileWithinRoot closes and removes a temporary file within root.
func CleanupTempFileWithinRoot(root Root, tempRel string, tempFile File) error {
	return cleanupAtomicTempFile(root, tempRel, tempFile)
}

// WriteFileUnder atomically writes targetPath only if it resolves under rootDir.
// Existing regular targets must be writable and retain their permission bits.
// Ownership follows atomic replacement semantics; writes never fall back to in-place mutation.
func WriteFileUnder(rootDir, targetPath string, data []byte, perm os.FileMode) (returnErr error) {
	target, err := resolveRootedTarget(rootDir, targetPath, rejectRootTarget)
	if err != nil {
		return err
	}
	root, err := openWriteRoot(target.rootAbs)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			returnErr = errors.Join(returnErr, closeErr)
		}
	}()
	return root.writeFileAtTarget(target, data, perm, false, 0)
}

func writeFileAtRoot(root Root, target rootedTarget, data []byte, perm os.FileMode) error {
	return writeFileAtRootWithOptions(root, target, data, perm, false)
}

func writeFileAtRootWithPermissionFallback(root Root, target rootedTarget, data []byte, perm os.FileMode) error {
	return writeFileAtRootWithOptions(root, target, data, perm, true)
}

func writeFileAtRootWithOptions(root Root, target rootedTarget, data []byte, perm os.FileMode, allowPermissionFallback bool) (returnErr error) {
	writePerm, existingInfo, err := resolvedWriteFilePerm(root, target, perm)
	if err != nil {
		return err
	}
	if existingInfo == nil {
		return writeAtomicReplacement(root, target.rel, data, writePerm, nil)
	}

	file, err := openPinnedReplacementTarget(root, target.rel, existingInfo)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			returnErr = errors.Join(returnErr, closeErr)
		}
	}()

	return writeAtomicReplacementWithPinnedTarget(root, target.rel, data, writePerm, file, allowPermissionFallback)
}

func writeFileIfAbsentAtRoot(root Root, target rootedTarget, data []byte, perm os.FileMode) (returnErr error) {
	if _, err := root.Lstat(target.rel); err == nil {
		return os.ErrExist
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	err := writeFileAtomicallyIfAbsentAtRoot(root, target.rel, data, perm)
	if err == nil || !errors.Is(err, errIdentityBoundReplacementUnsupported) {
		return err
	}
	return writeFileExclusivelyIfAbsentAtRoot(root, target.rel, data, perm)
}

func writeFileExclusivelyIfAbsentAtRoot(root Root, targetRel string, data []byte, perm os.FileMode) (returnErr error) {
	targetFile, err := root.OpenFile(targetRel, os.O_RDWR|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}

	targetInfo, err := targetFile.Stat()
	if err != nil {
		return closeFilePreservingPrimary(targetFile, err)
	}
	if !targetInfo.Mode().IsRegular() {
		return errors.Join(
			fmt.Errorf("target file is not regular after exclusive create: %s", targetRel),
			cleanupAtomicTempFileIfMatches(root, targetRel, targetInfo),
			targetFile.Close(),
		)
	}

	defer func() {
		if targetFile != nil {
			returnErr = errors.Join(returnErr, targetFile.Close())
		}
		if returnErr != nil {
			returnErr = errors.Join(returnErr, cleanupAtomicTempFileIfMatches(root, targetRel, targetInfo))
		}
	}()

	if _, err := targetFile.Write(data); err != nil {
		return err
	}
	refreshedInfo, err := targetFile.Stat()
	if err != nil {
		return err
	}
	targetInfo = refreshedInfo
	if err := targetFile.Chmod(perm); err != nil {
		return err
	}
	refreshedInfo, err = targetFile.Stat()
	if err != nil {
		return err
	}
	targetInfo = refreshedInfo
	if err := targetFile.Close(); err != nil {
		targetFile = nil
		return err
	}
	targetFile = nil
	return verifyPublishedPathMatchesInfo(root, targetRel, targetInfo, committedTargetChangedBeforeValidation)
}

func resolvedWriteFilePerm(root Root, target rootedTarget, requestedPerm os.FileMode) (os.FileMode, fs.FileInfo, error) {
	info, err := root.Lstat(target.rel)
	switch {
	case err == nil:
		if info.Mode()&os.ModeSymlink != 0 {
			return 0, nil, fmt.Errorf("target path is a symlink: %s", target.abs)
		}
		if !info.Mode().IsRegular() {
			return 0, nil, fmt.Errorf("target path is not a regular file: %s", target.abs)
		}
		return info.Mode().Perm(), info, nil
	case os.IsNotExist(err):
		return requestedPerm, nil, nil
	default:
		return 0, nil, err
	}
}

// WriteFileWithinRoot atomically writes targetPath using an already-open
// confined root.
func WriteFileWithinRoot(root Root, targetPath string, data []byte, perm os.FileMode) (returnErr error) {
	targetRel, err := resolveRelativeTarget(targetPath, rejectRootTarget)
	if err != nil {
		return err
	}
	return writeAtomicReplacement(root, targetRel, data, perm, nil)
}

// WriteFileReplacingUnder atomically writes targetPath only if it resolves
// under rootDir. Existing regular targets retain their permission bits.
// On Windows only, writes may fall back to in-place overwrite when the
// filesystem rejects replace-existing rename semantics for an existing file.
func WriteFileReplacingUnder(rootDir, targetPath string, data []byte, perm os.FileMode) (returnErr error) {
	target, err := resolveRootedTarget(rootDir, targetPath, rejectRootTarget)
	if err != nil {
		return err
	}
	root, err := openWriteRoot(target.rootAbs)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			returnErr = errors.Join(returnErr, closeErr)
		}
	}()
	return WriteFileReplacingWithinRoot(root.root, target.rel, data, perm)
}

// WriteFileReplacingWithinRoot atomically writes targetPath using an already-open
// confined root. Existing regular targets retain their permission bits. On
// Windows only, writes may fall back to in-place overwrite when the filesystem
// rejects replace-existing rename semantics for an existing file.
func WriteFileReplacingWithinRoot(root Root, targetPath string, data []byte, perm os.FileMode) error {
	targetRel, err := resolveRelativeTarget(targetPath, rejectRootTarget)
	if err != nil {
		return err
	}
	target := rootedTarget{rel: targetRel}
	writePerm, existingInfo, err := resolvedWriteFilePerm(root, target, perm)
	if err != nil {
		return err
	}
	return writeAtomicReplacement(root, target.rel, data, writePerm, existingInfo)
}

const cleanupFileChangedBeforeRemoval = "cleanup file changed before removal"

func cleanupAtomicTempFile(root Root, tempRel string, tempFile File) error {
	var cleanupErr error
	if tempFile != nil {
		if err := tempFile.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			cleanupErr = err
		}
	}
	if tempRel != "" {
		if err := root.Remove(tempRel); err != nil && !errors.Is(err, os.ErrNotExist) {
			if cleanupErr == nil {
				return err
			}
			return errors.Join(cleanupErr, err)
		}
	}
	return cleanupErr
}

func cleanupAtomicTempFileIfMatches(root Root, tempRel string, expected fs.FileInfo) error {
	err := cleanupAtomicTempFileIfMatchesOnce(root, tempRel, expected, cleanupFileChangedBeforeRemoval)
	if err == nil {
		return nil
	}
	if tempRel == "" || expected == nil || strings.Contains(err.Error(), cleanupFileChangedBeforeRemoval) {
		return err
	}
	return errors.Join(err, retryCleanupAtomicTempFileIfStillMatches(root, tempRel, expected, cleanupFileChangedBeforeRemoval))
}

func cleanupAtomicTempFileIfMatchesOnce(root Root, tempRel string, expected fs.FileInfo, changedBeforeRemoval string) error {
	if tempRel == "" {
		return nil
	}
	if expected == nil {
		return fmt.Errorf("cleanup file identity unavailable: %s", tempRel)
	}
	info, err := root.Lstat(tempRel)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || !os.SameFile(expected, info) {
		return nil
	}
	cleanupRel, err := stageIdentityBoundLink(root, tempRel, info, changedBeforeRemoval)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if errors.Is(err, errIdentityBoundLinkUnavailable) || identityBoundLinkUnsupported(err) {
			return removeFileIfMatches(root, tempRel, info, changedBeforeRemoval)
		}
		return err
	}
	if err := removeFileIfMatches(root, tempRel, expected, changedBeforeRemoval); err != nil {
		return errors.Join(err, removeFileIfMatches(root, cleanupRel, info, changedBeforeRemoval))
	}
	return removeFileIfMatches(root, cleanupRel, info, changedBeforeRemoval)
}

func retryCleanupAtomicTempFileIfStillMatches(root Root, tempRel string, expected fs.FileInfo, changedBeforeRemoval string) error {
	info, err := root.Lstat(tempRel)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !sameRegularFile(expected, info) {
		return nil
	}
	return removeFileIfMatches(root, tempRel, expected, changedBeforeRemoval)
}

func removeFileIfMatches(root Root, rel string, expected fs.FileInfo, message string) error {
	if rel == "" {
		return nil
	}
	if expected == nil {
		return fmt.Errorf("%s: %s", message, rel)
	}
	var err error
	if guardedRoot, ok := root.(identityBoundOperationsRoot); ok {
		err = guardedRoot.RemoveIfMatches(rel, expected, message)
	} else {
		err = removeFileIfMatchesUsingBasicRoot(root, rel, expected, message)
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func removeFileIfStillMatches(root Root, rel string, expected fs.FileInfo, message string) error {
	return removeFileIfMatches(root, rel, expected, message)
}

func linkFileIfMatchesUsingBasicRoot(root Root, oldName, newName string, expected fs.FileInfo, message string) (returnErr error) {
	quarantineDir, quarantineRel, err := identityBoundQuarantinePath(root, oldName)
	if err != nil {
		return err
	}
	cleanupDir := true
	defer func() {
		if cleanupDir {
			returnErr = errors.Join(returnErr, ignoreRemoveNotExist(root.Remove(quarantineDir)))
		}
	}()

	if err := root.Link(oldName, quarantineRel); err != nil {
		if identityBoundLinkUnsupported(err) {
			return fmt.Errorf("%w: %w", errIdentityBoundLinkUnavailable, err)
		}
		return err
	}

	quarantineInfo, err := publishedRegularFileInfo(root, quarantineRel, message)
	if err != nil {
		cleanupDir = false
		return err
	}
	if !sameRegularFile(expected, quarantineInfo) {
		cleanupErr := removeFileIfMatchesUsingBasicRoot(root, quarantineRel, quarantineInfo, message)
		if cleanupErr != nil {
			cleanupDir = false
		}
		return errors.Join(fmt.Errorf("%s: %s", message, oldName), cleanupErr)
	}

	if err := root.Link(quarantineRel, newName); err != nil {
		cleanupErr := removeFileIfMatchesUsingBasicRoot(root, quarantineRel, quarantineInfo, message)
		if cleanupErr != nil {
			cleanupDir = false
		}
		return errors.Join(err, cleanupErr)
	}

	newInfo, err := publishedRegularFileInfo(root, newName, message)
	if err != nil {
		cleanupErr := removeFileIfMatchesUsingBasicRoot(root, newName, quarantineInfo, message)
		return errors.Join(err, cleanupErr)
	}
	if !sameRegularFile(quarantineInfo, newInfo) {
		targetCleanupErr := removeFileIfMatchesUsingBasicRoot(root, newName, quarantineInfo, message)
		sourceCleanupErr := removeFileIfMatchesUsingBasicRoot(root, quarantineRel, quarantineInfo, message)
		if sourceCleanupErr != nil {
			cleanupDir = false
		}
		return errors.Join(fmt.Errorf("%s: %s", message, newName), targetCleanupErr, sourceCleanupErr)
	}

	cleanupErr := removeFileIfMatchesUsingBasicRoot(root, quarantineRel, quarantineInfo, message)
	if cleanupErr != nil {
		cleanupDir = false
	}
	return cleanupErr
}

func renameFileIfMatchesUsingBasicRoot(root Root, oldName, newName string, expected fs.FileInfo, message string) (_ bool, returnErr error) {
	if expected == nil {
		return false, fmt.Errorf("%s: %s", message, oldName)
	}
	if !expected.Mode().IsRegular() {
		return false, fmt.Errorf("%s: %s", message, oldName)
	}
	renameState, err := newBasicRootRenameState(root, oldName, newName, expected, message)
	if err != nil {
		return false, err
	}
	defer renameState.cleanup(&returnErr)

	if err := root.Rename(oldName, renameState.quarantineRel); err != nil {
		return false, err
	}
	if err := renameState.snapshotQuarantine(); err != nil {
		return false, err
	}
	if !sameRegularFile(expected, renameState.quarantineInfo) {
		return false, renameState.restoreSourceMismatch()
	}
	if err := renameState.publishToTarget(); err != nil {
		return false, err
	}
	return renameState.finishAfterTargetRename()
}

type basicRootRenameState struct {
	root                   Root
	oldName                string
	newName                string
	expected               fs.FileInfo
	message                string
	quarantineDir          string
	quarantineRel          string
	quarantineInfo         fs.FileInfo
	cleanupDir             bool
	cleanupQuarantineEntry bool
}

func newBasicRootRenameState(root Root, oldName, newName string, expected fs.FileInfo, message string) (*basicRootRenameState, error) {
	quarantineDir, quarantineRel, err := identityBoundQuarantinePath(root, oldName)
	if err != nil {
		return nil, err
	}
	return &basicRootRenameState{
		root:          root,
		oldName:       oldName,
		newName:       newName,
		expected:      expected,
		message:       message,
		quarantineDir: quarantineDir,
		quarantineRel: quarantineRel,
		cleanupDir:    true,
	}, nil
}

func (s *basicRootRenameState) cleanup(returnErr *error) {
	if s.cleanupQuarantineEntry {
		*returnErr = errors.Join(*returnErr, retryCleanupAtomicTempFileIfStillMatches(s.root, s.quarantineRel, s.quarantineInfo, s.message))
	}
	if s.cleanupDir {
		*returnErr = errors.Join(*returnErr, ignoreRemoveNotExist(s.root.Remove(s.quarantineDir)))
	}
}

func (s *basicRootRenameState) snapshotQuarantine() error {
	info, err := publishedRegularFileInfo(s.root, s.quarantineRel, s.message)
	if err != nil {
		return s.restoreSourceAfterSnapshotFailure(err)
	}
	s.quarantineInfo = info
	s.cleanupQuarantineEntry = true
	return nil
}

func (s *basicRootRenameState) restoreSourceAfterSnapshotFailure(snapshotErr error) error {
	restored, restoreErr := restoreQuarantinedPathNoReplace(s.root, s.quarantineRel, s.oldName, s.message, s.expected)
	if !restored {
		s.disableQuarantineCleanup()
	} else {
		s.quarantineInfo = s.expected
		s.cleanupQuarantineEntry = true
	}
	sourceRel := s.quarantineRel
	if restored {
		sourceRel = s.oldName
	}
	return withPublishRenameSource(errors.Join(snapshotErr, restoreErr), sourceRel)
}

func (s *basicRootRenameState) restoreSourceMismatch() error {
	restoreErr := s.restoreOriginalFromQuarantine()
	err := errors.Join(fmt.Errorf("%s: %s", s.message, s.oldName), restoreErr)
	return withPublishRenameSource(err, s.quarantineRel)
}

func (s *basicRootRenameState) publishToTarget() error {
	if err := s.root.Rename(s.quarantineRel, s.newName); err != nil {
		return s.handleTargetRenameError(err)
	}
	return nil
}

func (s *basicRootRenameState) handleTargetRenameError(err error) error {
	if errors.Is(err, syscall.EXDEV) {
		s.disableQuarantineCleanup()
		return withPublishRenameSource(err, s.quarantineRel)
	}
	restoreErr := s.restoreOriginalFromQuarantine()
	return withPublishRenameSource(errors.Join(err, restoreErr), s.quarantineRel)
}

func (s *basicRootRenameState) restoreOriginalFromQuarantine() error {
	restored, restoreErr := restoreQuarantinedPathNoReplace(s.root, s.quarantineRel, s.oldName, s.message, s.quarantineInfo)
	if !restored {
		s.disableQuarantineCleanup()
	}
	if restored && restoreErr == nil {
		s.cleanupQuarantineEntry = false
	}
	return restoreErr
}

func (s *basicRootRenameState) disableQuarantineCleanup() {
	s.cleanupDir = false
	s.cleanupQuarantineEntry = false
}

func (s *basicRootRenameState) finishAfterTargetRename() (bool, error) {
	stagedInfo, err := s.root.Lstat(s.quarantineRel)
	if errors.Is(err, os.ErrNotExist) {
		s.cleanupQuarantineEntry = false
		return true, nil
	}
	if err != nil {
		s.cleanupDir = false
		return false, withPublishRenameSource(err, s.quarantineRel)
	}
	if !sameRegularFile(s.quarantineInfo, stagedInfo) {
		s.disableQuarantineCleanup()
		return false, withPublishRenameSource(fmt.Errorf("%s: %s", s.message, s.quarantineRel), s.quarantineRel)
	}
	return s.removeUnconsumedQuarantineEntry()
}

func (s *basicRootRenameState) removeUnconsumedQuarantineEntry() (bool, error) {
	_, cleanupErr := finishRestoredQuarantinedPath(s.root, s.quarantineRel, s.message, s.quarantineInfo)
	if cleanupErr != nil {
		s.cleanupDir = false
		return false, cleanupErr
	}
	s.cleanupQuarantineEntry = false
	return false, nil
}

func removeFileIfMatchesUsingBasicRoot(root Root, rel string, expected fs.FileInfo, message string) (returnErr error) {
	quarantineDir, quarantineRel, err := identityBoundQuarantinePath(root, rel)
	if err != nil {
		return err
	}
	cleanupDir := true
	cleanupQuarantineEntry := false
	var quarantineInfo fs.FileInfo
	defer func() {
		if cleanupQuarantineEntry {
			returnErr = errors.Join(returnErr, retryCleanupAtomicTempFileIfStillMatches(root, quarantineRel, quarantineInfo, message))
		}
		if cleanupDir {
			returnErr = errors.Join(returnErr, ignoreRemoveNotExist(root.Remove(quarantineDir)))
		}
	}()

	if err := root.Rename(rel, quarantineRel); err != nil {
		return err
	}

	quarantineInfo, err = publishedRegularFileInfo(root, quarantineRel, message)
	if err != nil {
		cleanupDir = false
		return err
	}
	cleanupQuarantineEntry = true
	if !sameRegularFile(expected, quarantineInfo) {
		restored, restoreErr := restoreQuarantinedPathNoReplace(root, quarantineRel, rel, message, quarantineInfo)
		if !restored {
			cleanupDir = false
			cleanupQuarantineEntry = false
		}
		if restored && restoreErr == nil {
			cleanupQuarantineEntry = false
		}
		return errors.Join(fmt.Errorf("%s: %s", message, rel), restoreErr)
	}

	if err := removeVerifiedQuarantinedFile(root, quarantineRel, quarantineInfo, message); err != nil {
		cleanupDir = false
		return err
	}
	cleanupQuarantineEntry = false
	return nil
}

func removeVerifiedQuarantinedFile(root Root, rel string, expected fs.FileInfo, message string) error {
	if err := verifyPublishedPathMatchesInfo(root, rel, expected, message); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if err := root.Remove(rel); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func cleanupCreatedFileIfSameFile(root Root, rel string, expected fs.FileInfo, message string) (returnErr error) {
	info, ok, err := createdFileInfoIfSameFile(root, rel, expected, message)
	if err != nil || !ok {
		return err
	}
	return removeCreatedFileIfSameFile(root, rel, info, message)
}

func createdFileInfoIfSameFile(root Root, rel string, expected fs.FileInfo, message string) (fs.FileInfo, bool, error) {
	if rel == "" {
		return nil, false, nil
	}
	if expected == nil {
		return nil, false, fmt.Errorf("%s: %s", message, rel)
	}
	info, err := root.Lstat(rel)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !info.Mode().IsRegular() || !os.SameFile(expected, info) {
		return nil, false, nil
	}
	return info, true, nil
}

func removeCreatedFileIfSameFile(root Root, rel string, expected fs.FileInfo, message string) (returnErr error) {
	quarantineDir, quarantineRel, err := identityBoundQuarantinePath(root, rel)
	if err != nil {
		return err
	}
	cleanupDir := true
	cleanupQuarantineEntry := false
	var quarantineInfo fs.FileInfo
	defer func() {
		if cleanupQuarantineEntry {
			returnErr = errors.Join(returnErr, retryCleanupAtomicTempFileIfStillMatches(root, quarantineRel, quarantineInfo, message))
		}
		if cleanupDir {
			returnErr = errors.Join(returnErr, ignoreRemoveNotExist(root.Remove(quarantineDir)))
		}
	}()

	if err := root.Rename(rel, quarantineRel); err != nil {
		return err
	}
	quarantineInfo, err = publishedRegularFileInfo(root, quarantineRel, message)
	if err != nil {
		cleanupDir = false
		return err
	}
	cleanupQuarantineEntry = true
	if !os.SameFile(expected, quarantineInfo) {
		cleanupDir, cleanupQuarantineEntry, err = restoreMismatchedCreatedCleanup(root, quarantineRel, rel, message, quarantineInfo)
		return err
	}
	cleanupDir, cleanupQuarantineEntry, err = removeQuarantinedCreatedCleanup(root, quarantineRel, quarantineInfo, message)
	return err
}

func restoreMismatchedCreatedCleanup(root Root, quarantineRel, rel, message string, quarantineInfo fs.FileInfo) (bool, bool, error) {
	restored, restoreErr := restoreQuarantinedPathNoReplace(root, quarantineRel, rel, message, quarantineInfo)
	if !restored {
		return false, false, errors.Join(fmt.Errorf("%s: %s", message, rel), restoreErr)
	}
	if restoreErr == nil {
		return true, false, errors.Join(fmt.Errorf("%s: %s", message, rel), restoreErr)
	}
	return true, true, errors.Join(fmt.Errorf("%s: %s", message, rel), restoreErr)
}

func removeQuarantinedCreatedCleanup(root Root, quarantineRel string, quarantineInfo fs.FileInfo, message string) (bool, bool, error) {
	if err := removeFileIfStillMatches(root, quarantineRel, quarantineInfo, message); err != nil {
		return false, true, err
	}
	return true, false, nil
}

func createAtomicTempFile(root Root, dir string, perm os.FileMode) (string, File, error) {
	tempDir := filepath.Clean(dir)
	if tempDir == "." {
		tempDir = ""
	}

	for range 10 {
		name, err := randomTempNameFn()
		if err != nil {
			return "", nil, err
		}
		tempRel := filepath.Join(tempDir, name)
		file, err := root.OpenFile(tempRel, os.O_RDWR|os.O_CREATE|os.O_EXCL, perm)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", nil, err
		}
		return tempRel, file, nil
	}

	return "", nil, fmt.Errorf("create temp file: too many collisions")
}

func randomTempName() (string, error) {
	var suffix [8]byte
	if _, err := randReadFn(suffix[:]); err != nil {
		return "", fmt.Errorf("generate temp name: %w", err)
	}
	return fmt.Sprintf("%s%x", atomicTempPrefix, suffix), nil
}
