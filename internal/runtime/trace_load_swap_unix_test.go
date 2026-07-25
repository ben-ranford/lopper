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

type traceSwapMutationCase struct {
	name   string
	mutate func(t *testing.T, tracePath string)
}

func TestLoadTraceRejectsLeafSwapsBeforeOpen(t *testing.T) {
	for _, tc := range traceSwapMutationCases() {
		t.Run(tc.name, func(t *testing.T) {
			assertTraceLeafSwapRejectedBeforeOpen(t, tc)
		})
	}
}

func traceSwapMutationCases() []traceSwapMutationCase {
	return []traceSwapMutationCase{
		{name: "symlinked fifo", mutate: mutateTraceToSymlinkedFIFO},
		{name: "symlinked regular file", mutate: mutateTraceToSymlinkedRegularFile},
		{name: "direct fifo", mutate: mutateTraceToDirectFIFO},
		{name: "direct socket", mutate: mutateTraceToDirectSocket},
	}
}

func mutateTraceToSymlinkedFIFO(t *testing.T, tracePath string) {
	t.Helper()

	fifoPath := filepath.Join(filepath.Dir(tracePath), "trace.fifo")
	createTraceFIFO(t, fifoPath)
	removeOriginalTrace(t, tracePath)
	if err := os.Symlink(filepath.Base(fifoPath), tracePath); err != nil {
		t.Fatalf("swap trace to fifo symlink: %v", err)
	}
}

func mutateTraceToSymlinkedRegularFile(t *testing.T, tracePath string) {
	t.Helper()

	altTracePath := filepath.Join(filepath.Dir(tracePath), "trace-alt.ndjson")
	if err := os.WriteFile(altTracePath, []byte("{\"module\":\"left-pad\"}\n"), 0o600); err != nil {
		t.Fatalf("write alternate trace: %v", err)
	}
	removeOriginalTrace(t, tracePath)
	if err := os.Symlink(filepath.Base(altTracePath), tracePath); err != nil {
		t.Fatalf("swap trace to regular-file symlink: %v", err)
	}
}

func mutateTraceToDirectFIFO(t *testing.T, tracePath string) {
	t.Helper()

	fifoPath := filepath.Join(filepath.Dir(tracePath), "trace.fifo")
	createTraceFIFO(t, fifoPath)
	removeOriginalTrace(t, tracePath)
	if err := os.Rename(fifoPath, tracePath); err != nil {
		t.Fatalf("swap trace directly to fifo: %v", err)
	}
}

func mutateTraceToDirectSocket(t *testing.T, tracePath string) {
	t.Helper()

	removeOriginalTrace(t, tracePath)
	listener, err := net.Listen("unix", tracePath)
	if err != nil {
		t.Fatalf("swap trace directly to socket: %v", err)
	}
	t.Cleanup(func() {
		_ = listener.Close()
	})
}

func createTraceFIFO(t *testing.T, fifoPath string) {
	t.Helper()

	if err := syscall.Mkfifo(fifoPath, 0o600); err != nil {
		t.Fatalf("mkfifo trace: %v", err)
	}
}

func removeOriginalTrace(t *testing.T, tracePath string) {
	t.Helper()

	if err := os.Remove(tracePath); err != nil {
		t.Fatalf("remove original trace: %v", err)
	}
}

func assertTraceLeafSwapRejectedBeforeOpen(t *testing.T, tc traceSwapMutationCase) {
	t.Helper()

	rootDir := t.TempDir()
	tracePath := filepath.Join(rootDir, "trace.ndjson")
	if err := os.WriteFile(tracePath, []byte("{\"module\":\"lodash/map\"}\n"), 0o600); err != nil {
		t.Fatalf("write initial trace: %v", err)
	}

	restore := stubRuntimeTraceFileOpenState(os.Lstat, runtimeTraceOpenFileNoFollow, runtimeTraceOpenFileNoFollowOK, os.SameFile)
	t.Cleanup(restore)
	runtimeTraceBeforeOpen = func() {
		tc.mutate(t, tracePath)
	}

	errCh := make(chan error, 1)
	go func() {
		_, err := Load(tracePath)
		errCh <- err
	}()

	select {
	case err := <-errCh:
		if err == nil || !errors.Is(err, safeio.ErrNoFollowFinalComponent) {
			t.Fatalf("expected %s rejection, got %v", tc.name, err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("Load blocked after %s leaf swap", tc.name)
	}
}
