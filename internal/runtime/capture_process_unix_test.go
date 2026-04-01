//go:build unix

package runtime

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"testing"
)

func TestConfigureRuntimeCommand(t *testing.T) {
	cmd := exec.Command("sh", "-c", "exit 0")
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
