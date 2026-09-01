//go:build darwin || linux

package safeio

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

var (
	descriptorLstatFn            = descriptorLstat
	descriptorStatFn             = descriptorStat
	afterOpenSearchAncestorFn    = func(string) error { return nil }
	openSearchOnlyDirectoryAtFn  = openSearchOnlyDirectoryAt
	searchDirectoryAliasTargetFn = searchDirectoryAliasTarget
	searchDirectoryLstatFn       = os.Lstat
	searchDirectoryStatFn        = os.Stat
	descriptorMkdiratFn          = unix.Mkdirat
	descriptorLinkatFn           = unix.Linkat
	descriptorUnlinkatFn         = unix.Unlinkat
	descriptorFileWriteFn        = func(file *os.File, data []byte) (int, error) { return file.Write(data) }
	descriptorFileChmodFn        = func(file *os.File, perm os.FileMode) error { return file.Chmod(perm) }
	descriptorFileStatFn         = func(file *os.File) (os.FileInfo, error) { return file.Stat() }
	descriptorFileCloseFn        = func(file *os.File) error { return file.Close() }
)

const canonicalPathParentPerm os.FileMode = 0o750

type searchOnlyWriteRoot struct {
	file *os.File
}

// OpenCanonicalSearchOnlyWriteRoot pins a canonical root that may grant search
// permission without read permission. It supports the descriptor fallback
// writers, not general WriteRoot operations.
func OpenCanonicalSearchOnlyWriteRoot(rootDir string) (*WriteRoot, error) {
	rootAbs, err := resolveAbsolutePath("root", rootDir)
	if err != nil {
		return nil, err
	}
	rootAbs = canonicalSearchDirectoryPath(rootAbs)
	file, _, err := openSearchOnlyCanonicalDirectory(rootAbs)
	if err != nil {
		return nil, fmt.Errorf("open search-only canonical root: %w", err)
	}
	return &WriteRoot{root: &searchOnlyWriteRoot{file: file}, rootAbs: rootAbs}, nil
}

func (r *searchOnlyWriteRoot) Open(string) (File, error) {
	return nil, fmt.Errorf("search-only write root only supports descriptor fallback")
}

func (r *searchOnlyWriteRoot) OpenFile(name string, flag int, perm os.FileMode) (File, error) {
	if name != "." {
		return nil, fmt.Errorf("search-only write root only supports root descriptor access")
	}
	fd, err := unix.FcntlInt(r.file.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), r.file.Name()), nil
}

func (r *searchOnlyWriteRoot) OpenRoot(string) (Root, error) {
	return nil, fmt.Errorf("search-only write root only supports descriptor fallback")
}

func (r *searchOnlyWriteRoot) Lstat(name string) (os.FileInfo, error) {
	if name != "." {
		return nil, fmt.Errorf("search-only write root only supports root descriptor stat")
	}
	return r.file.Stat()
}

func (r *searchOnlyWriteRoot) Mkdir(string, os.FileMode) error {
	return fmt.Errorf("search-only write root only supports descriptor fallback")
}

func (r *searchOnlyWriteRoot) Chmod(string, os.FileMode) error {
	return fmt.Errorf("search-only write root only supports descriptor fallback")
}

func (r *searchOnlyWriteRoot) MkdirAll(string, os.FileMode) error {
	return fmt.Errorf("search-only write root only supports descriptor fallback")
}

func (r *searchOnlyWriteRoot) Link(string, string) error {
	return fmt.Errorf("search-only write root only supports descriptor fallback")
}

func (r *searchOnlyWriteRoot) Rename(string, string) error {
	return fmt.Errorf("search-only write root only supports descriptor fallback")
}

func (r *searchOnlyWriteRoot) Remove(string) error {
	return fmt.Errorf("search-only write root only supports descriptor fallback")
}

func (r *searchOnlyWriteRoot) Close() error {
	return r.file.Close()
}

