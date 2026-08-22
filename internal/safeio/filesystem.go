package safeio

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
)

// FileSystem captures the filesystem operations safeio needs.
type FileSystem interface {
	Abs(path string) (string, error)
	Rel(basepath, targpath string) (string, error)
	OpenRoot(name string) (Root, error)
	OpenRootNoFollow(name string) (Root, error)
}

// Root is a filesystem root used for path-confined operations.
type Root interface {
	Open(name string) (File, error)
	OpenFile(name string, flag int, perm os.FileMode) (File, error)
	OpenRoot(name string) (Root, error)
	Lstat(name string) (fs.FileInfo, error)
	Mkdir(name string, perm os.FileMode) error
	Chmod(name string, perm os.FileMode) error
	MkdirAll(name string, perm os.FileMode) error
	Link(oldName, newName string) error
	Rename(oldName, newName string) error
	Remove(name string) error
	Close() error
}

// File is the file handle behavior safeio needs for reads and atomic writes.
type File interface {
	io.Reader
	io.Writer
	io.Closer
	Stat() (fs.FileInfo, error)
	Chmod(perm os.FileMode) error
}

type ReadDirFile interface {
	File
	ReadDir(count int) ([]fs.DirEntry, error)
}

var ErrTargetPathSymlink = errors.New("target path is a symlink")

var fileSystem FileSystem = &osFileSystem{}
var runtimeGOOS = runtime.GOOS

// OpenRoot opens a confined filesystem root.
func OpenRoot(name string) (Root, error) {
	return fileSystem.OpenRoot(name)
}

// OpenRootNoFollow opens a confined filesystem root without following symlinks
// in any component of name.
func OpenRootNoFollow(name string) (Root, error) {
	return fileSystem.OpenRootNoFollow(name)
}

// VerifyDirectoryIdentity reports whether path still names the expected
// non-symlink directory.
func VerifyDirectoryIdentity(path string, expected fs.FileInfo) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || !os.SameFile(expected, info) {
		return fmt.Errorf("directory identity changed: %s", path)
	}
	return nil
}

// OpenOrCreatePinnedDirectory opens a direct child directory without following
// symlinks, creating it if absent and verifying that the opened handle still
// identifies the observed directory.
func OpenOrCreatePinnedDirectory(root Root, parentPath, name string, perm os.FileMode) (Root, error) {
	childPath := filepath.Join(parentPath, name)
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		if mkdirErr := root.Mkdir(name, perm); mkdirErr != nil {
			if !errors.Is(mkdirErr, fs.ErrExist) {
				return nil, mkdirErr
			}
		}
		info, err = root.Lstat(name)
	}
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("directory contains symlink: %s", childPath)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("directory is not a directory: %s", childPath)
	}
	next, err := root.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	openedInfo, err := next.Lstat(".")
	if err != nil {
		return nil, errors.Join(err, next.Close())
	}
	if !os.SameFile(info, openedInfo) {
		return nil, errors.Join(fmt.Errorf("directory changed while opening: %s", childPath), next.Close())
	}
	return next, nil
}

// OpenRootExistingAncestorNoFollow opens the deepest existing ancestor for
// name without following untrusted symlinks. It returns the opened ancestor,
// that ancestor's canonical path, and any remaining missing path components.
func OpenRootExistingAncestorNoFollow(name string) (Root, string, []string, error) {
	return openRootExistingAncestorNoFollowWith(name, fileSystem.Abs, filepath.Rel, fileSystem.OpenRoot, func(root Root, childName, requestedPath string) (Root, string, error) {
		return openRootChildPinnedWith(root, childName, requestedPath, fileSystem.OpenRootNoFollow, os.Stat, os.SameFile)
	})
}

type osFileSystem struct{}

func (*osFileSystem) Abs(path string) (string, error) {
	return filepath.Abs(path)
}

func (*osFileSystem) Rel(basepath, targpath string) (string, error) {
	return filepath.Rel(basepath, targpath)
}

