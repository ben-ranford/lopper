//go:build !windows

package scripts

import (
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
	sentinel := preflightSentinel(t, "unrelated")
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

func TestHookPreflightInterruptDuringOwnershipCmpCleansAnchor(t *testing.T) {
	tmp := t.TempDir()
	stateDir := filepath.Join(tmp, "state")
	if err := os.Mkdir(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	binDir := filepath.Join(tmp, "bin")
	if err := os.Mkdir(binDir, 0o700); err != nil {
		t.Fatal(err)
	}
	entered := filepath.Join(tmp, "cmp-entered")
	anchorFile := filepath.Join(tmp, "anchor-pid")
	runnerFile := filepath.Join(tmp, "runner-pid")
	cmpPIDFile := filepath.Join(tmp, "cmp-pid")
	installHookPreflightInterruptShims(t, binDir, entered, anchorFile, runnerFile, cmpPIDFile)
	registerHookPreflightInterruptCleanup(t, stateDir, anchorFile)
	cmd := exec.Command("sh", "-c", `. ./hook-config-preflight.sh
trap cleanup_preflight_git EXIT
run_preflight_git sh -c ':'`, "sh")
	cmd.Dir = "."
	cmd.Env = append(os.Environ(), "TMPDIR="+stateDir, "PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	outputFile, err := os.Create(filepath.Join(tmp, "output"))
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdout, cmd.Stderr = outputFile, outputFile
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- errors.Join(cmd.Wait(), outputFile.Close()) }()
	waitForHookPreflightCmp(t, entered, done, cmd, filepath.Join(tmp, "output"))
	interruptHookPreflightPIDs(t, runnerFile, cmpPIDFile)
	awaitHookPreflightCancellation(t, done, cmd)
	assertHookPreflightStateRemoved(t, stateDir)
	assertHookPreflightAnchorStopped(t, anchorFile)
}

func installHookPreflightInterruptShims(t *testing.T, binDir, entered, anchorFile, runnerFile, cmpPIDFile string) {
	t.Helper()
	cmpPath, err := exec.LookPath("cmp")
	if err != nil {
		t.Fatal(err)
	}
	shim := fmt.Sprintf("#!/bin/sh\ncase \"$2\" in */running-jobs) if [ -f \"${2%%/running-jobs}/result\" ] && [ ! -f %s ]; then cat \"$2\" >%s; printf '%%s\\n' \"$$\" >%s; : >%s; exec sleep 60; fi ;; esac\nexec %s \"$@\"\n", shellQuote(entered), shellQuote(anchorFile), shellQuote(cmpPIDFile), shellQuote(entered), shellQuote(cmpPath))
	writeFileMode(t, filepath.Join(binDir, "cmp"), shim, 0o755)
	bashPath, err := exec.LookPath("bash")
	if err != nil {
		t.Fatal(err)
	}
	bashShim := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$$\" >%s\nexec %s \"$@\"\n", shellQuote(runnerFile), shellQuote(bashPath))
	writeFileMode(t, filepath.Join(binDir, "bash"), bashShim, 0o755)
}

func registerHookPreflightInterruptCleanup(t *testing.T, stateDir, anchorFile string) {
	t.Helper()
	t.Cleanup(func() {
		ownedJobFiles, err := filepath.Glob(filepath.Join(stateDir, "lopper-hooks-preflight.*", "running-jobs"))
		if err != nil {
			t.Errorf("find owned running-job files: %v", err)
		}
		expectedJobFiles, err := filepath.Glob(filepath.Join(stateDir, "lopper-hooks-preflight.*", "expected-job"))
		if err != nil {
			t.Errorf("find owned expected-job files: %v", err)
		}
		if len(expectedJobFiles) > 0 {
			ownedJobFiles = append(ownedJobFiles, expectedJobFiles...)
		}
		ownedJobFiles = append(ownedJobFiles, anchorFile)
		seen := make(map[int]bool)
		for _, path := range ownedJobFiles {
			data, err := os.ReadFile(path)
			if err == nil {
				killRecordedGroups(t, data, seen)
			}
		}
	})
}

func killRecordedGroups(t *testing.T, data []byte, seen map[int]bool) {
	t.Helper()
	for _, value := range strings.Fields(string(data)) {
		pid, err := strconv.Atoi(value)
		if err != nil || pid <= 0 || seen[pid] {
			continue
		}
		seen[pid] = true
		if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			t.Errorf("clean up owned anchor process group %d: %v", pid, err)
		}
	}
}

func waitForHookPreflightCmp(t *testing.T, entered string, done <-chan error, cmd *exec.Cmd, outputPath string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(entered); err == nil {
			return
		}
		select {
		case err := <-done:
			t.Fatalf("preflight exited before cmp blocked: %v", err)
		case <-time.After(10 * time.Millisecond):
		}
	}
	if _, err := os.Stat(entered); err == nil {
		return
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		t.Errorf("kill interrupted harness group: %v", err)
	}
	<-done
	data, err := os.ReadFile(outputPath)
	if err != nil {
		t.Errorf("read harness output: %v", err)
	}
	t.Fatalf("ownership cmp did not block: %s", data)
}

func interruptHookPreflightPIDs(t *testing.T, paths ...string) {
	t.Helper()
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read interrupted process PID from %s: %v", path, err)
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil || pid <= 0 {
			t.Fatalf("invalid interrupted process PID in %s: %q", path, data)
		}
		if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
			t.Fatalf("interrupt process %d from %s: %v", pid, path, err)
		}
	}
}

func awaitHookPreflightCancellation(t *testing.T, done <-chan error, cmd *exec.Cmd) {
	t.Helper()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("preflight succeeded after interruption during ownership cmp")
		}
	case <-time.After(5 * time.Second):
		if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			t.Errorf("kill blocked harness: %v", err)
		}
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("harness did not exit after kill")
		}
		t.Fatal("preflight did not exit after interruption")
	}
}

func assertHookPreflightStateRemoved(t *testing.T, stateDir string) {
	t.Helper()
	entries, err := os.ReadDir(stateDir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("interrupted preflight left state: %v %v", entries, err)
	}
}

func assertHookPreflightAnchorStopped(t *testing.T, anchorFile string) {
	t.Helper()
	anchor, err := os.ReadFile(anchorFile)
	if err != nil {
		t.Fatal(err)
	}
	pids := strings.Fields(string(anchor))
	if len(pids) != 1 {
		t.Fatalf("owned job listing = %q, want exactly one anchor PID", anchor)
	}
	pid, err := strconv.Atoi(pids[0])
	if err != nil || pid <= 0 {
		t.Fatalf("invalid owned anchor PID %q: %v", pids[0], err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		} else if err != nil {
			t.Fatalf("inspect owned anchor %d: %v", pid, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("owned anchor %d survived interruption", pid)
}
