//go:build !windows

package scripts

import (
	"context"
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
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
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
