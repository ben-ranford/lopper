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
	return r.writeFileCreatingParentsWithOptions(targetPath, data, perm, parentPerm, writeToTargetParentOptions{
		write: writeFileAtRootWithChecks,
	})
}

// WriteFileCreatingParentsAfterParentReady atomically writes a root-relative
// file after parentReady validates state with the target parent pinned.
func (r *WriteRoot) WriteFileCreatingParentsAfterParentReady(targetPath string, data []byte, perm, parentPerm os.FileMode, parentReady func() error) error {
	return r.writeFileCreatingParentsAfterParentReadyWithOptions(targetPath, data, perm, parentPerm, parentReady, writeToTargetParentOptions{
		write: writeFileAtRootWithChecks,
	})
}

// WriteFileCreatingParentsAfterParentReadyWithPreWriteCheck atomically writes a
// root-relative file after parentReady validates state with the target parent
// pinned, then runs preWrite immediately before the file mutation begins.
func (r *WriteRoot) WriteFileCreatingParentsAfterParentReadyWithPreWriteCheck(targetPath string, data []byte, perm, parentPerm os.FileMode, parentReady, preWrite func() error) error {
	options := checkedWriteToTargetParentOptions()
	options.preWrite = preWrite
	options.postWrite = preWrite
	return r.writeFileCreatingParentsAfterParentReadyWithOptions(targetPath, data, perm, parentPerm, parentReady, options)
}

// WriteFileCreatingParentsAfterParentReadyWithPublishCheck atomically writes a
// root-relative file after parentReady validates state with the target parent
// pinned. It runs publishCheck immediately before publishing the target and
// again after the target has been committed.
func (r *WriteRoot) WriteFileCreatingParentsAfterParentReadyWithPublishCheck(targetPath string, data []byte, perm, parentPerm os.FileMode, parentReady, publishCheck func() error) error {
	options := checkedWriteToTargetParentOptions()
	options.commitReady = publishCheck
	options.postWrite = publishCheck
	return r.writeFileCreatingParentsAfterParentReadyWithOptions(targetPath, data, perm, parentPerm, parentReady, options)
}

// WriteFileCreatingParentsAfterParentReadyWithPinnedParentPublishCheck
// atomically writes a root-relative file after the target parent is pinned. It
// passes the actual pinned parent identity to publishCheck before publishing and
// again after the target has been committed.
func (r *WriteRoot) WriteFileCreatingParentsAfterParentReadyWithPinnedParentPublishCheck(targetPath string, data []byte, perm, parentPerm os.FileMode, publishCheck func(parentPath string, parentIdentity fs.FileInfo) error) error {
	options := checkedWriteToTargetParentOptions()
	options.publishParent = publishCheck
	return r.writeFileCreatingParentsWithOptions(targetPath, data, perm, parentPerm, options)
}

