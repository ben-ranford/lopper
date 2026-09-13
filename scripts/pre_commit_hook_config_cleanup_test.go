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
)

func TestHooksInstallRefusesUnsafeManagedHookBeforeChangingDirectoryPermissions(t *testing.T) {
	t.Parallel()

	repoDir := newHookTestRepository(t)
	managedHook := managedHookPath(t, repoDir)
	managedDir := filepath.Dir(managedHook)
	if err := os.MkdirAll(managedDir, 0o755); err != nil {
		t.Fatalf("create managed directory: %v", err)
	}
	if err := os.Chmod(managedDir, 0o755); err != nil {
		t.Fatalf("set managed directory mode: %v", err)
	}
	if err := syscall.Mkfifo(managedHook, 0o600); err != nil {
		t.Fatalf("create managed hook FIFO: %v", err)
	}

	output, err := runMakeWithEnv(repoDir, "hooks-install")
	if err == nil || !strings.Contains(string(output), "Refusing to replace non-regular managed pre-commit hook") {
		t.Fatalf("install with FIFO managed hook = %v\n%s", err, output)
	}
	info, err := os.Stat(managedDir)
	if err != nil {
		t.Fatalf("stat managed directory: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o755 {
		t.Fatalf("managed directory mode = %o, want 755", got)
	}
	if temporaryHooks, err := filepath.Glob(filepath.Join(managedDir, ".pre-commit.tmp.*")); err != nil || len(temporaryHooks) != 0 {
		t.Fatalf("temporary hooks = %#v err=%v", temporaryHooks, err)
	}
}

func TestHooksUninstallRejectsNonRegularDormantWorktreeConfigBeforeSnapshot(t *testing.T) {
	t.Parallel()
	for _, useSymlink := range []bool{false, true} {
		t.Run(fmt.Sprintf("symlink=%t", useSymlink), func(t *testing.T) { assertNonRegularDormantConfigIsRejected(t, useSymlink) })
	}
}

func TestHooksRejectForeignWorktreeFIFOConfigBeforeQueries(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		target  string
		symlink bool
	}{{"hooks-install", false}, {"hooks-install", true}, {"hooks-uninstall", false}, {"hooks-uninstall", true}} {
		t.Run(fmt.Sprintf("%s/symlink=%t", test.target, test.symlink), func(t *testing.T) { assertForeignWorktreeFIFORefusal(t, test.target, test.symlink) })
	}
}

func assertForeignWorktreeFIFORefusal(t *testing.T, target string, symlink bool) {
	t.Helper()
	repoDir := newHookTestRepository(t)
	runCommand(t, repoDir, "git", "config", "extensions.worktreeConfig", "true")
	if target == "hooks-uninstall" {
		runCommand(t, repoDir, "make", "hooks-install")
	}
	linkedDir := filepath.Join(t.TempDir(), "linked")
	runCommand(t, repoDir, "git", "worktree", "add", linkedDir)
	runCommand(t, linkedDir, "git", "config", "--worktree", "core.hooksPath", ".githooks")
	foreignConfig := filepath.Join(gitOutput(t, linkedDir, "rev-parse", "--path-format=absolute", "--git-dir"), "config.worktree")
	if err := os.Remove(foreignConfig); err != nil {
		t.Fatalf("remove foreign worktree config: %v", err)
	}
	fifo := foreignConfig + ".fifo"
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatalf("create foreign worktree config FIFO: %v", err)
	}
	if symlink {
		if err := os.Symlink(fifo, foreignConfig); err != nil {
			t.Fatalf("link foreign worktree config FIFO: %v", err)
		}
	} else if err := os.Rename(fifo, foreignConfig); err != nil {
		t.Fatalf("move foreign worktree config FIFO: %v", err)
	}
	output, err := runMakeWithTimeout(t, repoDir, target)
	if err == nil || !strings.Contains(string(output), "another worktree") {
		t.Fatalf("%s with foreign FIFO config = %v\n%s", target, err, output)
	}
	managedHook := managedHookPath(t, repoDir)
	if target == "hooks-install" {
		if _, err := os.Stat(filepath.Dir(managedHook)); !os.IsNotExist(err) {
			t.Fatalf("installer mutated managed hook state before foreign FIFO refusal: %v", err)
		}
	} else if _, err := os.Stat(managedHook); err != nil {
		t.Fatalf("uninstaller removed managed hook before foreign FIFO refusal: %v", err)
	}
}

