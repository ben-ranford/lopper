package safeio

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// WriteRoot pins a filesystem root for path-confined atomic writes.
type WriteRoot struct {
	root    Root
	rootAbs string
}

// ErrFileChanged indicates that a checked path resolved to a different file
// by the time it was opened.
var ErrFileChanged = errors.New("file changed while opening")

// ErrDirectoryLockUnsupported indicates that the platform cannot lock a
// pinned directory for cross-process serialization.
var ErrDirectoryLockUnsupported = errors.New("directory locking unsupported")

// ErrRenameNoReplaceUnsupported indicates that the platform cannot atomically
// rename a file while preserving an existing destination.
var ErrRenameNoReplaceUnsupported = errors.New("atomic no-replace rename unsupported")

// ErrPrivateFilePermissionsUnsupported indicates that the platform cannot
// create or prove an owner-only file through a pinned handle.
var ErrPrivateFilePermissionsUnsupported = errors.New("owner-only file permissions unsupported")

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
	if err := rejectUnsupportedWindowsRoot(rootDir); err != nil {
		return nil, err
	}
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

// Lstat returns info for a root-relative path within the pinned root.
func (r *WriteRoot) Lstat(name string) (fs.FileInfo, error) {
	targetRel, err := resolveRelativeTarget(name, allowRootTarget)
	if err != nil {
		return nil, err
	}
	return r.root.Lstat(targetRel)
}

// Chmod updates a root-relative path's permissions within the pinned root.
func (r *WriteRoot) Chmod(name string, perm os.FileMode) error {
	targetRel, err := resolveRelativeTarget(name, allowRootTarget)
	if err != nil {
		return err
	}
	return r.root.Chmod(targetRel, perm)
}

// WriteFileCreatingParents atomically writes a root-relative file, creating
// missing parent directories inside the pinned root.
func (r *WriteRoot) WriteFileCreatingParents(targetPath string, data []byte, perm, parentPerm os.FileMode) error {
	target, err := r.resolveTarget(targetPath)
	if err != nil {
		return err
	}
	return r.writeFileAtTarget(target, data, perm, true, parentPerm)
}

// MkdirAll creates a root-relative directory tree without following symlinks.
func (r *WriteRoot) MkdirAll(dirPath string, perm os.FileMode) error {
	dirRel, err := resolveRelativeTarget(dirPath, allowRootTarget)
	if err != nil {
		return err
	}
	dir, closeDir, err := r.openDirectory(dirRel, true, perm)
	if err != nil {
		return err
	}
	if closeDir {
		return dir.Close()
	}
	return nil
}

// MkdirAllDurable creates a root-relative directory tree without following
// symlinks and syncs each parent directory for every newly created level.
func (r *WriteRoot) MkdirAllDurable(dirPath string, perm os.FileMode) error {
	dirRel, err := resolveRelativeTarget(dirPath, allowRootTarget)
	if err != nil {
		return err
	}
	return mkdirAllDurable(r.root, r.rootAbs, dirRel, perm)
}

// ReadRegularFile reads a root-relative regular file without following a
// final-component symlink and verifies that the opened file is the one checked.
func (r *WriteRoot) ReadRegularFile(targetPath string) (data []byte, info fs.FileInfo, returnErr error) {
	return r.ReadRegularFileUnderLimit(targetPath, 0)
}

// ReadRegularFileUnderLimit reads a root-relative regular file without
// following a final-component symlink, verifies the opened file is the one
// checked, and does not exceed maxBytes when a positive limit is provided.
func (r *WriteRoot) ReadRegularFileUnderLimit(targetPath string, maxBytes int64) (data []byte, info fs.FileInfo, returnErr error) {
	return r.ReadPinnedRegularFileUnderLimit(targetPath, nil, maxBytes)
}

