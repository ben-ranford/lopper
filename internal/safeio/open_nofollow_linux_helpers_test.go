//go:build linux

package safeio

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestOpenPinnedNoFollowHandleFallsBackWhenOpenat2Unavailable(t *testing.T) {
	restore := stubOpenFileNoFollowSupportProbes(t)
	t.Cleanup(restore)
	openat2NoFollowProbe = func(int, string, *unix.OpenHow) (int, error) {
		return -1, unix.ENOSYS
	}

	rootDir := t.TempDir()
	tracePath := filepath.Join(rootDir, "trace.ndjson")
	if err := os.WriteFile(tracePath, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write trace: %v", err)
	}

	root, err := os.OpenRoot(rootDir)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Fatalf("close root: %v", closeErr)
		}
	}()

	rootFD, err := osRootFD(root)
	if err != nil {
		t.Fatalf("resolve root fd: %v", err)
	}

	fd, _, err := openPinnedNoFollowHandle(rootFD, filepath.Base(tracePath))
	if err != nil {
		t.Fatalf("expected ENOSYS fallback to succeed, got %v", err)
	}
	if err := closeNoFollowFD(fd); err != nil {
		t.Fatalf("close pinned fd: %v", err)
	}
}

func TestOpenPinnedNoFollowHandleRejectsNonRegularFile(t *testing.T) {
	rootDir := t.TempDir()
	dirName := "nested"
	if err := os.Mkdir(filepath.Join(rootDir, dirName), 0o755); err != nil {
		t.Fatalf("mkdir nested dir: %v", err)
	}

	root, err := os.OpenRoot(rootDir)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Fatalf("close root: %v", closeErr)
		}
	}()

	rootFD, err := osRootFD(root)
	if err != nil {
		t.Fatalf("resolve root fd: %v", err)
	}

	_, _, err = openPinnedNoFollowHandle(rootFD, dirName)
	if err == nil || !errors.Is(err, ErrNoFollowFinalComponent) {
		t.Fatalf("expected non-regular file rejection, got %v", err)
	}
}

func TestOpenPinnedNoFollowHandleNormalizesLeafSymlinkErrnos(t *testing.T) {
	for _, tc := range []struct {
		name  string
		errno error
	}{
		{name: "eloop", errno: unix.ELOOP},
		{name: "einval", errno: unix.EINVAL},
	} {
		t.Run(tc.name, func(t *testing.T) {
			restore := stubOpenFileNoFollowSupportProbes(t)
			t.Cleanup(restore)
			openat2NoFollowProbe = func(int, string, *unix.OpenHow) (int, error) {
				return -1, tc.errno
			}

			_, _, err := openPinnedNoFollowHandle(unix.AT_FDCWD, "trace.ndjson")
			if !errors.Is(err, ErrNoFollowFinalComponent) {
				t.Fatalf("expected normalized leaf rejection, got %v", err)
			}
			if !errors.Is(err, tc.errno) {
				t.Fatalf("expected original errno %v to remain recoverable, got %v", tc.errno, err)
			}
		})
	}
}

func TestOpenPinnedNoFollowHandleReturnsPathErrorForOpenat2Failure(t *testing.T) {
	restore := stubOpenFileNoFollowSupportProbes(t)
	t.Cleanup(restore)
	openat2NoFollowProbe = func(int, string, *unix.OpenHow) (int, error) {
		return -1, unix.EACCES
	}

	_, _, err := openPinnedNoFollowHandle(unix.AT_FDCWD, "trace.ndjson")
	var pathErr *os.PathError
	if !errors.As(err, &pathErr) || pathErr.Op != "openat2" {
		t.Fatalf("expected openat2 PathError, got %v", err)
	}
}

