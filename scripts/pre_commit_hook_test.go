package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestHooksInstallSnapshotsTrustedHookAndRejectsUnsafeStagedContent(t *testing.T) {
	t.Parallel()

	repoDir := newHookTestRepository(t)
	runCommand(t, repoDir, "make", "hooks-install")

	managedHook := managedHookPath(t, repoDir)
	assertFileEquals(t, managedHook, readRepositoryHook(t))
	assertConfigEquals(t, repoDir, filepath.Dir(managedHook))

	markerPath := filepath.Join(repoDir, "executed-marker")
	writeFileMode(t, filepath.Join(repoDir, ".githooks", "pre-commit"), "#!/bin/sh\ntouch "+markerPath+"\n", 0o755)
	writeFile(t, filepath.Join(repoDir, "Makefile"), "all:\n\ttouch "+markerPath+"\n")
	writeFile(t, filepath.Join(repoDir, "malicious_test.go"), "package fixture\n\nimport \"os\"\n\nfunc init() {\n\t_ = os.WriteFile(\""+markerPath+"\", []byte(\"ran\"), 0o600)\n}\n")
	writeFile(t, filepath.Join(repoDir, "safe.txt"), "safe\n")
	runCommand(t, repoDir, "git", "add", ".")
	runCommitWithHook(t, repoDir, "benign commit")
	if _, err := os.Stat(markerPath); !os.IsNotExist(err) {
		t.Fatalf("checkout-controlled content ran during commit: %v", err)
	}

	writeFile(t, filepath.Join(repoDir, "whitespace.txt"), "trailing space \n")
	runCommand(t, repoDir, "git", "add", "whitespace.txt")
	assertCommitFails(t, repoDir, "trailing whitespace")
	runCommand(t, repoDir, "git", "reset", "--", "whitespace.txt")

	tempDir := filepath.Join(t.TempDir(), "temporary files")
	if err := os.Mkdir(tempDir, 0o755); err != nil {
		t.Fatalf("create temporary directory: %v", err)
	}
	stagedGo := filepath.Join(repoDir, "unformatted.go")
	writeFile(t, stagedGo, "package fixture\n\nfunc unformatted(){}\n")
	runCommand(t, repoDir, "git", "add", "unformatted.go")
	writeFile(t, stagedGo, "package fixture\n\nfunc unformatted() {}\n")
	output := assertCommitFailsWithEnv(t, repoDir, "staged Go files must be gofmt-formatted", []string{"TMPDIR=" + tempDir})
	if !strings.Contains(output, "unformatted.go") {
		t.Fatalf("gofmt diagnostic does not identify the staged file:\n%s", output)
	}
	runCommand(t, repoDir, "git", "reset", "--", "unformatted.go")

	writeFile(t, filepath.Join(repoDir, "broken.go"), "package fixture\n\nfunc broken( {\n")
	runCommand(t, repoDir, "git", "add", "broken.go")
	assertCommitFailsWithEnv(t, repoDir, "broken.go", []string{"TMPDIR=" + tempDir})
	runCommand(t, repoDir, "git", "reset", "--", "broken.go")

	for _, name := range []string{": staged.go", "0:unformatted.go"} {
		writeFile(t, filepath.Join(repoDir, name), "package fixture\n\nfunc adversarialName(){}\n")
		runCommand(t, repoDir, "git", "add", "--", "./"+name)
		output := assertCommitFails(t, repoDir, "staged Go files must be gofmt-formatted")
		if !strings.Contains(output, name) {
			t.Fatalf("gofmt diagnostic does not identify %q:\n%s", name, output)
		}
		runCommand(t, repoDir, "git", "reset", "--", "./"+name)
	}
}

