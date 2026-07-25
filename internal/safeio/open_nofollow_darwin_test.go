//go:build darwin

package safeio

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestOpenParentRootNoFollowDarwinAliasJoinsLookupAndCloseErrors(t *testing.T) {
	lookupErr := errors.New("alias lookup failed")
	closeErr := errors.New("close volume root failed")
	withFileSystem(t, &fakeFileSystem{openRoot: func(name string) (Root, error) {
		if name != string(filepath.Separator) {
			t.Fatalf("unexpected volume root: %q", name)
		}
		return &fakeRoot{
			lstat: func(name string) (fs.FileInfo, error) {
				if name != "private" {
					t.Fatalf("unexpected alias component: %q", name)
				}
				return nil, lookupErr
			},
			close: func() error { return closeErr },
		}, nil
	}})

	root, err := openParentRootNoFollow(filepath.Join(string(filepath.Separator), "tmp", "trace-parent"))
	if root != nil {
		t.Fatal("expected failed alias traversal to return no root")
	}
	if !errors.Is(err, lookupErr) {
		t.Fatalf("expected alias lookup error, got %v", err)
	}
	if !errors.Is(err, closeErr) {
		t.Fatalf("expected volume root close error, got %v", err)
	}
}

func TestOpenParentRootNoFollowDarwinAliasClosesChildAfterParentCloseError(t *testing.T) {
	dirInfo := statTestPath(t, t.TempDir())
	parentCloseErr := errors.New("close parent alias root failed")
	childCloseErr := errors.New("close child alias root failed")
	childClosed := false

	child := &fakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			if name != "." {
				t.Fatalf("unexpected child lstat path: %q", name)
			}
			return dirInfo, nil
		},
		close: func() error {
			childClosed = true
			return childCloseErr
		},
	}
	withFileSystem(t, &fakeFileSystem{openRoot: func(name string) (Root, error) {
		if name != string(filepath.Separator) {
			t.Fatalf("unexpected volume root: %q", name)
		}
		return &fakeRoot{
			lstat: func(name string) (fs.FileInfo, error) {
				if name != "private" {
					t.Fatalf("unexpected alias component: %q", name)
				}
				return dirInfo, nil
			},
			openRoot: func(name string) (Root, error) {
				if name != "private" {
					t.Fatalf("unexpected opened alias component: %q", name)
				}
				return child, nil
			},
			close: func() error { return parentCloseErr },
		}, nil
	}})

	root, err := openParentRootNoFollow(filepath.Join(string(filepath.Separator), "tmp", "trace-parent"))
	if root != nil {
		t.Fatal("expected parent close failure to return no root")
	}
	if !errors.Is(err, parentCloseErr) {
		t.Fatalf("expected parent close error, got %v", err)
	}
	if !errors.Is(err, childCloseErr) {
		t.Fatalf("expected child close error, got %v", err)
	}
	if !childClosed {
		t.Fatal("expected child alias root to be closed")
	}
}

func TestOpenFileNoFollowDarwinRejectsFIFOLeafSwapWithoutBlocking(t *testing.T) {
	rootDir := t.TempDir()
	tracePath := filepath.Join(rootDir, "trace.ndjson")
	if err := os.WriteFile(tracePath, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write trace: %v", err)
	}

	withOpenFileNoFollowByVerificationBeforeOpen(t, func() {
		fifoPath := filepath.Join(rootDir, "trace.fifo")
		if err := syscall.Mkfifo(fifoPath, 0o600); err != nil {
			t.Fatalf("mkfifo trace: %v", err)
		}
		if err := os.Remove(tracePath); err != nil {
			t.Fatalf("remove original trace: %v", err)
		}
		if err := os.Rename(fifoPath, tracePath); err != nil {
			t.Fatalf("rename fifo into place: %v", err)
		}
	})

	err := openDarwinNoFollowWithTimeout(t, tracePath)
	if !errors.Is(err, ErrNoFollowFinalComponent) {
		t.Fatalf("expected FIFO swap rejection, got %v", err)
	}
}

func openDarwinNoFollowWithTimeout(t *testing.T, tracePath string) error {
	t.Helper()

	errCh := make(chan error, 1)
	go func() {
		file, err := OpenFileNoFollow(tracePath)
		if file != nil {
			if closeErr := file.Close(); closeErr != nil {
				errCh <- closeErr
				return
			}
		}
		errCh <- err
	}()

	select {
	case err := <-errCh:
		return err
	case <-time.After(500 * time.Millisecond):
		t.Fatal("OpenFileNoFollow blocked on FIFO leaf swap")
		return nil
	}
}

func TestOpenFileNoFollowDarwinUsesAtomicNoFollowFlags(t *testing.T) {
	originalOpenat := darwinOpenat
	t.Cleanup(func() {
		darwinOpenat = originalOpenat
	})

	var gotFlags int
	darwinOpenat = func(dirFD int, name string, flags int, mode uint32) (int, error) {
		gotFlags = flags
		return originalOpenat(dirFD, name, flags, mode)
	}

	tracePath := filepath.Join(t.TempDir(), "trace.ndjson")
	if err := os.WriteFile(tracePath, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write trace: %v", err)
	}
	file, err := OpenFileNoFollow(tracePath)
	if err != nil {
		t.Fatalf("OpenFileNoFollow(%q): %v", tracePath, err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close trace: %v", err)
	}

	requiredFlags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_NONBLOCK | unix.O_CLOEXEC
	if gotFlags&requiredFlags != requiredFlags {
		t.Fatalf("openat flags missing atomic no-follow requirements: got %#x want bits %#x", gotFlags, requiredFlags)
	}
}

func TestOpenFileNoFollowDarwinRetriesFstatAfterEINTR(t *testing.T) {
	originalFstat := darwinFstat
	t.Cleanup(func() {
		darwinFstat = originalFstat
	})

	fstatCalls := 0
	darwinFstat = func(fd int, stat *unix.Stat_t) error {
		fstatCalls++
		if fstatCalls == 1 {
			return unix.EINTR
		}
		return originalFstat(fd, stat)
	}

	tracePath := filepath.Join(t.TempDir(), "trace.ndjson")
	if err := os.WriteFile(tracePath, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write trace: %v", err)
	}
	file, err := OpenFileNoFollow(tracePath)
	if err != nil {
		t.Fatalf("OpenFileNoFollow(%q): %v", tracePath, err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close trace: %v", err)
	}
	if fstatCalls != 2 {
		t.Fatalf("expected fstat retry after EINTR, got %d calls", fstatCalls)
	}
}

func TestOpenDarwinRootFileNoFollowPreservesRootFDResolverError(t *testing.T) {
	originalResolver := darwinRootFDResolver
	t.Cleanup(func() {
		darwinRootFDResolver = originalResolver
	})

	resolverErr := errors.New("root fd unavailable")
	darwinRootFDResolver = func(*os.Root) (int, error) {
		return 0, resolverErr
	}

	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Fatalf("close root: %v", closeErr)
		}
	}()

	file, err := openDarwinRootFileNoFollow(root, "trace.ndjson")
	if file != nil {
		if closeErr := file.Close(); closeErr != nil {
			t.Fatalf("close unexpected file: %v", closeErr)
		}
		t.Fatal("expected root fd resolver failure to return no file")
	}
	if !errors.Is(err, ErrOpenFileNoFollowUnsupported) {
		t.Fatalf("expected unsupported sentinel, got %v", err)
	}
	if !errors.Is(err, resolverErr) {
		t.Fatalf("expected root fd resolver identity, got %v", err)
	}
}