func checkedWriteToTargetParentOptions() writeToTargetParentOptions {
	return writeToTargetParentOptions{write: writeFileAtRootWithChecks}
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
	return r.writeFileCreatingParentsWithOptions(targetPath, data, perm, parentPerm, writeToTargetParentOptions{
		write: writeFileIfAbsentAtRootWithChecks,
	})
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

// Remove deletes a root-relative path inside the pinned root.
func (r *WriteRoot) Remove(targetPath string) error {
	target, err := r.resolveTarget(targetPath)
	if err != nil {
		return err
	}
	return r.root.Remove(target.rel)
}

// Lstat returns identity information for a root-relative path.
func (r *WriteRoot) Lstat(targetPath string) (fs.FileInfo, error) {
	target, err := r.resolveTarget(targetPath)
	if err != nil {
		return nil, err
	}
	return r.root.Lstat(target.rel)
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
	return r.writeFileToTargetParent(target, data, perm, writeToTargetParentOptions{
		createParents: createParents,
		parentPerm:    parentPerm,
		write:         writeFileAtRootWithChecks,
	})
}

func (r *WriteRoot) writeFileCreatingParentsWithOptions(targetPath string, data []byte, perm, parentPerm os.FileMode, options writeToTargetParentOptions) error {
	target, err := r.resolveTarget(targetPath)
	if err != nil {
		return err
	}
	options.createParents = true
	options.parentPerm = parentPerm
	return r.writeFileToTargetParent(target, data, perm, options)
}

func (r *WriteRoot) writeFileCreatingParentsAfterParentReadyWithOptions(targetPath string, data []byte, perm, parentPerm os.FileMode, parentReady func() error, options writeToTargetParentOptions) error {
	options.parentReady = parentReady
	return r.writeFileCreatingParentsWithOptions(targetPath, data, perm, parentPerm, options)
}

type writeAtRootFunc func(root Root, target rootedTarget, data []byte, perm os.FileMode, options writeFileAtRootOptions) error

type writeFileAtRootOptions struct {
	allowPermissionFallback    bool
	commitReady                func() error
	postWrite                  func() error
	commitRename               atomicRenameFunc
	rollbackOnPostWriteFailure bool
}

type writeToTargetParentOptions struct {
	createParents              bool
	parentPerm                 os.FileMode
	parentReady                func() error
	preWrite                   func() error
	commitReady                func() error
	postWrite                  func() error
	publishParent              func(parentPath string, parentIdentity fs.FileInfo) error
	commitRename               atomicRenameFunc
	rollbackOnPostWriteFailure bool
	write                      writeAtRootFunc
}

func (r *WriteRoot) writeFileToTargetParent(target rootedTarget, data []byte, perm os.FileMode, options writeToTargetParentOptions) (returnErr error) {
	return r.withTargetParent(target, options.createParents, options.parentPerm, func(parent Root, parentTarget rootedTarget) error {
		if options.parentReady != nil {
			if err := options.parentReady(); err != nil {
				return err
			}
		}
		if options.preWrite != nil {
			if err := writeFilePreWriteReadyFn(); err != nil {
				return err
			}
			if err := options.preWrite(); err != nil {
				return err
			}
		}
		commitReady := options.commitReady
		postWrite := options.postWrite
		if options.publishParent != nil {
			parentIdentity, err := parent.Lstat(".")
			if err != nil {
				return err
			}
			parentPath := filepath.Dir(target.abs)
			parentCheck := func() error {
				if err := VerifyDirectoryIdentity(parentPath, parentIdentity); err != nil {
					return err
				}
				return options.publishParent(parentPath, parentIdentity)
			}
			commitReady = nil
			postWrite = parentCheck
			options.rollbackOnPostWriteFailure = true
			options.commitRename = func(oldName, newName string) error {
				if err := writeFileRenameReadyFn(); err != nil {
					return err
				}
				if err := parentCheck(); err != nil {
					return err
				}
				return renameAtPinnedDirectory(parent, parentIdentity, oldName, newName)
			}
			if err := parentCheck(); err != nil {
				return err
			}
		}
		return options.write(parent, parentTarget, data, perm, writeFileAtRootOptions{
			commitReady:                commitReady,
			postWrite:                  postWrite,
			commitRename:               options.commitRename,
			rollbackOnPostWriteFailure: options.rollbackOnPostWriteFailure,
		})
	})
}

func renameAtPinnedDirectory(parent Root, parentIdentity fs.FileInfo, oldName, newName string) error {
	if filepath.Dir(oldName) != "." || filepath.Dir(newName) != "." {
		return fmt.Errorf("rename paths must be direct children of pinned parent")
	}
	actual, err := parent.Lstat(".")
	if err != nil {
		return err
	}
	if !actual.IsDir() || !os.SameFile(parentIdentity, actual) {
		return fmt.Errorf("pinned parent identity changed")
	}
	return parent.Rename(oldName, newName)
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
	randomTempNameFn         = randomTempName
	randReadFn               = rand.Read
	writeFileParentReadyFn   = func() error { return nil }
	writeFilePreWriteReadyFn = func() error { return nil }
	writeFilePublishReadyFn  = func() error { return nil }
	writeFileRenameReadyFn   = func() error { return nil }
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
	return writeFileAtRootWithChecks(root, target, data, perm, writeFileAtRootOptions{})
}

func writeFileAtRootWithPostWriteCheck(root Root, target rootedTarget, data []byte, perm os.FileMode, postWrite func() error) error {
	return writeFileAtRootWithChecks(root, target, data, perm, writeFileAtRootOptions{postWrite: postWrite})
}

func writeFileAtRootWithChecks(root Root, target rootedTarget, data []byte, perm os.FileMode, options writeFileAtRootOptions) error {
	return writeFileAtRootWithOptions(root, target, data, perm, options)
}

func writeFileAtRootWithPermissionFallback(root Root, target rootedTarget, data []byte, perm os.FileMode) error {
	return writeFileAtRootWithOptions(root, target, data, perm, writeFileAtRootOptions{allowPermissionFallback: true})
}

func writeFileAtRootWithOptions(root Root, target rootedTarget, data []byte, perm os.FileMode, options writeFileAtRootOptions) (returnErr error) {
	writePerm, existingInfo, err := resolvedWriteFilePerm(root, target, perm)
	if err != nil {
		return err
	}
	if existingInfo == nil {
		return writeAtomicReplacementWithChecks(root, target.rel, data, writePerm, atomicReplacementOptions{
			commitReady:                options.commitReady,
			postWrite:                  options.postWrite,
			commitRename:               options.commitRename,
			rollbackOnPostWriteFailure: options.rollbackOnPostWriteFailure,
		})
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

	return writeAtomicReplacementWithPinnedTargetCallbacks(root, target.rel, data, writePerm, file, options.allowPermissionFallback, pinnedReplacementChecks{
		commitReady:                options.commitReady,
		postWrite:                  options.postWrite,
		commitRename:               options.commitRename,
		rollbackOnPostWriteFailure: options.rollbackOnPostWriteFailure,
	})
}

func writeFileIfAbsentAtRoot(root Root, target rootedTarget, data []byte, perm os.FileMode) (returnErr error) {
	return writeFileIfAbsentAtRootWithPostWriteCheck(root, target, data, perm, nil)
}

func writeFileIfAbsentAtRootWithChecks(root Root, target rootedTarget, data []byte, perm os.FileMode, options writeFileAtRootOptions) error {
	return writeFileIfAbsentAtRootWithPostWriteCheck(root, target, data, perm, options.postWrite)
}

func writeFileIfAbsentAtRootWithPostWriteCheck(root Root, target rootedTarget, data []byte, perm os.FileMode, postWrite func() error) (returnErr error) {
	if _, err := root.Lstat(target.rel); err == nil {
		return os.ErrExist
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := createFileExclusivelyAtRoot(root, target.rel, data, perm); err != nil {
		return err
	}
	if postWrite != nil {
		return postWrite()
	}
	return nil
}

func createFileExclusivelyAtRoot(root Root, targetRel string, data []byte, perm os.FileMode) (returnErr error) {
	file, err := root.OpenFile(targetRel, os.O_RDWR|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return os.ErrExist
		}
		return err
	}

	targetCreated := true
	defer func() {
		if targetCreated {
			returnErr = errors.Join(returnErr, cleanupAtomicTempFile(root, targetRel, file))
		}
	}()

	openedInfo, err := file.Stat()
	if err != nil {
		return err
	}
	if !openedInfo.Mode().IsRegular() {
		return fmt.Errorf("exclusive-create target is not a regular file: %s", targetRel)
	}
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Chmod(perm); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	file = nil
	pathInfo, err := root.Lstat(targetRel)
	if err != nil {
		targetCreated = false
		return fmt.Errorf("exclusive-create target changed before validation: %w", err)
	}
	if pathInfo.Mode()&os.ModeSymlink != 0 {
		targetCreated = false
		return fmt.Errorf("exclusive-create target became a symlink before validation: %s", targetRel)
	}
	if !pathInfo.Mode().IsRegular() || !os.SameFile(openedInfo, pathInfo) {
		targetCreated = false
		return fmt.Errorf("exclusive-create target changed before validation: %s", targetRel)
	}
	targetCreated = false
	return nil
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