// ReadRegularFilePrivateToOwnerUnderLimit reads a root-relative regular file
// without following a final-component symlink, verifies the opened file is the
// one checked, validates owner-only permissions on the same opened file handle,
// and does not exceed maxBytes when a positive limit is provided.
func (r *WriteRoot) ReadRegularFilePrivateToOwnerUnderLimit(targetPath string, maxBytes int64) (data []byte, info fs.FileInfo, private bool, returnErr error) {
	targetRel, err := resolveRelativeTarget(targetPath, rejectRootTarget)
	if err != nil {
		return nil, nil, false, err
	}
	targetAbs := filepath.Join(r.rootAbs, targetRel)
	info, err = r.root.Lstat(targetRel)
	if err != nil {
		return nil, nil, false, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, nil, false, fmt.Errorf("target path is a symlink: %s", targetAbs)
	}
	if !info.Mode().IsRegular() {
		return nil, nil, false, fmt.Errorf("target path is not a regular file: %s", targetAbs)
	}

	file, err := r.root.Open(targetRel)
	if err != nil {
		return nil, nil, false, err
	}
	defer func() {
		returnErr = errors.Join(returnErr, file.Close())
	}()
	openedInfo, err := file.Stat()
	if err != nil {
		return nil, nil, false, err
	}
	if !os.SameFile(info, openedInfo) {
		return nil, nil, false, fmt.Errorf("%w: %s", ErrFileChanged, targetAbs)
	}
	privateSnapshot, private, err := capturePrivateFileAccessSnapshot(file, openedInfo)
	if err != nil {
		return nil, nil, false, err
	}
	data, err = readOpenedFile(file, maxBytes)
	if err != nil {
		return nil, nil, false, err
	}
	postReadInfo, err := file.Stat()
	if err != nil {
		return nil, nil, false, err
	}
	if !sameFileSnapshot(openedInfo, postReadInfo) {
		return nil, nil, false, fmt.Errorf("%w: %s", ErrFileChanged, targetAbs)
	}
	postReadSnapshot, postReadPrivate, err := capturePrivateFileAccessSnapshot(file, postReadInfo)
	if err != nil {
		return nil, nil, false, err
	}
	if private != postReadPrivate || !samePrivateFileAccessSnapshot(privateSnapshot, postReadSnapshot) {
		return nil, nil, false, fmt.Errorf("%w: %s", ErrFileChanged, targetAbs)
	}
	return data, postReadInfo, postReadPrivate, nil
}

func sameFileSnapshot(before, after fs.FileInfo) bool {
	return os.SameFile(before, after) &&
		before.Mode() == after.Mode() &&
		before.Size() == after.Size() &&
		before.ModTime().Equal(after.ModTime())
}

// ReadPinnedRegularFileUnderLimit additionally verifies that the pinned root
// still refers to the same directory inode before opening the target file.
func (r *WriteRoot) ReadPinnedRegularFileUnderLimit(targetPath string, pinnedRootInfo fs.FileInfo, maxBytes int64) (data []byte, info fs.FileInfo, returnErr error) {
	targetRel, err := resolveRelativeTarget(targetPath, rejectRootTarget)
	if err != nil {
		return nil, nil, err
	}
	targetAbs := filepath.Join(r.rootAbs, targetRel)
	if pinnedRootInfo != nil {
		currentRootInfo, err := r.root.Lstat(".")
		if err != nil {
			return nil, nil, err
		}
		if !os.SameFile(pinnedRootInfo, currentRootInfo) {
			return nil, nil, fmt.Errorf("%w: %s", ErrFileChanged, r.rootAbs)
		}
	}
	info, err = r.root.Lstat(targetRel)
	if err != nil {
		return nil, nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, nil, fmt.Errorf("target path is a symlink: %s", targetAbs)
	}
	if !info.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("target path is not a regular file: %s", targetAbs)
	}

	file, err := r.root.Open(targetRel)
	if err != nil {
		return nil, nil, err
	}
	defer func() {
		returnErr = errors.Join(returnErr, file.Close())
	}()
	openedInfo, err := file.Stat()
	if err != nil {
		return nil, nil, err
	}
	if !os.SameFile(info, openedInfo) {
		return nil, nil, fmt.Errorf("%w: %s", ErrFileChanged, targetAbs)
	}
	data, err = readOpenedFile(file, maxBytes)
	if err != nil {
		return nil, nil, err
	}
	return data, openedInfo, nil
}

