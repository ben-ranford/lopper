//go:build !windows

package runtime

import (
	"os/exec"
	"syscall"
)

func killRuntimeCommandProcessGroupForTest(cmd *exec.Cmd) error {
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
