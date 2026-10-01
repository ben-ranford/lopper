//go:build !windows

package scripts

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
)

func TestHookPreflightKillsTermIgnoringReaderTree(t *testing.T) {
	t.Parallel()
	assertPreflightReaderTimeout(t, `#!/bin/sh
trap '' TERM
printf '%s\n' "$$" >> "$1"
sh -c 'trap "" TERM; printf "%s\n" "$$" >> "$1"; sleep 60 & printf "%s\n" "$!" >> "$1"; wait' sh "$1" &
wait
`)
}

func TestHookPreflightKillsChildSpawnedDuringTermination(t *testing.T) {
	t.Parallel()
	assertPreflightReaderTimeout(t, `#!/bin/sh
trap 'sleep 60 & printf "%s\n" "$!" >> "$1"; exit' TERM
printf '%s\n' "$$" >> "$1"
sleep 60 & printf '%s\n' "$!" >> "$1"
wait
`)
}

func TestHookPreflightPropagatesReaderParentInterrupt(t *testing.T) {
	assertPreflightReaderTermination(t, `#!/bin/sh
printf '%s\n' "$$" >> "$1"
sleep 60 & printf '%s\n' "$!" >> "$1"
sleep 60 & printf '%s\n' "$!" >> "$1"
kill -TERM "$PPID"
wait
`, 5*time.Second, false)
}

func assertPreflightReaderTimeout(t *testing.T, source string) {
	t.Helper()
	assertPreflightReaderTermination(t, source, 15*time.Second, true)
}

func assertPreflightReaderTermination(t *testing.T, source string, timeout time.Duration, expectDiagnostic bool) {
	t.Helper()
	tmp := t.TempDir()
	pidsFile := filepath.Join(tmp, "reader-pids")
	cleanupPreflightReaders(t, pidsFile)
	reader := filepath.Join(tmp, "reader")
	writeFileMode(t, reader, source, 0o755)
	stateDir := filepath.Join(tmp, "state")
	if err := os.Mkdir(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", `. ./hook-config-preflight.sh
trap cleanup_preflight_git EXIT
run_preflight_git "$1" "$2"
`, "sh", reader, pidsFile)
	cmd.Env = append(os.Environ(), "TMPDIR="+stateDir)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	output, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	if ctx.Err() != nil || !errors.As(err, &exitErr) || exitErr.ExitCode() != 124 || expectDiagnostic && !strings.Contains(string(output), "Timed out while reading Git preflight configuration") {
		t.Fatalf("reader timeout was not bounded: %v (context: %v)\n%s", err, ctx.Err(), output)
	}
	assertPreflightReaderTreeStopped(t, pidsFile)
	entries, err := os.ReadDir(stateDir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("timeout left state files: %v %v", entries, err)
	}
}

func assertPreflightReaderTreeStopped(t *testing.T, pidsFile string) {
	t.Helper()
	pids, err := os.ReadFile(pidsFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(strings.Fields(string(pids))) != 3 {
		t.Fatalf("reader tree did not start: %s", pids)
	}
	for _, value := range strings.Fields(string(pids)) {
		pid, err := strconv.Atoi(value)
		if err != nil {
			t.Fatal(err)
		}
		state, err := exec.Command("ps", "-p", value, "-o", "stat=").Output()
		var psExit *exec.ExitError
		if err != nil && (!errors.As(err, &psExit) || psExit.ExitCode() != 1) {
			t.Fatalf("inspect reader descendant %d: %v", pid, err)
		}
		if status := strings.TrimSpace(string(state)); status != "" && !strings.HasPrefix(status, "Z") {
			t.Errorf("reader descendant %d survived timeout: %s", pid, status)
		}
	}
}

// Register before starting the fixture so a failed timeout or partial startup also
// reaps every PID the fixture managed to record.
func cleanupPreflightReaders(t *testing.T, pidsFile string) {
	t.Helper()
	t.Cleanup(func() {
		pids, err := os.ReadFile(pidsFile)
		if errors.Is(err, os.ErrNotExist) {
			return
		}
		if err != nil {
			t.Error(err)
			return
		}
		for _, value := range strings.Fields(string(pids)) {
			pid, err := strconv.Atoi(value)
			if err != nil || pid <= 0 {
				t.Errorf("invalid fixture PID %q", value)
				continue
			}
			if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
				t.Errorf("clean up reader descendant %d: %v", pid, err)
			}
		}
	})
}

func TestHookPreflightPreservesReaderResultAndProcessIsolation(t *testing.T) {
	tmp := t.TempDir()
	pidsFile := filepath.Join(tmp, "reader-pids")
	cleanupPreflightReaders(t, pidsFile)
	reader := filepath.Join(tmp, "reader")
	writeFileMode(t, reader, `#!/bin/sh
printf '%s\n' "$$" >> "$1"
sleep 60 & printf '%s\n' "$!" >> "$1"
sleep 60 & printf '%s\n' "$!" >> "$1"
printf '%s\n' "$2"
printf 'reader diagnostic\n' >&2
exit 7
`, 0o755)
	poison := filepath.Join(tmp, "bash-env")
	writeFileMode(t, poison, "exit 99\n", 0o644)
	stateDir := filepath.Join(tmp, "state")
	if err := os.Mkdir(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := exec.Command("sleep", "60")
	if err := sentinel.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := sentinel.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			t.Errorf("clean up unrelated sentinel: %v", err)
		}
		var exitErr *exec.ExitError
		if err := sentinel.Wait(); err != nil && !errors.As(err, &exitErr) {
			t.Errorf("wait for unrelated sentinel: %v", err)
		}
	})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	argument := "literal $(exit 88) 'argument'"
	cmd := exec.CommandContext(ctx, "sh", "-c", `. ./hook-config-preflight.sh
trap cleanup_preflight_git EXIT
run_preflight_git "$1" "$2" "$3"
`, "sh", reader, pidsFile, argument)
	cmd.Env = append(os.Environ(), "TMPDIR="+stateDir, "BASH_ENV="+poison)
	cmd.WaitDelay = time.Second
	output, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	if ctx.Err() != nil || !errors.As(err, &exitErr) || exitErr.ExitCode() != 7 {
		t.Fatalf("reader status changed: %v (context: %v)\n%s", err, ctx.Err(), output)
	}
	if string(output) != argument+"\nreader diagnostic\n" {
		t.Fatalf("reader output changed: %q", output)
	}
	assertPreflightReaderTreeStopped(t, pidsFile)
	state, err := exec.Command("ps", "-p", strconv.Itoa(sentinel.Process.Pid), "-o", "stat=").Output()
	if status := strings.TrimSpace(string(state)); err != nil || status == "" || strings.HasPrefix(status, "Z") {
		t.Fatalf("unrelated process was signaled: %v %q", err, state)
	}
	entries, err := os.ReadDir(stateDir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("normal completion left state files: %v %v", entries, err)
	}
}
