//go:build linux

package safeio

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"golang.org/x/sys/unix"
)

func TestOpenFileNoFollowSupportedTrueOnLinux(t *testing.T) {
	if !OpenFileNoFollowSupported() {
		t.Fatal("expected linux no-follow support probe to succeed on this platform")
	}
}

func TestOpenFileNoFollowSupportedTrueImpliesOpenAndReadWorks(t *testing.T) {
	restore := stubOpenFileNoFollowSupportProbes(t)
	t.Cleanup(restore)

	rootDir := t.TempDir()
	targetPath := filepath.Join(rootDir, "trace.ndjson")
	const want = "{\"module\":\"lodash/map\"}\n"
	if err := os.WriteFile(targetPath, []byte(want), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}

	rootDirFile, err := os.Open(rootDir)
	if err != nil {
		t.Fatalf("open root dir: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := rootDirFile.Close(); closeErr != nil {
			t.Fatalf("close root dir: %v", closeErr)
		}
	})
	osRootFDResolver = func(*os.Root) (int, error) {
		return int(rootDirFile.Fd()), nil
	}
	procSelfFDReopen = func(string, int, uint32) (int, error) {
		return unix.Open(targetPath, unix.O_RDONLY|unix.O_CLOEXEC, 0)
	}

	file, err := OpenFileNoFollow(targetPath)
	if err != nil {
		t.Fatalf("OpenFileNoFollow(%q): %v", targetPath, err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			t.Fatalf("close file: %v", closeErr)
		}
	}()

	data, err := io.ReadAll(file)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if string(data) != want {
		t.Fatalf("unexpected file content: got %q want %q", string(data), want)
	}
}

func TestOpenFileNoFollowSupportedDoesNotDependOnTMPDIRWrites(t *testing.T) {
	restore := stubOpenFileNoFollowSupportProbes(t)
	t.Cleanup(restore)

	probePath, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	resolvedProbePath, err := filepath.EvalSymlinks(probePath)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", probePath, err)
	}
	openNoFollowProbePath = func() (string, error) { return resolvedProbePath, nil }
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing-tmpdir"))

	if !OpenFileNoFollowSupported() {
		t.Fatal("expected support probe to succeed without TMPDIR writes")
	}
}

func TestOpenFileNoFollowSupportedFailsWhenRootFDExtractionUnavailable(t *testing.T) {
	restore := stubOpenFileNoFollowSupportProbes(t)
	t.Cleanup(restore)

	rootFDProbeErr := errors.New("root fd probe failed")
	osRootFDResolver = func(*os.Root) (int, error) {
		return 0, rootFDProbeErr
	}
	openNoFollowProbePath = func() (string, error) {
		return mustRealExecutablePath(t), nil
	}

	if OpenFileNoFollowSupported() {
		t.Fatal("expected support probe to fail when root fd extraction is unavailable")
	}
}

func TestOpenFileNoFollowNormalizesUnavailableRootFDExtraction(t *testing.T) {
	restore := stubOpenFileNoFollowSupportProbes(t)
	t.Cleanup(restore)

	rootFDProbeErr := errors.New("root fd probe failed")
	osRootFDResolver = func(*os.Root) (int, error) {
		return 0, rootFDProbeErr
	}
	openat2NoFollowProbe = func(int, string, *unix.OpenHow) (int, error) {
		t.Fatal("expected root fd extraction to fail before openat2")
		return -1, nil
	}

	tracePath := filepath.Join(t.TempDir(), "trace.ndjson")
	if err := os.WriteFile(tracePath, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write trace: %v", err)
	}

	file, err := OpenFileNoFollow(tracePath)
	if file != nil {
		reportUnexpectedNoFollowFileClose(t, file)
		t.Fatal("expected unavailable root fd extraction to return no file")
	}
	if !errors.Is(err, ErrOpenFileNoFollowUnsupported) {
		t.Fatalf("expected unsupported sentinel, got %v", err)
	}
	if !errors.Is(err, rootFDProbeErr) {
		t.Fatalf("expected root fd extraction identity to remain recoverable, got %v", err)
	}
	if !strings.Contains(err.Error(), rootFDProbeErr.Error()) {
		t.Fatalf("expected root fd diagnostic to remain available, got %v", err)
	}
}

