//go:build linux

package safeio

import (
	"os"

	"golang.org/x/sys/unix"
)

func openSearchOnlyDirectory(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}
