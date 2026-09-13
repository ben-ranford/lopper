//go:build !windows

package scripts

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestHooksInstallRollbackRetainsCommonBackupWhenConfigBecomesDirectory(t *testing.T) {
	t.Parallel()

	for _, replaced := range []string{"common", "worktree"} {
		t.Run(replaced, func(t *testing.T) {
			repoDir := newHookTestRepository(t)
			assertIndependentHookFixture(t, repoDir)
			runCommand(t, repoDir, "git", "config", "extensions.worktreeConfig", "true")
			runCommand(t, repoDir, "git", "config", "--worktree", "core.hooksPath", ".githooks")
			commonDir := gitOutput(t, repoDir, "rev-parse", "--path-format=absolute", "--git-common-dir")
			commonConfig := filepath.Join(commonDir, "config")
			worktreeConfig := filepath.Join(gitOutput(t, repoDir, "rev-parse", "--path-format=absolute", "--git-dir"), "config.worktree")
			commonBefore := readHookConfigBytes(t, commonConfig)
			worktreeBefore := readHookConfigBytes(t, worktreeConfig)
			replacedPath, otherPath := commonConfig, worktreeConfig
			replacedBefore, otherBefore := commonBefore, worktreeBefore
			backupPattern := ".config.backup.*"
			if replaced == "worktree" {
				replacedPath, otherPath = worktreeConfig, commonConfig
				replacedBefore, otherBefore = worktreeBefore, commonBefore
				backupPattern = ".config.worktree.backup.*"
			}

			wrapperDir := configReplacementGitWrapper(t, replacedPath, "--fixed-value")
			output, err := runMakeWithEnv(repoDir, "hooks-install", "PATH="+wrapperDir+string(os.PathListSeparator)+os.Getenv("PATH"))
			if err == nil || !strings.Contains(string(output), "Unable to restore hook installation completely") {
				t.Fatalf("install after %s config replacement = %v\n%s", replaced, err, output)
			}
			assertDirectoryEmpty(t, replacedPath)
			assertFileEquals(t, otherPath, string(otherBefore))
			assertRetainedConfigBackup(t, filepath.Join(commonDir, "lopper-hooks"), backupPattern, replacedBefore)
		})
	}
}

func TestHooksInstallRollbackRetainsManagedHookBackupWhenHookBecomesDirectory(t *testing.T) {
	t.Parallel()

	repoDir := newHookTestRepository(t)
	assertIndependentHookFixture(t, repoDir)
	managedHook := managedHookPath(t, repoDir)
	if err := os.MkdirAll(filepath.Dir(managedHook), 0o700); err != nil {
		t.Fatalf("create managed hook directory: %v", err)
	}
	managedBefore := []byte("#!/bin/sh\necho original managed hook\n")
	writeFileMode(t, managedHook, string(managedBefore), 0o755)

	wrapperDir := configReplacementGitWrapper(t, managedHook, "--fixed-value")
	output, err := runMakeWithEnv(repoDir, "hooks-install", "PATH="+wrapperDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err == nil || !strings.Contains(string(output), "Unable to restore hook installation completely") {
		t.Fatalf("install after managed hook replacement = %v\n%s", err, output)
	}
	assertDirectoryEmpty(t, managedHook)
	assertRetainedConfigBackup(t, filepath.Dir(managedHook), ".pre-commit.backup.*", managedBefore)
}

func TestHooksUninstallRollbackRetainsBackupsWhenConfigTargetsBecomeDirectories(t *testing.T) {
	t.Parallel()

	for _, symlink := range []bool{false, true} {
		t.Run(fmt.Sprintf("symlink=%t", symlink), func(t *testing.T) {
			assertHooksUninstallRollbackRetainsBackup(t, symlink)
		})
	}
}

func assertHooksUninstallRollbackRetainsBackup(t *testing.T, symlink bool) {
	t.Helper()
	repoDir := newHookTestRepository(t)
	assertIndependentHookFixture(t, repoDir)
	runCommand(t, repoDir, "git", "config", "extensions.worktreeConfig", "true")
	runCommand(t, repoDir, "git", "config", "--worktree", "core.hooksPath", ".githooks")
	runCommand(t, repoDir, "make", "hooks-install")
	commonDir := gitOutput(t, repoDir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	commonConfig := filepath.Join(commonDir, "config")
	worktreeConfig := filepath.Join(gitOutput(t, repoDir, "rev-parse", "--path-format=absolute", "--git-dir"), "config.worktree")
	commonBefore := readHookConfigBytes(t, commonConfig)
	worktreeBefore := readHookConfigBytes(t, worktreeConfig)
	if symlink {
		replaceConfigWithSymlink(t, commonConfig, commonBefore)
	}
	wrapperDir := configReplacementGitWrapper(t, commonConfig, "--unset-all")
	output, err := runMakeWithEnv(repoDir, "hooks-uninstall", "PATH="+wrapperDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err == nil || !strings.Contains(string(output), "Unable to restore hook configuration completely") {
		t.Fatalf("uninstall after config replacement = %v\n%s", err, output)
	}
	assertDirectoryEmpty(t, commonConfig)
	assertFileEquals(t, worktreeConfig, string(worktreeBefore))
	assertRetainedConfigBackup(t, commonDir, ".lopper-hooks-config.*", commonBefore)
}

func replaceConfigWithSymlink(t *testing.T, configPath string, contents []byte) {
	t.Helper()
	target := filepath.Join(t.TempDir(), "replaced-target")
	if err := os.Remove(configPath); err != nil {
		t.Fatalf("remove common config for symlink: %v", err)
	}
	writeFile(t, target, string(contents))
	if err := os.Symlink(target, configPath); err != nil {
		t.Fatalf("link common config: %v", err)
	}
}

func configReplacementGitWrapper(t *testing.T, configPath, trigger string) string {
	t.Helper()
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("find git: %v", err)
	}
	wrapperDir := t.TempDir()
	writeFileMode(t, filepath.Join(wrapperDir, "git"), fmt.Sprintf(`#!/bin/sh
local_scope=false
matched=false
for arg do
	[ "$arg" = "--local" ] && local_scope=true
	if [ "$arg" = %q ]; then
		matched=true
	fi
done
if [ "$matched" = true ] && { [ %q != "--unset-all" ] || [ "$local_scope" = true ]; }; then
	if [ -L %q ]; then
		config_target="$(readlink %q)"
		rm -f "$config_target"
		mkdir "$config_target"
	else
		rm -f %q
		mkdir %q
	fi
	echo "forced config destination replacement" >&2
	exit 73
fi
exec %q "$@"
`, trigger, trigger, configPath, configPath, configPath, configPath, gitPath), 0o755)
	return wrapperDir
}

func assertDirectoryEmpty(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		t.Fatalf("replacement destination = %v, want directory", err)
	}
	entries, err := os.ReadDir(path)
	if err != nil || len(entries) != 0 {
		t.Fatalf("replacement destination entries = %#v err=%v", entries, err)
	}
}

func assertRetainedConfigBackup(t *testing.T, backupDir, pattern string, want []byte) {
	t.Helper()
	backups, err := filepath.Glob(filepath.Join(backupDir, pattern))
	if err != nil || len(backups) == 0 {
		t.Fatalf("retained backups = %#v err=%v", backups, err)
	}
	for _, backup := range backups {
		contents, readErr := os.ReadFile(backup)
		if readErr == nil && string(contents) == string(want) {
			return
		}
	}
	t.Fatalf("no retained %s backup matched original config", pattern)
}
