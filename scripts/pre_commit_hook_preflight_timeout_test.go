//go:build !windows

package scripts

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestHooksPreflightTimesOutOnBlockingGitConfigWithoutMutation(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(*testing.T, string) []string
	}{
		{"common FIFO", prepareCommonConfigFIFO(false)},
		{"common config symlink to FIFO", prepareCommonConfigFIFO(true)},
		{"global included FIFO", prepareIncludedFIFO("GIT_CONFIG_GLOBAL")},
		{"system included FIFO", prepareIncludedFIFO("GIT_CONFIG_SYSTEM")},
	} {
		for _, target := range []string{"hooks-install", "hooks-uninstall"} {
			t.Run(test.name+"/"+target, func(t *testing.T) {
				t.Parallel()
				assertHooksPreflightTimeout(t, test.name, target, test.setup)
			})
		}
	}
}

func TestHooksInstallPostWriteTimeoutRollsBackState(t *testing.T) {
	fixture := newPreflightTimeoutFixture(t, "hooks-install")
	tmpDir := t.TempDir()
	env := postWriteBlockingGitEnv(t)
	env = append(env, "TMPDIR="+tmpDir)
	output, err := runMakeWithPreflightTimeout(t, fixture.repoDir, "hooks-install", env...)
	if err == nil || !strings.Contains(string(output), "Timed out while reading Git preflight configuration") {
		t.Fatalf("post-write config timeout = %v\n%s", err, output)
	}
	assertPreflightFileEquals(t, fixture.configPath, fixture.configBefore)
	if _, err := os.Stat(filepath.Dir(fixture.managedHook)); !os.IsNotExist(err) {
		t.Fatalf("installer left managed hook state after post-write timeout: %v", err)
	}
	assertNoPreflightTimeoutTemps(t, tmpDir)
}

func TestHooksInstallBoundsRollbackWithBlockingConfig(t *testing.T) {
	for _, previousPath := range []string{"", ".githooks"} {
		t.Run("previous-path="+previousPath, func(t *testing.T) {
			t.Parallel()
			fixture := newPreflightTimeoutFixture(t, "hooks-install")
			if previousPath != "" {
				runCommand(t, fixture.repoDir, "git", "config", "--local", "core.hooksPath", previousPath)
			}
			tmpDir := t.TempDir()
			env := append(postWriteBlockingGitEnv(t), "HOOK_TIMEOUT_FIFO_CONFIG=1", "TMPDIR="+tmpDir)
			output, err := runMakeWithPreflightTimeout(t, fixture.repoDir, "hooks-install", env...)
			if err == nil || !strings.Contains(string(output), "Unable to restore core.hooksPath; retaining managed hook") {
				t.Fatalf("blocked rollback = %v\n%s", err, output)
			}
			assertCommonConfigFIFO(t, fixture.configPath, false)
			assertNoPreflightTimeoutTemps(t, tmpDir)
			if err := os.Remove(fixture.configPath); err != nil {
				t.Fatalf("remove blocking config FIFO: %v", err)
			}
			if err := os.Rename(fixture.configPath+".before-timeout", fixture.configPath); err != nil {
				t.Fatalf("restore activated config after timeout: %v", err)
			}
			runCommand(t, fixture.repoDir, fixture.managedHook)
		})
	}
}

func TestHooksInstallRollbackFailureRetainsUsableHook(t *testing.T) {
	for _, previousPath := range []string{"", ".githooks"} {
		t.Run("previous-path="+previousPath, func(t *testing.T) {
			t.Parallel()
			fixture := newPreflightTimeoutFixture(t, "hooks-install")
			if previousPath != "" {
				runCommand(t, fixture.repoDir, "git", "config", "--local", "core.hooksPath", previousPath)
			}
			env := append(postWriteBlockingGitEnv(t), "HOOK_TIMEOUT_LOCK_CONFIG=1")
			output, err := runMakeWithPreflightTimeout(t, fixture.repoDir, "hooks-install", env...)
			if err == nil || !strings.Contains(string(output), "Unable to restore core.hooksPath; retaining managed hook") {
				t.Fatalf("rollback failure = %v\n%s", err, output)
			}
			got := testutil.GitOutput(t, fixture.repoDir, "config", "--local", "--get", "core.hooksPath")
			if got != filepath.Dir(fixture.managedHook) {
				t.Fatalf("core.hooksPath = %q, want retained managed hook", got)
			}
			info, err := os.Stat(fixture.managedHook)
			if err != nil || info.Mode()&0o111 == 0 {
				t.Fatalf("retained hook is not executable: %v", err)
			}
			if err := os.Remove(fixture.configPath + ".lock"); err != nil {
				t.Fatalf("remove simulated config lock: %v", err)
			}
			runCommand(t, fixture.repoDir, fixture.managedHook)
		})
	}
}

