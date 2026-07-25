package safeio

import (
	"errors"
	"fmt"
	"io"
	"os"
)

var openFileNoFollowByVerificationBeforeOpen func()

func openFileNoFollowByVerification(root Root, name string) (File, error) {
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
	file, err := root.Open(name)
	if err != nil {
		return nil, err
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

func requireOpenedNoFollowOSFile(file File, platform string) (*os.File, error) {
	osFile, ok := file.(*os.File)
	if !ok {
		return nil, closeOpenedNoFollowFileWithError(file, openFileNoFollowUnsupportedError(platform))
	}
	return osFile, nil
}

func closeOpenedNoFollowFileWithError(file io.Closer, err error) error {
	return errors.Join(err, file.Close())
}
