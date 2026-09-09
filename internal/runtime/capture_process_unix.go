//go:build unix

package runtime

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

const runtimeCommandWaitDelay = 100 * time.Millisecond

var runtimeProcessSignal = func(process *os.Process, signal syscall.Signal) error {
	return process.Signal(signal)
}

var runtimeKillProcessGroup = syscall.Kill

func configureRuntimeCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = runtimeCommandWaitDelay
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		if err := runtimeProcessSignal(cmd.Process, syscall.Signal(0)); err != nil {
			if errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ESRCH) {
				return os.ErrProcessDone
			}
			return err
		}
		if err := runtimeKillProcessGroup(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
			if errors.Is(err, syscall.ESRCH) {
				return os.ErrProcessDone
			}
			return err
		}
		return nil
	}
}

// ConfigureCommandCancellation makes a command's cancellation terminate its
// process group where supported, preventing child helpers from outliving it.
func ConfigureCommandCancellation(cmd *exec.Cmd) {
	configureRuntimeCommand(cmd)
}

// StartCommand starts a command configured with ConfigureCommandCancellation.
func StartCommand(cmd *exec.Cmd) (func(), error) {
	return func() {}, cmd.Start()
}
