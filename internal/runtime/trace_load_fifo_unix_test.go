//go:build linux

package runtime

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ben-ranford/lopper/internal/safeio"
)

func TestLoadTraceRejectsFIFOPathBeforeOpen(t *testing.T) {
	fifoPath := filepath.Join(t.TempDir(), "trace.fifo")
	if err := syscall.Mkfifo(fifoPath, 0o600); err != nil {
		t.Fatalf("mkfifo trace: %v", err)
	}
	info, err := os.Lstat(fifoPath)
	if err != nil {
		t.Fatalf("lstat fifo: %v", err)
	}
	if info.Mode()&os.ModeNamedPipe == 0 {
		t.Fatalf("expected named pipe mode, got %v", info.Mode())
	}

	errCh := make(chan error, 1)
	go func() {
		_, err := Load(fifoPath)
		errCh <- err
	}()

	select {
	case err := <-errCh:
		if !safeio.OpenFileNoFollowSupported() {
			if !errors.Is(err, ErrTraceOpenUnsupported) {
				t.Fatalf("expected unsupported runtime trace open error, got %v", err)
			}
			return
		}
		if err == nil || !strings.Contains(err.Error(), "runtime trace path is not a regular file") {
			t.Fatalf("expected fifo rejection, got %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		writer, err := os.OpenFile(fifoPath, os.O_WRONLY, 0)
		if err != nil {
			t.Fatalf("release blocked fifo open: %v", err)
		}
		if err := writer.Close(); err != nil {
			t.Fatalf("close fifo writer: %v", err)
		}
		loadErr := <-errCh
		t.Fatalf("Load blocked on fifo path before returning %v", loadErr)
	}
}