func TestHooksInstallMigratesLegacyWorktreeHooksPath(t *testing.T) {
	t.Parallel()

	repoDir := newHookTestRepository(t)
	runCommand(t, repoDir, "git", "config", "extensions.worktreeConfig", "true")
	runCommand(t, repoDir, "git", "config", "--worktree", "core.hooksPath", ".githooks")
	runCommand(t, repoDir, "make", "hooks-install")

	managedHook := managedHookPath(t, repoDir)
	assertConfigEquals(t, repoDir, filepath.Dir(managedHook))
	markerPath := filepath.Join(repoDir, "worktree-marker")
	writeFileMode(t, filepath.Join(repoDir, ".githooks", "pre-commit"), "#!/bin/sh\ntouch "+markerPath+"\n", 0o755)
	writeFile(t, filepath.Join(repoDir, "Makefile"), "all:\n\ttouch "+markerPath+"\n")
	writeFile(t, filepath.Join(repoDir, "worktree.txt"), "safe\n")
	runCommand(t, repoDir, "git", "add", ".")
	runCommitWithHook(t, repoDir, "managed worktree commit")
	if _, err := os.Stat(markerPath); !os.IsNotExist(err) {
		t.Fatalf("worktree override executed checkout content: %v", err)
	}
	writeFile(t, filepath.Join(repoDir, "Makefile"), readRepositoryFile(t, "Makefile"))
	runCommand(t, repoDir, "make", "hooks-uninstall")
	assertNoHooksPath(t, repoDir, "--local", "local")
	assertNoHooksPath(t, repoDir, "--worktree", "worktree")
}

func TestHooksInstallPreservesMaskedCustomLocalHooksPath(t *testing.T) {
	t.Parallel()

	repoDir := newHookTestRepository(t)
	customDir := filepath.Join(repoDir, "custom-local-hooks")
	writeFileMode(t, filepath.Join(customDir, "pre-commit"), "#!/bin/sh\nexit 0\n", 0o755)
	runCommand(t, repoDir, "git", "config", "extensions.worktreeConfig", "true")
	runCommand(t, repoDir, "git", "config", "--local", "core.hooksPath", customDir)
	runCommand(t, repoDir, "git", "config", "--worktree", "core.hooksPath", ".githooks")

	command := exec.Command("make", "hooks-install")
	command.Dir = repoDir
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "Refusing to replace") {
		t.Fatalf("masked custom hook install = %v\n%s", err, output)
	}
	assertConfigEquals(t, repoDir, ".githooks")
	if got := gitOutput(t, repoDir, "config", "--local", "--get", "core.hooksPath"); got != customDir {
		t.Fatalf("local core.hooksPath = %q, want %q", got, customDir)
	}
	if got := gitOutput(t, repoDir, "config", "--worktree", "--get", "core.hooksPath"); got != ".githooks" {
		t.Fatalf("worktree core.hooksPath = %q, want .githooks", got)
	}
	if _, err := os.Stat(filepath.Join(customDir, "pre-commit")); err != nil {
		t.Fatalf("custom local hook was removed: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(managedHookPath(t, repoDir))); !os.IsNotExist(err) {
		t.Fatalf("installer created managed hook directory before refusal: %v", err)
	}
}

func TestHooksInstallUsesCommonGitDirectoryForLinkedWorktree(t *testing.T) {
	t.Parallel()

	repoDir := newHookTestRepository(t)
	linkedDir := filepath.Join(t.TempDir(), "linked")
	runCommand(t, repoDir, "git", "worktree", "add", linkedDir)
	command := exec.Command("make", "hooks-install")
	command.Dir = linkedDir
	output, err := command.CombinedOutput()
	if err != nil || strings.Contains(string(output), "cannot be used with multiple working trees") {
		t.Fatalf("linked worktree install = %v\n%s", err, output)
	}

	managedHook := managedHookPath(t, linkedDir)
	assertConfigEquals(t, linkedDir, filepath.Dir(managedHook))
	writeFile(t, filepath.Join(linkedDir, "linked.txt"), "linked\n")
	runCommand(t, linkedDir, "git", "add", "linked.txt")
	runCommitWithHook(t, linkedDir, "linked commit")
	command = exec.Command("make", "hooks-uninstall")
	command.Dir = linkedDir
	output, err = command.CombinedOutput()
	if err != nil || strings.Contains(string(output), "cannot be used with multiple working trees") {
		t.Fatalf("linked worktree uninstall = %v\n%s", err, output)
	}
}

