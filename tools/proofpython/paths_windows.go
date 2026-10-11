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
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || len(filepath.VolumeName(path)) != 2 {
		return errors.New("expected canonical local drive path")
	}
	for current := path; ; current = filepath.Dir(current) {
		name, err := windows.UTF16PtrFromString(current)
		if err != nil {
			return err
		}
		attributes, err := windows.GetFileAttributes(name)
		if err != nil {
			return err
		}
		if attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
			return errors.New("reparse path rejected")
		}
		if current == path && (attributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0) != directory {
			return errors.New("wrong path type")
		}
		if current == filepath.Dir(current) {
			break
		}
	}
	return nil
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
