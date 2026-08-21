//go:build darwin

package safeio

import (
	"os"

	"golang.org/x/sys/unix"
)

const darwinOSearch = 0x40000000 | unix.O_DIRECTORY | unix.O_NOFOLLOW

func openSearchOnlyDirectory(path string) (*os.File, error) {
	fd, err := unix.Open(path, darwinOSearch|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}

func openSearchOnlyDirectoryAt(parentFD int, name string) (*os.File, error) {
	fd, err := unix.Openat(parentFD, name, darwinOSearch|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}