func TestHooksInstallActivationFailureRemovesNewManagedHook(t *testing.T) {
	fixture := newPreflightTimeoutFixture(t, "hooks-install")
	if err := os.WriteFile(fixture.configPath+".lock", nil, 0o600); err != nil {
		t.Fatalf("create simulated config lock: %v", err)
	}
	output, err := runMakeWithPreflightTimeout(t, fixture.repoDir, "hooks-install")
	if err == nil || strings.Contains(string(output), "retaining managed hook") {
		t.Fatalf("activation failure = %v\n%s", err, output)
	}
	assertPreflightFileEquals(t, fixture.configPath, fixture.configBefore)
	if _, err := os.Stat(filepath.Dir(fixture.managedHook)); !os.IsNotExist(err) {
		t.Fatalf("installer left managed hook state after activation failed: %v", err)
	}
}

func TestHooksInstallInterruptCleansPreflightAndRollsBackState(t *testing.T) {
	fixture := newPreflightTimeoutFixture(t, "hooks-install")
	tmpDir := t.TempDir()
	env := postWriteBlockingGitEnv(t)
	env = append(env, "TMPDIR="+tmpDir)
	command := exec.Command("make", "hooks-install")
	command.Dir = fixture.repoDir
	command.Env = append(withoutGitEnv(), env...)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Start(); err != nil {
		t.Fatalf("start hooks-install: %v", err)
	}
	commandDone := make(chan error, 1)
	go func() { commandDone <- command.Wait() }()

	deadline := time.Now().Add(5 * time.Second)
	configWasUpdated := false
	for time.Now().Before(deadline) {
		config, err := os.ReadFile(fixture.configPath)
		if err == nil && strings.Contains(string(config), filepath.Dir(fixture.managedHook)) {
			configWasUpdated = true
			break
		}
		select {
		case err := <-commandDone:
			t.Fatalf("hooks-install exited before post-write read blocked: %v\n%s", err, output.String())
		case <-time.After(20 * time.Millisecond):
		}
	}
	if !configWasUpdated {
		killPreflightTestProcessGroup(t, command.Process.Pid, syscall.SIGKILL)
		<-commandDone
		t.Fatalf("hooks-install did not update config before blocking\n%s", output.String())
	}
	killPreflightTestProcessGroup(t, command.Process.Pid, syscall.SIGTERM)
	select {
	case err := <-commandDone:
		if err == nil {
			t.Fatal("hooks-install succeeded after interruption during post-write config read")
		}
	case <-time.After(5 * time.Second):
		killPreflightTestProcessGroup(t, command.Process.Pid, syscall.SIGKILL)
		<-commandDone
		t.Fatalf("hooks-install did not exit after interruption\n%s", output.String())
	}
	waitForPreflightInstallRollback(t, fixture, tmpDir)
}

func killPreflightTestProcessGroup(t *testing.T, pid int, signal syscall.Signal) {
	t.Helper()
	if err := syscall.Kill(-pid, signal); err != nil && !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("send %s to process group %d: %v", signal, pid, err)
	}
}