func TestOpenPinnedNoFollowHandleFailsClosedWhenPinnedFDStatFails(t *testing.T) {
	restore := stubOpenFileNoFollowSupportProbes(t)
	t.Cleanup(restore)

	tempFile, err := os.CreateTemp(t.TempDir(), "closed-fd-*")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	fd := int(tempFile.Fd())
	if err := tempFile.Close(); err != nil {
		t.Fatalf("close temp file: %v", err)
	}

	openat2NoFollowProbe = func(int, string, *unix.OpenHow) (int, error) {
		return fd, nil
	}

	_, _, err = openPinnedNoFollowHandle(unix.AT_FDCWD, "trace.ndjson")
	if err == nil || !strings.Contains(err.Error(), "fstat") {
		t.Fatalf("expected fstat failure, got %v", err)
	}
}

func TestReopenPinnedRegularFileRejectsChangedInode(t *testing.T) {
	restore := stubOpenFileNoFollowSupportProbes(t)
	t.Cleanup(restore)

	rootDir := t.TempDir()
	pinnedPath := filepath.Join(rootDir, "pinned.txt")
	otherPath := filepath.Join(rootDir, "other.txt")
	if err := os.WriteFile(pinnedPath, []byte("pinned"), 0o600); err != nil {
		t.Fatalf("write pinned file: %v", err)
	}
	if err := os.WriteFile(otherPath, []byte("other"), 0o600); err != nil {
		t.Fatalf("write other file: %v", err)
	}

	pinnedFile, err := os.Open(pinnedPath)
	if err != nil {
		t.Fatalf("open pinned file: %v", err)
	}
	defer func() {
		if closeErr := pinnedFile.Close(); closeErr != nil && !errors.Is(closeErr, os.ErrClosed) {
			t.Fatalf("close pinned file: %v", closeErr)
		}
	}()

	var pinnedStat unix.Stat_t
	if err := unix.Fstat(int(pinnedFile.Fd()), &pinnedStat); err != nil {
		t.Fatalf("fstat pinned file: %v", err)
	}

	procSelfFDReopen = func(string, int, uint32) (int, error) {
		return unix.Open(otherPath, unix.O_RDONLY|unix.O_CLOEXEC, 0)
	}

	file, err := reopenPinnedRegularFile(pinnedPath, int(pinnedFile.Fd()), pinnedStat)
	if file != nil {
		_ = file.Close()
		t.Fatal("expected inode mismatch to fail before returning a file")
	}
	if err == nil || !strings.Contains(err.Error(), "changed while reopening") {
		t.Fatalf("expected inode mismatch error, got %v", err)
	}
}

func TestReopenPinnedRegularFileFailsWhenProcSelfFDMissing(t *testing.T) {
	restore := stubOpenFileNoFollowSupportProbes(t)
	t.Cleanup(restore)

	rootDir := t.TempDir()
	pinnedPath := filepath.Join(rootDir, "pinned.txt")
	if err := os.WriteFile(pinnedPath, []byte("pinned"), 0o600); err != nil {
		t.Fatalf("write pinned file: %v", err)
	}

	pinnedFile, err := os.Open(pinnedPath)
	if err != nil {
		t.Fatalf("open pinned file: %v", err)
	}
	defer func() {
		if closeErr := pinnedFile.Close(); closeErr != nil {
			t.Fatalf("close pinned file: %v", closeErr)
		}
	}()

	var pinnedStat unix.Stat_t
	if err := unix.Fstat(int(pinnedFile.Fd()), &pinnedStat); err != nil {
		t.Fatalf("fstat pinned file: %v", err)
	}

	procSelfFDReopen = func(string, int, uint32) (int, error) {
		return -1, unix.ENOENT
	}

	file, err := reopenPinnedRegularFile(pinnedPath, int(pinnedFile.Fd()), pinnedStat)
	if file != nil {
		_ = file.Close()
		t.Fatal("expected missing /proc/self/fd to fail before returning a file")
	}
	if err == nil || !strings.Contains(err.Error(), "/proc/self/fd is required") {
		t.Fatalf("expected missing /proc/self/fd error, got %v", err)
	}
	if !errors.Is(err, ErrOpenFileNoFollowUnsupported) {
		t.Fatalf("expected unsupported sentinel, got %v", err)
	}
	if !errors.Is(err, unix.ENOENT) {
		t.Fatalf("expected original /proc/self/fd errno, got %v", err)
	}
}