func TestManagedHookChecksTrackedSymlinkReplacedByUnformattedGoFile(t *testing.T) {
	t.Parallel()

	repoDir := newHookTestRepository(t)
	linkPath := filepath.Join(repoDir, "replaced.go")
	if err := os.Symlink("tracked.txt", linkPath); err != nil {
		t.Fatalf("create tracked Go symlink: %v", err)
	}
	runCommand(t, repoDir, "git", "add", "replaced.go")
	runCommand(t, repoDir, "git", "-c", "core.hooksPath=/dev/null", "commit", "-m", "add Go symlink")
	runCommand(t, repoDir, "make", "hooks-install")

	if err := os.Remove(linkPath); err != nil {
		t.Fatalf("remove Go symlink: %v", err)
	}
	writeFile(t, linkPath, "package fixture\n\nfunc unformatted(){}\n")
	runCommand(t, repoDir, "git", "add", "replaced.go")
	assertCommitFails(t, repoDir, "staged Go files must be gofmt-formatted")
}

func TestManagedHookWorksWhenLinkedWorktreeSetsCoreBare(t *testing.T) {
	t.Parallel()

	repoDir := newHookTestRepository(t)
	linkedDir := filepath.Join(t.TempDir(), "linked")
	runCommand(t, repoDir, "git", "config", "extensions.worktreeConfig", "true")
	runCommand(t, repoDir, "git", "worktree", "add", linkedDir)
	runCommand(t, linkedDir, "make", "hooks-install")
	writeFile(t, filepath.Join(linkedDir, "linked.go"), "package fixture\n\nfunc unformatted(){}\n")
	runCommand(t, linkedDir, "git", "add", "linked.go")
	runCommand(t, linkedDir, "git", "config", "--worktree", "core.bare", "true")

	command := exec.Command(managedHookPath(t, linkedDir))
	command.Dir = linkedDir
	outputBytes, err := command.CombinedOutput()
	output := string(outputBytes)
	if err == nil || !strings.Contains(output, "staged Go files must be gofmt-formatted") {
		t.Fatalf("managed hook with core.bare=true = %v\n%s", err, output)
	}
	if strings.Contains(output, "this operation must be run in a work tree") {
		t.Fatalf("managed hook inherited core.bare=true:\n%s", output)
	}
	runCommand(t, linkedDir, "make", "hooks-uninstall")
	if _, err := os.Stat(managedHookPath(t, linkedDir)); !os.IsNotExist(err) {
		t.Fatalf("uninstall with core.bare=true left managed hook: %v", err)
	}
}

func TestManagedHookDoesNotRunConfiguredDiffHelpers(t *testing.T) {
	t.Parallel()

	repoDir := newHookTestRepository(t)
	runCommand(t, repoDir, "make", "hooks-install")
	markerPath := filepath.Join(repoDir, "diff-helper-ran")
	helperPath := filepath.Join(repoDir, "hostile-diff-helper")
	writeFileMode(t, helperPath, "#!/bin/sh\ntouch "+markerPath+"\nexit 0\n", 0o755)
	runCommand(t, repoDir, "git", "config", "diff.external", helperPath)
	runCommand(t, repoDir, "git", "config", "diff.hostile.textconv", helperPath)
	writeFile(t, filepath.Join(repoDir, ".gitattributes"), "*.go diff=hostile\n")
	writeFile(t, filepath.Join(repoDir, "hostile.go"), "package fixture\n\nfunc hostile() {}\n")
	runCommand(t, repoDir, "git", "add", ".gitattributes", "hostile.go")
	runCommitWithHook(t, repoDir, "stage hostile diff fixture")
	writeFile(t, filepath.Join(repoDir, "hostile.go"), "package fixture\n\nfunc hostileChanged() {}\n")
	runCommand(t, repoDir, "git", "add", "hostile.go")
	runCommitWithHook(t, repoDir, "commit without diff helper")
	if _, err := os.Stat(markerPath); !os.IsNotExist(err) {
		t.Fatalf("configured diff helper ran during commit: %v", err)
	}
}