// RegularFilePrivateToOwner verifies that targetPath still names expectedInfo
// and that its platform permissions grant access only to its owner.
func (r *WriteRoot) RegularFilePrivateToOwner(targetPath string, expectedInfo fs.FileInfo) (private bool, returnErr error) {
	targetRel, err := resolveRelativeTarget(targetPath, rejectRootTarget)
	if err != nil {
		return false, err
	}
	targetAbs := filepath.Join(r.rootAbs, targetRel)
	info, err := r.root.Lstat(targetRel)
	if err != nil {
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return false, fmt.Errorf("target path is a symlink: %s", targetAbs)
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("target path is not a regular file: %s", targetAbs)
	}
	if expectedInfo != nil && !os.SameFile(expectedInfo, info) {
		return false, fmt.Errorf("%w: %s", ErrFileChanged, targetAbs)
	}

	file, err := r.root.Open(targetRel)
	if err != nil {
		return false, err
	}
	defer func() {
		returnErr = errors.Join(returnErr, file.Close())
	}()
	openedInfo, err := file.Stat()
	if err != nil {
		return false, err
	}
	if !os.SameFile(info, openedInfo) {
		return false, fmt.Errorf("%w: %s", ErrFileChanged, targetAbs)
	}
	return filePrivateToOwner(file, openedInfo)
}

// CreateTempFile creates an exclusive temporary file in the pinned root.
func (r *WriteRoot) CreateTempFile(perm os.FileMode) (string, File, error) {
	return createAtomicTempFile(r.root, ".", perm)
}

// CreatePrivateTempFile creates an exclusive owner-only temporary file in the
// pinned root without exposing file contents under broader permissions.
func (r *WriteRoot) CreatePrivateTempFile() (string, File, error) {
	return createPrivateAtomicTempFile(r.root, ".")
}

// CleanupTempFile closes and removes a temporary file in the pinned root.
func (r *WriteRoot) CleanupTempFile(tempPath string, tempFile File) error {
	return cleanupAtomicTempFile(r.root, tempPath, tempFile)
}

// Link creates a hard link between two root-relative paths.
func (r *WriteRoot) Link(oldPath, newPath string) error {
	oldRel, err := resolveRelativeTarget(oldPath, rejectRootTarget)
	if err != nil {
		return err
	}
	newRel, err := resolveRelativeTarget(newPath, rejectRootTarget)
	if err != nil {
		return err
	}
	return r.root.Link(oldRel, newRel)
}

// Rename atomically renames one root-relative path to another.
func (r *WriteRoot) Rename(oldPath, newPath string) error {
	oldRel, err := resolveRelativeTarget(oldPath, rejectRootTarget)
	if err != nil {
		return err
	}
	newRel, err := resolveRelativeTarget(newPath, rejectRootTarget)
	if err != nil {
		return err
	}
	return r.root.Rename(oldRel, newRel)
}

// RenameNoReplace atomically renames one direct child of the pinned root to
// another, returning an existence error without changing either file when the
// destination already exists.
func (r *WriteRoot) RenameNoReplace(oldPath, newPath string) (returnErr error) {
	oldRel, err := resolveDirectRootChild(oldPath)
	if err != nil {
		return err
	}
	newRel, err := resolveDirectRootChild(newPath)
	if err != nil {
		return err
	}
	directory, err := r.openPinnedRootDirectory()
	if err != nil {
		return err
	}
	defer func() {
		returnErr = errors.Join(returnErr, directory.Close())
	}()
	fd, err := descriptorForFile(directory, ErrRenameNoReplaceUnsupported)
	if err != nil {
		return err
	}
	if err := renameNoReplaceInDirectory(fd, oldRel, newRel); err != nil {
		return &os.LinkError{Op: "rename-no-replace", Old: oldRel, New: newRel, Err: err}
	}
	return nil
}

// LockDirectory acquires an exclusive advisory lock on the pinned root
// directory. The kernel releases the lock if the process exits.
func (r *WriteRoot) LockDirectory() (io.Closer, error) {
	directory, err := r.openPinnedRootDirectory()
	if err != nil {
		return nil, err
	}
	fd, err := descriptorForFile(directory, ErrDirectoryLockUnsupported)
	if err != nil {
		return nil, closeFilePreservingPrimary(directory, err)
	}
	if err := lockDirectoryDescriptor(fd); err != nil {
		return nil, closeFilePreservingPrimary(directory, err)
	}
	return &pinnedDirectoryLock{directory: directory, fd: fd}, nil
}

