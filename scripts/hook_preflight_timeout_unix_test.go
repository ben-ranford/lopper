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
	tmp := t.TempDir()
	pidsFile := filepath.Join(tmp, "reader-pids")
	reader := filepath.Join(tmp, "reader")
	writeFileMode(t, reader, `#!/bin/sh
trap '' TERM
printf '%s\n' "$$" >> "$1"
sh -c 'trap "" TERM; printf "%s\n" "$$" >> "$1"; sleep 60 & printf "%s\n" "$!" >> "$1"; wait' sh "$1" &
wait
`, 0o755)
	stateDir := filepath.Join(tmp, "state")
	if err := os.Mkdir(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
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
	if ctx.Err() != nil || !errors.As(err, &exitErr) || exitErr.ExitCode() != 124 || !strings.Contains(string(output), "Timed out while reading Git preflight configuration") {
		t.Fatalf("TERM-ignoring reader was not bounded: %v (context: %v)\n%s", err, ctx.Err(), output)
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
		defer func() {
			if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
				t.Errorf("clean up reader descendant %d: %v", pid, err)
			}
		}()
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

func TestHookPreflightProcessTreeFormats(t *testing.T) {
	for _, listing := range []string{
		"UID PID PPID C STIME TTY TIME CMD\nben 10 1 0 now ? 0 reader\nben 11 10 0 now ? 0 child\nben 12 11 0 now ? 0 grandchild\nben 20 1 0 now ? 0 unrelated\n",
		"PID PPID TTY UID STIME COMMAND\n10 1 ? 1000 now reader\nI 11 10 ? 1000 now child\n12 11 ? 1000 now grandchild\n20 1 ? 1000 now unrelated\n",
	} {
		cmd := exec.Command("sh", "-c", `. ./hook-config-preflight.sh
ps() { printf '%s' "$PREFLIGHT_PS_LISTING"; }
preflight_process_tree 10
`)
		// Pass the listing through the environment without interpolating shell code.
		cmd.Env = append(os.Environ(), "PREFLIGHT_PS_LISTING="+listing)
		output, err := cmd.CombinedOutput()
		if err != nil || string(output) != "12\n11\n10\n" {
			t.Fatalf("process tree: %v\n%s", err, output)
		}
	}
}
