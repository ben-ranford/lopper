//go:build linux

package safeio

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
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

	rootDir := t.TempDir()
	probePath := filepath.Join(rootDir, "trace.ndjson")
	if err := os.WriteFile(probePath, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write probe: %v", err)
	}
	openNoFollowProbePath = func() (string, error) { return probePath, nil }
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing-tmpdir"))

	if !OpenFileNoFollowSupported() {
		t.Fatal("expected support probe to succeed without TMPDIR writes")
	}
}

func TestOpenFileNoFollowWorksWhenSupportProbePathIsMissing(t *testing.T) {
	restore := stubOpenFileNoFollowSupportProbes(t)
	t.Cleanup(restore)

	tracePath := mustRegularProbePath(t)
	openNoFollowProbePath = func() (string, error) {
		return "", fs.ErrNotExist
	}

	if OpenFileNoFollowSupported() {
		t.Fatal("expected missing probe path to remain non-definitive")
	}

	file, err := OpenFileNoFollow(tracePath)
	if err != nil {
		t.Fatalf("OpenFileNoFollow(%q): %v", tracePath, err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close file: %v", err)
	}
}

func TestOpenFileNoFollowSupportedDoesNotCacheOpenRootPermissionError(t *testing.T) {
	restore := stubOpenFileNoFollowSupportProbes(t)
	t.Cleanup(restore)

	probePath := mustRegularProbePath(t)
	openNoFollowProbePath = func() (string, error) { return probePath, nil }
	attempts := 0
	openNoFollowProbeRoot = func(name string) (*os.Root, error) {
		attempts++
		if attempts == 1 {
			return nil, os.ErrPermission
		}
		return os.OpenRoot(name)
	}

	if OpenFileNoFollowSupported() {
		t.Fatal("expected initial probe-root permission failure to fail closed")
	}
	if !OpenFileNoFollowSupported() {
		t.Fatal("expected later support result after non-definitive probe-root error")
	}
	if attempts != 2 {
		t.Fatalf("expected probe-root to retry after non-definitive error, got %d attempts", attempts)
	}
}

func TestOpenFileNoFollowSupportedConcurrentNonDefinitiveResolverFailureDoesNotPoisonCache(t *testing.T) {
	restore := stubOpenFileNoFollowSupportProbes(t)
	t.Cleanup(restore)

	probePath := mustRegularProbePath(t)
	openNoFollowProbePath = func() (string, error) { return probePath, nil }

	rootDirFile, err := os.Open(filepath.Dir(probePath))
	if err != nil {
		t.Fatalf("open probe root dir: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := rootDirFile.Close(); closeErr != nil {
			t.Fatalf("close probe root dir: %v", closeErr)
		}
	})

	var attempts atomic.Int32
	osRootFDResolver = func(*os.Root) (int, error) {
		if attempts.Add(1) == 1 {
			return 0, errors.New("temporary root fd resolver failure")
		}
		return int(rootDirFile.Fd()), nil
	}

	start := make(chan struct{})
	results := make(chan bool, 8)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results <- OpenFileNoFollowSupported()
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	sawFalse := false
	sawTrue := false
	for supported := range results {
		if supported {
			sawTrue = true
			continue
		}
		sawFalse = true
	}
	if !sawFalse || !sawTrue {
		t.Fatalf("expected mixed results while cache recovered from non-definitive failure, got false=%v true=%v", sawFalse, sawTrue)
	}
	if !OpenFileNoFollowSupported() {
		t.Fatal("expected supported result to remain cached after concurrent recovery")
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
		return mustRegularProbePath(t), nil
	}

	if OpenFileNoFollowSupported() {
		t.Fatal("expected support probe to fail when root fd extraction is unavailable")
	}
}

func TestOpenFileNoFollowSupportedDoesNotCacheOrdinaryRootFDResolverFailure(t *testing.T) {
	restore := stubOpenFileNoFollowSupportProbes(t)
	t.Cleanup(restore)

	probePath := mustRegularProbePath(t)
	openNoFollowProbePath = func() (string, error) { return probePath, nil }

	rootDirFile, err := os.Open(filepath.Dir(probePath))
	if err != nil {
		t.Fatalf("open probe root dir: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := rootDirFile.Close(); closeErr != nil {
			t.Fatalf("close probe root dir: %v", closeErr)
		}
	})

	temporaryResolverErr := errors.New("temporary root fd resolver failure")
	attempts := 0
	osRootFDResolver = func(*os.Root) (int, error) {
		attempts++
		if attempts == 1 {
			return 0, temporaryResolverErr
		}
		return int(rootDirFile.Fd()), nil
	}

	if OpenFileNoFollowSupported() {
		t.Fatal("expected ordinary root fd resolver failure to fail closed")
	}
	if !OpenFileNoFollowSupported() {
		t.Fatal("expected ordinary root fd resolver failure not to poison the cache")
	}
	if !OpenFileNoFollowSupported() {
		t.Fatal("expected confirmed support result to be cached")
	}
	if attempts != 2 {
		t.Fatalf("expected resolver retry before caching support, got %d attempts", attempts)
	}
}

