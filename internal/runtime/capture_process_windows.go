//go:build windows

package runtime

import (
	"os"
	"os/exec"
	"sync"
	"time"
	"unsafe"

	win "golang.org/x/sys/windows"
)

const runtimeCommandWaitDelay = 100 * time.Millisecond

var runtimeCommandJobs sync.Map

func configureRuntimeCommand(cmd *exec.Cmd) {
	cmd.WaitDelay = runtimeCommandWaitDelay
	cmd.Cancel = func() error {
		job, ok := runtimeCommandJobs.Load(cmd)
		if !ok {
			if cmd.Process == nil {
				return os.ErrProcessDone
			}
			return cmd.Process.Kill()
		}
		return win.TerminateJobObject(job.(win.Handle), 1)
	}
}

// ConfigureCommandCancellation applies process-tree cancellation on Windows.
func ConfigureCommandCancellation(cmd *exec.Cmd) {
	configureRuntimeCommand(cmd)
}

// StartCommand starts a command in a Windows job object. Cancellation of that
// job terminates the direct command and any transport helpers it created.
func StartCommand(cmd *exec.Cmd) (func(), error) {
	job, err := win.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	info := win.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = win.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := win.SetInformationJobObject(job, win.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		_ = win.CloseHandle(job)
		return nil, err
	}
	cleanup := func() {
		runtimeCommandJobs.Delete(cmd)
		_ = win.CloseHandle(job)
	}
	runtimeCommandJobs.Store(cmd, job)
	if err := cmd.Start(); err != nil {
		cleanup()
		return nil, err
	}

	process, err := win.OpenProcess(win.PROCESS_SET_QUOTA|win.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		_ = cmd.Process.Kill()
		cleanup()
		return nil, err
	}
	defer win.CloseHandle(process)
	if err := win.AssignProcessToJobObject(job, process); err != nil {
		_ = cmd.Process.Kill()
		cleanup()
		return nil, err
	}
	return cleanup, nil
}
