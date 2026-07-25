//go:build linux

package safeio

import (
	"errors"
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

var (
	openat2NoFollowProbe = unix.Openat2
	procSelfFDReopen     = unix.Open
	closeNoFollowFD      = unix.Close
	osRootFDResolver     = osRootFD
)

func openRootFileNoFollow(root *os.Root, name string) (*os.File, error) {
	rootFD, err := osRootFDResolver(root)
	if err != nil {
		return nil, err
	}

	return openRegularFileNoFollowFromRootFD(rootFD, name)
}

func openRegularFileNoFollowFromRootFD(rootFD int, name string) (*os.File, error) {
	pinnedFD, pinnedStat, err := openPinnedNoFollowHandle(rootFD, name)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = closeNoFollowFD(pinnedFD)
	}()

	return reopenPinnedRegularFile(name, pinnedFD, pinnedStat)
}

func openPinnedNoFollowHandle(rootFD int, name string) (int, unix.Stat_t, error) {
	pinnedFD, err := openat2NoFollowProbe(rootFD, name, &unix.OpenHow{
		Flags:   uint64(unix.O_PATH | syscall.O_CLOEXEC | syscall.O_NOFOLLOW),
		Resolve: uint64(unix.RESOLVE_BENEATH | unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_NO_SYMLINKS),
	})
	if err != nil {
		if err == unix.ENOSYS {
			return -1, unix.Stat_t{}, fmt.Errorf("no-follow file open unsupported on linux: kernel openat2 support is required")
		}
		return -1, unix.Stat_t{}, normalizeNoFollowLeafOpenError("openat2", name, err)
	}

	var pinnedStat unix.Stat_t
	if err := unix.Fstat(pinnedFD, &pinnedStat); err != nil {
		_ = closeNoFollowFD(pinnedFD)
		return -1, unix.Stat_t{}, &os.PathError{Op: "fstat", Path: name, Err: err}
	}
	if pinnedStat.Mode&unix.S_IFMT != unix.S_IFREG {
		_ = closeNoFollowFD(pinnedFD)
		return -1, unix.Stat_t{}, normalizeNoFollowLeafOpenError("openat2", name, ErrNoFollowFinalComponent)
	}

	return pinnedFD, pinnedStat, nil
}

func reopenPinnedRegularFile(path string, pinnedFD int, pinnedStat unix.Stat_t) (*os.File, error) {
	reopenedFD, err := procSelfFDReopen(fmt.Sprintf("/proc/self/fd/%d", pinnedFD), syscall.O_RDONLY|syscall.O_CLOEXEC, 0)
	if err != nil {
		if err == unix.ENOENT || err == unix.ENOTDIR {
			return nil, fmt.Errorf("no-follow file open unsupported on linux: /proc/self/fd is required to reopen a pinned regular file")
		}
		return nil, &os.PathError{Op: "open pinned regular file", Path: path, Err: err}
	}

	var reopenedStat unix.Stat_t
	if err := unix.Fstat(reopenedFD, &reopenedStat); err != nil {
		_ = closeNoFollowFD(reopenedFD)
		return nil, &os.PathError{Op: "fstat reopened file", Path: path, Err: err}
	}
	if !sameUnixFile(pinnedStat, reopenedStat) {
		_ = closeNoFollowFD(reopenedFD)
		return nil, &os.PathError{Op: "open pinned regular file", Path: path, Err: fmt.Errorf("pinned regular file changed while reopening")}
	}

	file := os.NewFile(uintptr(reopenedFD), path)
	if file == nil {
		_ = closeNoFollowFD(reopenedFD)
		return nil, fmt.Errorf("open pinned regular file %s: failed to wrap fd", path)
	}
	return file, nil
}

func sameUnixFile(a, b unix.Stat_t) bool {
	return a.Dev == b.Dev && a.Ino == b.Ino
}

func normalizeNoFollowLeafOpenError(op string, path string, err error) error {
	if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.EINVAL) {
		err = errors.Join(ErrNoFollowFinalComponent, err)
	}
	if errors.Is(err, ErrNoFollowFinalComponent) {
		return &os.PathError{Op: op, Path: path, Err: err}
	}
	return &os.PathError{Op: op, Path: path, Err: err}
}