func TestHooksInstallRefusesLinkedWorktreeOverrideBeforeMutationAndSucceedsAfterRemoval(t *testing.T) {
	t.Parallel()

	repoDir := newHookTestRepository(t)
	linkedDir := filepath.Join(t.TempDir(), "linked")
	runCommand(t, repoDir, "git", "config", "extensions.worktreeConfig", "true")
	runCommand(t, repoDir, "git", "worktree", "add", linkedDir)
	runCommand(t, linkedDir, "git", "config", "--worktree", "core.hooksPath", ".githooks")

	command := exec.Command("make", "hooks-install")
	command.Dir = repoDir
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

func TestHooksUninstallFailsWhenConfigIsLockedWithoutRemovingManagedHook(t *testing.T) {
	t.Parallel()

	repoDir := newHookTestRepository(t)
	runCommand(t, repoDir, "make", "hooks-install")
	managedHook := managedHookPath(t, repoDir)
	managedDir := filepath.Dir(managedHook)
	gitDir := gitOutput(t, repoDir, "rev-parse", "--path-format=absolute", "--git-dir")
	writeFile(t, filepath.Join(gitDir, "config.lock"), "locked\n")

	output, err := runMakeWithEnv(repoDir, "hooks-uninstall")
	if err == nil || !strings.Contains(string(output), "Unable to remove managed core.hooksPath") {
		t.Fatalf("uninstall with config lock = %v\n%s", err, output)
	}
	assertConfigValues(t, repoDir, "--local", managedDir)
	if _, err := os.Stat(managedHook); err != nil {
		t.Fatalf("uninstaller removed managed hook while config was locked: %v", err)
	}
}

func TestHooksUninstallRejectsMalformedForeignWorktreeConfigBeforeDeletingManagedHook(t *testing.T) {
	t.Parallel()

	repoDir := newHookTestRepository(t)
	runCommand(t, repoDir, "git", "config", "extensions.worktreeConfig", "true")
	runCommand(t, repoDir, "make", "hooks-install")
	linkedDir := filepath.Join(t.TempDir(), "linked")
	runCommand(t, repoDir, "git", "worktree", "add", linkedDir)
	foreignGitDir := gitOutput(t, linkedDir, "rev-parse", "--path-format=absolute", "--git-dir")
	writeFile(t, filepath.Join(foreignGitDir, "config.worktree"), "[broken\n")

	output, err := runMakeWithEnv(repoDir, "hooks-uninstall")
	if err == nil {
		t.Fatalf("uninstall with malformed foreign worktree config unexpectedly succeeded:\n%s", output)
	}
	managedHook := managedHookPath(t, repoDir)
	assertConfigValues(t, repoDir, "--local", filepath.Dir(managedHook))
	if _, err := os.Stat(managedHook); err != nil {
		t.Fatalf("uninstaller removed managed hook before foreign-config refusal: %v", err)
	}
}

func TestHooksUninstallRejectsMalformedGlobalIncludedConfigBeforeDeletingManagedHook(t *testing.T) {
	t.Parallel()

	repoDir := newHookTestRepository(t)
	runCommand(t, repoDir, "make", "hooks-install")
	managedHook := managedHookPath(t, repoDir)
	managedDir := filepath.Dir(managedHook)
	globalConfig := filepath.Join(t.TempDir(), "global.gitconfig")
	brokenConfig := filepath.Join(t.TempDir(), "broken.gitconfig")
	writeFile(t, globalConfig, "[include]\n\tpath = "+brokenConfig+"\n")
	writeFile(t, brokenConfig, "[broken\n")

	output, err := runMakeWithEnv(repoDir, "hooks-uninstall", "GIT_CONFIG_GLOBAL="+globalConfig, "GIT_CONFIG_NOSYSTEM=1")
	if err == nil {
		t.Fatalf("uninstall with malformed global included config unexpectedly succeeded:\n%s", output)
	}
	assertConfigValues(t, repoDir, "--local", managedDir)
	if _, err := os.Stat(managedHook); err != nil {
		t.Fatalf("uninstaller removed managed hook before global-config refusal: %v", err)
	}
}

func TestHooksInstallRefusesIncludedAndNewlineHooksPathsWithoutMutation(t *testing.T) {
	t.Parallel()

	repoDir := newHookTestRepository(t)
	linkedDir := filepath.Join(t.TempDir(), "linked")
	runCommand(t, repoDir, "git", "config", "extensions.worktreeConfig", "true")
	runCommand(t, repoDir, "git", "worktree", "add", linkedDir)
	includeFile := filepath.Join(t.TempDir(), "hooks.inc")
	writeFile(t, includeFile, "[core]\n\thooksPath = .githooks\n")
	runCommand(t, linkedDir, "git", "config", "--worktree", "includeIf.onbranch:main.path", includeFile)

	command := exec.Command("make", "hooks-install")
	command.Dir = repoDir
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "includes another config") {
		t.Fatalf("included worktree install = %v\n%s", err, output)
	}
	if _, err := os.Stat(filepath.Dir(managedHookPath(t, repoDir))); !os.IsNotExist(err) {
		t.Fatalf("installer created managed hook directory before refusal: %v", err)
	}

	runCommand(t, linkedDir, "git", "config", "--worktree", "--unset-all", "includeIf.onbranch:main.path")
	newlinePath := ".githooks\n" + filepath.Dir(managedHookPath(t, repoDir))
	runCommand(t, repoDir, "git", "config", "--local", "core.hooksPath", newlinePath)
	command = exec.Command("make", "hooks-install")
	command.Dir = repoDir
	output, err = command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "Refusing to replace") {
		t.Fatalf("newline hook path install = %v\n%s", err, output)
	}
	if got := gitOutput(t, repoDir, "config", "--local", "--get", "core.hooksPath"); got != newlinePath {
		t.Fatalf("newline core.hooksPath = %q", got)
	}
}