// Remove removes a root-relative path.
func (r *WriteRoot) Remove(targetPath string) error {
	targetRel, err := resolveRelativeTarget(targetPath, rejectRootTarget)
	if err != nil {
		return err
	}
	return r.root.Remove(targetRel)
}

// Sync flushes directory-entry changes for the pinned root.
func (r *WriteRoot) Sync() (returnErr error) {
	return syncRootDirectory(r.root)
}

type pinnedDirectoryLock struct {
	directory File
	fd        uintptr
}

type descriptorFile interface {
	Fd() uintptr
	Close() error
}

func (l *pinnedDirectoryLock) Close() error {
	if l == nil || l.directory == nil {
		return nil
	}
	directory := l.directory
	l.directory = nil
	return errors.Join(unlockDirectoryDescriptor(l.fd), directory.Close())
}

func (r *WriteRoot) openPinnedRootDirectory() (File, error) {
	expected, err := r.root.Lstat(".")
	if err != nil {
		return nil, err
	}
	if !expected.IsDir() {
		return nil, fmt.Errorf("pinned root is not a directory")
	}
	directory, err := r.root.Open(".")
	if err != nil {
		return nil, err
	}
	opened, err := directory.Stat()
	if err != nil {
		return nil, closeFilePreservingPrimary(directory, err)
	}
	if !opened.IsDir() || !os.SameFile(expected, opened) {
		return nil, closeFilePreservingPrimary(directory, fmt.Errorf("%w: pinned root directory", ErrFileChanged))
	}
	return directory, nil
}

func descriptorForFile(file File, unsupportedErr error) (uintptr, error) {
	descriptor, ok := file.(descriptorFile)
	if !ok {
		return 0, unsupportedErr
	}
	fd := descriptor.Fd()
	const maxSignedInt32 = uintptr(^uint32(0) >> 1)
	if fd > maxSignedInt32 {
		return 0, fmt.Errorf("%w: file descriptor out of range", unsupportedErr)
	}
	return fd, nil
}

func resolveDirectRootChild(path string) (string, error) {
	rel, err := resolveRelativeTarget(path, rejectRootTarget)
	if err != nil {
		return "", err
	}
	if filepath.Dir(rel) != "." {
		return "", fmt.Errorf("path must name a direct child of the pinned root: %s", path)
	}
	if strings.IndexByte(rel, 0) >= 0 {
		return "", fmt.Errorf("path contains NUL byte")
	}
	return rel, nil
}

func (r *WriteRoot) resolveTarget(targetPath string) (rootedTarget, error) {
	rel, err := resolveRelativeTarget(targetPath, rejectRootTarget)
	if err != nil {
		return rootedTarget{}, err
	}
	return rootedTarget{rootAbs: r.rootAbs, rel: rel, abs: filepath.Join(r.rootAbs, rel)}, nil
}

func (r *WriteRoot) writeFileAtTarget(target rootedTarget, data []byte, perm os.FileMode, createParents bool, parentPerm os.FileMode) (returnErr error) {
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
	return writeFileAtRoot(parent, parentTarget, data, perm)
}

func (r *WriteRoot) openTargetParent(target rootedTarget, create bool, perm os.FileMode) (Root, bool, error) {
	parentRel := filepath.Dir(target.rel)
	return r.openDirectory(parentRel, create, perm)
}

func (r *WriteRoot) openDirectory(dirRel string, create bool, perm os.FileMode) (Root, bool, error) {
	return openDirectoryWithinRoot(r.root, r.rootAbs, dirRel, create, perm)
}

