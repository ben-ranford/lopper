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
	descriptorLstatFn           = descriptorLstat
	descriptorStatFn            = descriptorStat
	afterOpenSearchAncestorFn   = func(string) error { return nil }
	openSearchOnlyDirectoryAtFn = openSearchOnlyDirectoryAt
)

const canonicalPathParentPerm os.FileMode = 0o750

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
// descriptor-pinned canonical parent. Missing targets are created exclusively;
// existing targets are replaced only when they are regular files.
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
	separator := string(os.PathSeparator)
	for _, alias := range []string{filepath.Join(separator, "tmp"), filepath.Join(separator, "var")} {
		target, ok := trustedRootAliasTarget(alias)
		if !ok {
			continue
		}
		if cleanPath == alias {
			return target
		}
		if strings.HasPrefix(cleanPath, alias+separator) {
			return filepath.Join(target, strings.TrimPrefix(cleanPath, alias+separator))
		}
	}
	return cleanPath
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
		if mkdirErr := unix.Mkdirat(parentFD, name, uint32(perm)); mkdirErr != nil && !errors.Is(mkdirErr, os.ErrExist) {
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

	if _, err := tempFile.Write(data); err != nil {
		return err
	}
	if err := tempFile.Chmod(perm); err != nil {
		return err
	}
	tempInfo, err := tempFile.Stat()
	if err != nil {
		return err
	}
	if !tempInfo.Mode().IsRegular() {
		return fmt.Errorf("temporary file is not regular: %s", tempRel)
	}
	if err := tempFile.Close(); err != nil {
		return err
	}
	tempFile = nil

	if err := unix.Linkat(parentFD, tempRel, parentFD, targetRel, 0); err != nil {
		if errors.Is(err, os.ErrExist) {
			return os.ErrExist
		}
		return err
	}
	if err := unix.Unlinkat(parentFD, tempRel, 0); err != nil {
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

func writeFileAtomicallyReplacingUnderDescriptorPath(parentFD int, targetRel string, data []byte, perm os.FileMode) (returnErr error) {
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

	targetFile, err := openDescriptorReplacementTarget(parentFD, targetRel, targetInfo)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := targetFile.Close(); closeErr != nil {
			returnErr = errors.Join(returnErr, closeErr)
		}
	}()

	writePerm := descriptorInfoPerm(targetInfo)
	tempRel, tempFile, err := createDescriptorTempFile(parentFD, writePerm)
	if err != nil {
		return err
	}
	tempCreated := true
	defer func() {
		if tempCreated {
			returnErr = errors.Join(returnErr, cleanupDescriptorTempFile(parentFD, tempRel, tempFile))
		}
	}()

	if _, err := tempFile.Write(data); err != nil {
		return err
	}
	if err := tempFile.Chmod(writePerm); err != nil {
		return err
	}
	tempInfo, err := tempFile.Stat()
	if err != nil {
		return err
	}
	if !tempInfo.Mode().IsRegular() {
		return fmt.Errorf("temporary file is not regular: %s", tempRel)
	}
	if err := tempFile.Close(); err != nil {
		return err
	}
	tempFile = nil

	if err := unix.Renameat(parentFD, tempRel, parentFD, targetRel); err != nil {
		return err
	}
	tempCreated = false

	pathInfo, err := descriptorLstatFn(parentFD, targetRel)
	if err != nil {
		return fmt.Errorf("replacement target changed before validation: %w", err)
	}
	if descriptorInfoIsSymlink(pathInfo) {
		return fmt.Errorf("replacement target became a symlink before validation: %s", targetRel)
	}
	if !descriptorInfoIsRegular(pathInfo) || !sameDescriptorFileInfo(tempInfo, pathInfo) {
		return fmt.Errorf("replacement target changed before validation: %s", targetRel)
	}
	return nil
}

func openDescriptorReplacementTarget(parentFD int, targetRel string, expectedInfo descriptorFileInfo) (*os.File, error) {
	fd, err := unix.Openat(parentFD, targetRel, unix.O_WRONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), targetRel)
	openedInfo, err := descriptorStatFn(fd)
	if err != nil {
		return nil, closeFileWithError(file, err)
	}
	if !descriptorInfoIsRegular(openedInfo) || !sameDescriptorInfos(expectedInfo, openedInfo) {
		err := fmt.Errorf("target changed while opening for replacement: %s", targetRel)
		return nil, closeFileWithError(file, err)
	}
	return file, nil
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

func descriptorInfoPerm(info descriptorFileInfo) os.FileMode {
	return os.FileMode(info.mode).Perm()
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
		if err := tempFile.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			cleanupErr = err
		}
	}
	if tempName != "" {
		if err := unix.Unlinkat(parentFD, tempName, 0); err != nil && !errors.Is(err, os.ErrNotExist) {
			if cleanupErr == nil {
				return err
			}
			return errors.Join(cleanupErr, err)
		}
	}
	return cleanupErr
}