func runMakeWithTimeout(t *testing.T, repoDir, target string) ([]byte, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "make", target)
	command.Dir = repoDir
	command.Env = hookTestEnv()
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error { return syscall.Kill(-command.Process.Pid, syscall.SIGKILL) }
	command.WaitDelay = time.Second
	output, err := command.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("%s timed out: %v\n%s", target, err, output)
	}
	return output, err
}

func TestHooksInstallAllowsForeignRegularWorktreeConfigSymlink(t *testing.T) {
	t.Parallel()

	repoDir := newHookTestRepository(t)
	runCommand(t, repoDir, "git", "config", "extensions.worktreeConfig", "true")
	linkedDir := filepath.Join(t.TempDir(), "linked")
	runCommand(t, repoDir, "git", "worktree", "add", linkedDir)
	runCommand(t, linkedDir, "git", "config", "--worktree", "core.hooksPath", ".githooks")
	foreignGitDir := gitOutput(t, linkedDir, "rev-parse", "--path-format=absolute", "--git-dir")
	foreignConfig := filepath.Join(foreignGitDir, "config.worktree")
	target := filepath.Join(t.TempDir(), "foreign-config")
	writeFile(t, target, "")
	if err := os.Remove(foreignConfig); err != nil {
		t.Fatalf("remove foreign worktree config: %v", err)
	}
	if err := os.Symlink(target, foreignConfig); err != nil {
		t.Fatalf("link foreign worktree config: %v", err)
	}

	output, err := runMakeWithEnv(repoDir, "hooks-install")
	if err != nil {
		t.Fatalf("install with foreign regular config symlink = %v\n%s", err, output)
	}
}

func TestHooksUninstallFailsClosedWhenEffectiveReferenceScannerFails(t *testing.T) {
	t.Parallel()
	for _, command := range []string{"xargs", "grep"} {
		t.Run(command, func(t *testing.T) {
			repoDir := newHookTestRepository(t)
			runCommand(t, repoDir, "make", "hooks-install")
			managedHook := managedHookPath(t, repoDir)
			globalConfig := filepath.Join(t.TempDir(), "global-config")
			writeFile(t, globalConfig, "[core]\n\thooksPath = /unrelated/hooks\n")
			wrapperDir := t.TempDir()
			writeFileMode(t, filepath.Join(wrapperDir, command), "#!/bin/sh\necho forced effective reference scanner failure >&2\nexit 73\n", 0o755)

			output, err := runMakeWithEnv(repoDir, "hooks-uninstall", "PATH="+wrapperDir+string(os.PathListSeparator)+os.Getenv("PATH"), "GIT_CONFIG_GLOBAL="+globalConfig)
			if err == nil || !strings.Contains(string(output), "Unable to inspect effective core.hooksPath") {
				t.Fatalf("uninstall with effective %s failure = %v\n%s", command, err, output)
			}
			if _, err := os.Stat(managedHook); err != nil {
				t.Fatalf("uninstaller removed managed hook after effective %s failure: %v", command, err)
			}
		})
	}
}

func TestHooksUninstallFailsClosedWhenReferenceScannerFails(t *testing.T) {
	t.Parallel()
	for _, command := range []string{"xargs", "grep"} {
		t.Run(command, func(t *testing.T) {
			repoDir := newHookTestRepository(t)
			runCommand(t, repoDir, "git", "config", "extensions.worktreeConfig", "true")
			runCommand(t, repoDir, "make", "hooks-install")
			managedHook := managedHookPath(t, repoDir)
			linkedDir := filepath.Join(t.TempDir(), "linked")
			runCommand(t, repoDir, "git", "worktree", "add", linkedDir)
			runCommand(t, linkedDir, "git", "config", "--worktree", "core.hooksPath", ".githooks")
			wrapperDir := t.TempDir()
			writeFileMode(t, filepath.Join(wrapperDir, command), "#!/bin/sh\necho forced reference scanner failure >&2\nexit 73\n", 0o755)

			output, err := runMakeWithEnv(repoDir, "hooks-uninstall", "PATH="+wrapperDir+string(os.PathListSeparator)+os.Getenv("PATH"))
			if err == nil || !strings.Contains(string(output), "Unable to inspect worktree config worktree core.hooksPath") {
				t.Fatalf("uninstall with %s failure = %v\n%s", command, err, output)
			}
			if _, err := os.Stat(managedHook); err != nil {
				t.Fatalf("uninstaller removed managed hook after %s failure: %v", command, err)
			}
		})
	}
}

