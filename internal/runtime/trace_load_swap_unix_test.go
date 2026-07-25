//go:build linux

package runtime

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/ben-ranford/lopper/internal/safeio"
)

func TestLoadTraceRejectsLeafSwappedToSymlinkedFIFOBeforeOpen(t *testing.T) {
	rootDir := t.TempDir()
	tracePath := filepath.Join(rootDir, "trace.ndjson")
	if err := os.WriteFile(tracePath, []byte("{\"module\":\"lodash/map\"}\n"), 0o600); err != nil {
		t.Fatalf("write initial trace: %v", err)
	}
	fifoPath := filepath.Join(rootDir, "trace.fifo")
	if err := syscall.Mkfifo(fifoPath, 0o600); err != nil {
		t.Fatalf("mkfifo trace: %v", err)
	}

	restore := stubRuntimeTraceFileOpenState(os.Lstat, runtimeTraceOpenFileNoFollow, runtimeTraceOpenFileNoFollowOK, os.SameFile)
	t.Cleanup(restore)
	runtimeTraceBeforeOpen = func() {
		if err := os.Remove(tracePath); err != nil {
			t.Fatalf("remove original trace: %v", err)
		}
		if err := os.Symlink(filepath.Base(fifoPath), tracePath); err != nil {
			t.Fatalf("swap trace to fifo symlink: %v", err)
		}
	}

	errCh := make(chan error, 1)
	go func() {
		_, err := Load(tracePath)
		errCh <- err
	}()

	select {
	case err := <-errCh:
		if err == nil || !errors.Is(err, safeio.ErrNoFollowFinalComponent) {
			t.Fatalf("expected swapped fifo symlink rejection, got %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Load blocked after leaf was swapped to a symlinked FIFO")
	}
}

func TestLoadTraceRejectsLeafSwappedToSymlinkedRegularFileBeforeOpen(t *testing.T) {
	rootDir := t.TempDir()
	tracePath := filepath.Join(rootDir, "trace.ndjson")
	if err := os.WriteFile(tracePath, []byte("{\"module\":\"lodash/map\"}\n"), 0o600); err != nil {
		t.Fatalf("write initial trace: %v", err)
	}
	altTracePath := filepath.Join(rootDir, "trace-alt.ndjson")
	if err := os.WriteFile(altTracePath, []byte("{\"module\":\"left-pad\"}\n"), 0o600); err != nil {
		t.Fatalf("write alternate trace: %v", err)
	}

	restore := stubRuntimeTraceFileOpenState(os.Lstat, runtimeTraceOpenFileNoFollow, runtimeTraceOpenFileNoFollowOK, os.SameFile)
	t.Cleanup(restore)
	runtimeTraceBeforeOpen = func() {
		if err := os.Remove(tracePath); err != nil {
			t.Fatalf("remove original trace: %v", err)
		}
		if err := os.Symlink(filepath.Base(altTracePath), tracePath); err != nil {
			t.Fatalf("swap trace to regular-file symlink: %v", err)
		}
	}

	errCh := make(chan error, 1)
	go func() {
		_, err := Load(tracePath)
		errCh <- err
	}()

	select {
	case err := <-errCh:
		if err == nil || !errors.Is(err, safeio.ErrNoFollowFinalComponent) {
			t.Fatalf("expected swapped regular-file symlink rejection, got %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Load blocked after leaf was swapped to a symlinked regular file")
	}
}

func TestLoadTraceRejectsLeafSwappedDirectlyToFIFOBeforeOpen(t *testing.T) {
	rootDir := t.TempDir()
	tracePath := filepath.Join(rootDir, "trace.ndjson")
	if err := os.WriteFile(tracePath, []byte("{\"module\":\"lodash/map\"}\n"), 0o600); err != nil {
		t.Fatalf("write initial trace: %v", err)
	}
	fifoPath := filepath.Join(rootDir, "trace.fifo")
	if err := syscall.Mkfifo(fifoPath, 0o600); err != nil {
		t.Fatalf("mkfifo trace: %v", err)
	}

	restore := stubRuntimeTraceFileOpenState(os.Lstat, runtimeTraceOpenFileNoFollow, runtimeTraceOpenFileNoFollowOK, os.SameFile)
	t.Cleanup(restore)
	runtimeTraceBeforeOpen = func() {
		if err := os.Remove(tracePath); err != nil {
			t.Fatalf("remove original trace: %v", err)
		}
		if err := os.Rename(fifoPath, tracePath); err != nil {
			t.Fatalf("swap trace directly to fifo: %v", err)
		}
	}

	errCh := make(chan error, 1)
	go func() {
		_, err := Load(tracePath)
		errCh <- err
	}()

	select {
	case err := <-errCh:
		if err == nil || !errors.Is(err, safeio.ErrNoFollowFinalComponent) {
			t.Fatalf("expected direct fifo swap rejection, got %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Load blocked after leaf was swapped directly to a FIFO")
	}
}

func TestLoadTraceRejectsLeafSwappedDirectlyToSocketBeforeOpen(t *testing.T) {
	rootDir := t.TempDir()
	tracePath := filepath.Join(rootDir, "trace.ndjson")
	if err := os.WriteFile(tracePath, []byte("{\"module\":\"lodash/map\"}\n"), 0o600); err != nil {
		t.Fatalf("write initial trace: %v", err)
	}

	var listener net.Listener
	restore := stubRuntimeTraceFileOpenState(os.Lstat, runtimeTraceOpenFileNoFollow, runtimeTraceOpenFileNoFollowOK, os.SameFile)
	t.Cleanup(restore)
	t.Cleanup(func() {
		if listener != nil {
			_ = listener.Close()
		}
	})
	runtimeTraceBeforeOpen = func() {
		if err := os.Remove(tracePath); err != nil {
			t.Fatalf("remove original trace: %v", err)
		}
		var err error
		listener, err = net.Listen("unix", tracePath)
		if err != nil {
			t.Fatalf("swap trace directly to socket: %v", err)
		}
	}

	errCh := make(chan error, 1)
	go func() {
		_, err := Load(tracePath)
		errCh <- err
	}()

	select {
	case err := <-errCh:
		if err == nil || !errors.Is(err, safeio.ErrNoFollowFinalComponent) {
			t.Fatalf("expected direct socket swap rejection, got %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Load blocked after leaf was swapped directly to a socket")
	}
}