// WriteFileAtomicallyIfAbsentUnderCanonicalPath publishes targetPath only when
// the target is absent. It is a narrow fallback for Unix directories that allow
// search/write but not ordinary read permission, where os.Root cannot pin the
// target parent.
func WriteFileAtomicallyIfAbsentUnderCanonicalPath(targetPath string, data []byte, perm os.FileMode) (returnErr error) {
	targetAbs, err := resolveAbsolutePath("target", targetPath)
	if err != nil {
		return err
	}
	parent := filepath.Dir(targetAbs)

	parentFile, parentFD, err := openOrCreateSearchOnlyCanonicalDirectory(parent, canonicalPathParentPerm)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := parentFile.Close(); closeErr != nil {
			returnErr = errors.Join(returnErr, closeErr)
		}
	}()

	targetRel := filepath.Base(targetAbs)
	if err := rejectExistingDescriptorPath(parentFD, targetRel); err != nil {
		return err
	}
	return writeFileAtomicallyIfAbsentUnderDescriptorPath(parentFD, targetRel, data, perm)
}

// WriteFileAtomicallyReplacingUnderCanonicalPath publishes targetPath under a
// descriptor-pinned canonical parent. Missing targets are created exclusively.
// Existing targets fail closed because portable descriptor APIs cannot replace
// a path only when the destination still matches a previously validated inode.
func WriteFileAtomicallyReplacingUnderCanonicalPath(targetPath string, data []byte, perm os.FileMode) (returnErr error) {
	targetAbs, err := resolveAbsolutePath("target", targetPath)
	if err != nil {
		return err
	}
	parent := filepath.Dir(targetAbs)

	parentFile, parentFD, err := openOrCreateSearchOnlyCanonicalDirectory(parent, canonicalPathParentPerm)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := parentFile.Close(); closeErr != nil {
			returnErr = errors.Join(returnErr, closeErr)
		}
	}()

	targetRel := filepath.Base(targetAbs)
	return writeFileAtomicallyReplacingUnderDescriptorPath(parentFD, targetRel, data, perm)
}

// WriteFileAtomicallyIfAbsentUnderPinnedRoot publishes targetPath only when
// absent, using the already-open root descriptor as the lookup anchor.
func (r *WriteRoot) WriteFileAtomicallyIfAbsentUnderPinnedRoot(targetPath string, data []byte, perm os.FileMode) (returnErr error) {
	target, err := r.resolveTarget(targetPath)
	if err != nil {
		return err
	}
	parentFile, parentFD, targetRel, err := r.openOrCreateSearchOnlyTargetParent(target, canonicalPathParentPerm)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := parentFile.Close(); closeErr != nil {
			returnErr = errors.Join(returnErr, closeErr)
		}
	}()
	if err := rejectExistingDescriptorPath(parentFD, targetRel); err != nil {
		return err
	}
	return writeFileAtomicallyIfAbsentUnderDescriptorPath(parentFD, targetRel, data, perm)
}

// WriteFileAtomicallyReplacingUnderPinnedRoot publishes targetPath under an
// already-open root descriptor. Existing targets fail closed for the same
// descriptor-stability reason as WriteFileAtomicallyReplacingUnderCanonicalPath.
func (r *WriteRoot) WriteFileAtomicallyReplacingUnderPinnedRoot(targetPath string, data []byte, perm os.FileMode) (returnErr error) {
	target, err := r.resolveTarget(targetPath)
	if err != nil {
		return err
	}
	parentFile, parentFD, targetRel, err := r.openOrCreateSearchOnlyTargetParent(target, canonicalPathParentPerm)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := parentFile.Close(); closeErr != nil {
			returnErr = errors.Join(returnErr, closeErr)
		}
	}()
	return writeFileAtomicallyReplacingUnderDescriptorPath(parentFD, targetRel, data, perm)
}

