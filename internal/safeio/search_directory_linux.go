//go:build linux

package safeio

import (
	"os"

	"golang.org/x/sys/unix"
)

const linuxSearchDirectoryFlags = unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW

func searchOnlyDirectoryOpenFlags() int {
	return linuxSearchDirectoryFlags
}

func openSearchOnlyDirectory(path string) (*os.File, error) {
	fd, err := unix.Open(path, linuxSearchDirectoryFlags, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}

func openSearchOnlyDirectoryAt(parentFD int, name string) (*os.File, error) {
	fd, err := unix.Openat(parentFD, name, linuxSearchDirectoryFlags, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}
