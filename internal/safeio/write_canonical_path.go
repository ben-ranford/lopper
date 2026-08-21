//go:build darwin || linux

package safeio

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

var descriptorLstatFn = descriptorLstat

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
	if err := verifyCanonicalExistingDirectory(parent); err != nil {
		return err
	}

	parentFile, parentFD, err := openSearchOnlyCanonicalDirectory(parent)
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

func openSearchOnlyCanonicalDirectory(path string) (*os.File, int, error) {
	file, err := openSearchOnlyDirectory(path)
	if err != nil {
		return nil, -1, err
	}
	info, err := file.Stat()
	if err != nil {
		return nil, -1, closeFileWithError(file, err)
	}
	pathInfo, err := os.Lstat(path)
	if err != nil {
		return nil, -1, closeFileWithError(file, err)
	}
	if pathInfo.Mode()&os.ModeSymlink != 0 || !pathInfo.IsDir() || !os.SameFile(info, pathInfo) {
		return nil, -1, closeFileWithError(file, fmt.Errorf("directory changed while opening: %s", path))
	}
	return file, int(file.Fd()), nil
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

func verifyCanonicalExistingDirectory(path string) error {
	absPath, err := resolveAbsolutePath("directory", path)
	if err != nil {
		return err
	}
	ancestors := []string{filepath.Clean(absPath)}
	for {
		parent := filepath.Dir(ancestors[len(ancestors)-1])
		if parent == ancestors[len(ancestors)-1] {
			break
		}
		ancestors = append(ancestors, parent)
	}

	for idx := len(ancestors) - 1; idx >= 0; idx-- {
		info, err := os.Lstat(ancestors[idx])
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			if _, ok := trustedRootAliasTarget(ancestors[idx]); ok {
				continue
			}
			return fmt.Errorf("directory contains symlink: %s", ancestors[idx])
		}
		if !info.IsDir() {
			return fmt.Errorf("directory is not a directory: %s", ancestors[idx])
		}
	}
	return nil
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
