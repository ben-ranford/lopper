//go:build windows

package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	win "golang.org/x/sys/windows"
)

// Observe through a separate handle: do not consume exec.Cmd's Wait or invoke
// cancellation. The wait and exit-code samples may straddle a natural exit.
func windowsParentSnapshot(pid uint32) string {
	handle, err := win.OpenProcess(win.SYNCHRONIZE|win.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return fmt.Sprintf("pid=%d open_error=%v", pid, err)
	}
	status, waitErr := win.WaitForSingleObject(handle, 0)
	var exitCode uint32
	exitErr := win.GetExitCodeProcess(handle, &exitCode)
	closeErr := win.CloseHandle(handle)
	state := "unknown"
	if waitErr == nil {
		switch status {
		case win.WAIT_OBJECT_0:
			state = "exited"
		case uint32(win.WAIT_TIMEOUT):
			state = "running"
		}
	}
	return fmt.Sprintf("pid=%d state=%s wait_status=%d wait_error=%v exit_code=%d exit_error=%v close_error=%v", pid, state, status, waitErr, exitCode, exitErr, closeErr)
}

func TestWindowsParentSnapshotOpenError(t *testing.T) {
	if got := windowsParentSnapshot(0); !strings.Contains(got, "open_error=") || strings.Contains(got, "open_error=<nil>") {
		t.Fatalf("expected process query failure, got %s", got)
	}
}

func TestWindowsParentSnapshotDoesNotWaitOrCancel(t *testing.T) {
	for _, running := range []bool{true, false} {
		t.Run(fmt.Sprintf("running=%t", running), func(t *testing.T) {
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			cmd := exec.CommandContext(ctx, executable, "-test.run=^TestWindowsDescendantHelper$")
			role := "observer-exit"
			if running {
				role = "child"
			}
			marker := filepath.Join(t.TempDir(), "child.pid")
			cmd.Env = append(os.Environ(), "LOPPER_WINDOWS_DESCENDANT_ROLE="+role, "LOPPER_WINDOWS_DESCENDANT_MARKER="+marker)
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() {
				cancel()
				if cmd.ProcessState == nil {
					_ = cmd.Wait()
				}
			}()
			wantState, wantExit := "running", uint32(259)
			if running {
				readChildPID(t, marker, windowsChildMarkerStartupTimeout)
			} else {
				wantState, wantExit = "exited", 17
				handle, err := win.OpenProcess(win.SYNCHRONIZE, false, uint32(cmd.Process.Pid))
				if err != nil {
					t.Fatal(err)
				}
				status, waitErr := win.WaitForSingleObject(handle, uint32(windowsChildMarkerStartupTimeout.Milliseconds()))
				closeErr := win.CloseHandle(handle)
				if waitErr != nil || closeErr != nil || status != win.WAIT_OBJECT_0 {
					t.Fatalf("wait for natural exit: status=%d wait_error=%v close_error=%v", status, waitErr, closeErr)
				}
			}
			for range 2 {
				got := windowsParentSnapshot(uint32(cmd.Process.Pid))
				if !strings.Contains(got, "state="+wantState+" ") || !strings.Contains(got, fmt.Sprintf("wait_error=<nil> exit_code=%d exit_error=<nil> close_error=<nil>", wantExit)) {
					t.Fatalf("unexpected parent observation: %s", got)
				}
				if ctx.Err() != nil || cmd.ProcessState != nil {
					t.Fatalf("observer consumed command lifecycle: context_error=%v process_state=%v", ctx.Err(), cmd.ProcessState)
				}
			}
			if !running {
				var exitErr *exec.ExitError
				if err := cmd.Wait(); !errors.As(err, &exitErr) || exitErr.ExitCode() != 17 {
					t.Fatalf("observer did not preserve ordinary reaping: %v", err)
				}
			}
		})
	}
}
