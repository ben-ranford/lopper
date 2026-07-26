//go:build linux

package safeio

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
)

var (
	openat2NoFollowProbe   = unix.Openat2
	openatNoFollowProbe    = unix.Openat
	procSelfFDReopen       = unix.Open
	closeNoFollowFD        = unix.Close
	newNoFollowOSFile      = os.NewFile
	closeNoFollowProbeRoot = (*os.Root).Close
	osRootFDResolver       = osRootFD
	openNoFollowProbe      = probeOpenFileNoFollowSupport
	openNoFollowProbePath  = defaultOpenNoFollowProbePath

	openNoFollowSupportOnce sync.Once
	openNoFollowSupported   bool
)

func openRootFileNoFollow(root *os.Root, name string) (*os.File, error) {
	rootFD, err := osRootFDResolver(root)
	if err != nil {
		return nil, fmt.Errorf("%w on linux: root fd extraction is unavailable: %w", ErrOpenFileNoFollowUnsupported, err)
	}

	return openRegularFileNoFollowFromRootFD(rootFD, name)
}

func probeOpenFileNoFollowSupport() (supported bool) {
	probePath, err := openNoFollowProbePath()
	if err != nil {
		return false
	}

	root, err := os.OpenRoot(filepath.Dir(probePath))
	if err != nil {
		return false
	}
	defer func() {
		if closeErr := closeNoFollowProbeRoot(root); closeErr != nil {
			supported = false
		}
	}()

	rootFD, err := osRootFDResolver(root)
	if err != nil {
		return false
	}

	file, err := openRegularFileNoFollowFromRootFD(rootFD, filepath.Base(probePath))
	if err != nil {
		return false
	}
	return file.Close() == nil
}

func defaultOpenNoFollowProbePath() (string, error) {
	probePath, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(probePath)
}

func openRegularFileNoFollowFromRootFD(rootFD int, name string) (*os.File, error) {
	pinnedFD, pinnedStat, err := openPinnedNoFollowHandle(rootFD, name)
	if err != nil {
		return nil, err
	}

	file, err := reopenPinnedRegularFile(name, pinnedFD, pinnedStat)
	return finishPinnedNoFollowOpen(file, pinnedFD, err)
}

func finishPinnedNoFollowOpen(file *os.File, pinnedFD int, openErr error) (*os.File, error) {
	closeErr := closeNoFollowFD(pinnedFD)
	if closeErr == nil {
		return file, openErr
	}
	if file == nil {
		return nil, errors.Join(openErr, closeErr)
	}
	return nil, errors.Join(openErr, closeErr, file.Close())
}

func openPinnedNoFollowHandle(rootFD int, name string) (int, unix.Stat_t, error) {
	pinnedFD, err := openat2NoFollowProbe(rootFD, name, &unix.OpenHow{
		Flags:   uint64(unix.O_PATH | syscall.O_CLOEXEC | syscall.O_NOFOLLOW),
		Resolve: uint64(unix.RESOLVE_BENEATH | unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_NO_SYMLINKS),
	})
	if err != nil {
		if shouldFallbackToOpenat(err) {
			return openPinnedNoFollowHandleFallback(rootFD, name)
		}
		return -1, unix.Stat_t{}, normalizeNoFollowLeafOpenError("openat2", name, err)
	}

	return validatePinnedNoFollowHandle("openat2", name, pinnedFD)
}

func openPinnedNoFollowHandleFallback(rootFD int, name string) (int, unix.Stat_t, error) {
	if err := validateOpenNoFollowName(name); err != nil {
		return -1, unix.Stat_t{}, err
	}

	pinnedFD, err := openatNoFollowProbe(rootFD, name, unix.O_PATH|syscall.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return -1, unix.Stat_t{}, normalizeNoFollowLeafOpenError("openat", name, err)
	}
	return validatePinnedNoFollowHandle("openat", name, pinnedFD)
}

func validatePinnedNoFollowHandle(op string, path string, pinnedFD int) (int, unix.Stat_t, error) {
	var pinnedStat unix.Stat_t
	if err := unix.Fstat(pinnedFD, &pinnedStat); err != nil {
		statErr := &os.PathError{Op: "fstat", Path: path, Err: err}
		return -1, unix.Stat_t{}, closeNoFollowFDWithError(pinnedFD, statErr)
	}
	if pinnedStat.Mode&unix.S_IFMT != unix.S_IFREG {
		openErr := normalizeNoFollowLeafOpenError(op, path, ErrNoFollowFinalComponent)
		return -1, unix.Stat_t{}, closeNoFollowFDWithError(pinnedFD, openErr)
	}

	return pinnedFD, pinnedStat, nil
}

func shouldFallbackToOpenat(err error) bool {
	return errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EPERM)
}

func reopenPinnedRegularFile(path string, pinnedFD int, pinnedStat unix.Stat_t) (*os.File, error) {
	reopenedFD, err := procSelfFDReopen(fmt.Sprintf("/proc/self/fd/%d", pinnedFD), syscall.O_RDONLY|syscall.O_CLOEXEC, 0)
	if err != nil {
		if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ENOTDIR) {
			return nil, fmt.Errorf("%w on linux: /proc/self/fd is required to reopen a pinned regular file: %w", ErrOpenFileNoFollowUnsupported, err)
		}
		return nil, &os.PathError{Op: "open pinned regular file", Path: path, Err: err}
	}

	var reopenedStat unix.Stat_t
	if err := unix.Fstat(reopenedFD, &reopenedStat); err != nil {
		statErr := &os.PathError{Op: "fstat reopened file", Path: path, Err: err}
		return nil, closeNoFollowFDWithError(reopenedFD, statErr)
	}
	if !sameUnixFile(pinnedStat, reopenedStat) {
		openErr := &os.PathError{Op: "open pinned regular file", Path: path, Err: fmt.Errorf("pinned regular file changed while reopening")}
		return nil, closeNoFollowFDWithError(reopenedFD, openErr)
	}

	file := newNoFollowOSFile(uintptr(reopenedFD), path)
	if file == nil {
		openErr := fmt.Errorf("open pinned regular file %s: failed to wrap fd", path)
		return nil, closeNoFollowFDWithError(reopenedFD, openErr)
	}
	return file, nil
}

func closeNoFollowFDWithError(fd int, err error) error {
	return errors.Join(err, closeNoFollowFD(fd))
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
