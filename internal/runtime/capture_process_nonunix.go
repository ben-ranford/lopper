//go:build !unix && !windows

package runtime

import (
	"os/exec"
	"time"
)

const runtimeCommandWaitDelay = 100 * time.Millisecond

func configureRuntimeCommand(cmd *exec.Cmd) {
	cmd.WaitDelay = runtimeCommandWaitDelay
}

// ConfigureCommandCancellation applies the portable cancellation behavior.
func ConfigureCommandCancellation(cmd *exec.Cmd) {
	configureRuntimeCommand(cmd)
}

// StartCommand starts a command configured with ConfigureCommandCancellation.
// These platforms do not allocate a process-tree handle that needs cleanup.
func StartCommand(cmd *exec.Cmd) (func() error, error) {
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return func() error { return nil }, nil
}
