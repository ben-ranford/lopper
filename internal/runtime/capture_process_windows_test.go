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
	"strings"
	"syscall"
	"testing"
	"time"

	win "golang.org/x/sys/windows"
)

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
	defer cleanup()
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
	scriptPath := filepath.Join(tempDir, "spawn-child.ps1")
	if err := os.WriteFile(scriptPath, []byte(`param([string]$marker)
$child = Start-Process -FilePath 'cmd.exe' -ArgumentList '/c ping -n 30 127.0.0.1 >NUL' -PassThru
[System.IO.File]::WriteAllText($marker, $child.Id.ToString())
Wait-Process -Id $child.Id
`), 0o600); err != nil {
		t.Fatalf("write child process script: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", scriptPath, markerPath)
	ConfigureCommandCancellation(cmd)
	cleanup, err := StartCommand(cmd)
	if err != nil {
		t.Fatalf("start job command: %v", err)
	}
	defer cleanup()
	defer func() {
		cancel()
		if cmd.ProcessState == nil {
			if waitErr := cmd.Wait(); waitErr != nil {
				t.Logf("reap cancelled command: %v", waitErr)
			}
		}
	}()

	childPID := readChildPID(t, markerPath, 5*time.Second)
	cancel()
	if err := cmd.Wait(); err == nil {
		t.Fatal("expected cancelled command to return an error")
	}
	if err := waitForProcessExit(childPID, 2*time.Second); err != nil {
		t.Fatalf("wait for child process %d to exit: %v", childPID, err)
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

func waitForProcessExit(processID uint32, timeout time.Duration) error {
	process, err := win.OpenProcess(win.SYNCHRONIZE, false, processID)
	if err != nil {
		return nil
	}
	defer win.CloseHandle(process)

	status, err := win.WaitForSingleObject(process, uint32(timeout.Milliseconds()))
	if err != nil {
		return err
	}
	if status != win.WAIT_OBJECT_0 {
		return fmt.Errorf("process did not exit within %s", timeout)
	}
	return nil
}
