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
	tests := []traceSwapMutationCase{
		{
			name: "symlinked fifo",
			mutate: func(t *testing.T, tracePath string) {
				fifoPath := filepath.Join(filepath.Dir(tracePath), "trace.fifo")
				if err := syscall.Mkfifo(fifoPath, 0o600); err != nil {
					t.Fatalf("mkfifo trace: %v", err)
				}
				if err := os.Remove(tracePath); err != nil {
					t.Fatalf("remove original trace: %v", err)
				}
				if err := os.Symlink(filepath.Base(fifoPath), tracePath); err != nil {
					t.Fatalf("swap trace to fifo symlink: %v", err)
				}
			},
		},
		{
			name: "symlinked regular file",
			mutate: func(t *testing.T, tracePath string) {
				altTracePath := filepath.Join(filepath.Dir(tracePath), "trace-alt.ndjson")
				if err := os.WriteFile(altTracePath, []byte("{\"module\":\"left-pad\"}\n"), 0o600); err != nil {
					t.Fatalf("write alternate trace: %v", err)
				}
				if err := os.Remove(tracePath); err != nil {
					t.Fatalf("remove original trace: %v", err)
				}
				if err := os.Symlink(filepath.Base(altTracePath), tracePath); err != nil {
					t.Fatalf("swap trace to regular-file symlink: %v", err)
				}
			},
		},
		{
			name: "direct fifo",
			mutate: func(t *testing.T, tracePath string) {
				fifoPath := filepath.Join(filepath.Dir(tracePath), "trace.fifo")
				if err := syscall.Mkfifo(fifoPath, 0o600); err != nil {
					t.Fatalf("mkfifo trace: %v", err)
				}
				if err := os.Remove(tracePath); err != nil {
					t.Fatalf("remove original trace: %v", err)
				}
				if err := os.Rename(fifoPath, tracePath); err != nil {
					t.Fatalf("swap trace directly to fifo: %v", err)
				}
			},
		},
		{
			name: "direct socket",
			mutate: func(t *testing.T, tracePath string) {
				if err := os.Remove(tracePath); err != nil {
					t.Fatalf("remove original trace: %v", err)
				}
				listener, err := net.Listen("unix", tracePath)
				if err != nil {
					t.Fatalf("swap trace directly to socket: %v", err)
				}
				t.Cleanup(func() {
					_ = listener.Close()
				})
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assertTraceLeafSwapRejectedBeforeOpen(t, tc)
		})
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
