//go:build unix

package runtime

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestConfigureRuntimeCommandCancelPropagatesSignalErrors(t *testing.T) {
	originalSignal := runtimeProcessSignal
	originalKill := runtimeKillProcessGroup
	t.Cleanup(func() {
		runtimeProcessSignal = originalSignal
		runtimeKillProcessGroup = originalKill
	})

	expected := syscall.EPERM
	runtimeProcessSignal = func(*os.Process, syscall.Signal) error {
		return expected
	}

	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	cmd.Process = &os.Process{Pid: 42}
	configureRuntimeCommand(cmd)

	if err := cmd.Cancel(); !errors.Is(err, expected) {
		t.Fatalf("expected signal error %v, got %v", expected, err)
	}
}

func TestConfigureRuntimeCommandCancelPropagatesKillErrors(t *testing.T) {
	originalSignal := runtimeProcessSignal
	originalKill := runtimeKillProcessGroup
	t.Cleanup(func() {
		runtimeProcessSignal = originalSignal
		runtimeKillProcessGroup = originalKill
	})

	expected := syscall.EPERM
	runtimeProcessSignal = func(*os.Process, syscall.Signal) error {
		return nil
	}
	runtimeKillProcessGroup = func(int, syscall.Signal) error {
		return expected
	}

	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	cmd.Process = &os.Process{Pid: 42}
	configureRuntimeCommand(cmd)

	if err := cmd.Cancel(); !errors.Is(err, expected) {
		t.Fatalf("expected kill error %v, got %v", expected, err)
	}
}

func TestConfigureRuntimeCommand(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "exit 0")
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

func TestConfigureRuntimeCommandCancelRunningProcess(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), "/bin/sh", "-c", "sleep 5")
	configureRuntimeCommand(cmd)

	if err := cmd.Start(); err != nil {
		t.Fatalf("start process: %v", err)
	}
	if err := cmd.Cancel(); err != nil {
		t.Fatalf("cancel running process: %v", err)
	}
	if err := cmd.Wait(); err != nil && !errors.Is(err, os.ErrProcessDone) && !strings.Contains(err.Error(), "signal: killed") {
		t.Fatalf("wait process: %v", err)
	}
}

func TestStartCommandCleanupTerminatesProcessGroupAfterParentExit(t *testing.T) {
	markerPath := filepath.Join(t.TempDir(), "child.pid")
	cmd := exec.CommandContext(context.Background(), "/bin/sh", "-c", "sleep 30 & printf '%s' \"$!\" > \"$1\"", "runtime-helper", markerPath)
	configureRuntimeCommand(cmd)

	cleanup, err := StartCommand(cmd)
	if err != nil {
		t.Fatalf("start command: %v", err)
	}
	cleanedUp := false
	defer func() {
		if !cleanedUp {
			if err := cleanup(); err != nil {
				t.Errorf("cleanup command: %v", err)
			}
		}
	}()
	if err := cmd.Wait(); err != nil {
		t.Fatalf("wait for parent command: %v", err)
	}

	contents, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatalf("read child process ID: %v", err)
	}
	childPID, err := strconv.Atoi(string(contents))
	if err != nil {
		t.Fatalf("parse child process ID %q: %v", contents, err)
	}
	if err := syscall.Kill(childPID, 0); err != nil {
		t.Fatalf("expected child process %d to outlive its parent: %v", childPID, err)
	}

	if err := cleanup(); err != nil {
		t.Fatalf("cleanup process group: %v", err)
	}
	cleanedUp = true
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		terminated, err := testutil.ProcessTerminated(childPID)
		if err != nil {
			t.Fatalf("check child process %d: %v", childPID, err)
		}
		if terminated {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("child process %d outlived process group cleanup", childPID)
}

func TestRuntimeProcessTerminatedKeepsLiveProcess(t *testing.T) {
	terminated, err := testutil.ProcessTerminated(os.Getpid())
	if err != nil {
		t.Fatalf("check current process: %v", err)
	}
	if terminated {
		t.Fatal("expected current process to remain live")
	}
}
