//go:build windows

package runtime

import (
	"errors"
	"fmt"
	"sync"
	"time"
	"unsafe"

	win "golang.org/x/sys/windows"
)

type runtimeJobProcess struct {
	id     uint32
	handle win.Handle
}

type runtimeJob struct {
	mu         sync.Mutex
	handle     win.Handle
	timeout    time.Duration
	processes  []runtimeJobProcess
	stopped    bool
	stopErr    error
	closed     bool
	cleanupErr error
}

// stop captures process objects before requesting asynchronous termination.
// The active-process limit closes admission first, so descendants cannot start
// new user code between the member snapshot and TerminateJobObject.
func (job *runtimeJob) stop() error {
	job.mu.Lock()
	defer job.mu.Unlock()
	return job.stopLocked(time.Now().Add(job.waitTimeout()))
}

func (job *runtimeJob) stopLocked(deadline time.Time) error {
	if job.stopped {
		return job.stopErr
	}
	job.stopped = true
	info := win.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = win.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE | win.JOB_OBJECT_LIMIT_ACTIVE_PROCESS
	_, limitErr := win.SetInformationJobObject(job.handle, win.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)))
	var captureErr error
	job.processes, captureErr = captureJobProcesses(job.handle, deadline)
	terminateErr := win.TerminateJobObject(job.handle, 1)
	job.stopErr = errors.Join(limitErr, captureErr, terminateErr)
	return job.stopErr
}

func (job *runtimeJob) cleanup() error {
	job.mu.Lock()
	defer job.mu.Unlock()
	if job.closed {
		return job.cleanupErr
	}
	deadline := time.Now().Add(job.waitTimeout())
	stopErr := job.stopLocked(deadline)
	var waitErr error
	for _, process := range job.processes {
		status, err := win.WaitForSingleObject(process.handle, remainingJobWaitMilliseconds(deadline))
		if err == nil && status != win.WAIT_OBJECT_0 {
			err = fmt.Errorf("wait for Windows job process %d: status %d", process.id, status)
		}
		waitErr = errors.Join(waitErr, err, win.CloseHandle(process.handle))
	}
	job.processes = nil
	emptyErr := waitForJobEmptyUntil(job.handle, deadline)
	closeErr := win.CloseHandle(job.handle)
	job.closed = true
	job.cleanupErr = errors.Join(stopErr, waitErr, emptyErr, closeErr)
	return job.cleanupErr
}

func remainingJobWaitMilliseconds(deadline time.Time) uint32 {
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return 0
	}
	milliseconds := remaining / time.Millisecond
	if remaining%time.Millisecond != 0 {
		milliseconds++
	}
	if milliseconds >= time.Duration(win.INFINITE) {
		return win.INFINITE - 1
	}
	return uint32(milliseconds)
}

func captureJobProcesses(job win.Handle, deadline time.Time) ([]runtimeJobProcess, error) {
	ids, err := jobProcessIDs(job, deadline)
	if err != nil {
		return nil, err
	}
	processes := make([]runtimeJobProcess, 0, len(ids))
	var captureErr error
	for _, id := range ids {
		if !time.Now().Before(deadline) {
			captureErr = errors.Join(captureErr, errors.New("capture Windows job processes: cancellation deadline exceeded"))
			break
		}
		process, err := openJobProcess(job, uint32(id))
		captureErr = errors.Join(captureErr, err)
		if process == 0 {
			continue
		}

		processes = append(processes, runtimeJobProcess{id: uint32(id), handle: process})
	}
	return processes, captureErr
}

func openJobProcess(job win.Handle, id uint32) (win.Handle, error) {
	process, err := win.OpenProcess(win.SYNCHRONIZE|win.PROCESS_QUERY_LIMITED_INFORMATION, false, id)
	if errors.Is(err, win.ERROR_INVALID_PARAMETER) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("open Windows job process %d: %w", id, err)
	}
	member, err := processInJob(process, job)
	if err == nil && member {
		return process, nil
	}
	if err == nil {
		err = checkExitedJobProcess(process, id)
	}
	return 0, errors.Join(err, win.CloseHandle(process))
}

func checkExitedJobProcess(process win.Handle, id uint32) error {
	status, err := win.WaitForSingleObject(process, 0)
	if err != nil {
		return err
	}
	if status != win.WAIT_OBJECT_0 {
		return fmt.Errorf("process %d changed Windows job membership before capture", id)
	}
	return nil
}

func jobProcessIDs(job win.Handle, deadline time.Time) ([]uintptr, error) {
	// Both header fields are DWORDs, followed by pointer-sized process IDs.
	headerWords := 8 / int(unsafe.Sizeof(uintptr(0)))
	count := 16
	for {
		if !time.Now().Before(deadline) {
			return nil, errors.New("enumerate Windows job processes: cancellation deadline exceeded")
		}
		if count > 1<<20 {
			return nil, errors.New("capture Windows job processes: process list exceeds limit")
		}
		data := make([]uintptr, headerWords+count)
		err := win.QueryInformationJobObject(job, win.JobObjectBasicProcessIdList, uintptr(unsafe.Pointer(&data[0])), uint32(len(data)*int(unsafe.Sizeof(uintptr(0)))), nil)
		header := (*[2]uint32)(unsafe.Pointer(&data[0]))
		if errors.Is(err, win.ERROR_MORE_DATA) {
			count = max(count*2, int(header[0]))
			continue
		}
		if err != nil {
			return nil, err
		}
		if header[1] > uint32(count) {
			return nil, errors.New("query Windows job processes: invalid process count")
		}
		return data[headerWords : headerWords+int(header[1])], nil
	}
}

var isProcessInJob = win.NewLazySystemDLL("kernel32.dll").NewProc("IsProcessInJob")

func processInJob(process, job win.Handle) (bool, error) {
	var member int32
	result, _, err := isProcessInJob.Call(uintptr(process), uintptr(job), uintptr(unsafe.Pointer(&member)))
	if result == 0 {
		return false, err
	}
	return member != 0, nil
}

func (job *runtimeJob) waitTimeout() time.Duration {
	if job.timeout <= 0 {
		return runtimeCommandWaitDelay
	}
	return job.timeout
}
