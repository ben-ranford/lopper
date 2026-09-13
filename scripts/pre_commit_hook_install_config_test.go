package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestHooksInstallRefusesLinkedWorktreeOverrideBeforeMutationAndSucceedsAfterRemoval(t *testing.T) {
	t.Parallel()

	repoDir := newHookTestRepository(t)
	linkedDir := filepath.Join(t.TempDir(), "linked")
	runCommand(t, repoDir, "git", "config", "extensions.worktreeConfig", "true")
	runCommand(t, repoDir, "git", "worktree", "add", linkedDir)
	runCommand(t, linkedDir, "git", "config", "--worktree", "core.hooksPath", ".githooks")

	command := exec.Command("make", "hooks-install")
	command.Dir = repoDir
	command.Env = hookTestEnv()
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "another worktree") {
		t.Fatalf("linked worktree install = %v\n%s", err, output)
	}
	assertNoHooksPath(t, repoDir, "--local", "local")
	if got := gitOutput(t, linkedDir, "config", "--worktree", "--get", "core.hooksPath"); got != ".githooks" {
		t.Fatalf("linked worktree core.hooksPath = %q, want .githooks", got)
	}
	if _, err := os.Stat(filepath.Dir(managedHookPath(t, repoDir))); !os.IsNotExist(err) {
		t.Fatalf("installer created managed hook directory before refusal: %v", err)
	}
	runCommand(t, linkedDir, "git", "config", "--worktree", "--unset-all", "core.hooksPath")
	runCommand(t, repoDir, "make", "hooks-install")
	assertConfigEquals(t, repoDir, filepath.Dir(managedHookPath(t, repoDir)))
}

func TestHooksInstallAllowsSafeLinkedWorktrees(t *testing.T) {
	t.Parallel()

	repoDir := newHookTestRepository(t)
	linkedDir := filepath.Join(t.TempDir(), "linked")
	runCommand(t, repoDir, "git", "config", "extensions.worktreeConfig", "true")
	runCommand(t, repoDir, "git", "worktree", "add", linkedDir)
	runCommand(t, repoDir, "make", "hooks-install")
	assertConfigEquals(t, repoDir, filepath.Dir(managedHookPath(t, repoDir)))
}

func TestHooksInstallAndUninstallPreserveCustomHooks(t *testing.T) {
	t.Parallel()

	repoDir := newHookTestRepository(t)
	customDir := filepath.Join(repoDir, "custom-hooks")
	writeFileMode(t, filepath.Join(customDir, "pre-commit"), "#!/bin/sh\nexit 0\n", 0o755)
	runCommand(t, repoDir, "git", "config", "--local", "core.hooksPath", customDir)

	command := exec.Command("make", "hooks-install")
	command.Dir = repoDir
	command.Env = hookTestEnv()
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "Refusing to replace") {
		t.Fatalf("custom hook install = %v\n%s", err, output)
	}
	runCommand(t, repoDir, "make", "hooks-uninstall")
	assertConfigEquals(t, repoDir, customDir)
	if _, err := os.Stat(filepath.Join(customDir, "pre-commit")); err != nil {
		t.Fatalf("custom hook was removed: %v", err)
	}
}

func TestHooksInstallIsIdempotentAndUninstallsOnlyManagedOrLegacyPaths(t *testing.T) {
	t.Parallel()

	repoDir := newHookTestRepository(t)
	runCommand(t, repoDir, "make", "hooks-install")
	managedHook := managedHookPath(t, repoDir)
	runCommand(t, repoDir, "make", "hooks-install")
	if _, err := os.Stat(managedHook); err != nil {
		t.Fatalf("managed hook missing after repeat install: %v", err)
	}
	runCommand(t, repoDir, "make", "hooks-uninstall")
	if _, err := os.Stat(managedHook); !os.IsNotExist(err) {
		t.Fatalf("managed hook remains after uninstall: %v", err)
	}
	assertNoHooksPath(t, repoDir, "--local", "local")

	runCommand(t, repoDir, "git", "config", "--local", "core.hooksPath", ".githooks")
	runCommand(t, repoDir, "make", "hooks-uninstall")
	assertNoHooksPath(t, repoDir, "--local", "local")
}

func TestHooksInstallRefusesMultiValueCustomHooksPathsWithoutMutation(t *testing.T) {
	t.Parallel()

	assertHooksInstallRefusesMultiValueHooksPaths(t, "--local", "custom hooks")
}

func TestHooksInstallRefusesMultiValueWorktreeHooksPathsWithoutMutation(t *testing.T) {
	t.Parallel()

	assertHooksInstallRefusesMultiValueHooksPaths(t, "--worktree", "custom worktree hooks")
}