func TestOpenFileNoFollowSupportedCachesConfirmedRootFDResolverUnsupported(t *testing.T) {
	restore := stubOpenFileNoFollowSupportProbes(t)
	t.Cleanup(restore)

	openNoFollowProbePath = func() (string, error) {
		return mustRegularProbePath(t), nil
	}

	mechanismUnsupported := fmt.Errorf("%w on linux: root fd extraction unavailable", ErrOpenFileNoFollowUnsupported)
	attempts := 0
	osRootFDResolver = func(*os.Root) (int, error) {
		attempts++
		return 0, mechanismUnsupported
	}

	if OpenFileNoFollowSupported() {
		t.Fatal("expected confirmed unsupported resolver result to fail closed")
	}
	if OpenFileNoFollowSupported() {
		t.Fatal("expected confirmed unsupported resolver result to stay cached false")
	}
	if attempts != 1 {
		t.Fatalf("expected confirmed unsupported resolver result to be cached, got %d attempts", attempts)
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

	probePath := mustRegularProbePath(t)
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

	probePath := mustRegularProbePath(t)
	openNoFollowProbePath = func() (string, error) {
		return probePath, nil
	}
	realClose := closeNoFollowProbeRoot
	closeErr := errors.New("close support probe root")
	closeNoFollowProbeRoot = func(root *os.Root) error {
		return errors.Join(realClose(root), closeErr)
	}

	if supported, _ := probeOpenFileNoFollowSupport(); supported {
		t.Fatal("expected root close failure to make the support probe fail closed")
	}
}

func TestProbeOpenFileNoFollowSupportFailsWhenProbePathResolutionFails(t *testing.T) {
	restore := stubOpenFileNoFollowSupportProbes(t)
	t.Cleanup(restore)

	openNoFollowProbePath = func() (string, error) {
		return "", errors.New("resolve support probe path")
	}
	if supported, _ := probeOpenFileNoFollowSupport(); supported {
		t.Fatal("expected probe path resolution failure to report unsupported")
	}
}

func TestProbeOpenFileNoFollowSupportFailsWhenOpenedProbeFileCloseFails(t *testing.T) {
	restore := stubOpenFileNoFollowSupportProbes(t)
	t.Cleanup(restore)

	probePath := mustRegularProbePath(t)
	openNoFollowProbePath = func() (string, error) {
		return probePath, nil
	}
	closeNoFollowProbeFile = func(*os.File) error {
		return errors.New("close opened probe file")
	}

	if supported, cacheable := probeOpenFileNoFollowSupport(); supported || cacheable {
		t.Fatalf("expected probe file close failure to fail closed without caching, got supported=%v cacheable=%v", supported, cacheable)
	}
}

func TestDefaultOpenNoFollowProbePathUsesResolvedExecutable(t *testing.T) {
	restore := stubOpenFileNoFollowSupportProbes(t)
	t.Cleanup(restore)

	probePath := mustRegularProbePath(t)
	noFollowExecutablePath = func() (string, error) {
		return "/proc/self/exe", nil
	}
	noFollowEvalSymlinks = func(path string) (string, error) {
		if path != "/proc/self/exe" {
			t.Fatalf("unexpected executable path: %q", path)
		}
		return probePath, nil
	}

	got, err := defaultOpenNoFollowProbePath()
	if err != nil {
		t.Fatalf("defaultOpenNoFollowProbePath: %v", err)
	}
	if got != probePath {
		t.Fatalf("unexpected probe path: got %q want %q", got, probePath)
	}
}

func TestDefaultOpenNoFollowProbePathReturnsNotExistForNonRegularTarget(t *testing.T) {
	restore := stubOpenFileNoFollowSupportProbes(t)
	t.Cleanup(restore)

	noFollowExecutablePath = func() (string, error) {
		return "/proc/self/exe", nil
	}
	noFollowEvalSymlinks = func(string) (string, error) {
		return "/resolved/probe", nil
	}
	noFollowLstat = func(name string) (fs.FileInfo, error) {
		if name != "/resolved/probe" {
			t.Fatalf("unexpected lstat path: %q", name)
		}
		return &verificationFileInfo{name: filepath.Base(name), mode: os.ModeDir | 0o755}, nil
	}

	_, err := defaultOpenNoFollowProbePath()
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("expected fs.ErrNotExist for non-regular probe target, got %v", err)
	}
}

