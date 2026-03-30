//go:build !unix

package runtime

import (
	"os/exec"
	"time"
)

const runtimeCommandWaitDelay = 100 * time.Millisecond

func configureRuntimeCommand(cmd *exec.Cmd) {
	cmd.WaitDelay = runtimeCommandWaitDelay
}
