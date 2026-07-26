//go:build windows

package safeio

import (
	"errors"
	"io/fs"
	"syscall"
)

type windowsHandleFileIdentity struct {
	volumeSerial uint32
	fileIndexHi  uint32
	fileIndexLo  uint32
}

type fdFile interface {
	File
	Fd() uintptr
}

var windowsHandleFileIdentityFn = windowsHandleFileIdentityFromFD

// Windows builds enforce same-volume traversal using
// GetFileInformationByHandle volume identity from pinned root handles.
// Build-tag contract: callers may rely on stable device identity being
// supported when this file is selected.
func sameDeviceIdentitySupported() bool {
	return true
}

func sameDeviceRootPair(parent Root, child Root, _, _ fs.FileInfo) bool {
	parentIdentity, err := windowsRootIdentity(parent)
	if err != nil {
		return false
	}
	childIdentity, err := windowsRootIdentity(child)
	if err != nil {
		return false
	}
	return parentIdentity.volumeSerial == childIdentity.volumeSerial
}

func windowsRootIdentity(root Root) (windowsHandleFileIdentity, error) {
	if root == nil {
		return windowsHandleFileIdentity{}, errors.New("root is required")
	}
	file, err := root.Open(".")
	if err != nil {
		return windowsHandleFileIdentity{}, err
	}
	defer file.Close()
	handleFile, ok := file.(fdFile)
	if !ok {
		return windowsHandleFileIdentity{}, errors.New("root handle does not expose a file descriptor")
	}
	return windowsHandleFileIdentityFn(handleFile.Fd())
}

func windowsHandleFileIdentityFromFD(fd uintptr) (windowsHandleFileIdentity, error) {
	var info syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(syscall.Handle(fd), &info); err != nil {
		return windowsHandleFileIdentity{}, err
	}
	return windowsHandleFileIdentity{
		volumeSerial: info.VolumeSerialNumber,
		fileIndexHi:  info.FileIndexHigh,
		fileIndexLo:  info.FileIndexLow,
	}, nil
}
