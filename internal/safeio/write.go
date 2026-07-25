package safeio

import (
	"crypto/rand"
	"errors"
	"fmt"
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
	return r.root.Lstat(name)
}

// Chmod updates a root-relative path's permissions within the pinned root.
func (r *WriteRoot) Chmod(name string, perm os.FileMode) error {
	return r.root.Chmod(name, perm)
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

// CreateTempFile creates an exclusive temporary file in the pinned root.
func (r *WriteRoot) CreateTempFile(perm os.FileMode) (string, File, error) {
	return createAtomicTempFile(r.root, ".", perm)
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
	if dirRel == "." {
		return r.root, false, nil
	}

	current := r.root
	currentOwned := false
	currentAbs := r.rootAbs
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
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("output parent contains symlink: %s", path)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("output parent is not a directory: %s", path)
	}

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
	return writeAtomicReplacement(root, target.rel, data, writePerm, nil)
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
