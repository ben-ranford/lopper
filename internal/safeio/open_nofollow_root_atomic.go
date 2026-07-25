//go:build darwin || windows

package safeio

import (
	"errors"
	"fmt"
	"os"
)

type atomicRootFileOpener func(root *os.Root, name string) (*os.File, error)

func openRootFileNoFollowAtomic(root *os.Root, name string, openFile atomicRootFileOpener) (*os.File, error) {
	if err := validateOpenNoFollowName(name); err != nil {
		return nil, err
	}

	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, &os.PathError{Op: "lstat", Path: name, Err: ErrNoFollowFinalComponent}
	}
	if !info.Mode().IsRegular() {
		return nil, &os.PathError{Op: "lstat", Path: name, Err: ErrNoFollowFinalComponent}
	}

	if openFileNoFollowByVerificationBeforeOpen != nil {
		openFileNoFollowByVerificationBeforeOpen()
	}

	file, err := openFile(root, name)
	if err != nil {
		return nil, normalizeAtomicNoFollowOpenError(root, name, err)
	}
	openedInfo, err := file.Stat()
	if err != nil {
		return nil, closeOpenedNoFollowFileWithError(file, err)
	}
	if !openedInfo.Mode().IsRegular() {
		return nil, closeOpenedNoFollowFileWithError(file, &os.PathError{Op: "stat", Path: name, Err: ErrNoFollowFinalComponent})
	}
	currentInfo, err := root.Lstat(name)
	if err != nil {
		return nil, closeOpenedNoFollowFileWithError(file, err)
	}
	if currentInfo.Mode()&os.ModeSymlink != 0 {
		return nil, closeOpenedNoFollowFileWithError(file, &os.PathError{Op: "lstat", Path: name, Err: ErrNoFollowFinalComponent})
	}
	if !currentInfo.Mode().IsRegular() {
		return nil, closeOpenedNoFollowFileWithError(file, &os.PathError{Op: "lstat", Path: name, Err: ErrNoFollowFinalComponent})
	}
	if !os.SameFile(info, currentInfo) {
		return nil, closeOpenedNoFollowFileWithError(file, &os.PathError{Op: "open", Path: name, Err: fmt.Errorf("pinned regular file changed while opening")})
	}
	if !os.SameFile(info, openedInfo) {
		return nil, closeOpenedNoFollowFileWithError(file, &os.PathError{Op: "open", Path: name, Err: fmt.Errorf("pinned regular file changed while opening")})
	}
	return file, nil
}

func normalizeAtomicNoFollowOpenError(root *os.Root, path string, err error) error {
	switch {
	case errors.Is(err, ErrNoFollowFinalComponent):
		return err
	case errors.Is(err, os.ErrNotExist):
		return &os.PathError{Op: "openat", Path: path, Err: err}
	case isAtomicNoFollowLeafError(err):
		return &os.PathError{Op: "openat", Path: path, Err: errors.Join(ErrNoFollowFinalComponent, err)}
	}

	info, statErr := root.Lstat(path)
	if statErr == nil && (info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular()) {
		return &os.PathError{Op: "openat", Path: path, Err: errors.Join(ErrNoFollowFinalComponent, err)}
	}
	return &os.PathError{Op: "openat", Path: path, Err: err}
}
