//go:build darwin

package safeio

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"syscall"

	"golang.org/x/sys/unix"
)

var (
	darwinOpenat         = unix.Openat
	darwinFstat          = unix.Fstat
	darwinClose          = unix.Close
	darwinRootFDResolver = osRootFD
)

func openRootFileNoFollow(root *os.Root, name string) (*os.File, error) {
	return openRootFileNoFollowAtomic(root, name, openDarwinRootFileNoFollow)
}

func openDarwinRootFileNoFollow(root *os.Root, name string) (*os.File, error) {
	rootFD, err := darwinRootFDResolver(root)
	if err != nil {
		return nil, fmt.Errorf("%w on darwin: root fd extraction is unavailable: %w", ErrOpenFileNoFollowUnsupported, err)
	}

	fd, err := openDarwinNoFollowFD(rootFD, name)
	runtime.KeepAlive(root)
	if err != nil {
		return nil, err
	}

	var stat unix.Stat_t
	if err := fstatDarwinNoFollowFD(fd, &stat); err != nil {
		return nil, closeDarwinNoFollowFDWithError(fd, &os.PathError{Op: "fstat", Path: name, Err: err})
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return nil, closeDarwinNoFollowFDWithError(fd, &os.PathError{Op: "fstat", Path: name, Err: ErrNoFollowFinalComponent})
	}

	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		return nil, closeDarwinNoFollowFDWithError(fd, fmt.Errorf("openat %s: failed to wrap fd", name))
	}
	return file, nil
}

func openDarwinNoFollowFD(rootFD int, name string) (int, error) {
	flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_NONBLOCK | unix.O_CLOEXEC
	for {
		fd, err := darwinOpenat(rootFD, name, flags, 0)
		if !errors.Is(err, unix.EINTR) {
			return fd, err
		}
	}
}

func fstatDarwinNoFollowFD(fd int, stat *unix.Stat_t) error {
	for {
		err := darwinFstat(fd, stat)
		if !errors.Is(err, unix.EINTR) {
			return err
		}
	}
}

func closeDarwinNoFollowFDWithError(fd int, err error) error {
	return errors.Join(err, darwinClose(fd))
}

func isAtomicNoFollowLeafError(err error) bool {
	return errorsIsAny(err, syscall.ELOOP, syscall.ENOTDIR, syscall.EISDIR)
}