func openDirectoryWithinRoot(root Root, rootAbs, dirRel string, create bool, perm os.FileMode) (Root, bool, error) {
	if dirRel == "." {
		return root, false, nil
	}

	current := root
	currentOwned := false
	currentAbs := rootAbs
	for _, part := range strings.Split(dirRel, string(os.PathSeparator)) {
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
	info, err := lstatOrCreateDirectory(root, name, create, perm)
	if err != nil {
		return nil, err
	}
	return openInspectedDirectory(root, name, path, info)
}

func openInspectedDirectory(root Root, name, path string, info fs.FileInfo) (Root, error) {
	if err := validateInspectedDirectory(path, info); err != nil {
		return nil, err
	}
	return openVerifiedDirectory(root, name, path, info)
}

func validateInspectedDirectory(path string, info fs.FileInfo) error {
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("output parent contains symlink: %s", path)
	}
	if !info.IsDir() {
		return fmt.Errorf("output parent is not a directory: %s", path)
	}
	return nil
}

func openVerifiedDirectory(root Root, name, path string, info fs.FileInfo) (Root, error) {
	next, err := root.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	openedInfo, err := next.Lstat(".")
	if err != nil {
		return nil, closeRootWithError(next, err)
	}
	if !os.SameFile(info, openedInfo) {
		return nil, closeRootWithError(next, fmt.Errorf("output parent changed while opening: %s", path))
	}
	return next, nil
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

func mkdirAllDurable(root Root, rootAbs, dirRel string, perm os.FileMode) (returnErr error) {
	if dirRel == "." {
		return nil
	}

	current := root
	currentOwned := false
	currentAbs := rootAbs
	for _, part := range strings.Split(dirRel, string(os.PathSeparator)) {
		if part == "" || part == "." {
			continue
		}

		partAbs := filepath.Join(currentAbs, part)
		next, err := openDurableDirectoryChild(current, part, partAbs, perm)
		if err != nil {
			return closeOwnedRootWithError(current, currentOwned, err)
		}
		if currentOwned {
			if err := current.Close(); err != nil {
				return closeRootWithError(next, err)
			}
		}
		current = next
		currentOwned = true
		currentAbs = partAbs
	}

	if currentOwned {
		return current.Close()
	}
	return nil
}

func openDurableDirectoryChild(root Root, name, path string, perm os.FileMode) (Root, error) {
	info, syncParent, err := lstatOrCreateDirectoryTracked(root, name, perm)
	if err != nil {
		return nil, err
	}
	if err := validateInspectedDirectory(path, info); err != nil {
		return nil, err
	}
	if syncParent {
		if err := syncRootDirectory(root); err != nil {
			return nil, err
		}
	}
	return openVerifiedDirectory(root, name, path, info)
}

func lstatOrCreateDirectoryTracked(root Root, name string, perm os.FileMode) (fs.FileInfo, bool, error) {
	info, err := root.Lstat(name)
	if !os.IsNotExist(err) {
		return info, false, err
	}
	if mkdirErr := root.Mkdir(name, perm); mkdirErr != nil {
		if !errors.Is(mkdirErr, fs.ErrExist) {
			return nil, false, mkdirErr
		}
		info, err = root.Lstat(name)
		return info, true, err
	}
	info, err = root.Lstat(name)
	return info, true, err
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

func writeFileAtRoot(root Root, target rootedTarget, data []byte, perm os.FileMode) (returnErr error) {
	writePerm, existingInfo, err := resolvedWriteFilePerm(root, target, perm)
	if err != nil {
		return err
	}
	if existingInfo != nil {
		file, err := root.OpenFile(target.rel, os.O_WRONLY, 0)
		if err != nil {
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}
	}
	return writeAtomicReplacement(root, target, data, writePerm, existingInfo, atomicReplacementOptions{
		allowInPlaceFallback: true,
	})
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
	return writeAtomicReplacement(root, rootedTarget{rel: targetRel}, data, perm, nil, atomicReplacementOptions{
		allowInPlaceFallback: true,
	})
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
	return writeAtomicReplacement(root, target, data, writePerm, existingInfo, atomicReplacementOptions{
		allowInPlaceFallback: true,
	})
}

// WriteFileReplacing atomically writes targetPath using an already-open confined
// write root. Existing regular targets retain their permission bits. On
// Windows only, writes may fall back to in-place overwrite when the filesystem
// rejects replace-existing rename semantics for an existing file.
func (r *WriteRoot) WriteFileReplacing(targetPath string, data []byte, perm os.FileMode) error {
	targetRel, err := resolveRelativeTarget(targetPath, rejectRootTarget)
	if err != nil {
		return err
	}
	target := rootedTarget{
		rootAbs: r.rootAbs,
		rel:     targetRel,
		abs:     filepath.Join(r.rootAbs, targetRel),
	}
	writePerm, existingInfo, err := resolvedWriteFilePerm(r.root, target, perm)
	if err != nil {
		return err
	}
	return writeAtomicReplacement(r.root, target, data, writePerm, existingInfo, atomicReplacementOptions{
		allowInPlaceFallback: true,
	})
}

// WriteFileReplacingWithExactPermissions atomically writes targetPath using an
// already-open confined write root and applies perm even when replacing an
// existing regular file. On Windows only, writes may fall back to a pinned
// in-place overwrite when replace-existing rename semantics are unavailable.
func (r *WriteRoot) WriteFileReplacingWithExactPermissions(targetPath string, data []byte, perm os.FileMode) error {
	return r.writeFileReplacingWithExactPermissions(targetPath, data, perm, true)
}

// WriteFileReplacingAtomicallyWithExactPermissions replaces targetPath with a
// complete, synced temporary file and applies perm to the replacement. It never
// falls back to mutating an existing target in place when rename cannot commit.
func (r *WriteRoot) WriteFileReplacingAtomicallyWithExactPermissions(targetPath string, data []byte, perm os.FileMode) error {
	return r.writeFileReplacingWithExactPermissions(targetPath, data, perm, false)
}

// WritePrivateFileReplacingAtomically replaces targetPath with a complete,
// synced owner-only temporary file and never falls back to in-place mutation.
func (r *WriteRoot) WritePrivateFileReplacingAtomically(targetPath string, data []byte) error {
	targetRel, err := resolveRelativeTarget(targetPath, rejectRootTarget)
	if err != nil {
		return err
	}
	target := rootedTarget{
		rootAbs: r.rootAbs,
		rel:     targetRel,
		abs:     filepath.Join(r.rootAbs, targetRel),
	}
	_, existingInfo, err := resolvedWriteFilePerm(r.root, target, 0o600)
	if err != nil {
		return err
	}
	return writeAtomicReplacement(r.root, target, data, 0o600, existingInfo, atomicReplacementOptions{
		forceReplacementPerm: true,
		privateTemp:          true,
	})
}

func (r *WriteRoot) writeFileReplacingWithExactPermissions(targetPath string, data []byte, perm os.FileMode, allowInPlaceFallback bool) error {
	targetRel, err := resolveRelativeTarget(targetPath, rejectRootTarget)
	if err != nil {
		return err
	}
	target := rootedTarget{
		rootAbs: r.rootAbs,
		rel:     targetRel,
		abs:     filepath.Join(r.rootAbs, targetRel),
	}
	_, existingInfo, err := resolvedWriteFilePerm(r.root, target, perm)
	if err != nil {
		return err
	}
	return writeAtomicReplacement(r.root, target, data, perm, existingInfo, atomicReplacementOptions{
		forceReplacementPerm: true,
		allowInPlaceFallback: allowInPlaceFallback,
	})
}

func cleanupAtomicTempFile(root Root, tempRel string, tempFile File) error {
	closeErr := closeAtomicTempFile(tempFile)
	removeErr := removeAtomicTempFile(root, tempRel)
	if closeErr == nil {
		return removeErr
	}
	if removeErr == nil {
		return closeErr
	}
	return errors.Join(closeErr, removeErr)
}

func closeAtomicTempFile(tempFile File) error {
	if tempFile == nil {
		return nil
	}
	err := tempFile.Close()
	if errors.Is(err, os.ErrClosed) {
		return nil
	}
	return err
}

func removeAtomicTempFile(root Root, tempRel string) error {
	if tempRel == "" {
		return nil
	}
	resolvedTempRel, err := resolveRelativeTarget(tempRel, rejectRootTarget)
	if err != nil {
		return err
	}
	err = root.Remove(resolvedTempRel)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func createAtomicTempFile(root Root, dir string, perm os.FileMode) (string, File, error) {
	tempDir, err := resolveRelativeTarget(dir, allowRootTarget)
	if err != nil {
		return "", nil, err
	}
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