func assertHooksInstallRefusesMultiValueHooksPaths(t *testing.T, scope, customDirectory string) {
	t.Helper()

	repoDir := newHookTestRepository(t)
	customDir := filepath.Join(repoDir, customDirectory)
	if scope == "--worktree" {
		runCommand(t, repoDir, "git", "config", "extensions.worktreeConfig", "true")
	}
	runCommand(t, repoDir, "git", "config", scope, "--add", "core.hooksPath", ".githooks")
	runCommand(t, repoDir, "git", "config", scope, "--add", "core.hooksPath", customDir)

	command := exec.Command("make", "hooks-install")
	command.Dir = repoDir
	command.Env = hookTestEnv()
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "Refusing to replace") {
		t.Fatalf("multi-value %s install = %v\n%s", scope, err, output)
	}
	assertConfigValues(t, repoDir, scope, ".githooks", customDir)
	if _, err := os.Stat(filepath.Dir(managedHookPath(t, repoDir))); !os.IsNotExist(err) {
		t.Fatalf("installer created managed hook directory before refusal: %v", err)
	}
}

func TestHooksInstallUsesLocalWorktreeConfigWhenGlobalConfigIsTrue(t *testing.T) {
	t.Parallel()

	repoDir := newHookTestRepository(t)
	linkedDir := filepath.Join(t.TempDir(), "linked")
	runCommand(t, repoDir, "git", "worktree", "add", linkedDir)
	globalConfig := filepath.Join(t.TempDir(), "global.gitconfig")
	writeFile(t, globalConfig, "[extensions]\n\tworktreeConfig = true\n")

	output, err := runMakeWithEnv(repoDir, "hooks-install", "GIT_CONFIG_GLOBAL="+globalConfig, "GIT_CONFIG_NOSYSTEM=1")
	if err != nil {
		t.Fatalf("install with global-only worktreeConfig = %v\n%s", err, output)
	}
	assertConfigEquals(t, repoDir, filepath.Dir(managedHookPath(t, repoDir)))
	currentGitDir := gitOutput(t, repoDir, "rev-parse", "--path-format=absolute", "--git-dir")
	if _, err := os.Stat(filepath.Join(currentGitDir, "config.worktree")); !os.IsNotExist(err) {
		t.Fatalf("install wrote a worktree config for global-only worktreeConfig: %v", err)
	}

	output, err = runMakeWithEnv(repoDir, "hooks-uninstall", "GIT_CONFIG_GLOBAL="+globalConfig, "GIT_CONFIG_NOSYSTEM=1")
	if err != nil || strings.Contains(string(output), "cannot be used with multiple working trees") {
		t.Fatalf("uninstall with global-only worktreeConfig = %v\n%s", err, output)
	}
	if _, err := os.Stat(managedHookPath(t, repoDir)); !os.IsNotExist(err) {
		t.Fatalf("uninstall retained managed hook with global-only worktreeConfig: %v", err)
	}
}

func TestHooksInstallAndUninstallRejectMalformedLocalWorktreeConfigBeforeMutation(t *testing.T) {
	t.Parallel()

	repoDir := newHookTestRepository(t)
	gitDir := gitOutput(t, repoDir, "rev-parse", "--path-format=absolute", "--git-dir")
	configPath := filepath.Join(gitDir, "config")
	managedHook := managedHookPath(t, repoDir)
	runCommand(t, repoDir, "git", "config", "--local", "extensions.worktreeConfig", "invalid")

	output, err := runMakeWithEnv(repoDir, "hooks-install")
	if err == nil {
		t.Fatalf("install with malformed local worktreeConfig unexpectedly succeeded:\n%s", output)
	}
	assertFileDoesNotContain(t, configPath, "hooksPath")
	if _, err := os.Stat(filepath.Dir(managedHook)); !os.IsNotExist(err) {
		t.Fatalf("installer created managed hook directory before malformed-config refusal: %v", err)
	}

	writeFileMode(t, managedHook, "#!/bin/sh\nexit 0\n", 0o755)
	managedDir := filepath.Dir(managedHook)
	configData, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read malformed local config: %v", err)
	}
	writeFile(t, configPath, string(configData)+"\n[core]\n\thooksPath = "+managedDir+"\n")
	output, err = runMakeWithEnv(repoDir, "hooks-uninstall")
	if err == nil {
		t.Fatalf("uninstall with malformed local worktreeConfig unexpectedly succeeded:\n%s", output)
	}
	assertFileContains(t, configPath, managedDir)
	if _, err := os.Stat(managedHook); err != nil {
		t.Fatalf("uninstaller removed managed hook before malformed-config refusal: %v", err)
	}
}
