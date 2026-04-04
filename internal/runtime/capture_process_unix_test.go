//go:build unix

package runtime

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
)

func TestConfigureRuntimeCommand(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	configureRuntimeCommand(cmd)

	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setpgid {
		t.Fatalf("expected Setpgid to be enabled, got %#v", cmd.SysProcAttr)
	}
	if cmd.WaitDelay != runtimeCommandWaitDelay {
		t.Fatalf("expected wait delay %v, got %v", runtimeCommandWaitDelay, cmd.WaitDelay)
	}

	if err := cmd.Cancel(); !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("expected os.ErrProcessDone when process is nil, got %v", err)
	}

	cmd.Process = &os.Process{Pid: 999999}
	if err := cmd.Cancel(); !errors.Is(err, os.ErrProcessDone) && !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("expected missing process error, got %v", err)
	}
}

func TestConfigureRuntimeCommandCancelRunningProcess(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), "/bin/sh", "-c", "sleep 5")
	configureRuntimeCommand(cmd)

	if err := cmd.Start(); err != nil {
		t.Fatalf("start process: %v", err)
	}
	if err := cmd.Cancel(); err != nil {
		t.Fatalf("cancel running process: %v", err)
	}
	if err := cmd.Wait(); err != nil && !errors.Is(err, os.ErrProcessDone) && !strings.Contains(err.Error(), "signal: killed") {
		t.Fatalf("wait process: %v", err)
	}
}