func (*osFileSystem) OpenRoot(name string) (Root, error) {
	root, err := os.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	return &osRoot{root: root}, nil
}

func (f *osFileSystem) OpenRootNoFollow(name string) (Root, error) {
	return openRootNoFollowWith(name, f.Abs, filepath.Rel, f.OpenRoot, f.openRootChildPinned)
}

func openRootNoFollowWith(name string, absFn func(string) (string, error), relFn func(string, string) (string, error), openRootFn func(string) (Root, error), openRootChildFn func(Root, string, string) (Root, string, error)) (Root, error) {
	absName, err := absFn(name)
	if err != nil {
		return nil, err
	}

	volumeRoot := filepath.VolumeName(absName) + string(os.PathSeparator)
	rel, err := relFn(volumeRoot, absName)
	if err != nil {
		return nil, err
	}
	root, err := openRootFn(volumeRoot)
	if err != nil {
		return nil, err
	}
	if rel == "." {
		return root, nil
	}

	currentPath := volumeRoot
	for _, part := range strings.Split(rel, string(os.PathSeparator)) {
		if part == "" || part == "." {
			continue
		}
		requestedPath := filepath.Join(currentPath, part)
		next, nextPath, err := openRootChildFn(root, part, requestedPath)
		if err != nil {
			return nil, closeRootWithError(root, err)
		}
		if err := root.Close(); err != nil {
			return nil, closeRootWithError(next, err)
		}
		root = next
		currentPath = nextPath
	}
	return root, nil
}

func (f *osFileSystem) openRootChildPinned(root Root, name, requestedPath string) (Root, string, error) {
	return openRootChildPinnedWith(root, name, requestedPath, f.OpenRootNoFollow, os.Stat, os.SameFile)
}

func openRootChildPinnedWith(root Root, name, requestedPath string, openRootNoFollowFn func(string) (Root, error), statFn func(string) (fs.FileInfo, error), sameFileFn func(fs.FileInfo, fs.FileInfo) bool) (Root, string, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return nil, "", err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		next, err := openRootChildNoFollow(root, name, requestedPath)
		return next, requestedPath, err
	}

	targetPath, ok := trustedRootAliasTarget(requestedPath)
	if !ok {
		return nil, "", fmt.Errorf("root contains symlink: %s", requestedPath)
	}

	return openPinnedRootAliasWith(targetPath, requestedPath, openRootNoFollowFn, statFn, sameFileFn)
}

func openPinnedRootAliasWith(targetPath string, requestedPath string, openRootNoFollowFn func(string) (Root, error), statFn func(string) (fs.FileInfo, error), sameFileFn func(fs.FileInfo, fs.FileInfo) bool) (Root, string, error) {
	next, err := openRootNoFollowFn(targetPath)
	if err != nil {
		return nil, "", err
	}
	targetInfo, err := statFn(targetPath)
	if err != nil {
		return nil, "", closeRootWithError(next, err)
	}
	openedInfo, err := next.Lstat(".")
	if err != nil {
		return nil, "", closeRootWithError(next, err)
	}
	if !sameFileFn(targetInfo, openedInfo) {
		return nil, "", closeRootWithError(next, fmt.Errorf("root changed while opening: %s", requestedPath))
	}
	return next, targetPath, nil
}

func trustedRootAliasTarget(requestedPath string) (string, bool) {
	if runtimeGOOS != "darwin" {
		return "", false
	}

	switch filepath.Clean(requestedPath) {
	case filepath.Join(string(os.PathSeparator), "tmp"):
		return filepath.Join(string(os.PathSeparator), "private", "tmp"), true
	case filepath.Join(string(os.PathSeparator), "var"):
		return filepath.Join(string(os.PathSeparator), "private", "var"), true
	default:
		return "", false
	}
}

