//go:build windows

package runtime

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"
)

func TestStartCommandConfiguresWindowsJobCancellation(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), "cmd", "/c", "exit 0")
	ConfigureCommandCancellation(cmd)

	cleanup, err := StartCommand(cmd)
	if err != nil {
		t.Fatalf("start job command: %v", err)
	}
	defer cleanup()
	if err := cmd.Wait(); err != nil {
		t.Fatalf("wait job command: %v", err)
	}
}

func TestWindowsCommandCancellationWithoutProcess(t *testing.T) {
	cmd := exec.Command("cmd", "/c", "exit 0")
	ConfigureCommandCancellation(cmd)
	if err := cmd.Cancel(); !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("expected missing-process cancellation error, got %v", err)
	}
}