func TestDefaultOpenNoFollowProbePathPropagatesExecutableAndResolutionFailures(t *testing.T) {
	for _, tc := range []struct {
		name    string
		execErr error
		evalErr error
	}{
		{name: "executable failure", execErr: errors.New("executable unavailable")},
		{name: "eval symlinks failure", evalErr: errors.New("eval symlinks failed")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			restore := stubOpenFileNoFollowSupportProbes(t)
			t.Cleanup(restore)

			noFollowExecutablePath = func() (string, error) {
				if tc.execErr != nil {
					return "", tc.execErr
				}
				return "/proc/self/exe", nil
			}
			noFollowEvalSymlinks = func(string) (string, error) {
				if tc.evalErr != nil {
					return "", tc.evalErr
				}
				return mustRegularProbePath(t), nil
			}

			_, err := defaultOpenNoFollowProbePath()
			switch {
			case tc.execErr != nil && !errors.Is(err, tc.execErr):
				t.Fatalf("expected executable error %v, got %v", tc.execErr, err)
			case tc.evalErr != nil && !errors.Is(err, tc.evalErr):
				t.Fatalf("expected eval symlinks error %v, got %v", tc.evalErr, err)
			}
		})
	}
}

func stubOpenFileNoFollowSupportProbes(t *testing.T) func() {
	t.Helper()

	originalOpenat2 := openat2NoFollowProbe
	originalOpenat := openatNoFollowProbe
	originalProcOpen := procSelfFDReopen
	originalCloseFD := closeNoFollowFD
	originalNewFile := newNoFollowOSFile
	originalCloseProbeFile := closeNoFollowProbeFile
	originalExecutable := noFollowExecutablePath
	originalEvalSymlinks := noFollowEvalSymlinks
	originalLstat := noFollowLstat
	originalCloseRoot := closeNoFollowProbeRoot
	originalOpenRoot := openNoFollowProbeRoot
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
		closeNoFollowProbeFile = originalCloseProbeFile
		noFollowExecutablePath = originalExecutable
		noFollowEvalSymlinks = originalEvalSymlinks
		noFollowLstat = originalLstat
		closeNoFollowProbeRoot = originalCloseRoot
		openNoFollowProbeRoot = originalOpenRoot
		osRootFDResolver = originalRootFD
		openNoFollowProbe = originalProbe
		openNoFollowProbePath = originalProbePath
		resetOpenFileNoFollowSupportCache()
	}
}

func resetOpenFileNoFollowSupportCache() {
	openNoFollowSupportMu.Lock()
	defer openNoFollowSupportMu.Unlock()
	openNoFollowSupportCached = false
	openNoFollowSupported = false
}

func TestOpenFileNoFollowSupportedRetriesTransientProbeFailure(t *testing.T) {
	restore := stubOpenFileNoFollowSupportProbes(t)
	t.Cleanup(restore)

	probePath := mustRegularProbePath(t)
	openNoFollowProbePath = func() (string, error) { return probePath, nil }
	attempts := 0
	procSelfFDReopen = func(string, int, uint32) (int, error) {
		attempts++
		if attempts == 1 {
			return -1, unix.EMFILE
		}
		return unix.Open(probePath, unix.O_RDONLY|unix.O_CLOEXEC, 0)
	}

	if OpenFileNoFollowSupported() {
		t.Fatal("expected transient EMFILE probe failure to fail closed")
	}
	if !OpenFileNoFollowSupported() {
		t.Fatal("expected transient EMFILE probe failure not to be cached")
	}
	if !OpenFileNoFollowSupported() {
		t.Fatal("expected confirmed support result to be cached")
	}
	if attempts != 2 {
		t.Fatalf("expected one retry before caching support, got %d attempts", attempts)
	}
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

func mustRegularProbePath(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "probe.ndjson")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write probe: %v", err)
	}
	return path
}