func openValidatedChildRoot(root Root, name, path string, infoFn func() (fs.FileInfo, error), symlinkMessage, notDirMessage, changedMessage string) (Root, error) {
	info, err := infoFn()
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%s: %s", symlinkMessage, path)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s: %s", notDirMessage, path)
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
		return nil, closeRootWithError(next, fmt.Errorf("%s: %s", changedMessage, path))
	}
	return next, nil
}

func openRootChildNoFollow(root Root, name, path string) (Root, error) {
	return openValidatedChildRoot(root, name, path, func() (fs.FileInfo, error) { return root.Lstat(name) }, "root contains symlink", "root is not a directory", "root changed while opening")
}

func openRootExistingAncestorNoFollowWith(name string, absFn func(string) (string, error), relFn func(string, string) (string, error), openRootFn func(string) (Root, error), openRootChildFn func(Root, string, string) (Root, string, error)) (Root, string, []string, error) {
	absName, err := absFn(name)
	if err != nil {
		return nil, "", nil, err
	}

	volumeRoot := filepath.VolumeName(absName) + string(os.PathSeparator)
	rel, err := relFn(volumeRoot, absName)
	if err != nil {
		return nil, "", nil, err
	}
	root, err := openRootFn(volumeRoot)
	if err != nil {
		return nil, "", nil, err
	}
	if rel == "." {
		return root, volumeRoot, nil, nil
	}
	_, parts := splitPinnedPath(rel)
	return openExistingRootAncestors(root, volumeRoot, parts, openRootChildFn)
}

func openExistingRootAncestors(root Root, currentPath string, parts []string, openRootChildFn func(Root, string, string) (Root, string, error)) (Root, string, []string, error) {
	for idx, part := range parts {
		requestedPath := filepath.Join(currentPath, part)
		exists, err := rootChildExists(root, part)
		if err != nil {
			return nil, "", nil, closeRootWithError(root, err)
		}
		if !exists {
			return root, currentPath, parts[idx:], nil
		}
		next, nextPath, err := openRootChildFn(root, part, requestedPath)
		if err != nil {
			return nil, "", nil, closeRootWithError(root, err)
		}
		if err := root.Close(); err != nil {
			return nil, "", nil, closeRootWithError(next, err)
		}
		root = next
		currentPath = nextPath
	}
	return root, currentPath, nil, nil
}