func assertNonRegularDormantConfigIsRejected(t *testing.T, useSymlink bool) {
	t.Helper()
	repoDir, managedHook, worktreeConfig := newNonRegularDormantConfigFixture(t, useSymlink)
	cpPath, err := exec.LookPath("cp")
	if err != nil {
		t.Fatalf("find cp: %v", err)
	}
	marker := filepath.Join(t.TempDir(), "cp-called")
	wrapperDir := t.TempDir()
	writeFileMode(t, filepath.Join(wrapperDir, "cp"), fmt.Sprintf(`#!/bin/sh
for arg do if [ "$arg" = %q ]; then touch %q; exit 73; fi; done
exec %q "$@"
`, worktreeConfig, marker, cpPath), 0o755)
	assertUnsafeConfigUninstallResult(t, repoDir, managedHook, marker, wrapperDir)
}

func newNonRegularDormantConfigFixture(t *testing.T, useSymlink bool) (string, string, string) {
	t.Helper()
	repoDir := newHookTestRepository(t)
	runCommand(t, repoDir, "make", "hooks-install")
	managedHook := managedHookPath(t, repoDir)
	worktreeConfig := filepath.Join(gitOutput(t, repoDir, "rev-parse", "--path-format=absolute", "--git-dir"), "config.worktree")
	runCommand(t, repoDir, "git", "config", "extensions.worktreeConfig", "true")
	runCommand(t, repoDir, "git", "config", "--worktree", "core.hooksPath", ".githooks")
	runCommand(t, repoDir, "git", "config", "--local", "extensions.worktreeConfig", "false")
	if err := os.Remove(worktreeConfig); err != nil {
		t.Fatalf("remove worktree config: %v", err)
	}
	fifo := worktreeConfig + ".fifo"
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatalf("create worktree config FIFO: %v", err)
	}
	placeNonRegularConfig(t, fifo, worktreeConfig, useSymlink)
	return repoDir, managedHook, worktreeConfig
}

func placeNonRegularConfig(t *testing.T, fifo, config string, useSymlink bool) {
	t.Helper()
	if useSymlink {
		if err := os.Symlink(fifo, config); err != nil {
			t.Fatalf("link worktree config to FIFO: %v", err)
		}
		return
	}
	if err := os.Rename(fifo, config); err != nil {
		t.Fatalf("move FIFO to worktree config: %v", err)
	}
}

func assertUnsafeConfigUninstallResult(t *testing.T, repoDir, managedHook, marker, wrapperDir string) {
	t.Helper()
	output, err := runMakeWithEnv(repoDir, "hooks-uninstall", "PATH="+wrapperDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err == nil || !strings.Contains(string(output), "Refusing unsafe current worktree Git config") {
		t.Fatalf("uninstall with non-regular worktree config = %v\n%s", err, output)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("uninstall attempted config snapshot before refusing unsafe config: %v", err)
	}
	if _, err := os.Stat(managedHook); err != nil {
		t.Fatalf("uninstall removed managed hook before unsafe config refusal: %v", err)
	}
}

func TestHooksUninstallRestoresConfigurationWhenManagedHookFinalizationFails(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name         string
		removeBefore bool
		signal       bool
	}{
		{name: "removal failure"},
		{name: "failure after removal", removeBefore: true},
		{name: "termination after removal", removeBefore: true, signal: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			assertManagedHookFinalizationRollback(t, test.name, test.removeBefore, test.signal)
		})
	}
}

