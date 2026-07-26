//go:build windows

package runtime

import "os/exec"

func killRuntimeCommandProcessGroupForTest(cmd *exec.Cmd) error {
	return cmd.Process.Kill()
}