func rootChildExists(root Root, name string) (bool, error) {
	_, err := root.Lstat(name)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func closeRootWithError(root Root, err error) error {
	if closeErr := root.Close(); closeErr != nil {
		return errors.Join(err, closeErr)
	}
	return err
}

type osRoot struct {
	root *os.Root
}

func OpenPinnedFile(root Root, name string) (_ File, err error) {
	file, roots, err := openPinnedPath(root, name, pinnedChildExpectFile)
	if err != nil {
		return nil, err
	}
	if len(roots) == 0 {
		return file, nil
	}
	return &pinnedFile{File: file, roots: roots}, nil
}

func OpenPinnedDirectory(root Root, name string) (_ ReadDirFile, err error) {
	file, roots, err := openPinnedPath(root, name, pinnedChildExpectDirectory)
	if err != nil {
		return nil, err
	}
	dir, ok := file.(ReadDirFile)
	if ok {
		if len(roots) == 0 {
			return dir, nil
		}
		return &pinnedReadDirFile{ReadDirFile: dir, roots: roots}, nil
	}
	if len(roots) > 0 {
		err = closeRootsWithError(roots, fs.ErrInvalid)
	} else {
		err = fs.ErrInvalid
	}
	if closeErr := file.Close(); closeErr != nil {
		return nil, errors.Join(err, closeErr)
	}
	return nil, err
}

type pinnedFile struct {
	File
	roots []Root
}

func (f *pinnedFile) Close() error {
	return closeRootsWithError(f.roots, f.File.Close())
}

type pinnedReadDirFile struct {
	ReadDirFile
	roots []Root
}

func (f *pinnedReadDirFile) Close() error {
	return closeRootsWithError(f.roots, f.ReadDirFile.Close())
}

func openPinnedPath(root Root, name string, kind pinnedChildKind) (_ File, _ []Root, err error) {
	cleanName, parts := splitPinnedPath(name)
	if len(parts) <= 1 {
		file, err := openPinnedChildAtPath(root, cleanName, cleanName, kind)
		return file, nil, err
	}

	roots, leafRoot, leafName, leafPath, err := openPinnedAncestors(root, parts)
	if err != nil {
		return nil, nil, err
	}
	file, err := openPinnedChildAtPath(leafRoot, leafName, leafPath, kind)
	if err != nil {
		return nil, nil, closeRootsWithError(roots, err)
	}
	return file, roots, nil
}

func splitPinnedPath(name string) (string, []string) {
	cleanName := filepath.Clean(name)
	if cleanName == "." {
		return cleanName, nil
	}

	rawParts := strings.Split(cleanName, string(os.PathSeparator))
	parts := make([]string, 0, len(rawParts))
	for _, part := range rawParts {
		if part == "" || part == "." {
			continue
		}
		parts = append(parts, part)
	}
	return cleanName, parts
}

func openPinnedAncestors(root Root, parts []string) (_ []Root, _ Root, _ string, _ string, err error) {
	opened := make([]Root, 0, len(parts)-1)
	current := root
	currentPath := ""
	for _, part := range parts[:len(parts)-1] {
		currentPath = filepath.Join(currentPath, part)
		next, openErr := openRootChildNoFollow(current, part, currentPath)
		if openErr != nil {
			return nil, nil, "", "", closeRootsWithError(opened, openErr)
		}
		opened = append(opened, next)
		current = next
	}
	return opened, current, parts[len(parts)-1], filepath.Join(parts...), nil
}

func closeRootsWithError(roots []Root, err error) error {
	for idx := len(roots) - 1; idx >= 0; idx-- {
		err = errors.Join(err, roots[idx].Close())
	}
	return err
}

func openPinnedChildAtPath(root Root, name, path string, kind pinnedChildKind) (_ File, err error) {
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, &targetPathSymlinkError{path: path}
	}
	if kind == pinnedChildExpectFile && info.IsDir() {
		return nil, fs.ErrInvalid
	}
	if kind == pinnedChildExpectDirectory && !info.IsDir() {
		return nil, fs.ErrInvalid
	}

	file, err := root.Open(name)
	if err != nil {
		return nil, normalizePathEscapesRootError(path, err)
	}

	openedInfo, err := file.Stat()
	if err != nil {
		return nil, closeFileWithError(file, err)
	}
	if kind == pinnedChildExpectFile && openedInfo.IsDir() {
		return nil, closeFileWithError(file, fs.ErrInvalid)
	}
	if kind == pinnedChildExpectDirectory && !openedInfo.IsDir() {
		return nil, closeFileWithError(file, fs.ErrInvalid)
	}
	if !os.SameFile(info, openedInfo) {
		return nil, closeFileWithError(file, fmt.Errorf("path changed while opening: %s", path))
	}
	return file, nil
}

type pinnedChildKind int

const (
	pinnedChildExpectFile pinnedChildKind = iota
	pinnedChildExpectDirectory
)

type targetPathSymlinkError struct {
	path string
}

func (e *targetPathSymlinkError) Error() string {
	return fmt.Sprintf("%s: %s", ErrTargetPathSymlink, e.path)
}

func (*targetPathSymlinkError) Unwrap() error {
	return ErrTargetPathSymlink
}

func (*targetPathSymlinkError) Is(target error) bool {
	return target == syscall.ELOOP
}

func closeFileWithError(file File, err error) error {
	if closeErr := file.Close(); closeErr != nil {
		return errors.Join(err, closeErr)
	}
	return err
}

func (r *osRoot) Open(name string) (File, error) {
	return r.root.Open(name)
}

func (r *osRoot) OpenFile(name string, flag int, perm os.FileMode) (File, error) {
	return r.root.OpenFile(name, flag, perm)
}