func TestOpenFileNoFollowSupportedFailsWhenProcSelfFDUnavailable(t *testing.T) {
	restore := stubOpenFileNoFollowSupportProbes(t)
	t.Cleanup(restore)

	probePath := mustRealExecutablePath(t)
	openNoFollowProbePath = func() (string, error) {
		return probePath, nil
	}
	procSelfFDReopen = func(string, int, uint32) (int, error) {
		return -1, unix.ENOENT
	}

	if OpenFileNoFollowSupported() {
		t.Fatal("expected support probe to fail when /proc/self/fd reopen is unavailable")
	}

	file, err := OpenFileNoFollow(probePath)
	if file != nil {
		reportUnexpectedNoFollowFileClose(t, file)
		t.Fatal("expected unavailable /proc/self/fd to return no file")
	}
	if !errors.Is(err, ErrOpenFileNoFollowUnsupported) {
		t.Fatalf("expected unsupported sentinel, got %v", err)
	}
	if !errors.Is(err, unix.ENOENT) {
		t.Fatalf("expected original /proc/self/fd ENOENT identity, got %v", err)
	}
}

func TestProbeOpenFileNoFollowSupportFailsWhenRootCloseFails(t *testing.T) {
	restore := stubOpenFileNoFollowSupportProbes(t)
	t.Cleanup(restore)

	probePath := mustRealExecutablePath(t)
	openNoFollowProbePath = func() (string, error) {
		return probePath, nil
	}
	realClose := closeNoFollowProbeRoot
	closeErr := errors.New("close support probe root")
	closeNoFollowProbeRoot = func(root *os.Root) error {
		return errors.Join(realClose(root), closeErr)
	}

	if probeOpenFileNoFollowSupport() {
		t.Fatal("expected root close failure to make the support probe fail closed")
	}
}

func TestProbeOpenFileNoFollowSupportFailsWhenProbePathResolutionFails(t *testing.T) {
	restore := stubOpenFileNoFollowSupportProbes(t)
	t.Cleanup(restore)

	openNoFollowProbePath = func() (string, error) {
		return "", errors.New("resolve support probe path")
	}
	if probeOpenFileNoFollowSupport() {
		t.Fatal("expected probe path resolution failure to report unsupported")
	}
}

func stubOpenFileNoFollowSupportProbes(t *testing.T) func() {
	t.Helper()

	originalOpenat2 := openat2NoFollowProbe
	originalOpenat := openatNoFollowProbe
	originalProcOpen := procSelfFDReopen
	originalCloseFD := closeNoFollowFD
	originalNewFile := newNoFollowOSFile
	originalCloseRoot := closeNoFollowProbeRoot
	originalRootFD := osRootFDResolver
	originalProbe := openNoFollowProbe
	originalProbePath := openNoFollowProbePath

	resetOpenFileNoFollowSupportCache()

	closeNoFollowFD = func(fd int) error {
		if fd < 0 {
			return errors.New("unexpected negative fd")
		}
		return originalCloseFD(fd)
	}

	return func() {
		openat2NoFollowProbe = originalOpenat2
		openatNoFollowProbe = originalOpenat
		procSelfFDReopen = originalProcOpen
		closeNoFollowFD = originalCloseFD
		newNoFollowOSFile = originalNewFile
		closeNoFollowProbeRoot = originalCloseRoot
		osRootFDResolver = originalRootFD
		openNoFollowProbe = originalProbe
		openNoFollowProbePath = originalProbePath
		resetOpenFileNoFollowSupportCache()
	}
}

func resetOpenFileNoFollowSupportCache() {
	openNoFollowSupportOnce = sync.Once{}
	openNoFollowSupported = false
}

func TestOpenFileNoFollowFallsBackWhenOpenat2IsBlocked(t *testing.T) {
	restore := stubOpenFileNoFollowSupportProbes(t)
	t.Cleanup(restore)

	openat2NoFollowProbe = func(int, string, *unix.OpenHow) (int, error) {
		return -1, unix.EPERM
	}

	rootDir := t.TempDir()
	tracePath := filepath.Join(rootDir, "trace.ndjson")
	if err := os.WriteFile(tracePath, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write trace: %v", err)
	}

	file, err := OpenFileNoFollow(tracePath)
	if err != nil {
		t.Fatalf("expected openat fallback to succeed, got %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close file: %v", err)
	}
}

func mustRealExecutablePath(t *testing.T) string {
	t.Helper()

	path, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", path, err)
	}
	return path
}
