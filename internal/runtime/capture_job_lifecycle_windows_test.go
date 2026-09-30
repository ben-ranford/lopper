//go:build windows

package runtime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	win "golang.org/x/sys/windows"
)

type windowsLifecycleProcess struct {
	pid    uint32
	handle win.Handle
}

type windowsLifecycleFixture struct {
	cmd         *exec.Cmd
	cleanup     func() error
	cleanupUsed bool
	descendants []windowsLifecycleProcess
}

func TestWindowsJobCleanupWaitsForDescendants(t *testing.T) {
	fixture := startWindowsLifecycleFixture(t)
	fixture.cleanupUsed = true
	if err := fixture.cleanup(); err != nil {
		t.Fatalf("direct cleanup: %v", err)
	}
	if err := windowsLifecycleExitStatus(fixture.descendants); err != nil {
		t.Fatal(err)
	}
}

func TestWindowsJobCleanupIsConcurrentAndRepeatable(t *testing.T) {
	fixture := startWindowsLifecycleFixture(t)
	fixture.cleanupUsed = true
	const callers = 8
	start := make(chan struct{})
	results := make(chan error, callers)
	for range callers {
		go func() {
			<-start
			cleanupErr := fixture.cleanup()
			// Check at every return, so later callers cannot conceal an early
			// return while a descendant's retained handle is still nonsignaled.
			results <- errors.Join(cleanupErr, windowsLifecycleExitStatus(fixture.descendants))
		}()
	}
	close(start)
	for range callers {
		if err := <-results; err != nil {
			t.Errorf("concurrent cleanup: %v", err)
		}
	}
	if err := fixture.cleanup(); err != nil {
		t.Errorf("repeated cleanup: %v", err)
	}
	if err := windowsLifecycleExitStatus(fixture.descendants); err != nil {
		t.Error(err)
	}
}

func TestWindowsJobRepeatedStartPreservesCommand(t *testing.T) {
	fixture := startWindowsLifecycleFixture(t)
	parent, err := win.OpenProcess(win.SYNCHRONIZE, false, uint32(fixture.cmd.Process.Pid))
	if err != nil {
		t.Fatalf("retain original parent: %v", err)
	}
	t.Cleanup(func() {
		if err := win.CloseHandle(parent); err != nil {
			t.Errorf("close parent handle: %v", err)
		}
	})
	repeatedCleanup, err := StartCommand(fixture.cmd)
	if repeatedCleanup != nil {
		t.Cleanup(func() { _ = repeatedCleanup() })
	}
	if err == nil {
		t.Fatal("expected repeated StartCommand to reject the live command")
	}
	assertWindowsLifecycleProcessLive(t, "original parent after rejected start", windowsLifecycleProcess{pid: uint32(fixture.cmd.Process.Pid), handle: parent})
	for _, process := range fixture.descendants {
		assertWindowsLifecycleProcessLive(t, "descendant after rejected start", process)
	}
	if err := fixture.cmd.Cancel(); err != nil {
		t.Fatalf("cancel original command after rejected start: %v", err)
	}
	// Cancel initiates asynchronous termination. Before invoking cleanup, use
	// its existing wait budget to prove it still targets the original job.
	// A lost command/job association would kill only the parent here.
	deadline := time.Now().Add(fixture.cmd.WaitDelay)
	for _, process := range fixture.descendants {
		remaining := uint32(max(0, time.Until(deadline).Milliseconds()))
		if status, err := win.WaitForSingleObject(process.handle, remaining); err != nil || status != win.WAIT_OBJECT_0 {
			t.Errorf("rejected start lost cancellation of descendant %d: status=%d error=%v", process.pid, status, err)
		}
	}
	fixture.cleanupUsed = true
	if err := fixture.cleanup(); err != nil {
		t.Fatalf("cleanup original command: %v", err)
	}
	if err := windowsLifecycleExitStatus(fixture.descendants); err != nil {
		t.Fatal(err)
	}
}

func TestWindowsJobCancellationRacesCleanup(t *testing.T) {
	fixture := startWindowsLifecycleFixture(t)
	parent, err := win.OpenProcess(win.SYNCHRONIZE, false, uint32(fixture.cmd.Process.Pid))
	if err != nil {
		t.Fatalf("retain parent for cancellation race: %v", err)
	}
	t.Cleanup(func() {
		if err := win.CloseHandle(parent); err != nil {
			t.Errorf("close parent handle: %v", err)
		}
	})
	fixture.cleanupUsed = true
	start := make(chan struct{})
	results := make(chan error, 2)
	go func() {
		<-start
		err := fixture.cmd.Cancel()
		if errors.Is(err, os.ErrProcessDone) {
			err = nil
		}
		// Cleanup can finish before Cancel loads the job. Windows rejects
		// TerminateProcess on an exited process with ERROR_ACCESS_DENIED.
		if errors.Is(err, win.ERROR_ACCESS_DENIED) {
			status, waitErr := win.WaitForSingleObject(parent, 0)
			if waitErr == nil && status == win.WAIT_OBJECT_0 {
				err = nil
			}
		}
		results <- err
	}()
	go func() {
		<-start
		cleanupErr := fixture.cleanup()
		results <- errors.Join(cleanupErr, windowsLifecycleExitStatus(fixture.descendants))
	}()
	close(start)
	for range 2 {
		if err := <-results; err != nil {
			t.Errorf("cancellation racing cleanup: %v", err)
		}
	}
	if err := fixture.cleanup(); err != nil {
		t.Errorf("cleanup after cancellation race: %v", err)
	}
	if err := windowsLifecycleExitStatus(fixture.descendants); err != nil {
		t.Error(err)
	}
}

