//go:build !windows

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// Portable inventory tests exercise real files; Windows provider entry is rejected separately.
func canonicalDirectory(path string) error { return canonicalPath(path, true) }
func canonicalRegular(path string) error   { return canonicalPath(path, false) }
func canonicalPath(path string, directory bool) error {
	if err := validatePathSize(path); err != nil {
		return err
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("path is not canonical absolute")
	}
	for current := path; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("reparse path rejected")
		}
		if current == path && info.IsDir() != directory {
			return errors.New("wrong path type")
		}
		if current == filepath.Dir(current) {
			break
		}
	}
	return nil
}
func verifyOpenedPath(file *os.File, path string) error {
	a, err := file.Stat()
	if err != nil {
		return err
	}
	b, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !os.SameFile(a, b) {
		return errors.New("opened file identity differs")
	}
	return nil
}

func fileIdentity(file *os.File) (string, error) {
	var info syscall.Stat_t
	if err := syscall.Fstat(int(file.Fd()), &info); err != nil {
		return "", err
	}
	if info.Dev < 0 {
		return "", errors.New("negative device identity")
	}
	return fmt.Sprintf("%016x:%016x", uint64(info.Dev), uint64(info.Ino)), nil
}
