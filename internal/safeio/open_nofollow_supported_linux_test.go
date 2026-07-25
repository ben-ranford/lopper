//go:build linux

package safeio

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"golang.org/x/sys/unix"
)

func TestOpenFileNoFollowSupportedFalseWhenOpenat2Unavailable(t *testing.T) {
	restore := stubOpenFileNoFollowSupportProbes(t)
	t.Cleanup(restore)
	openat2NoFollowProbe = func(int, string, *unix.OpenHow) (int, error) {
		return -1, unix.ENOSYS
	}

	if OpenFileNoFollowSupported() {
		t.Fatal("expected openat2 probe failure to disable no-follow support")
	}
}

func TestOpenFileNoFollowSupportedFalseWhenProcSelfFDMissing(t *testing.T) {
	restore := stubOpenFileNoFollowSupportProbes(t)
	t.Cleanup(restore)
	procSelfFDReopen = func(string, int, uint32) (int, error) {
		return -1, unix.ENOENT
	}

	if OpenFileNoFollowSupported() {
		t.Fatal("expected missing /proc/self/fd to disable no-follow support")
	}
}

func TestOpenFileNoFollowSupportedFalseWhenRootFDExtractionFails(t *testing.T) {
	restore := stubOpenFileNoFollowSupportProbes(t)
	t.Cleanup(restore)
	osRootFDResolver = func(*os.Root) (int, error) {
		return 0, fmt.Errorf("root fd unavailable")
	}

	if OpenFileNoFollowSupported() {
		t.Fatal("expected root fd extraction failure to disable no-follow support")
	}
}

func TestOpenFileNoFollowSupportedFalseWhenMkdirTempFails(t *testing.T) {
	restore := stubOpenFileNoFollowSupportProbes(t)
	t.Cleanup(restore)
	mkdirTempNoFollowSupport = func(string, string) (string, error) {
		return "", fmt.Errorf("mkdir temp failed")
	}

	if OpenFileNoFollowSupported() {
		t.Fatal("expected temp-dir creation failure to disable no-follow support")
	}
}

func TestOpenFileNoFollowSupportedFalseWhenProbeWriteFails(t *testing.T) {
	restore := stubOpenFileNoFollowSupportProbes(t)
	t.Cleanup(restore)
	writeFileNoFollowSupport = func(string, []byte, os.FileMode) error {
		return fmt.Errorf("write failed")
	}

	if OpenFileNoFollowSupported() {
		t.Fatal("expected probe write failure to disable no-follow support")
	}
}

func TestOpenFileNoFollowSupportedFalseWhenOpenRootFails(t *testing.T) {
	restore := stubOpenFileNoFollowSupportProbes(t)
	t.Cleanup(restore)
	openRootNoFollowSupport = func(string) (*os.Root, error) {
		return nil, fmt.Errorf("open root failed")
	}

	if OpenFileNoFollowSupported() {
		t.Fatal("expected root open failure to disable no-follow support")
	}
}

func TestOpenFileNoFollowSupportedFalseWhenCleanupFails(t *testing.T) {
	restore := stubOpenFileNoFollowSupportProbes(t)
	t.Cleanup(restore)
	originalRemoveAll := removeAllNoFollowSupport
	removeAllNoFollowSupport = func(path string) error {
		_ = originalRemoveAll(path)
		return fmt.Errorf("remove failed")
	}

	if OpenFileNoFollowSupported() {
		t.Fatal("expected cleanup failure to disable no-follow support")
	}
}

func TestOpenFileNoFollowSupportedCachesResult(t *testing.T) {
	restore := stubOpenFileNoFollowSupportProbes(t)
	t.Cleanup(restore)
	var openat2Calls atomic.Int32
	originalOpenat2 := openat2NoFollowProbe
	openat2NoFollowProbe = func(dirfd int, path string, how *unix.OpenHow) (int, error) {
		openat2Calls.Add(1)
		return originalOpenat2(dirfd, path, how)
	}

	want := OpenFileNoFollowSupported()
	for range 3 {
		if got := OpenFileNoFollowSupported(); got != want {
			t.Fatalf("expected cached support probe result %t, got %t", want, got)
		}
	}
	if got := openat2Calls.Load(); got != 1 {
		t.Fatalf("expected one openat2 probe, got %d", got)
	}
}

func TestOpenFileNoFollowSupportedIsRaceSafe(t *testing.T) {
	restore := stubOpenFileNoFollowSupportProbes(t)
	t.Cleanup(restore)
	var openat2Calls atomic.Int32
	originalOpenat2 := openat2NoFollowProbe
	openat2NoFollowProbe = func(dirfd int, path string, how *unix.OpenHow) (int, error) {
		openat2Calls.Add(1)
		return originalOpenat2(dirfd, path, how)
	}

	want := OpenFileNoFollowSupported()
	const goroutines = 32
	results := make(chan bool, goroutines)
	var wg sync.WaitGroup
	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- OpenFileNoFollowSupported()
		}()
	}
	wg.Wait()
	close(results)

	for got := range results {
		if got != want {
			t.Fatalf("expected concurrent support probes to agree on %t, got %t", want, got)
		}
	}
	if got := openat2Calls.Load(); got != 1 {
		t.Fatalf("expected one openat2 probe across concurrent calls, got %d", got)
	}
}