func (r *osRoot) OpenRoot(name string) (Root, error) {
	root, err := r.root.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	return &osRoot{root: root}, nil
}

func (r *osRoot) Lstat(name string) (fs.FileInfo, error) {
	return r.root.Lstat(name)
}

func (r *osRoot) Mkdir(name string, perm os.FileMode) error {
	return r.root.Mkdir(name, perm)
}

func (r *osRoot) Chmod(name string, perm os.FileMode) error {
	return r.root.Chmod(name, perm)
}

func (r *osRoot) MkdirAll(name string, perm os.FileMode) error {
	return r.root.MkdirAll(name, perm)
}

func (r *osRoot) Link(oldName, newName string) error {
	return r.root.Link(oldName, newName)
}

func ignoreRemoveNotExist(err error) error {
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (r *osRoot) LinkIfMatches(oldName, newName string, expected fs.FileInfo, message string) (returnErr error) {
	return linkFileIfMatchesUsingBasicRoot(r, oldName, newName, expected, message)
}

func (r *osRoot) Rename(oldName, newName string) error {
	return r.root.Rename(oldName, newName)
}

func (r *osRoot) RenameIfMatches(oldName, newName string, expected fs.FileInfo, message string) error {
	_, err := r.RenameIfMatchesState(oldName, newName, expected, message)
	return err
}

// RenameIfMatchesState renames only the inode validated after it has been
// quarantined under a private staging directory. The result reports whether
// oldName was consumed; a false result is a same-inode rename no-op.
func (r *osRoot) RenameIfMatchesState(oldName, newName string, expected fs.FileInfo, message string) (_ bool, returnErr error) {
	return renameFileIfMatchesUsingBasicRoot(r, oldName, newName, expected, message)
}

func (r *osRoot) Remove(name string) error {
	return r.root.Remove(name)
}

func (r *osRoot) RemoveIfMatches(name string, expected fs.FileInfo, message string) (returnErr error) {
	return removeFileIfMatchesUsingBasicRoot(r, name, expected, message)
}

func identityBoundQuarantinePath(root Root, sourceRel string) (string, string, error) {
	dir := filepath.Dir(sourceRel)
	if dir == "." {
		dir = ""
	}
	for range 10 {
		name, err := randomTempNameFn()
		if err != nil {
			return "", "", err
		}
		quarantineDir := filepath.Join(dir, name)
		if err := root.Mkdir(quarantineDir, 0o700); errors.Is(err, os.ErrExist) {
			continue
		} else if err != nil {
			return "", "", err
		}
		return quarantineDir, filepath.Join(quarantineDir, "entry"), nil
	}
	return "", "", fmt.Errorf("create identity-bound quarantine: too many collisions")
}

func restoreQuarantinedPathNoReplace(root Root, stagedRel, originalRel, message string, expected fs.FileInfo) (bool, error) {
	if err := root.Link(stagedRel, originalRel); err != nil {
		if !identityBoundLinkUnsupported(err) {
			return false, errors.Join(fmt.Errorf("%s: %s", message, originalRel), err)
		}
		if _, statErr := root.Lstat(originalRel); statErr == nil {
			return false, errors.Join(fmt.Errorf("%s: %s", message, originalRel), os.ErrExist)
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return false, errors.Join(fmt.Errorf("%s: %s", message, originalRel), statErr)
		}
		if err := root.Rename(stagedRel, originalRel); err != nil {
			return false, errors.Join(fmt.Errorf("%s: %s", message, originalRel), err)
		}
		restoredInfo, err := publishedRegularFileInfo(root, originalRel, message)
		if err != nil {
			return true, err
		}
		if !sameRegularFile(expected, restoredInfo) {
			return true, fmt.Errorf("%s: %s", message, originalRel)
		}
		return true, nil
	}
	if err := removeFileIfMatchesUsingBasicRoot(root, stagedRel, expected, message); err != nil {
		return false, err
	}
	return true, nil
}

func (r *osRoot) Close() error {
	return r.root.Close()
}
