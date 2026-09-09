//go:build windows

package runtime

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"

	win "golang.org/x/sys/windows"
)

func TestStartCommandConfiguresWindowsJobCancellation(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), "cmd", "/c", "echo resumed")
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: win.CREATE_NO_WINDOW}
	var output bytes.Buffer
	cmd.Stdout = &output
	ConfigureCommandCancellation(cmd)

	cleanup, err := StartCommand(cmd)
	if err != nil {
		t.Fatalf("start job command: %v", err)
	}
	defer cleanup()
	if err := cmd.Wait(); err != nil {
		t.Fatalf("wait job command: %v", err)
	}
	if !strings.Contains(output.String(), "resumed") {
		t.Fatalf("expected suspended command to resume, got %q", output.String())
	}
	if cmd.SysProcAttr.CreationFlags&win.CREATE_SUSPENDED == 0 || cmd.SysProcAttr.CreationFlags&win.CREATE_NO_WINDOW == 0 {
		t.Fatalf("expected suspended start to preserve creation flags, got %#x", cmd.SysProcAttr.CreationFlags)
	}
}

func TestWindowsCommandCancellationWithoutProcess(t *testing.T) {
	cmd := exec.Command("cmd", "/c", "exit 0")
	ConfigureCommandCancellation(cmd)
	if err := cmd.Cancel(); !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("expected missing-process cancellation error, got %v", err)
	}
}