func TestOpenFileNoFollowSupportedTrueImpliesOpenAndReadWorks(t *testing.T) {
	if !OpenFileNoFollowSupported() {
		t.Skip("no-follow file open unsupported in this Linux environment")
	}

	rootDir := t.TempDir()
	targetPath := filepath.Join(rootDir, "trace.ndjson")
	const want = "{\"module\":\"lodash/map\"}\n"
	if err := os.WriteFile(targetPath, []byte(want), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
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

func stubOpenFileNoFollowSupportProbes(t *testing.T) func() {
	t.Helper()

	originalSupported := openFileNoFollowSupported
	originalOpenat2 := openat2NoFollowProbe
	originalProcOpen := procSelfFDReopen
	originalCloseFD := closeNoFollowFD
	originalRootFD := osRootFDResolver
	originalMkdirTemp := mkdirTempNoFollowSupport
	originalWriteFile := writeFileNoFollowSupport
	originalRemoveAll := removeAllNoFollowSupport
	originalOpenRoot := openRootNoFollowSupport
	originalOpenRootFile := openRootFileNoFollowProbe
	originalCloseRoot := closeRootNoFollowSupport
	originalCloseFile := closeFileNoFollowSupport
	originalReadAll := readAllNoFollowSupport

	resetOpenFileNoFollowSupportProbe()
	closeNoFollowFD = func(fd int) error {
		if fd < 0 {
			return errors.New("unexpected negative fd")
		}
		return originalCloseFD(fd)
	}

	return func() {
		resetOpenFileNoFollowSupportProbe()
		openFileNoFollowSupported = originalSupported
		openat2NoFollowProbe = originalOpenat2
		procSelfFDReopen = originalProcOpen
		closeNoFollowFD = originalCloseFD
		osRootFDResolver = originalRootFD
		mkdirTempNoFollowSupport = originalMkdirTemp
		writeFileNoFollowSupport = originalWriteFile
		removeAllNoFollowSupport = originalRemoveAll
		openRootNoFollowSupport = originalOpenRoot
		openRootFileNoFollowProbe = originalOpenRootFile
		closeRootNoFollowSupport = originalCloseRoot
		closeFileNoFollowSupport = originalCloseFile
		readAllNoFollowSupport = originalReadAll
	}
}

func resetOpenFileNoFollowSupportProbe() {
	openFileNoFollowSupportedOnce = sync.Once{}
	openFileNoFollowSupported = false
}

func TestOpenFileNoFollowSupportedFalseWhenRootCloseFails(t *testing.T) {
	restore := stubOpenFileNoFollowSupportProbes(t)
	t.Cleanup(restore)

	probeDir := t.TempDir()
	mkdirTempNoFollowSupport = func(string, string) (string, error) { return probeDir, nil }
	openRootFileNoFollowProbe = func(*os.Root, string) (*os.File, error) {
		return os.Open(filepath.Join(probeDir, "probe.ndjson"))
	}
	closeRootNoFollowSupport = func(root *os.Root) error {
		_ = root.Close()
		return fmt.Errorf("close root failed")
	}

	if OpenFileNoFollowSupported() {
		t.Fatal("expected root close failure to disable no-follow support")
	}
}

func TestOpenFileNoFollowSupportedFalseWhenFileCloseFails(t *testing.T) {
	restore := stubOpenFileNoFollowSupportProbes(t)
	t.Cleanup(restore)

	probeDir := t.TempDir()
	mkdirTempNoFollowSupport = func(string, string) (string, error) { return probeDir, nil }
	openRootFileNoFollowProbe = func(*os.Root, string) (*os.File, error) {
		return os.Open(filepath.Join(probeDir, "probe.ndjson"))
	}
	closeFileNoFollowSupport = func(file *os.File) error {
		_ = file.Close()
		return fmt.Errorf("close file failed")
	}

	if OpenFileNoFollowSupported() {
		t.Fatal("expected file close failure to disable no-follow support")
	}
}

func TestOpenFileNoFollowSupportedFalseWhenProbeReadFails(t *testing.T) {
	restore := stubOpenFileNoFollowSupportProbes(t)
	t.Cleanup(restore)

	readAllNoFollowSupport = func(io.Reader) ([]byte, error) {
		return nil, fmt.Errorf("read failed")
	}

	if OpenFileNoFollowSupported() {
		t.Fatal("expected probe read failure to disable no-follow support")
	}
}
