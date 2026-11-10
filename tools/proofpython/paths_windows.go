//go:build windows

package main

import (
	"errors"
	"fmt"
	"golang.org/x/sys/windows"
	"os"
	"path/filepath"
	"strings"
)

func canonicalDirectory(path string) error { return canonicalPath(path, true) }
func canonicalRegular(path string) error   { return canonicalPath(path, false) }

func canonicalPath(path string, directory bool) error {
	if err := validatePathSize(path); err != nil {
		return err
	}
	if err := requireCanonicalDrive(path); err != nil {
		return err
	}
	return walkCanonicalPath(path, directory)
}

func requireCanonicalDrive(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || len(filepath.VolumeName(path)) != 2 {
		return errors.New("expected canonical local drive path")
	}
	return nil
}

func walkCanonicalPath(path string, directory bool) error {
	current := path
	reparseHops := 0
	for {
		attributes, err := fileAttributes(current)
		if err != nil {
			return err
		}
		if attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
			next, err := advanceDirectoryReparse(path, current, directory, &reparseHops)
			if err != nil {
				return err
			}
			current = next
			continue
		}
		if err := requireLeafType(path, current, attributes, directory); err != nil {
			return err
		}
		parent := filepath.Dir(current)
		if current == parent {
			return nil
		}
		current = parent
	}
}

func fileAttributes(path string) (uint32, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	return windows.GetFileAttributes(name)
}

func advanceDirectoryReparse(root, current string, directory bool, hops *int) (string, error) {
	// A file symlink can retarget the interpreter. A directory junction is how
	// the hosted tool cache is mounted; follow it and keep checking the resolved path.
	if current == root && !directory {
		return "", errors.New("reparse path rejected")
	}
	*hops++
	if *hops > 8 {
		return "", errors.New("reparse path rejected")
	}
	resolved, err := resolvedDirectory(current)
	if err != nil || resolved == current {
		return "", errors.New("reparse path rejected")
	}
	return resolved, nil
}

func requireLeafType(root, current string, attributes uint32, directory bool) error {
	if current == root && (attributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0) != directory {
		return errors.New("wrong path type")
	}
	return nil
}

func resolvedDirectory(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	var buffer [maxPathUnits + 1]uint16
	n, err := windows.GetFinalPathNameByHandle(windows.Handle(file.Fd()), &buffer[0], uint32(len(buffer)), 0)
	if err != nil {
		return "", err
	}
	if n == 0 || n >= uint32(len(buffer)) {
		return "", errors.New("truncated final directory path")
	}
	resolved := windows.UTF16ToString(buffer[:n])
	if !strings.HasPrefix(resolved, `\\?\`) {
		return "", errors.New("final directory path is not DOS volume form")
	}
	resolved = filepath.Clean(strings.TrimPrefix(resolved, `\\?\`))
	if !filepath.IsAbs(resolved) || len(filepath.VolumeName(resolved)) != 2 || filepath.VolumeName(resolved) != filepath.VolumeName(path) {
		return "", errors.New("reparse path rejected")
	}
	return resolved, nil
}

func openedPath(file *os.File) (string, error) {
	var buffer [maxPathUnits + 1]uint16
	n, err := windows.GetFinalPathNameByHandle(windows.Handle(file.Fd()), &buffer[0], uint32(len(buffer)), 0)
	if err != nil {
		return "", err
	}
	if n == 0 || n >= uint32(len(buffer)) {
		return "", errors.New("truncated final file path")
	}
	path := windows.UTF16ToString(buffer[:n])
	if !strings.HasPrefix(path, `\\?\`) {
		return "", errors.New("final path is not DOS volume form")
	}
	path = strings.TrimPrefix(path, `\\?\`)
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	if err := canonicalPath(path, info.IsDir()); err != nil {
		return "", err
	}
	return path, nil
}

func verifyOpenedPath(file *os.File, path string) error {
	actual, err := openedPath(file)
	if err != nil {
		return err
	}
	if !strings.EqualFold(actual, path) {
		return errors.New("opened file escaped canonical path")
	}
	before, err := file.Stat()
	if err != nil {
		return err
	}
	current, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !os.SameFile(before, current) {
		return errors.New("opened file identity changed")
	}
	return nil
}

func fileIdentity(file *os.File) (string, error) {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &info); err != nil {
		return "", err
	}
	index := uint64(info.FileIndexHigh)<<32 | uint64(info.FileIndexLow)
	return fmt.Sprintf("%016x:%016x", uint64(info.VolumeSerialNumber), index), nil
}