func postWriteBlockingGitEnv(t *testing.T) []string {
	t.Helper()
	shimDir := t.TempDir()
	fifo := filepath.Join(t.TempDir(), "post-write.fifo")
	counter := filepath.Join(t.TempDir(), "effective-reads")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatalf("create blocking config FIFO: %v", err)
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("find git: %v", err)
	}
	shim := fmt.Sprintf(`#!/bin/sh
if [ "$1" = config ] && [ "$2" = --get ] && [ "$3" = core.hooksPath ]; then
	count=0
	[ ! -f "$HOOK_TIMEOUT_COUNTER" ] || count=$(cat "$HOOK_TIMEOUT_COUNTER")
	count=$((count + 1))
	echo "$count" >"$HOOK_TIMEOUT_COUNTER"
	if [ "$count" -eq 2 ]; then
		if [ "${HOOK_TIMEOUT_LOCK_CONFIG-}" = 1 ]; then : >.git/config.lock; fi
		if [ "${HOOK_TIMEOUT_FIFO_CONFIG-}" = 1 ]; then
			mv .git/config .git/config.before-timeout
			mkfifo .git/config
		fi
		exec cat "$HOOK_TIMEOUT_FIFO"
	fi
fi
exec %s "$@"
`, shellQuote(realGit))
	writeFile(t, filepath.Join(shimDir, "git"), shim)
	if err := os.Chmod(filepath.Join(shimDir, "git"), 0o755); err != nil {
		t.Fatalf("make git shim executable: %v", err)
	}
	return []string{
		"PATH=" + shimDir + string(os.PathListSeparator) + os.Getenv("PATH"),
		"HOOK_TIMEOUT_COUNTER=" + counter,
		"HOOK_TIMEOUT_FIFO=" + fifo,
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
	}
}

func waitForPreflightInstallRollback(t *testing.T, fixture preflightTimeoutFixture, tmpDir string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		config, err := os.ReadFile(fixture.configPath)
		_, stateErr := os.Stat(filepath.Dir(fixture.managedHook))
		temps, globErr := filepath.Glob(filepath.Join(tmpDir, "lopper-hooks-*"))
		if err == nil && string(config) == string(fixture.configBefore) && os.IsNotExist(stateErr) && globErr == nil && len(temps) == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	assertPreflightFileEquals(t, fixture.configPath, fixture.configBefore)
	if _, err := os.Stat(filepath.Dir(fixture.managedHook)); !os.IsNotExist(err) {
		t.Fatalf("installer left managed hook state after interruption: %v", err)
	}
	assertNoPreflightTimeoutTemps(t, tmpDir)
}

type preflightTimeoutFixture struct {
	repoDir, configPath, managedHook string
	configBefore, hookBefore         []byte
	hookMode                         os.FileMode
}

func assertHooksPreflightTimeout(t *testing.T, name, target string, setup func(*testing.T, string) []string) {
	t.Helper()
	fixture := newPreflightTimeoutFixture(t, target)
	tmpDir := t.TempDir()
	output, err := runMakeWithPreflightTimeout(t, fixture.repoDir, target, append([]string{"TMPDIR=" + tmpDir}, setup(t, fixture.repoDir)...)...)
	if err == nil || !strings.Contains(string(output), "Timed out while reading Git preflight configuration") {
		t.Fatalf("%s with %s = %v\n%s", target, name, err, output)
	}
	assertPreflightConfigUnchanged(t, fixture, name)
	assertNoPreflightTimeoutTemps(t, tmpDir)
	assertPreflightHookUnchanged(t, fixture, target)
}

