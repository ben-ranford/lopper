//go:build windows

package runtime

import (
	"bytes"
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

	win "golang.org/x/sys/windows"
)

const windowsChildMarkerStartupTimeout = 10 * time.Second

func TestStartCommandConfiguresWindowsJobCancellation(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), "cmd", "/c", "echo resumed")
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: win.CREATE_NO_WINDOW}
	var output bytes.Buffer
	cmd.Stdout = &output
	ConfigureCommandCancellation(cmd)

	cleanup, err := StartCommand(cmd)
	if err != nil {
		t.Fatalf("start job command: %v", err)
	}
	defer func() {
		if err := cleanup(); err != nil {
			t.Errorf("cleanup job command: %v", err)
		}
	}()
	if err := cmd.Wait(); err != nil {
		t.Fatalf("wait job command: %v", err)
	}
	if !strings.Contains(output.String(), "resumed") {
		t.Fatalf("expected suspended command to resume, got %q", output.String())
	}
	if cmd.SysProcAttr.CreationFlags&win.CREATE_SUSPENDED == 0 || cmd.SysProcAttr.CreationFlags&win.CREATE_NO_WINDOW == 0 {
		t.Fatalf("expected suspended start to preserve creation flags, got %#x", cmd.SysProcAttr.CreationFlags)
	}
}

func TestWindowsCommandCancellationWithoutProcess(t *testing.T) {
	cmd := exec.Command("cmd", "/c", "exit 0")
	ConfigureCommandCancellation(cmd)
	if err := cmd.Cancel(); !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("expected missing-process cancellation error, got %v", err)
	}
}

func TestStartCommandCancellationTerminatesWindowsDescendant(t *testing.T) {
	tempDir := t.TempDir()
	markerPath := filepath.Join(tempDir, "child.pid")
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("locate test executable: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestWindowsDescendantHelper$")
	cmd.Env = append(os.Environ(), "LOPPER_WINDOWS_DESCENDANT_ROLE=parent", "LOPPER_WINDOWS_DESCENDANT_MARKER="+markerPath)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	ConfigureCommandCancellation(cmd)
	cleanup, err := StartCommand(cmd)
	if err != nil {
		t.Fatalf("start job command: %v", err)
	}
	cleanedUp := false
	defer func() {
		if !cleanedUp {
			if err := cleanup(); err != nil {
				t.Errorf("cleanup descendant command: %v", err)
			}
		}
	}()
	defer func() {
		cancel()
		if cmd.ProcessState == nil {
			if waitErr := cmd.Wait(); waitErr != nil {
				t.Logf("reap cancelled command: %v", waitErr)
			}
		}
		if t.Failed() {
			t.Logf("descendant helper output: %s", output.String())
		}
	}()

	childPID := readChildPID(t, markerPath, windowsChildMarkerStartupTimeout)
	childProcess, err := win.OpenProcess(win.SYNCHRONIZE, false, childPID)
	if err != nil {
		t.Fatalf("open child process %d for synchronization: %v", childPID, err)
	}
	defer win.CloseHandle(childProcess)

	cancel()
	if err := cmd.Wait(); err == nil {
		t.Fatal("expected cancelled command to return an error")
	}
	if err := cleanup(); err != nil {
		t.Fatalf("cleanup descendant command: %v", err)
	}
	cleanedUp = true
	status, err := win.WaitForSingleObject(childProcess, 0)
	if err != nil {
		t.Fatalf("check child process %d after cleanup: %v", childPID, err)
	}
	if status != win.WAIT_OBJECT_0 {
		t.Fatalf("child process %d remained active after cleanup (status %d)", childPID, status)
	}
}

func readChildPID(t *testing.T, markerPath string, timeout time.Duration) uint32 {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		contents, err := os.ReadFile(markerPath)
		if err == nil {
			pid, parseErr := strconv.ParseUint(strings.TrimSpace(string(contents)), 10, 32)
			if parseErr != nil {
				t.Fatalf("parse child process ID: %v", parseErr)
			}
			return uint32(pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("child process marker was not created within %s", timeout)
	return 0
}

// TestWindowsDescendantHelper runs only in subprocesses of the cancellation test.
// Using the already-built test executable avoids PowerShell and cmd startup.
func TestWindowsDescendantHelper(t *testing.T) {
	switch os.Getenv("LOPPER_WINDOWS_DESCENDANT_ROLE") {
	case "parent":
		executable, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		child := exec.Command(executable, "-test.run=^TestWindowsDescendantHelper$")
		child.Env = append(os.Environ(), "LOPPER_WINDOWS_DESCENDANT_ROLE=child")
		child.Stdout = os.Stdout
		child.Stderr = os.Stderr
		if err := child.Run(); err != nil {
			t.Fatalf("run descendant helper: %v", err)
		}
	case "child":
		marker := os.Getenv("LOPPER_WINDOWS_DESCENDANT_MARKER")
		// Publish only a complete PID after the descendant is actually running.
		if err := os.WriteFile(marker+".tmp", []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
			t.Fatalf("write descendant readiness: %v", err)
		}
		if err := os.Rename(marker+".tmp", marker); err != nil {
			t.Fatalf("publish descendant readiness: %v", err)
		}
		for {
			time.Sleep(time.Minute)
		}
	}
}
