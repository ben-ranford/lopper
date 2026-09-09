//go:build windows

package runtime

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
	"unsafe"

	win "golang.org/x/sys/windows"
)

const runtimeCommandWaitDelay = 100 * time.Millisecond

var runtimeCommandJobs sync.Map

type jobBasicAccountingInformation struct {
	totalUserTime             int64
	totalKernelTime           int64
	thisPeriodTotalUserTime   int64
	thisPeriodTotalKernelTime int64
	totalPageFaultCount       uint32
	totalProcesses            uint32
	activeProcesses           uint32
	totalTerminatedProcesses  uint32
}

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

// StartCommand starts a command suspended, attaches it to a Windows job object,
// then resumes it. This prevents child helpers from running before job
// membership makes them subject to cancellation.
func StartCommand(cmd *exec.Cmd) (func() error, error) {
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
	cleanup := func() error {
		runtimeCommandJobs.Delete(cmd)
		terminateErr := win.TerminateJobObject(job, 1)
		waitErr := waitForJobEmpty(job, cmd.WaitDelay)
		closeErr := win.CloseHandle(job)
		return errors.Join(terminateErr, waitErr, closeErr)
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= win.CREATE_SUSPENDED
	if err := cmd.Start(); err != nil {
		return nil, errors.Join(err, cleanup())
	}

	process, err := win.OpenProcess(win.PROCESS_SET_QUOTA|win.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		reapErr := terminateAndReapCommand(cmd)
		return nil, errors.Join(err, reapErr, cleanup())
	}
	defer win.CloseHandle(process)
	if err := win.AssignProcessToJobObject(job, process); err != nil {
		reapErr := terminateAndReapCommand(cmd)
		return nil, errors.Join(err, reapErr, cleanup())
	}
	runtimeCommandJobs.Store(cmd, job)
	if err := resumeCommandPrimaryThread(uint32(cmd.Process.Pid)); err != nil {
		reapErr := terminateJobAndReapCommand(job, cmd)
		return nil, errors.Join(err, reapErr, cleanup())
	}
	return cleanup, nil
}

func resumeCommandPrimaryThread(processID uint32) error {
	snapshot, err := win.CreateToolhelp32Snapshot(win.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return err
	}
	defer win.CloseHandle(snapshot)

	entry := win.ThreadEntry32{Size: uint32(unsafe.Sizeof(win.ThreadEntry32{}))}
	for err := win.Thread32First(snapshot, &entry); err == nil; err = win.Thread32Next(snapshot, &entry) {
		if entry.OwnerProcessID != processID {
			continue
		}
		thread, openErr := win.OpenThread(win.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
		if openErr != nil {
			return openErr
		}
		defer win.CloseHandle(thread)
		_, err = win.ResumeThread(thread)
		return err
	}
	return fmt.Errorf("find suspended primary thread for process %d: %w", processID, err)
}

func terminateAndReapCommand(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	killErr := cmd.Process.Kill()
	waitErr := cmd.Wait()
	if errors.Is(killErr, os.ErrProcessDone) {
		killErr = nil
	}
	return errors.Join(killErr, waitErr)
}

func terminateJobAndReapCommand(job win.Handle, cmd *exec.Cmd) error {
	terminateErr := win.TerminateJobObject(job, 1)
	waitErr := cmd.Wait()
	jobWaitErr := waitForJobEmpty(job, cmd.WaitDelay)
	return errors.Join(terminateErr, waitErr, jobWaitErr)
}

func waitForJobEmpty(job win.Handle, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = runtimeCommandWaitDelay
	}
	deadline := time.Now().Add(timeout)
	for {
		info := jobBasicAccountingInformation{}
		if err := win.QueryInformationJobObject(job, win.JobObjectBasicAccountingInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), nil); err != nil {
			return err
		}
		if info.activeProcesses == 0 {
			return nil
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("wait for Windows job to empty: %d active processes", info.activeProcesses)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