func TestHooksInstallUninstallPreservesCustomMultiValuePathsAndSharedHook(t *testing.T) {
	t.Parallel()

	repoDir := newHookTestRepository(t)
	linkedDir := filepath.Join(t.TempDir(), "linked")
	runCommand(t, repoDir, "git", "config", "extensions.worktreeConfig", "true")
	runCommand(t, repoDir, "make", "hooks-install")
	managedDir := filepath.Dir(managedHookPath(t, repoDir))
	localCustom := filepath.Join(repoDir, "custom local hooks")
	worktreeCustom := filepath.Join(repoDir, "custom worktree hooks")
	runCommand(t, repoDir, "git", "config", "--local", "--add", "core.hooksPath", localCustom)
	runCommand(t, repoDir, "git", "config", "--local", "--add", "core.hooksPath", ".githooks")
	runCommand(t, repoDir, "git", "config", "--worktree", "--add", "core.hooksPath", worktreeCustom)
	runCommand(t, repoDir, "git", "config", "--worktree", "--add", "core.hooksPath", ".githooks")
	runCommand(t, repoDir, "git", "worktree", "add", linkedDir)
	runCommand(t, linkedDir, "git", "config", "--worktree", "--replace-all", "core.hooksPath", managedDir)
	includeFile := filepath.Join(t.TempDir(), "shared-hooks.inc")
	writeFile(t, includeFile, "[core]\n\thooksPath = "+managedDir+"\n")
	runCommand(t, linkedDir, "git", "config", "--worktree", "include.path", includeFile)

	command := exec.Command("make", "hooks-uninstall")
	command.Dir = repoDir
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("hooks-uninstall = %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "Preserved") || strings.Contains(string(output), "Removed") {
		t.Fatalf("preserved-hook uninstall output = %q", output)
	}
	assertConfigValues(t, repoDir, "--local", localCustom)
	assertConfigValues(t, repoDir, "--worktree", worktreeCustom)
	assertConfigValues(t, linkedDir, "--worktree", managedDir)
	if got := gitOutput(t, linkedDir, "config", "--includes", "--get-all", "core.hooksPath"); !strings.Contains(got, managedDir) {
		t.Fatalf("included shared hook reference = %q", got)
	}
	if _, err := os.Stat(managedHookPath(t, repoDir)); err != nil {
		t.Fatalf("shared managed hook was removed while linked worktree still uses it: %v", err)
	}
}

func TestHooksUninstallPreservesHookForForeignManagedPathAliases(t *testing.T) {
	t.Parallel()

	repoDir := newHookTestRepository(t)
	runCommand(t, repoDir, "git", "config", "extensions.worktreeConfig", "true")
	runCommand(t, repoDir, "make", "hooks-install")
	managedHook := managedHookPath(t, repoDir)
	managedDir := filepath.Dir(managedHook)
	linkedDir := filepath.Join(t.TempDir(), "linked")
	runCommand(t, repoDir, "git", "worktree", "add", linkedDir)
	relativeManagedDir, err := filepath.Rel(linkedDir, managedDir)
	if err != nil {
		t.Fatalf("make managed hook path relative: %v", err)
	}
	runCommand(t, linkedDir, "git", "config", "--worktree", "--add", "core.hooksPath", managedDir+string(filepath.Separator))
	runCommand(t, linkedDir, "git", "config", "--worktree", "--add", "core.hooksPath", relativeManagedDir)

	runCommand(t, repoDir, "make", "hooks-uninstall")
	if _, err := os.Stat(managedHook); err != nil {
		t.Fatalf("uninstaller removed managed hook used through foreign aliases: %v", err)
	}
}