func (r *WriteRoot) openOrCreateSearchOnlyTargetParent(target rootedTarget, perm os.FileMode) (*os.File, int, string, error) {
	rootFile, rootFD, err := r.openSearchOnlyRootDescriptor()
	if err != nil {
		return nil, -1, "", err
	}
	parentRel := filepath.Dir(target.rel)
	if parentRel == "." {
		return rootFile, rootFD, filepath.Base(target.rel), nil
	}
	_, parts := splitPinnedPath(parentRel)
	parentFile, parentFD, err := openSearchOnlyDirectoryParts(rootFile, r.rootAbs, parts, true, perm)
	if err != nil {
		return nil, -1, "", err
	}
	return parentFile, parentFD, filepath.Base(target.rel), nil
}

func (r *WriteRoot) openSearchOnlyRootDescriptor() (*os.File, int, error) {
	file, err := r.root.OpenFile(".", searchOnlyDirectoryOpenFlags(), 0)
	if err != nil {
		return nil, -1, err
	}
	osFile, ok := file.(*os.File)
	if !ok {
		return nil, -1, closeFileWithError(file, fmt.Errorf("pinned root does not expose a file descriptor"))
	}
	return osFile, int(osFile.Fd()), nil
}

func openSearchOnlyCanonicalDirectory(path string) (*os.File, int, error) {
	return openSearchOnlyCanonicalDirectoryWithOptions(path, false, 0)
}

func openOrCreateSearchOnlyCanonicalDirectory(path string, perm os.FileMode) (*os.File, int, error) {
	return openSearchOnlyCanonicalDirectoryWithOptions(path, true, perm)
}

func openSearchOnlyCanonicalDirectoryWithOptions(path string, create bool, perm os.FileMode) (*os.File, int, error) {
	absPath, err := resolveAbsolutePath("directory", path)
	if err != nil {
		return nil, -1, err
	}
	absPath = canonicalSearchDirectoryPath(absPath)
	volumeRoot := filepath.VolumeName(absPath) + string(os.PathSeparator)
	root, err := openSearchOnlyDirectory(volumeRoot)
	if err != nil {
		return nil, -1, err
	}
	rel, err := filepath.Rel(volumeRoot, absPath)
	if err != nil {
		return nil, -1, closeFileWithError(root, err)
	}
	if rel == "." {
		return root, int(root.Fd()), nil
	}
	_, parts := splitPinnedPath(rel)
	return openSearchOnlyDirectoryParts(root, volumeRoot, parts, create, perm)
}

func canonicalSearchDirectoryPath(path string) string {
	cleanPath := filepath.Clean(path)
	volumeRoot := filepath.VolumeName(cleanPath) + string(os.PathSeparator)
	rel, err := filepath.Rel(volumeRoot, cleanPath)
	if err != nil || rel == "." {
		return cleanPath
	}
	parts := strings.Split(rel, string(os.PathSeparator))
	if len(parts) == 0 || parts[0] == "" || parts[0] == "." || parts[0] == ".." {
		return cleanPath
	}
	aliasRoot := filepath.Join(volumeRoot, parts[0])
	targetRoot, ok := searchDirectoryAliasTargetFn(aliasRoot)
	if !ok {
		return cleanPath
	}
	if len(parts) == 1 {
		return targetRoot
	}
	return filepath.Join(targetRoot, filepath.Join(parts[1:]...))
}

func searchDirectoryAliasTarget(path string) (string, bool) {
	if !isSearchDirectoryRootLevelAlias(path) {
		return "", false
	}
	if target, ok := trustedRootAliasTarget(path); ok {
		return target, true
	}
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", false
	}
	return target, true
}

func isSearchDirectoryRootLevelAlias(path string) bool {
	cleanPath := filepath.Clean(path)
	if !filepath.IsAbs(cleanPath) {
		return false
	}
	parent := filepath.Dir(cleanPath)
	if parent == cleanPath || filepath.Dir(parent) != parent {
		return false
	}
	linkInfo, err := searchDirectoryLstatFn(cleanPath)
	if err != nil || linkInfo.Mode()&os.ModeSymlink == 0 {
		return false
	}
	targetInfo, err := searchDirectoryStatFn(cleanPath)
	return err == nil && targetInfo.IsDir()
}

