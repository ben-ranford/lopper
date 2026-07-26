//go:build linux

package safeio

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"syscall"
)

func TestOpenFileNoFollowRejectsLeafSymlink(t *testing.T) {
	rootDir := t.TempDir()
	targetPath := filepath.Join(rootDir, "target.txt")
	if err := os.WriteFile(targetPath, []byte("hello"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	linkPath := filepath.Join(rootDir, "leaf-link.txt")
	if err := os.Symlink(filepath.Base(targetPath), linkPath); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	if !OpenFileNoFollowSupported() {
		assertOpenFileNoFollowUnsupported(t, linkPath)
		return
	}

	file, err := OpenFileNoFollow(linkPath)
	if file != nil {
		if closeErr := file.Close(); closeErr != nil {
			t.Fatalf("close unexpected file: %v", closeErr)
		}
	}
	if err != nil && errors.Is(err, ErrNoFollowFinalComponent) {
		return
	}
	if err == nil {
		t.Fatalf("expected leaf symlink rejection, got %v", err)
	}
	t.Fatalf("expected leaf symlink rejection, got %v", err)
}

func TestOpenFileNoFollowRejectsDirectLeafFIFOReplacementWithoutBlocking(t *testing.T) {
	rootDir := t.TempDir()
	targetPath := filepath.Join(rootDir, "trace.ndjson")
	if err := os.WriteFile(targetPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}

	fifoPath := filepath.Join(rootDir, "trace.fifo")
	if err := syscall.Mkfifo(fifoPath, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	if err := os.Remove(targetPath); err != nil {
		t.Fatalf("remove original target: %v", err)
	}
	if err := os.Rename(fifoPath, targetPath); err != nil {
		t.Fatalf("rename fifo into place: %v", err)
	}

	if !OpenFileNoFollowSupported() {
		assertOpenFileNoFollowUnsupported(t, targetPath)
		return
	}

	openErr := openNoFollowWithTimeout(t, targetPath)
	if openErr == nil || !errors.Is(openErr, ErrNoFollowFinalComponent) {
		t.Fatalf("expected direct fifo replacement rejection, got %v", openErr)
	}
}

func TestOpenFileNoFollowRejectsDirectLeafSocketReplacementWithoutBlocking(t *testing.T) {
	rootDir := t.TempDir()
	targetPath := filepath.Join(rootDir, "trace.ndjson")
	if err := os.WriteFile(targetPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	if err := os.Remove(targetPath); err != nil {
		t.Fatalf("remove original target: %v", err)
	}

	listener, err := net.Listen("unix", targetPath)
	if err != nil {
		t.Fatalf("listen unix socket: %v", err)
	}
	defer func() {
		if closeErr := listener.Close(); closeErr != nil {
			t.Fatalf("close unix socket: %v", closeErr)
		}
	}()

	if !OpenFileNoFollowSupported() {
		assertOpenFileNoFollowUnsupported(t, targetPath)
		return
	}

	openErr := openNoFollowWithTimeout(t, targetPath)
	if openErr == nil || !errors.Is(openErr, ErrNoFollowFinalComponent) {
		t.Fatalf("expected direct socket replacement rejection, got %v", openErr)
	}
}

func assertOpenFileNoFollowUnsupported(t *testing.T, path string) {
	t.Helper()

	file, err := OpenFileNoFollow(path)
	if file != nil {
		if closeErr := file.Close(); closeErr != nil {
			t.Fatalf("close unexpected file: %v", closeErr)
		}
	}
	if err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("expected unsupported error, got %v", err)
	}
}

func openNoFollowWithTimeout(t *testing.T, path string) error {
	t.Helper()

	errCh := make(chan error, 1)
	go func() {
		file, err := OpenFileNoFollow(path)
		if file != nil {
			err = errors.Join(err, file.Close())
		}
		errCh <- err
	}()

	select {
	case err := <-errCh:
		return err
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("OpenFileNoFollow blocked for %s", path)
		return nil
	}
}
