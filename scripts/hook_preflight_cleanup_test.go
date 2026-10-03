//go:build !windows

package scripts

import (
	"bytes"
	"context"
	"errors"
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

func TestHookPreflightWatchdogSurvivesGroupCancellation(t *testing.T) {
	for _, signal := range []syscall.Signal{syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM} {
		t.Run(signal.String(), func(t *testing.T) {
			t.Parallel()
			assertHookPreflightGroupCancellation(t, signal)
		})
	}
}

func assertHookPreflightGroupCancellation(t *testing.T, signal syscall.Signal) {
	t.Helper()
	stateDir, evidenceDir, binDir := t.TempDir(), t.TempDir(), t.TempDir()
	writeFileMode(t, filepath.Join(binDir, "bash"), `#!/bin/sh
while [ "$1" != -- ]; do shift; done
shift
state_dir=$1
printf '%s\n' "$$" >"$state_dir/runner-pid"
trap 'if [ -f "$state_dir/expired" ]; then printf x >"$HOOK_EXPIRY_WITNESS"; exit 143; fi' HUP INT TERM
printf x >"$HOOK_RUNNER_READY"
while :; do sleep 0.01; done
`, 0o755)
	ctx, cancel := context.WithTimeout(t.Context(), 16*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "sh", "-c", `. ./hook-config-preflight.sh
cleanup_preflight_temps() { cleanup_preflight_git; }
trap 'preflight_signal_status=129; preflight_handle_signal' HUP
trap 'preflight_signal_status=130; preflight_handle_signal' INT
trap 'preflight_signal_status=143; preflight_handle_signal' TERM
trap cleanup_preflight_git EXIT
run_preflight_git true
`)
	ready := filepath.Join(evidenceDir, "ready")
	expired := filepath.Join(evidenceDir, "expired")
	command.Env = append(os.Environ(), "TMPDIR="+stateDir, "PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"), "HOOK_RUNNER_READY="+ready, "HOOK_EXPIRY_WITNESS="+expired)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error { return syscall.Kill(-command.Process.Pid, syscall.SIGKILL) }
	command.WaitDelay = time.Second
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var commandErr error
	go func() { commandErr = command.Wait(); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	waitForHookRollbackAttempt(t, ready)
	killPreflightTestProcessGroup(t, command.Process.Pid, signal)
	<-done
	var exitErr *exec.ExitError
	if ctx.Err() != nil || !errors.As(commandErr, &exitErr) || exitErr.ExitCode() != 128+int(signal) {
		t.Fatalf("group cancellation was not bounded: err=%v context=%v output=%s", commandErr, ctx.Err(), output.String())
	}
	assertHookPreflightGroupCleanup(t, stateDir, expired, command.Process.Pid)
}

func assertHookPreflightGroupCleanup(t *testing.T, stateDir, expired string, pid int) {
	t.Helper()
	if _, err := os.Stat(expired); err != nil {
		t.Fatalf("runner exited without the actual watchdog expiring: %v", err)
	}
	if entries, err := os.ReadDir(stateDir); err != nil || len(entries) != 0 {
		t.Fatalf("group cancellation left preflight state: %v %v", entries, err)
	}
	if err := syscall.Kill(-pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("group cancellation left an owned process: %v", err)
	}
}