func openSearchOnlyDirectoryParts(current *os.File, currentPath string, parts []string, create bool, perm os.FileMode) (_ *os.File, _ int, returnErr error) {
	for _, part := range parts {
		if err := afterOpenSearchAncestorFn(currentPath); err != nil {
			return nil, -1, closeFileWithError(current, err)
		}
		nextPath := filepath.Join(currentPath, part)
		next, err := openSearchOnlyChildDirectoryWithOptions(int(current.Fd()), part, nextPath, create, perm)
		if err != nil {
			return nil, -1, closeFileWithError(current, err)
		}
		if err := current.Close(); err != nil {
			return nil, -1, closeFileWithError(next, err)
		}
		current = next
		currentPath = nextPath
	}
	return current, int(current.Fd()), nil
}

func openSearchOnlyChildDirectory(parentFD int, name, path string) (*os.File, error) {
	return openSearchOnlyChildDirectoryWithOptions(parentFD, name, path, false, 0)
}

func openSearchOnlyChildDirectoryWithOptions(parentFD int, name, path string, create bool, perm os.FileMode) (*os.File, error) {
	info, err := descriptorLstat(parentFD, name)
	if errors.Is(err, os.ErrNotExist) && create {
		if mkdirErr := descriptorMkdiratFn(parentFD, name, uint32(perm)); mkdirErr != nil && !errors.Is(mkdirErr, os.ErrExist) {
			return nil, mkdirErr
		}
		info, err = descriptorLstat(parentFD, name)
	}
	if err != nil {
		return nil, err
	}
	if descriptorInfoIsSymlink(info) {
		return nil, fmt.Errorf("directory contains symlink: %s", path)
	}
	if !descriptorInfoIsDirectory(info) {
		return nil, fmt.Errorf("directory is not a directory: %s", path)
	}

	file, err := openSearchOnlyDirectoryAtFn(parentFD, name)
	if err != nil {
		return nil, err
	}
	openedInfo, err := descriptorStatFn(int(file.Fd()))
	if err != nil {
		return nil, closeFileWithError(file, err)
	}
	if !descriptorInfoIsDirectory(openedInfo) || !sameDescriptorInfos(info, openedInfo) {
		return nil, closeFileWithError(file, fmt.Errorf("directory changed while opening: %s", path))
	}
	return file, nil
}

func writeFileAtomicallyIfAbsentUnderDescriptorPath(parentFD int, targetRel string, data []byte, perm os.FileMode) (returnErr error) {
	tempRel, tempFile, err := createDescriptorTempFile(parentFD, perm)
	if err != nil {
		return err
	}
	tempCreated := true
	defer func() {
		if tempCreated {
			returnErr = errors.Join(returnErr, cleanupDescriptorTempFile(parentFD, tempRel, tempFile))
		}
	}()

	if _, err := descriptorFileWriteFn(tempFile, data); err != nil {
		return err
	}
	if err := descriptorFileChmodFn(tempFile, perm); err != nil {
		return err
	}
	tempInfo, err := descriptorFileStatFn(tempFile)
	if err != nil {
		return err
	}
	if !tempInfo.Mode().IsRegular() {
		return fmt.Errorf("temporary file is not regular: %s", tempRel)
	}
	if err := descriptorFileCloseFn(tempFile); err != nil {
		return err
	}
	tempFile = nil

	if err := descriptorLinkatFn(parentFD, tempRel, parentFD, targetRel, 0); err != nil {
		if errors.Is(err, os.ErrExist) {
			return os.ErrExist
		}
		return err
	}
	if err := descriptorUnlinkatFn(parentFD, tempRel, 0); err != nil {
		return err
	}
	tempCreated = false

	pathInfo, err := descriptorLstatFn(parentFD, targetRel)
	if err != nil {
		return fmt.Errorf("exclusive-create target changed before validation: %w", err)
	}
	if descriptorInfoIsSymlink(pathInfo) {
		return fmt.Errorf("exclusive-create target became a symlink before validation: %s", targetRel)
	}
	if !descriptorInfoIsRegular(pathInfo) || !sameDescriptorFileInfo(tempInfo, pathInfo) {
		return fmt.Errorf("exclusive-create target changed before validation: %s", targetRel)
	}
	return nil
}