func newPreflightTimeoutFixture(t *testing.T, target string) preflightTimeoutFixture {
	t.Helper()
	repoDir := newHookFixture(t)
	commonDir := testutil.GitOutput(t, repoDir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	managedDir := filepath.Join(commonDir, "lopper-hooks")
	if target == "hooks-install" {
		if err := os.RemoveAll(managedDir); err != nil {
			t.Fatalf("remove installed hook fixture: %v", err)
		}
		runCommand(t, repoDir, "git", "config", "--local", "--unset", "core.hooksPath")
	}
	fixture := preflightTimeoutFixture{repoDir: repoDir, configPath: filepath.Join(commonDir, "config"), managedHook: filepath.Join(managedDir, "pre-commit")}
	fixture.configBefore = readPreflightFile(t, fixture.configPath)
	if target == "hooks-uninstall" {
		fixture.hookBefore = readPreflightFile(t, fixture.managedHook)
		info, err := os.Stat(fixture.managedHook)
		if err != nil {
			t.Fatalf("stat managed hook before preflight timeout: %v", err)
		}
		fixture.hookMode = info.Mode()
	}
	return fixture
}

func assertPreflightConfigUnchanged(t *testing.T, fixture preflightTimeoutFixture, name string) {
	t.Helper()
	if name == "common FIFO" || name == "common config symlink to FIFO" {
		assertCommonConfigFIFO(t, fixture.configPath, name == "common config symlink to FIFO")
		return
	}
	assertPreflightFileEquals(t, fixture.configPath, fixture.configBefore)
}

func assertPreflightHookUnchanged(t *testing.T, fixture preflightTimeoutFixture, target string) {
	t.Helper()
	if target == "hooks-install" {
		if _, err := os.Stat(filepath.Dir(fixture.managedHook)); !os.IsNotExist(err) {
			t.Fatalf("installer mutated managed hook state before preflight timeout: %v", err)
		}
		return
	}
	assertPreflightFileEquals(t, fixture.managedHook, fixture.hookBefore)
	info, err := os.Stat(fixture.managedHook)
	if err != nil || info.Mode() != fixture.hookMode {
		t.Fatalf("managed hook mode after preflight timeout = %v err=%v, want %v", info.Mode(), err, fixture.hookMode)
	}
}

func assertCommonConfigFIFO(t *testing.T, configPath string, symlink bool) {
	t.Helper()
	path := configPath
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("lstat common config: %v", err)
	}
	if symlink {
		if info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("common config is no longer a symlink")
		}
		path, err = os.Readlink(path)
		if err != nil {
			t.Fatalf("read common config symlink: %v", err)
		}
		info, err = os.Stat(path)
		if err != nil {
			t.Fatalf("stat common config FIFO target: %v", err)
		}
	}
	if info.Mode()&os.ModeNamedPipe == 0 {
		t.Fatalf("common config FIFO was modified: mode=%v", info.Mode())
	}
}

func prepareCommonConfigFIFO(symlink bool) func(*testing.T, string) []string {
	return func(t *testing.T, repoDir string) []string {
		t.Helper()
		config := filepath.Join(testutil.GitOutput(t, repoDir, "rev-parse", "--path-format=absolute", "--git-common-dir"), "config")
		if err := os.Remove(config); err != nil {
			t.Fatalf("remove common config: %v", err)
		}
		fifo := config + ".fifo"
		if err := syscall.Mkfifo(fifo, 0o600); err != nil {
			t.Fatalf("create common config FIFO: %v", err)
		}
		if symlink {
			if err := os.Symlink(fifo, config); err != nil {
				t.Fatalf("link common config FIFO: %v", err)
			}
		} else if err := os.Rename(fifo, config); err != nil {
			t.Fatalf("place common config FIFO: %v", err)
		}
		return nil
	}
}

func prepareIncludedFIFO(scope string) func(*testing.T, string) []string {
	return func(t *testing.T, _ string) []string {
		t.Helper()
		fifo := filepath.Join(t.TempDir(), "included.fifo")
		if err := syscall.Mkfifo(fifo, 0o600); err != nil {
			t.Fatalf("create included FIFO: %v", err)
		}
		config := filepath.Join(t.TempDir(), "gitconfig")
		writeFile(t, config, fmt.Sprintf("[include]\n\tpath = %s\n", fifo))
		if scope == "GIT_CONFIG_GLOBAL" {
			return []string{"GIT_CONFIG_GLOBAL=" + config, "GIT_CONFIG_NOSYSTEM=1"}
		}
		return []string{"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=0", "GIT_CONFIG_SYSTEM=" + config}
	}
}

func runMakeWithPreflightTimeout(t *testing.T, repoDir, target string, env ...string) ([]byte, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "make", target)
	command.Dir = repoDir
	command.Env = append(withoutGitEnv(), env...)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error { return syscall.Kill(-command.Process.Pid, syscall.SIGKILL) }
	command.WaitDelay = time.Second
	output, err := command.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("%s preflight timeout test exceeded deadline: %v\n%s", target, err, output)
	}
	return output, err
}

func readPreflightFile(t *testing.T, path string) []byte {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return contents
}

func assertPreflightFileEquals(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(want) {
		t.Fatalf("%s contents = %q err=%v, want %q", path, got, err, want)
	}
}

func assertNoPreflightTimeoutTemps(t *testing.T, tmpDir string) {
	t.Helper()
	temps, err := filepath.Glob(filepath.Join(tmpDir, "lopper-hooks-*"))
	if err != nil || len(temps) != 0 {
		t.Fatalf("preflight temps = %#v err=%v", temps, err)
	}
}
