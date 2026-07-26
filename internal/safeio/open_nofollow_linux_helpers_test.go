//go:build linux

package safeio

import (
	"errors"
	"io"
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

func TestOpenPinnedNoFollowHandleFallbackRejectsNonLeafNames(t *testing.T) {
	restore := stubOpenFileNoFollowSupportProbes(t)
	t.Cleanup(restore)

	openatNoFollowProbe = func(int, string, int, uint32) (int, error) {
		t.Fatal("expected invalid name rejection before openat fallback")
		return -1, nil
	}

	for _, tc := range invalidOpenNoFollowNameCases(t.TempDir()) {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := openPinnedNoFollowHandleFallback(unix.AT_FDCWD, tc.path)
			if !errors.Is(err, os.ErrInvalid) {
				t.Fatalf("expected invalid name rejection for %q, got %v", tc.path, err)
			}
		})
	}
}

func TestOpenPinnedNoFollowHandleFallbackReturnsPathErrorForOpenatFailure(t *testing.T) {
	restore := stubOpenFileNoFollowSupportProbes(t)
	t.Cleanup(restore)

	openatNoFollowProbe = func(int, string, int, uint32) (int, error) {
		return -1, unix.EACCES
	}

	_, _, err := openPinnedNoFollowHandleFallback(unix.AT_FDCWD, "trace.ndjson")
	var pathErr *os.PathError
	if !errors.As(err, &pathErr) || pathErr.Op != "openat" {
		t.Fatalf("expected openat PathError, got %v", err)
	}
	if !errors.Is(err, unix.EACCES) {
		t.Fatalf("expected original openat error, got %v", err)
	}
}