func writeFileAtomicallyReplacingUnderDescriptorPath(parentFD int, targetRel string, data []byte, perm os.FileMode) error {
	targetInfo, targetErr := descriptorLstatFn(parentFD, targetRel)
	switch {
	case errors.Is(targetErr, os.ErrNotExist):
		return writeFileAtomicallyIfAbsentUnderDescriptorPath(parentFD, targetRel, data, perm)
	case targetErr != nil:
		return targetErr
	case descriptorInfoIsSymlink(targetInfo):
		return fmt.Errorf("target path is a symlink: %s", targetRel)
	case !descriptorInfoIsRegular(targetInfo):
		return fmt.Errorf("target path is not a regular file: %s", targetRel)
	}
	return fmt.Errorf("existing target cannot be safely replaced under descriptor fallback: %s", targetRel)
}

func rejectExistingDescriptorPath(parentFD int, targetRel string) error {
	info, err := descriptorLstat(parentFD, targetRel)
	switch {
	case err == nil:
		if descriptorInfoIsSymlink(info) {
			return os.ErrExist
		}
		return os.ErrExist
	case errors.Is(err, os.ErrNotExist):
		return nil
	default:
		return err
	}
}

type descriptorFileInfo struct {
	dev  string
	ino  string
	mode uint32
}

func descriptorLstat(parentFD int, name string) (descriptorFileInfo, error) {
	var stat unix.Stat_t
	if err := unix.Fstatat(parentFD, name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return descriptorFileInfo{}, err
	}
	return descriptorFileInfo{dev: fmt.Sprint(stat.Dev), ino: fmt.Sprint(stat.Ino), mode: uint32(stat.Mode)}, nil
}

func descriptorInfoIsRegular(info descriptorFileInfo) bool {
	return info.mode&unix.S_IFMT == unix.S_IFREG
}

func descriptorInfoIsSymlink(info descriptorFileInfo) bool {
	return info.mode&unix.S_IFMT == unix.S_IFLNK
}

func descriptorInfoIsDirectory(info descriptorFileInfo) bool {
	return info.mode&unix.S_IFMT == unix.S_IFDIR
}

func descriptorStat(fd int) (descriptorFileInfo, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return descriptorFileInfo{}, err
	}
	return descriptorFileInfo{dev: fmt.Sprint(stat.Dev), ino: fmt.Sprint(stat.Ino), mode: uint32(stat.Mode)}, nil
}

func sameDescriptorInfos(left, right descriptorFileInfo) bool {
	return left.dev == right.dev && left.ino == right.ino
}

func sameDescriptorFileInfo(tempInfo os.FileInfo, targetInfo descriptorFileInfo) bool {
	stat, ok := tempInfo.Sys().(*syscall.Stat_t)
	return ok && fmt.Sprint(stat.Dev) == targetInfo.dev && fmt.Sprint(stat.Ino) == targetInfo.ino
}

func createDescriptorTempFile(parentFD int, perm os.FileMode) (string, *os.File, error) {
	for range 10 {
		name, err := randomTempNameFn()
		if err != nil {
			return "", nil, err
		}
		fd, err := unix.Openat(parentFD, name, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, uint32(perm))
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", nil, err
		}
		return name, os.NewFile(uintptr(fd), name), nil
	}
	return "", nil, fmt.Errorf("create temp file: too many collisions")
}

func cleanupDescriptorTempFile(parentFD int, tempName string, tempFile *os.File) error {
	var cleanupErr error
	if tempFile != nil {
		if err := descriptorFileCloseFn(tempFile); err != nil && !errors.Is(err, os.ErrClosed) {
			cleanupErr = err
		}
	}
	if tempName != "" {
		if err := descriptorUnlinkatFn(parentFD, tempName, 0); err != nil && !errors.Is(err, os.ErrNotExist) {
			if cleanupErr == nil {
				return err
			}
			return errors.Join(cleanupErr, err)
		}
	}
	return cleanupErr
}
