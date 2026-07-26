package safeio

import (
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

type canonicalTargetKind int

const (
	canonicalTargetDirectory canonicalTargetKind = iota
	canonicalTargetFile
)

// WriteRoot pins a filesystem root for path-confined atomic writes.
type WriteRoot struct {
	root    Root
	rootAbs string
}

// CanonicalPath reports the canonical filesystem path pinned by this write root.
func (r *WriteRoot) CanonicalPath() string {
	return r.rootAbs
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

// OpenExistingCanonicalWriteRoot opens the deepest existing ancestor needed to
// create or update targetPath without following symlinks during ancestor
// discovery. It returns the pinned ancestor root and the remaining relative
// suffix under that root.
func OpenExistingCanonicalWriteRoot(targetPath string, isDir bool) (*WriteRoot, string, error) {
	kind := canonicalTargetFile
	if isDir {
		kind = canonicalTargetDirectory
	}
	return openExistingCanonicalWriteRoot(targetPath, kind)
}

func openWriteRoot(rootAbs string) (*WriteRoot, error) {
	root, err := fileSystem.OpenRoot(rootAbs)
	if err != nil {
		return nil, fmt.Errorf("open root: %w", err)
	}
	return &WriteRoot{root: root, rootAbs: rootAbs}, nil
}

// OpenRelativeWriteRoot opens a child write root from an already-open parent.
// Existing path components must be real directories; missing components are
// created only when create is true.
func OpenRelativeWriteRoot(parent Root, targetPath string, create bool, perm os.FileMode) (*WriteRoot, error) {
	if parent == nil {
		return nil, errors.New("parent root is required")
	}
	targetRel, err := resolveRelativeTarget(targetPath, allowRootTarget)
	if err != nil {
		return nil, err
	}
	current, currentOwned, currentPath, err := openRelativeWriteRootDescendants(parent, targetRel, create, perm)
	if err != nil {
		return nil, err
	}
	if !currentOwned {
		next, err := current.OpenRoot(".")
		if err != nil {
			return nil, err
		}
		current = next
	}
	return &WriteRoot{root: current, rootAbs: currentPath}, nil
}

func openRelativeWriteRootDescendants(parent Root, targetRel string, create bool, perm os.FileMode) (Root, bool, string, error) {
	current := parent
	currentOwned := false
	currentPath := "."
	for _, part := range strings.Split(targetRel, string(os.PathSeparator)) {
		if part == "" || part == "." {
			continue
		}
		partPath := filepath.Join(currentPath, part)
		next, err := openTargetParentChild(current, part, partPath, create, perm)
		if err != nil {
			return nil, false, "", closeOwnedRootWithError(current, currentOwned, err)
		}
		if currentOwned {
			if err := current.Close(); err != nil {
				return nil, false, "", closeRootWithError(next, err)
			}
		}
		current = next
		currentOwned = true
		currentPath = partPath
	}
	return current, currentOwned, currentPath, nil
}

// Close releases the pinned filesystem root.
func (r *WriteRoot) Close() error {
	return r.root.Close()
}

// Lstat reports metadata for a root-relative path under the pinned root.
func (r *WriteRoot) Lstat(targetPath string) (fs.FileInfo, error) {
	targetRel, err := resolveRelativeTarget(targetPath, allowRootTarget)
	if err != nil {
		return nil, err
	}
	return r.root.Lstat(targetRel)
}

// ReadFile reads a root-relative file under the pinned root.
func (r *WriteRoot) ReadFile(targetPath string) ([]byte, error) {
	return ReadFileWithinRoot(r.root, targetPath)
}

// EnsureDir creates targetPath as a directory under the pinned root, creating
// missing parent directories without following symlinks.
func (r *WriteRoot) EnsureDir(targetPath string, perm os.FileMode) (returnErr error) {
	target, err := r.resolveTarget(targetPath)
	if err != nil {
		return err
	}
	parent, closeParent, err := r.openTargetParent(target, true, perm)
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
	name := filepath.Base(target.rel)
	child, err := openTargetParentChild(parent, name, target.abs, true, perm)
	if err != nil {
		return err
	}
	return child.Close()
}

// WriteFileCreatingParents atomically writes a root-relative file, creating
// missing parent directories inside the pinned root.
func (r *WriteRoot) WriteFileCreatingParents(targetPath string, data []byte, perm, parentPerm os.FileMode) error {
	target, err := r.resolveTarget(targetPath)
	if err != nil {
		return err
	}
	return r.writeFileAtTarget(target, data, perm, true, parentPerm, false)
}

// WriteFileReplacingParents atomically writes a root-relative file, creating
// missing parent directories inside the pinned root. Existing regular targets
// retain their permission bits, and Windows keeps the established fallback
// behavior for replace-existing failures on the final file.
func (r *WriteRoot) WriteFileReplacingParents(targetPath string, data []byte, perm, parentPerm os.FileMode) error {
	target, err := r.resolveTarget(targetPath)
	if err != nil {
		return err
	}
	return r.writeFileAtTarget(target, data, perm, true, parentPerm, true)
}

// WriteFileExclusiveCreatingParents writes a new root-relative file and fails
// if the target already exists.
func (r *WriteRoot) WriteFileExclusiveCreatingParents(targetPath string, data []byte, perm, parentPerm os.FileMode) (returnErr error) {
	target, err := r.resolveTarget(targetPath)
	if err != nil {
		return err
	}
	parent, closeParent, err := r.openTargetParent(target, true, parentPerm)
	if err != nil {
		return err
	}
	if closeParent {
		defer func() {
			returnErr = errors.Join(returnErr, parent.Close())
		}()
	}

	targetName := filepath.Base(target.rel)
	file, err := parent.OpenFile(targetName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	removeOnError := true
	writeSucceeded := false
	defer func() {
		closeErr := file.Close()
		if closeErr == nil && writeSucceeded {
			removeOnError = false
		} else {
			returnErr = errors.Join(returnErr, closeErr)
		}
		if removeOnError {
			if removeErr := parent.Remove(targetName); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				returnErr = errors.Join(returnErr, removeErr)
			}
		}
	}()
	if _, err := io.Copy(file, bytes.NewReader(data)); err != nil {
		return err
	}
	writeSucceeded = true
	return nil
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

func openExistingCanonicalWriteRoot(targetPath string, kind canonicalTargetKind) (_ *WriteRoot, rel string, err error) {
	targetAbs, err := resolveAbsolutePath("target", targetPath)
	if err != nil {
		return nil, "", err
	}
	searchAbs, targetRelParts := canonicalWriteTargetPlan(targetAbs, kind)

	volumeRoot := filepath.VolumeName(searchAbs) + string(os.PathSeparator)
	root, err := fileSystem.OpenRootNoFollow(volumeRoot)
	if err != nil {
		return nil, "", fmt.Errorf("open canonical root: %w", err)
	}

	cursor := canonicalWriteRootCursor{root: root, rootAbs: volumeRoot, owned: true}
	defer func() {
		if err == nil {
			return
		}
		if closeErr := cursor.closeIfOwned(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()

	relToSearch, err := filepath.Rel(volumeRoot, searchAbs)
	if err != nil {
		return nil, "", err
	}
	if relToSearch == "." {
		return cursor.writeRoot(), joinCanonicalRelative(targetRelParts), nil
	}
	targetRelParts, err = cursor.descendSearchPath(strings.Split(relToSearch, string(os.PathSeparator)), targetRelParts)
	if err != nil {
		return nil, "", err
	}
	return cursor.writeRoot(), joinCanonicalRelative(targetRelParts), nil
}

func canonicalWriteTargetPlan(targetAbs string, kind canonicalTargetKind) (string, []string) {
	searchAbs := filepath.Clean(targetAbs)
	targetRelParts := make([]string, 0, 8)
	if kind == canonicalTargetFile {
		targetRelParts = append(targetRelParts, filepath.Base(searchAbs))
		searchAbs = filepath.Dir(searchAbs)
	}
	return searchAbs, targetRelParts
}

func joinCanonicalRelative(parts []string) string {
	if len(parts) == 0 {
		return "."
	}
	return filepath.Join(parts...)
}

func closeOwnedRoot(root Root, owned bool) error {
	if !owned {
		return nil
	}
	return root.Close()
}

type canonicalWriteRootCursor struct {
	root    Root
	rootAbs string
	owned   bool
}

func (c *canonicalWriteRootCursor) writeRoot() *WriteRoot {
	return &WriteRoot{root: c.root, rootAbs: c.rootAbs}
}

func (c *canonicalWriteRootCursor) closeIfOwned() error {
	return closeOwnedRoot(c.root, c.owned)
}

func (c *canonicalWriteRootCursor) descendSearchPath(searchParts, targetRelParts []string) ([]string, error) {
	for i, part := range searchParts {
		if part == "" || part == "." {
			continue
		}

		partAbs := filepath.Join(c.rootAbs, part)
		next, err := openTargetParentChild(c.root, part, partAbs, false, 0)
		if os.IsNotExist(err) {
			return append(searchParts[i:], targetRelParts...), nil
		}
		if err != nil {
			return nil, err
		}
		if err := c.replace(next, partAbs); err != nil {
			return nil, err
		}
	}
	return targetRelParts, nil
}

func (c *canonicalWriteRootCursor) replace(next Root, nextAbs string) error {
	if c.owned {
		if closeErr := c.root.Close(); closeErr != nil {
			if nextCloseErr := next.Close(); nextCloseErr != nil {
				return errors.Join(closeErr, nextCloseErr)
			}
			return closeErr
		}
	}

	c.root = next
	c.rootAbs = nextAbs
	c.owned = true
	return nil
}

func (r *WriteRoot) writeFileAtTarget(target rootedTarget, data []byte, perm os.FileMode, createParents bool, parentPerm os.FileMode, replacing bool) (returnErr error) {
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
	parentTargetRel := filepath.Base(target.rel)
	if replacing {
		return WriteFileReplacingWithinRoot(parent, parentTargetRel, data, perm)
	}
	parentTarget := target
	parentTarget.rel = parentTargetRel
	return writeFileAtRoot(parent, parentTarget, data, perm)
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
	parentInfo, err := root.Lstat(".")
	if err != nil {
		return nil, err
	}
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
	if !enforceSameDeviceBoundary(root, next, parentInfo, openedInfo) {
		return nil, closeRootWithError(next, fmt.Errorf("output parent crosses device boundary: %s", path))
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
	return root.writeFileAtTarget(target, data, perm, false, 0, false)
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