func TestOpenPinnedNoFollowHandleRejectsNonRegularFile(t *testing.T) {
	restore := stubOpenFileNoFollowSupportProbes(t)
	t.Cleanup(restore)

	closeErr := errors.New("close non-regular pinned fd")
	stubCloseNoFollowFDWithError(t, closeErr)

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
	if !errors.Is(err, closeErr) {
		t.Fatalf("expected pinned fd close error to remain recoverable, got %v", err)
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

type reopenPinnedRegularFileFailureCase struct {
	name       string
	reopenFD   func(t *testing.T, rootDir string) int
	reopenErr  error
	wantErr    error
	wantPathOp string
	wantMsg    string
	avoidErr   error
}

func TestReopenPinnedRegularFileFailureModes(t *testing.T) {
	for _, tc := range reopenPinnedRegularFileFailureCases() {
		t.Run(tc.name, func(t *testing.T) {
			assertReopenPinnedRegularFileFailure(t, tc)
		})
	}
}

func reopenPinnedRegularFileFailureCases() []reopenPinnedRegularFileFailureCase {
	return []reopenPinnedRegularFileFailureCase{
		{
			name:     "rejects changed inode",
			reopenFD: reopenOtherRegularFileFD,
			wantMsg:  "changed while reopening",
		},
		{
			name:      "fails when proc self fd missing",
			reopenErr: unix.ENOENT,
			wantErr:   ErrOpenFileNoFollowUnsupported,
			wantMsg:   "/proc/self/fd is required",
		},
		{
			name:       "returns path error for reopen failure",
			reopenErr:  unix.EACCES,
			wantErr:    unix.EACCES,
			wantPathOp: "open pinned regular file",
			avoidErr:   ErrOpenFileNoFollowUnsupported,
		},
		{
			name:     "fails closed when reopened fd stat fails",
			reopenFD: reopenClosedPipeReaderFD,
			wantMsg:  "fstat reopened file",
		},
	}
}

func assertReopenPinnedRegularFileFailure(t *testing.T, tc reopenPinnedRegularFileFailureCase) {
	t.Helper()

	restore := stubOpenFileNoFollowSupportProbes(t)
	t.Cleanup(restore)

	pinnedPath, pinnedFD, pinnedStat := openPinnedRegularFileFixture(t)
	procSelfFDReopen = func(string, int, uint32) (int, error) {
		if tc.reopenErr != nil {
			return -1, tc.reopenErr
		}
		return tc.reopenFD(t, filepath.Dir(pinnedPath)), nil
	}

	file, err := reopenPinnedRegularFile(pinnedPath, pinnedFD, pinnedStat)
	if file != nil {
		reportUnexpectedNoFollowFileClose(t, file)
		t.Fatal("expected reopen failure to return no file")
	}
	assertReopenPinnedRegularFileError(t, tc, err)
}

func TestOpenRegularFileNoFollowFromRootFDJoinsPinnedCloseAndReopenErrors(t *testing.T) {
	restore := stubOpenFileNoFollowSupportProbes(t)
	t.Cleanup(restore)

	rootFD, name := openRootFDRegularFileFixture(t)
	reopenErr := errors.New("reopen pinned file")
	procSelfFDReopen = func(string, int, uint32) (int, error) {
		return -1, reopenErr
	}
	closeErr := errors.New("close pinned fd")
	stubCloseNoFollowFDWithError(t, closeErr)

	file, err := openRegularFileNoFollowFromRootFD(rootFD, name)
	if file != nil {
		reportUnexpectedNoFollowFileClose(t, file)
		t.Fatal("expected combined reopen and close failure to return no file")
	}
	if !errors.Is(err, reopenErr) || !errors.Is(err, closeErr) {
		t.Fatalf("expected joined reopen and close errors, got %v", err)
	}
}

func TestOpenRegularFileNoFollowFromRootFDClosesReopenedFileAfterPinnedCloseFailure(t *testing.T) {
	restore := stubOpenFileNoFollowSupportProbes(t)
	t.Cleanup(restore)

	rootFD, name := openRootFDRegularFileFixture(t)
	realReopen := procSelfFDReopen
	reopenedFD := -1
	procSelfFDReopen = func(path string, flags int, mode uint32) (int, error) {
		fd, err := realReopen(path, flags, mode)
		reopenedFD = fd
		return fd, err
	}
	closeErr := errors.New("close pinned fd")
	stubCloseNoFollowFDWithError(t, closeErr)

	file, err := openRegularFileNoFollowFromRootFD(rootFD, name)
	if file != nil {
		reportUnexpectedNoFollowFileClose(t, file)
		t.Fatal("expected pinned close failure to return no file")
	}
	if !errors.Is(err, closeErr) {
		t.Fatalf("expected pinned close error, got %v", err)
	}
	if reopenedFD < 0 {
		t.Fatal("expected the pinned file to be reopened before cleanup failed")
	}
	var stat unix.Stat_t
	if statErr := unix.Fstat(reopenedFD, &stat); !errors.Is(statErr, unix.EBADF) {
		t.Fatalf("expected reopened fd to be closed, got %v", statErr)
	}
}

func TestReopenPinnedRegularFileJoinsWrapAndCloseErrors(t *testing.T) {
	restore := stubOpenFileNoFollowSupportProbes(t)
	t.Cleanup(restore)

	pinnedPath, pinnedFD, pinnedStat := openPinnedRegularFileFixture(t)
	procSelfFDReopen = func(string, int, uint32) (int, error) {
		return unix.Open(pinnedPath, unix.O_RDONLY|unix.O_CLOEXEC, 0)
	}
	newNoFollowOSFile = func(uintptr, string) *os.File {
		return nil
	}
	closeErr := errors.New("close unwrapped reopened fd")
	stubCloseNoFollowFDWithError(t, closeErr)

	file, err := reopenPinnedRegularFile(pinnedPath, pinnedFD, pinnedStat)
	if file != nil {
		reportUnexpectedNoFollowFileClose(t, file)
		t.Fatal("expected wrap failure to return no file")
	}
	if err == nil || !strings.Contains(err.Error(), "failed to wrap fd") {
		t.Fatalf("expected fd wrap failure, got %v", err)
	}
	if !errors.Is(err, closeErr) {
		t.Fatalf("expected reopened fd close error to remain recoverable, got %v", err)
	}
}

func reopenOtherRegularFileFD(t *testing.T, rootDir string) int {
	t.Helper()

	otherPath := filepath.Join(rootDir, "other.txt")
	if err := os.WriteFile(otherPath, []byte("other"), 0o600); err != nil {
		t.Fatalf("write other file: %v", err)
	}
	fd, err := unix.Open(otherPath, unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatalf("open other file: %v", err)
	}
	return fd
}

func reopenClosedPipeReaderFD(t *testing.T, _ string) int {
	t.Helper()

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
	return fd
}

func openPinnedRegularFileFixture(t *testing.T) (string, int, unix.Stat_t) {
	rootDir := t.TempDir()
	pinnedPath := filepath.Join(rootDir, "pinned.txt")
	if err := os.WriteFile(pinnedPath, []byte("pinned"), 0o600); err != nil {
		t.Fatalf("write pinned file: %v", err)
	}

	pinnedFile, err := os.Open(pinnedPath)
	if err != nil {
		t.Fatalf("open pinned file: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := pinnedFile.Close(); closeErr != nil && !errors.Is(closeErr, os.ErrClosed) {
			t.Fatalf("close pinned file: %v", closeErr)
		}
	})

	var pinnedStat unix.Stat_t
	if err := unix.Fstat(int(pinnedFile.Fd()), &pinnedStat); err != nil {
		t.Fatalf("fstat pinned file: %v", err)
	}
	return pinnedPath, int(pinnedFile.Fd()), pinnedStat
}

func openRootFDRegularFileFixture(t *testing.T) (int, string) {
	rootDir := t.TempDir()
	const name = "trace.ndjson"
	if err := os.WriteFile(filepath.Join(rootDir, name), []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write trace: %v", err)
	}
	root, err := os.OpenRoot(rootDir)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Errorf("close root: %v", closeErr)
		}
	})
	rootFD, err := osRootFD(root)
	if err != nil {
		t.Fatalf("resolve root fd: %v", err)
	}
	return rootFD, name
}

func stubCloseNoFollowFDWithError(t *testing.T, closeErr error) {
	t.Helper()

	originalClose := closeNoFollowFD
	closeNoFollowFD = func(fd int) error {
		return errors.Join(originalClose(fd), closeErr)
	}
	t.Cleanup(func() {
		closeNoFollowFD = originalClose
	})
}

func reportUnexpectedNoFollowFileClose(t *testing.T, file io.Closer) {
	t.Helper()
	if closeErr := file.Close(); closeErr != nil {
		t.Errorf("close unexpected no-follow file: %v", closeErr)
	}
}

func assertReopenPinnedRegularFileError(t *testing.T, tc reopenPinnedRegularFileFailureCase, err error) {
	t.Helper()

	if tc.wantMsg != "" && (err == nil || !strings.Contains(err.Error(), tc.wantMsg)) {
		t.Fatalf("expected error containing %q, got %v", tc.wantMsg, err)
	}
	if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
		t.Fatalf("expected error matching %v, got %v", tc.wantErr, err)
	}
	if tc.wantPathOp != "" {
		var pathErr *os.PathError
		if !errors.As(err, &pathErr) || pathErr.Op != tc.wantPathOp {
			t.Fatalf("expected %s PathError, got %v", tc.wantPathOp, err)
		}
	}
	if tc.avoidErr != nil && errors.Is(err, tc.avoidErr) {
		t.Fatalf("expected error to remain distinct from %v, got %v", tc.avoidErr, err)
	}
}