func TestWindowsJobPreCanceledStartReleasesCommand(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cmd := windowsLifecycleCommand(t, ctx, "parent", t.TempDir())
	ConfigureCommandCancellation(cmd)
	t.Cleanup(func() {
		if cmd.Process != nil && cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	cleanup, err := StartCommand(cmd)
	if cleanup != nil {
		t.Cleanup(func() { _ = cleanup() })
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled start, got %v", err)
	}
	if cmd.Process != nil {
		t.Fatal("pre-canceled command acquired a process")
	}
	if err := cmd.Cancel(); !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("failed start retained command cancellation state: %v", err)
	}
}

func startWindowsLifecycleFixture(t *testing.T) *windowsLifecycleFixture {
	t.Helper()
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cmd := windowsLifecycleCommand(t, ctx, "parent", dir)
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	ConfigureCommandCancellation(cmd)
	cleanup, err := StartCommand(cmd)
	if err != nil {
		cancel()
		t.Fatalf("start lifecycle helper: %v", err)
	}
	fixture := &windowsLifecycleFixture{cmd: cmd, cleanup: cleanup}
	t.Cleanup(func() {
		cleanupWindowsLifecycleFixture(t, fixture, cancel, &output)
	})
	for _, role := range []string{"branch", "leaf", "grandchild"} {
		pid := readChildPID(t, filepath.Join(dir, role+".pid"), windowsChildMarkerStartupTimeout)
		handle, err := win.OpenProcess(win.SYNCHRONIZE|win.PROCESS_TERMINATE, false, pid)
		if err != nil {
			t.Fatalf("retain %s process %d: %v", role, pid, err)
		}
		process := windowsLifecycleProcess{pid: pid, handle: handle}
		fixture.descendants = append(fixture.descendants, process)
		assertWindowsLifecycleProcessLive(t, role+" before cleanup", process)
	}
	return fixture
}

func cleanupWindowsLifecycleFixture(t *testing.T, fixture *windowsLifecycleFixture, cancel context.CancelFunc, output *bytes.Buffer) {
	t.Helper()
	cancel()
	if !fixture.cleanupUsed {
		if err := fixture.cleanup(); err != nil {
			t.Errorf("fallback lifecycle cleanup: %v", err)
		}
	}
	// Ensure known descendants cannot leak even when a cleanup assertion
	// fails. These kills happen only after the strict return-state checks.
	for _, process := range fixture.descendants {
		status, _ := win.WaitForSingleObject(process.handle, 0)
		if status != win.WAIT_OBJECT_0 {
			_ = win.TerminateProcess(process.handle, 1)
		}
	}
	_ = fixture.cmd.Wait()
	for _, process := range fixture.descendants {
		if err := win.CloseHandle(process.handle); err != nil {
			t.Errorf("close descendant handle: %v", err)
		}
	}
	if t.Failed() {
		t.Logf("lifecycle helper output: %s", output.String())
	}
}

func assertWindowsLifecycleProcessLive(t *testing.T, description string, process windowsLifecycleProcess) {
	t.Helper()
	if status, err := win.WaitForSingleObject(process.handle, 0); err != nil || status != uint32(win.WAIT_TIMEOUT) {
		t.Fatalf("%s process %d was not live: status=%d error=%v", description, process.pid, status, err)
	}
}

func windowsLifecycleExitStatus(processes []windowsLifecycleProcess) error {
	var result error
	for _, process := range processes {
		status, err := win.WaitForSingleObject(process.handle, 0)
		if err != nil {
			result = errors.Join(result, fmt.Errorf("check descendant %d: %w", process.pid, err))
		} else if status != win.WAIT_OBJECT_0 {
			result = errors.Join(result, fmt.Errorf("descendant %d remained nonsignaled after cleanup: status %d", process.pid, status))
		}
	}
	return result
}

func windowsLifecycleCommand(t *testing.T, ctx context.Context, role, dir string) *exec.Cmd {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("locate lifecycle helper executable: %v", err)
	}
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestWindowsJobLifecycleHelper$")
	cmd.Env = append(os.Environ(), "LOPPER_WINDOWS_LIFECYCLE_ROLE="+role, "LOPPER_WINDOWS_LIFECYCLE_DIR="+dir)
	return cmd
}

func TestWindowsJobLifecycleHelper(t *testing.T) {
	role := os.Getenv("LOPPER_WINDOWS_LIFECYCLE_ROLE")
	if role == "" {
		return
	}
	dir := os.Getenv("LOPPER_WINDOWS_LIFECYCLE_DIR")
	var childRoles []string
	switch role {
	case "parent":
		childRoles = []string{"branch", "leaf"}
	case "branch":
		childRoles = []string{"grandchild"}
	}
	for _, childRole := range childRoles {
		child := windowsLifecycleCommand(t, context.Background(), childRole, dir)
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err := child.Start(); err != nil {
			t.Fatalf("start %s: %v", childRole, err)
		}
		defer func() {
			_ = child.Process.Kill()
			_ = child.Wait()
		}()
	}
	marker := filepath.Join(dir, role+".pid")
	if err := os.WriteFile(marker+".tmp", []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatalf("write %s readiness: %v", role, err)
	}
	if err := os.Rename(marker+".tmp", marker); err != nil {
		t.Fatalf("publish %s readiness: %v", role, err)
	}
	for {
		time.Sleep(time.Minute)
	}
}
