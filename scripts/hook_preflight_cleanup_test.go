//go:build !windows

package scripts

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestHookPreflightCleanupKeepsWatchdogUntilRunnerExits(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	watchdogFired := filepath.Join(t.TempDir(), "watchdog-fired")
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "sh", "-c", `. ./hook-config-preflight.sh
preflight_state_dir=$1
(
	interrupted=
	trap 'if [ -n "$interrupted" ]; then exit 0; fi; interrupted=1; printf x >"$preflight_state_dir/runner-interrupted"' TERM
	printf x >"$preflight_state_dir/runner-ready"
	while :; do sleep 0.01; done
) & preflight_runner_pid=$!
(
	trap 'exit 0' TERM
	while [ ! -f "$preflight_state_dir/runner-interrupted" ]; do sleep 0.01; done
	printf x >"$2"
	kill -TERM "$preflight_runner_pid"
) & preflight_watchdog_pid=$!
while [ ! -f "$preflight_state_dir/runner-ready" ]; do sleep 0.01; done
cleanup_preflight_git
`, "sh", stateDir, watchdogFired)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error { return syscall.Kill(-command.Process.Pid, syscall.SIGKILL) }
	command.WaitDelay = time.Second
	output, err := command.CombinedOutput()
	if err != nil || ctx.Err() != nil {
		t.Fatalf("cleanup did not retain the watchdog until its runner exited: err=%v context=%v output=%s", err, ctx.Err(), output)
	}
	if _, err := os.Stat(watchdogFired); err != nil {
		t.Fatalf("runner exited without the watchdog firing: %v", err)
	}
	if _, err := os.Stat(stateDir); !os.IsNotExist(err) {
		t.Fatalf("cleanup left preflight state: %v", err)
	}
}