func assertManagedHookFinalizationRollback(t *testing.T, name string, removeBefore, signal bool) {
	t.Helper()
	repoDir, managedHook, configPath, configBefore := newManagedHookFinalizationFixture(t)
	wrapperDir := newManagedHookFailureWrapper(t, managedHook, removeBefore, signal)
	output, err := runMakeWithEnv(repoDir, "hooks-uninstall", "PATH="+wrapperDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err == nil || (!signal && !strings.Contains(string(output), "forced managed hook removal failure")) {
		t.Fatalf("uninstall with managed hook %s = %v\n%s", name, err, output)
	}
	assertFileEquals(t, configPath, string(configBefore))
	if _, err := os.Stat(managedHook); err != nil {
		t.Fatalf("managed hook missing after %s: %v", name, err)
	}
	assertNoHookConfigBackups(t, repoDir)
	assertNoManagedHookBackups(t, managedHook)
}

func newManagedHookFinalizationFixture(t *testing.T) (string, string, string, []byte) {
	t.Helper()
	repoDir := newHookTestRepository(t)
	runCommand(t, repoDir, "make", "hooks-install")
	managedHook := managedHookPath(t, repoDir)
	configPath := filepath.Join(gitOutput(t, repoDir, "rev-parse", "--path-format=absolute", "--git-dir"), "config")
	return repoDir, managedHook, configPath, readHookConfigBytes(t, configPath)
}

func newManagedHookFailureWrapper(t *testing.T, managedHook string, removeBefore, signal bool) string {
	t.Helper()
	rmPath, err := exec.LookPath("rm")
	if err != nil {
		t.Fatalf("find rm: %v", err)
	}
	body := `echo "forced managed hook removal failure" >&2; exit 73`
	if removeBefore {
		body = fmt.Sprintf("%q \"$@\"\necho \"forced managed hook removal failure\" >&2\nexit 73", rmPath)
	}
	if signal {
		body = fmt.Sprintf("%q \"$@\"\nkill -TERM \"$PPID\"\nexit 0", rmPath)
	}
	wrapperDir := t.TempDir()
	writeFileMode(t, filepath.Join(wrapperDir, "rm"), fmt.Sprintf("#!/bin/sh\nfor arg do if [ \"$arg\" = %q ]; then %s; fi; done\nexec %q \"$@\"\n", managedHook, body, rmPath), 0o755)
	return wrapperDir
}

func assertNoManagedHookBackups(t *testing.T, managedHook string) {
	t.Helper()
	if backups, err := filepath.Glob(filepath.Join(filepath.Dir(managedHook), ".pre-commit.*.uninstall-backup.*")); err != nil || len(backups) != 0 {
		t.Fatalf("managed hook backups = %#v err=%v", backups, err)
	}
}

func TestHooksUninstallPreservesRecoveryStateWhenManagedHookRestoreFails(t *testing.T) {
	t.Parallel()

	repoDir := newHookTestRepository(t)
	runCommand(t, repoDir, "make", "hooks-install")
	managedHook := managedHookPath(t, repoDir)
	managedDir := filepath.Dir(managedHook)
	gitDir := gitOutput(t, repoDir, "rev-parse", "--path-format=absolute", "--git-dir")
	configPath := filepath.Join(gitDir, "config")
	rmPath, err := exec.LookPath("rm")
	if err != nil {
		t.Fatalf("find rm: %v", err)
	}
	mvPath, err := exec.LookPath("mv")
	if err != nil {
		t.Fatalf("find mv: %v", err)
	}
	wrapperDir := t.TempDir()
	writeFileMode(t, filepath.Join(wrapperDir, "rm"), fmt.Sprintf(`#!/bin/sh
for arg do if [ "$arg" = %q ]; then %q "$@"; echo "forced removal failure" >&2; exit 73; fi; done
exec %q "$@"
`, managedHook, rmPath, rmPath), 0o755)
	writeFileMode(t, filepath.Join(wrapperDir, "mv"), fmt.Sprintf(`#!/bin/sh
for arg do case "$arg" in *.uninstall-backup.*) echo "forced managed hook restore failure" >&2; exit 74;; esac; done
exec %q "$@"
`, mvPath), 0o755)

	output, err := runMakeWithEnv(repoDir, "hooks-uninstall", "PATH="+wrapperDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err == nil || !strings.Contains(string(output), "Unable to restore hook configuration completely") || !strings.Contains(string(output), "forced managed hook restore failure") {
		t.Fatalf("uninstall with managed hook restore failure = %v\n%s", err, output)
	}
	assertFileDoesNotContain(t, configPath, managedDir)
	if _, err := os.Stat(managedHook); !os.IsNotExist(err) {
		t.Fatalf("managed hook exists after failed restoration: %v", err)
	}
	if backups, err := filepath.Glob(filepath.Join(gitDir, ".lopper-hooks-config.*")); err != nil || len(backups) == 0 {
		t.Fatalf("configuration recovery backups = %#v err=%v", backups, err)
	}
	if backups, err := filepath.Glob(filepath.Join(managedDir, ".pre-commit.*.uninstall-backup.*")); err != nil || len(backups) != 1 {
		t.Fatalf("managed hook recovery backups = %#v err=%v", backups, err)
	}
}

func TestHooksUninstallRollsBackNonExecutableOriginalManagedHookWhenConfigLocked(t *testing.T) {
	t.Parallel()

	repoDir := newHookTestRepository(t)
	runCommand(t, repoDir, "make", "hooks-install")
	managedHook := managedHookPath(t, repoDir)
	if err := os.Chmod(managedHook, 0o600); err != nil {
		t.Fatalf("make managed hook non-executable: %v", err)
	}
	gitDir := gitOutput(t, repoDir, "rev-parse", "--path-format=absolute", "--git-dir")
	writeFile(t, filepath.Join(gitDir, "config.lock"), "locked\n")

	output, err := runMakeWithEnv(repoDir, "hooks-uninstall")
	if err == nil || !strings.Contains(string(output), "Unable to remove managed core.hooksPath") || strings.Contains(string(output), "Unable to restore hook configuration completely") {
		t.Fatalf("uninstall with non-executable original hook and config lock = %v\n%s", err, output)
	}
	assertConfigValues(t, repoDir, "--local", filepath.Dir(managedHook))
	info, err := os.Stat(managedHook)
	if err != nil {
		t.Fatalf("stat original managed hook: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("original managed hook mode = %v, want 600", info.Mode())
	}
	assertNoHookConfigBackups(t, repoDir)
	assertNoManagedHookBackups(t, managedHook)
}

func TestHooksUninstallRefusesConfigurationRestoreAfterManagedHookReplacement(t *testing.T) {
	t.Parallel()
	for _, replacement := range []string{"symlink", "different regular file", "identical non-executable regular file"} {
		t.Run(replacement, func(t *testing.T) { assertManagedHookReplacementPreservesRecoveryState(t, replacement) })
	}
}

func assertManagedHookReplacementPreservesRecoveryState(t *testing.T, replacement string) {
	t.Helper()
	repoDir := newHookTestRepository(t)
	runCommand(t, repoDir, "make", "hooks-install")
	managedHook := managedHookPath(t, repoDir)
	managedDir := filepath.Dir(managedHook)
	configPath := filepath.Join(gitOutput(t, repoDir, "rev-parse", "--path-format=absolute", "--git-dir"), "config")
	rmPath, err := exec.LookPath("rm")
	if err != nil {
		t.Fatalf("find rm: %v", err)
	}
	wrapperDir := t.TempDir()
	writeFileMode(t, filepath.Join(wrapperDir, "rm"), replacementRMWrapper(t, rmPath, managedHook, replacement), 0o755)
	output, err := runMakeWithEnv(repoDir, "hooks-uninstall", "PATH="+wrapperDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err == nil || !strings.Contains(string(output), "Unable to restore hook configuration completely") {
		t.Fatalf("uninstall after managed hook replacement = %v\n%s", err, output)
	}
	assertFileDoesNotContain(t, configPath, managedDir)
	assertManagedHookRecoveryState(t, managedHook, replacement)
}

func replacementRMWrapper(t *testing.T, rmPath, managedHook, replacement string) string {
	t.Helper()
	replacementCommand := fmt.Sprintf("printf '%%s\\n' replacement > %q", managedHook)
	switch replacement {
	case "symlink":
		target := managedHook + ".replacement"
		replacementCommand = fmt.Sprintf("printf '%%s\\n' replacement > %q\nln -s %q %q", target, target, managedHook)
	case "identical non-executable regular file":
		replacementCommand = fmt.Sprintf("for backup in %q/.pre-commit.*.uninstall-backup.*; do cp \"$backup\" %q && chmod 600 %q && break; done", filepath.Dir(managedHook), managedHook, managedHook)
	}
	return fmt.Sprintf(`#!/bin/sh
for arg do if [ "$arg" = %q ]; then %q "$@"; %s; echo "forced removal failure" >&2; exit 73; fi; done
exec %q "$@"
`, managedHook, rmPath, replacementCommand, rmPath)
}

func assertManagedHookRecoveryState(t *testing.T, managedHook, replacement string) {
	t.Helper()
	switch replacement {
	case "symlink":
		if _, err := os.Readlink(managedHook); err != nil {
			t.Fatalf("replacement symlink missing: %v", err)
		}
	case "different regular file":
		if got := string(readHookConfigBytes(t, managedHook)); got != "replacement\n" {
			t.Fatalf("replacement hook = %q", got)
		}
	case "identical non-executable regular file":
		info, err := os.Stat(managedHook)
		if err != nil {
			t.Fatalf("stat identical replacement hook: %v", err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("identical replacement hook mode = %v, want 600", info.Mode())
		}
	default:
		t.Fatalf("unknown managed hook replacement %q", replacement)
	}
	if backups, err := filepath.Glob(filepath.Join(filepath.Dir(managedHook), ".pre-commit.*.uninstall-backup.*")); err != nil || len(backups) != 1 {
		t.Fatalf("managed hook recovery backups = %#v err=%v", backups, err)
	}
}