func TestReopenPinnedRegularFileReturnsPathErrorForReopenFailure(t *testing.T) {
	restore := stubOpenFileNoFollowSupportProbes(t)
	t.Cleanup(restore)

	rootDir := t.TempDir()
	pinnedPath := filepath.Join(rootDir, "pinned.txt")
	if err := os.WriteFile(pinnedPath, []byte("pinned"), 0o600); err != nil {
		t.Fatalf("write pinned file: %v", err)
	}

	pinnedFile, err := os.Open(pinnedPath)
	if err != nil {
		t.Fatalf("open pinned file: %v", err)
	}
	defer func() {
		if closeErr := pinnedFile.Close(); closeErr != nil {
			t.Fatalf("close pinned file: %v", closeErr)
		}
	}()

	var pinnedStat unix.Stat_t
	if err := unix.Fstat(int(pinnedFile.Fd()), &pinnedStat); err != nil {
		t.Fatalf("fstat pinned file: %v", err)
	}

	procSelfFDReopen = func(string, int, uint32) (int, error) {
		return -1, unix.EACCES
	}

	file, err := reopenPinnedRegularFile(pinnedPath, int(pinnedFile.Fd()), pinnedStat)
	if file != nil {
		_ = file.Close()
		t.Fatal("expected reopen failure to return no file")
	}

	var pathErr *os.PathError
	if !errors.As(err, &pathErr) || pathErr.Op != "open pinned regular file" {
		t.Fatalf("expected reopen PathError, got %v", err)
	}
	if !errors.Is(err, unix.EACCES) {
		t.Fatalf("expected original operational error, got %v", err)
	}
	if errors.Is(err, ErrOpenFileNoFollowUnsupported) {
		t.Fatalf("expected operational error to remain distinct from unsupported capability, got %v", err)
	}
}

func TestReopenPinnedRegularFileFailsClosedWhenReopenedFDStatFails(t *testing.T) {
	restore := stubOpenFileNoFollowSupportProbes(t)
	t.Cleanup(restore)

	rootDir := t.TempDir()
	pinnedPath := filepath.Join(rootDir, "pinned.txt")
	if err := os.WriteFile(pinnedPath, []byte("pinned"), 0o600); err != nil {
		t.Fatalf("write pinned file: %v", err)
	}

	pinnedFile, err := os.Open(pinnedPath)
	if err != nil {
		t.Fatalf("open pinned file: %v", err)
	}
	defer func() {
		if closeErr := pinnedFile.Close(); closeErr != nil && !errors.Is(closeErr, os.ErrClosed) {
			t.Fatalf("close pinned file: %v", closeErr)
		}
	}()

	var pinnedStat unix.Stat_t
	if err := unix.Fstat(int(pinnedFile.Fd()), &pinnedStat); err != nil {
		t.Fatalf("fstat pinned file: %v", err)
	}

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	fd := int(reader.Fd())
	if err := reader.Close(); err != nil {
		t.Fatalf("close pipe reader: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close pipe writer: %v", err)
	}

	procSelfFDReopen = func(string, int, uint32) (int, error) {
		return fd, nil
	}

	file, err := reopenPinnedRegularFile(pinnedPath, int(pinnedFile.Fd()), pinnedStat)
	if file != nil {
		_ = file.Close()
		t.Fatal("expected reopened fd stat failure to fail before returning a file")
	}
	if err == nil || !strings.Contains(err.Error(), "fstat reopened file") {
		t.Fatalf("expected reopened fstat failure, got %v", err)
	}
}