func newHookTestRepository(t *testing.T) string {
	t.Helper()

	repoDir := filepath.Join(t.TempDir(), "repo")
	runCommand(t, t.TempDir(), "git", "init", "-b", "main", repoDir)
	runCommand(t, repoDir, "git", "config", "user.name", "Test User")
	runCommand(t, repoDir, "git", "config", "user.email", "test@example.com")
	writeFile(t, filepath.Join(repoDir, "Makefile"), readRepositoryFile(t, "Makefile"))
	writeFileMode(t, filepath.Join(repoDir, ".githooks", "pre-commit"), readRepositoryHook(t), 0o755)
	writeFile(t, filepath.Join(repoDir, "tracked.txt"), "baseline\n")
	runCommand(t, repoDir, "git", "add", ".")
	runCommand(t, repoDir, "git", "-c", "core.hooksPath=/dev/null", "commit", "-m", "baseline")
	return repoDir
}

func managedHookPath(t *testing.T, repoDir string) string {
	t.Helper()
	commonDir := gitOutput(t, repoDir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	return filepath.Join(commonDir, "lopper-hooks", "pre-commit")
}

func readRepositoryHook(t *testing.T) string {
	t.Helper()
	return readRepositoryFile(t, filepath.Join(".githooks", "pre-commit"))
}

func readRepositoryFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(repoPath(t, path))
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func assertFileEquals(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(data) != want {
		t.Fatalf("contents of %s differ from reviewed hook", path)
	}
}

func assertFileContains(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if !strings.Contains(string(data), want) {
		t.Fatalf("%s does not contain %q", path, want)
	}
}

func assertFileDoesNotContain(t *testing.T, path, unwanted string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if strings.Contains(string(data), unwanted) {
		t.Fatalf("%s unexpectedly contains %q", path, unwanted)
	}
}

func assertConfigEquals(t *testing.T, repoDir, want string) {
	t.Helper()
	if got := gitOutput(t, repoDir, "config", "--get", "core.hooksPath"); got != want {
		t.Fatalf("core.hooksPath = %q, want %q", got, want)
	}
}

func gitOutput(t *testing.T, repoDir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = repoDir
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

func runMakeWithEnv(repoDir, target string, env ...string) ([]byte, error) {
	command := exec.Command("make", target)
	command.Dir = repoDir
	command.Env = append(os.Environ(), env...)
	return command.CombinedOutput()
}

func assertNoHooksPath(t *testing.T, repoDir, scope, scopeName string) {
	t.Helper()
	command := exec.Command("git", "config", scope, "--get", "core.hooksPath")
	command.Dir = repoDir
	output, err := command.CombinedOutput()
	if err == nil || len(output) != 0 {
		t.Fatalf("%s core.hooksPath remains: %v\n%s", scopeName, err, output)
	}
}

func assertConfigValues(t *testing.T, repoDir, scope string, want ...string) {
	t.Helper()
	got := strings.Split(gitOutput(t, repoDir, "config", scope, "--get-all", "core.hooksPath"), "\n")
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("%s core.hooksPath = %q, want %q", scope, got, want)
	}
}

func assertCommitFails(t *testing.T, repoDir, wantOutput string) string {
	t.Helper()
	return assertCommitFailsWithEnv(t, repoDir, wantOutput, nil)
}

func assertCommitFailsWithEnv(t *testing.T, repoDir, wantOutput string, env []string) string {
	t.Helper()
	command := exec.Command("git", "commit", "-m", "must fail")
	command.Dir = repoDir
	command.Env = append(os.Environ(), env...)
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), wantOutput) {
		t.Fatalf("commit = %v\n%s", err, output)
	}
	return string(output)
}

func runCommitWithHook(t *testing.T, repoDir, message string) {
	t.Helper()
	command := exec.Command("git", "commit", "-m", message)
	command.Dir = repoDir
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git commit -m %s: %v\n%s", message, err, output)
	}
}
