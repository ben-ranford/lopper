//go:build unix

package runtime

import (
	"context"
	"errors"
	"os"
	"strings"
	"syscall"
	"testing"
)

// Windows has no process-group signal equivalent, so this test remains Unix-only.
func TestConfigureRuntimeCommandCancelMapsESRCHToProcessDone(t *testing.T) {
	cmd := shellCommand(context.Background(), "-c", "sleep 5")
	configureRuntimeCommand(cmd)

	if err := cmd.Start(); err != nil {
		t.Fatalf("start process: %v", err)
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		t.Fatalf("kill process group: %v", err)
	}
	if err := cmd.Wait(); err != nil && !errors.Is(err, os.ErrProcessDone) && !strings.Contains(err.Error(), "signal: killed") {
		t.Fatalf("wait process: %v", err)
	}

	err := cmd.Cancel()
	if !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("expected ESRCH to map to os.ErrProcessDone, got %v", err)
	}
}
