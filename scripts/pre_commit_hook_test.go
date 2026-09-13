package scripts

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/gitexec"
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

	for _, name := range []string{": staged.go", "0:unformatted.go", ":(glob)literal.go"} {
		writeFile(t, filepath.Join(repoDir, name), "package fixture\n\nfunc adversarialName(){}\n")
		runCommand(t, repoDir, "git", "--literal-pathspecs", "add", "--", "./"+name)
		output := assertCommitFails(t, repoDir, "staged Go files must be gofmt-formatted")
		if !strings.Contains(output, name) {
			t.Fatalf("gofmt diagnostic does not identify %q:\n%s", name, output)
		}
		runCommand(t, repoDir, "git", "reset", "--", "./"+name)
	}
}

func TestHooksInstallSecuresManagedHookDirectory(t *testing.T) {
	t.Parallel()

	repoDir := newHookTestRepository(t)
	managedDir := filepath.Dir(managedHookPath(t, repoDir))
	if err := os.MkdirAll(managedDir, 0o755); err != nil {
		t.Fatalf("create managed hook directory: %v", err)
	}
	if err := os.Chmod(managedDir, 0o777); err != nil {
		t.Fatalf("make managed hook directory permissive: %v", err)
	}

	output, err := runMakeWithUmask(repoDir, "hooks-install", "000")
	if err != nil {
		t.Fatalf("hooks-install with permissive umask = %v\n%s", err, output)
	}
	info, err := os.Stat(managedDir)
	if err != nil {
		t.Fatalf("stat managed hook directory: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("managed hook directory permissions = %o, want 700", got)
	}
}

func TestHooksInstallRefusesManagedHookDirectoryBeforeActivation(t *testing.T) {
	t.Parallel()

	repoDir := newHookTestRepository(t)
	managedHook := managedHookPath(t, repoDir)
	if err := os.MkdirAll(managedHook, 0o700); err != nil {
		t.Fatalf("create managed hook directory: %v", err)
	}
	writeFile(t, filepath.Join(managedHook, "sentinel"), "preserve\n")

	output, err := runMakeWithEnv(repoDir, "hooks-install")
	if err == nil || !strings.Contains(string(output), "Refusing to replace managed pre-commit hook directory") {
		t.Fatalf("install with managed hook directory = %v\n%s", err, output)
	}
	assertNoHooksPath(t, repoDir, "--local", "local")
	if _, err := os.Stat(filepath.Join(managedHook, "sentinel")); err != nil {
		t.Fatalf("managed hook directory was modified before refusal: %v", err)
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

func TestHooksInstallMigratesDormantCurrentWorktreeLegacyHookPath(t *testing.T) {
	t.Parallel()

	repoDir := newHookTestRepository(t)
	runCommand(t, repoDir, "git", "config", "extensions.worktreeConfig", "true")
	runCommand(t, repoDir, "git", "config", "--worktree", "core.hooksPath", ".githooks")
	runCommand(t, repoDir, "git", "config", "--local", "extensions.worktreeConfig", "false")

	runCommand(t, repoDir, "make", "hooks-install")
	managedHook := managedHookPath(t, repoDir)
	gitDir := gitOutput(t, repoDir, "rev-parse", "--path-format=absolute", "--git-dir")
	if got := gitOutput(t, repoDir, "config", "--file", filepath.Join(gitDir, "config.worktree"), "--get", "core.hooksPath"); got != filepath.Dir(managedHook) {
		t.Fatalf("dormant worktree core.hooksPath = %q, want %q", got, filepath.Dir(managedHook))
	}

	runCommand(t, repoDir, "git", "config", "--local", "extensions.worktreeConfig", "true")
	markerPath := filepath.Join(repoDir, "dormant-worktree-marker")
	writeFileMode(t, filepath.Join(repoDir, ".githooks", "pre-commit"), "#!/bin/sh\ntouch "+markerPath+"\n", 0o755)
	writeFile(t, filepath.Join(repoDir, "dormant-worktree.txt"), "safe\n")
	runCommand(t, repoDir, "git", "add", "dormant-worktree.txt")
	runCommitWithHook(t, repoDir, "dormant worktree hook remains managed")
	if _, err := os.Stat(markerPath); !os.IsNotExist(err) {
		t.Fatalf("dormant worktree hook executed checkout content: %v", err)
	}
}

func TestHooksInstallRefusesDormantCurrentWorktreeCustomHookPathBeforeMutation(t *testing.T) {
	t.Parallel()

	repoDir := newHookTestRepository(t)
	customDir := filepath.Join(repoDir, "dormant-custom-hooks")
	runCommand(t, repoDir, "git", "config", "extensions.worktreeConfig", "true")
	runCommand(t, repoDir, "git", "config", "--worktree", "core.hooksPath", customDir)
	runCommand(t, repoDir, "git", "config", "--local", "extensions.worktreeConfig", "false")

	output, err := runMakeWithEnv(repoDir, "hooks-install")
	if err == nil || !strings.Contains(string(output), "Refusing to replace current worktree dormant core.hooksPath") {
		t.Fatalf("install with dormant custom worktree hook = %v\n%s", err, output)
	}
	assertNoHooksPath(t, repoDir, "--local", "local")
	gitDir := gitOutput(t, repoDir, "rev-parse", "--path-format=absolute", "--git-dir")
	if got := gitOutput(t, repoDir, "config", "--file", filepath.Join(gitDir, "config.worktree"), "--get", "core.hooksPath"); got != customDir {
		t.Fatalf("dormant custom worktree core.hooksPath = %q, want %q", got, customDir)
	}
	if _, err := os.Stat(filepath.Dir(managedHookPath(t, repoDir))); !os.IsNotExist(err) {
		t.Fatalf("installer created managed hook directory before dormant custom-path refusal: %v", err)
	}
}

func TestHooksInstallRefusesDormantForeignWorktreeHookPathsBeforeMutation(t *testing.T) {
	t.Parallel()

	for _, hooksPath := range []string{".githooks", "foreign dormant custom hooks"} {
		t.Run(hooksPath, func(t *testing.T) {
			repoDir := newHookTestRepository(t)
			linkedDir := filepath.Join(t.TempDir(), "linked")
			runCommand(t, repoDir, "git", "config", "extensions.worktreeConfig", "true")
			runCommand(t, repoDir, "git", "worktree", "add", linkedDir)
			runCommand(t, linkedDir, "git", "config", "--worktree", "core.hooksPath", hooksPath)
			runCommand(t, repoDir, "git", "config", "--local", "extensions.worktreeConfig", "false")

			output, err := runMakeWithEnv(repoDir, "hooks-install")
			if err == nil || !strings.Contains(string(output), "another worktree") || !strings.Contains(string(output), "dormant") {
				t.Fatalf("install with dormant foreign worktree hook = %v\n%s", err, output)
			}
			assertNoHooksPath(t, repoDir, "--local", "local")
			foreignGitDir := gitOutput(t, linkedDir, "rev-parse", "--path-format=absolute", "--git-dir")
			if got := gitOutput(t, repoDir, "config", "--file", filepath.Join(foreignGitDir, "config.worktree"), "--get", "core.hooksPath"); got != hooksPath {
				t.Fatalf("dormant foreign worktree core.hooksPath = %q, want %q", got, hooksPath)
			}
			if _, err := os.Stat(filepath.Dir(managedHookPath(t, repoDir))); !os.IsNotExist(err) {
				t.Fatalf("installer created managed hook directory before dormant foreign-path refusal: %v", err)
			}
		})
	}
}

func TestHooksInstallLeavesConfigUnchangedWhenLegacyWorktreeConfigIsLocked(t *testing.T) {
	t.Parallel()

	repoDir := newHookTestRepository(t)
	runCommand(t, repoDir, "git", "config", "extensions.worktreeConfig", "true")
	runCommand(t, repoDir, "git", "config", "--worktree", "core.hooksPath", ".githooks")
	gitDir := gitOutput(t, repoDir, "rev-parse", "--path-format=absolute", "--git-dir")
	writeFile(t, filepath.Join(gitDir, "config.worktree.lock"), "locked\n")

	output, err := runMakeWithEnv(repoDir, "hooks-install")
	if err == nil {
		t.Fatalf("install with locked worktree config unexpectedly succeeded:\n%s", output)
	}
	assertNoHooksPath(t, repoDir, "--local", "local")
	assertConfigValues(t, repoDir, "--worktree", ".githooks")
}

func TestHooksInstallRollsBackSnapshotWhenLocalConfigIsLocked(t *testing.T) {
	t.Parallel()

	repoDir := newHookTestRepository(t)
	runCommand(t, repoDir, "git", "config", "--local", "core.hooksPath", ".githooks")
	gitDir := gitOutput(t, repoDir, "rev-parse", "--path-format=absolute", "--git-dir")
	writeFile(t, filepath.Join(gitDir, "config.lock"), "locked\n")

	output, err := runMakeWithEnv(repoDir, "hooks-install")
	if err == nil || !strings.Contains(string(output), "could not lock config file") {
		t.Fatalf("install with locked local config = %v\n%s", err, output)
	}
	assertConfigValues(t, repoDir, "--local", ".githooks")
	if _, err := os.Stat(filepath.Dir(managedHookPath(t, repoDir))); !os.IsNotExist(err) {
		t.Fatalf("installer retained a snapshot after local-config failure: %v", err)
	}
}

func TestHooksInstallRollsBackWorktreeConfigWhenLocalConfigIsLocked(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name                       string
		worktreeConfigStillEnabled bool
	}{
		{name: "active worktree config", worktreeConfigStillEnabled: true},
		{name: "dormant worktree config", worktreeConfigStillEnabled: false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			repoDir := newHookTestRepository(t)
			runCommand(t, repoDir, "git", "config", "extensions.worktreeConfig", "true")
			runCommand(t, repoDir, "git", "config", "--worktree", "core.hooksPath", ".githooks")
			if !testCase.worktreeConfigStillEnabled {
				runCommand(t, repoDir, "git", "config", "--local", "extensions.worktreeConfig", "false")
			}
			gitDir := gitOutput(t, repoDir, "rev-parse", "--path-format=absolute", "--git-dir")
			writeFile(t, filepath.Join(gitDir, "config.lock"), "locked\n")

			output, err := runMakeWithEnv(repoDir, "hooks-install")
			if err == nil || !strings.Contains(string(output), "could not lock config file") {
				t.Fatalf("install with locked local config = %v\n%s", err, output)
			}
			if got := gitOutput(t, repoDir, "config", "--file", filepath.Join(gitDir, "config.worktree"), "--get", "core.hooksPath"); got != ".githooks" {
				t.Fatalf("worktree core.hooksPath after rollback = %q, want .githooks", got)
			}
			assertNoHooksPath(t, repoDir, "--local", "local")
			if _, err := os.Stat(filepath.Dir(managedHookPath(t, repoDir))); !os.IsNotExist(err) {
				t.Fatalf("installer retained a snapshot after worktree-config rollback: %v", err)
			}
		})
	}
}

func TestHooksInstallRollsBackAllConfigurationWhenActivationVerificationFails(t *testing.T) {
	t.Parallel()

	repoDir := newHookTestRepository(t)
	runCommand(t, repoDir, "git", "config", "extensions.worktreeConfig", "true")
	runCommand(t, repoDir, "git", "config", "--worktree", "core.hooksPath", ".githooks")
	runCommand(t, repoDir, "git", "config", "--local", "core.hooksPath", ".githooks")
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("find git: %v", err)
	}
	wrapperDir := t.TempDir()
	writeFileMode(t, filepath.Join(wrapperDir, "git"), fmt.Sprintf(`#!/bin/sh
for arg do
	if [ "$arg" = "--fixed-value" ]; then
		echo "forced activation verification failure" >&2
		exit 73
	fi
done
exec %q "$@"
`, gitPath), 0o755)

	output, err := runMakeWithEnv(repoDir, "hooks-install", "PATH="+wrapperDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err == nil || !strings.Contains(string(output), "Managed core.hooksPath was not activated") {
		t.Fatalf("install with activation verification failure = %v\n%s", err, output)
	}
	assertConfigValues(t, repoDir, "--local", ".githooks")
	assertConfigValues(t, repoDir, "--worktree", ".githooks")
	if _, err := os.Stat(filepath.Dir(managedHookPath(t, repoDir))); !os.IsNotExist(err) {
		t.Fatalf("installer retained a snapshot after activation-verification rollback: %v", err)
	}
}

func TestHooksInstallPreservesRecoveryStateWhenConfigRollbackFails(t *testing.T) {
	t.Parallel()

	repoDir := newHookTestRepository(t)
	runCommand(t, repoDir, "git", "config", "--local", "core.hooksPath", ".githooks")
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("find git: %v", err)
	}
	mvPath, err := exec.LookPath("mv")
	if err != nil {
		t.Fatalf("find mv: %v", err)
	}
	wrapperDir := t.TempDir()
	writeFileMode(t, filepath.Join(wrapperDir, "git"), fmt.Sprintf(`#!/bin/sh
for arg do
	if [ "$arg" = "--fixed-value" ]; then
		echo "forced activation verification failure" >&2
		exit 73
	fi
done
exec %q "$@"
`, gitPath), 0o755)
	writeFileMode(t, filepath.Join(wrapperDir, "mv"), fmt.Sprintf(`#!/bin/sh
for arg do
	case "$arg" in
	*.config.*.backup)
		echo "forced config rollback failure" >&2
		exit 74
		;;
	esac
done
exec %q "$@"
`, mvPath), 0o755)

	output, err := runMakeWithEnv(repoDir, "hooks-install", "PATH="+wrapperDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err == nil || !strings.Contains(string(output), "Unable to restore hook configuration") {
		t.Fatalf("install with failed config rollback = %v\n%s", err, output)
	}
	managedHook := managedHookPath(t, repoDir)
	if _, err := os.Stat(managedHook); err != nil {
		t.Fatalf("installer removed reviewed hook after config rollback failure: %v", err)
	}
	if got := gitOutput(t, repoDir, "config", "--local", "--get", "core.hooksPath"); got != filepath.Dir(managedHook) {
		t.Fatalf("local core.hooksPath after failed rollback = %q, want %q", got, filepath.Dir(managedHook))
	}
	backups, err := filepath.Glob(filepath.Join(filepath.Dir(managedHook), ".config.*.backup"))
	if err != nil || len(backups) == 0 {
		t.Fatalf("config recovery backup was not retained: %v", err)
	}
}

func TestHooksInstallTerminatesThroughRollbackOnActivationSignal(t *testing.T) {
	t.Parallel()

	repoDir := newHookTestRepository(t)
	runCommand(t, repoDir, "git", "config", "--local", "core.hooksPath", ".githooks")
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("find git: %v", err)
	}
	wrapperDir := t.TempDir()
	writeFileMode(t, filepath.Join(wrapperDir, "git"), fmt.Sprintf(`#!/bin/sh
for arg do
	if [ "$arg" = "--fixed-value" ]; then
		kill -TERM "$PPID"
		exit 73
	fi
done
exec %q "$@"
`, gitPath), 0o755)

	output, err := runMakeWithEnv(repoDir, "hooks-install", "PATH="+wrapperDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err == nil || strings.Contains(string(output), "Installed reviewed pre-commit hook") {
		t.Fatalf("install interrupted during activation = %v\n%s", err, output)
	}
	assertConfigValues(t, repoDir, "--local", ".githooks")
	if _, err := os.Stat(filepath.Dir(managedHookPath(t, repoDir))); !os.IsNotExist(err) {
		t.Fatalf("installer retained hook state after activation signal: %v", err)
	}
}

func TestHooksInstallRefusesManagedHookSymlinkBeforeMutation(t *testing.T) {
	t.Parallel()

	repoDir := newHookTestRepository(t)
	managedHook := managedHookPath(t, repoDir)
	target := filepath.Join(t.TempDir(), "pre-commit-target")
	writeFileMode(t, target, "#!/bin/sh\nexit 0\n", 0o755)
	if err := os.MkdirAll(filepath.Dir(managedHook), 0o700); err != nil {
		t.Fatalf("create managed hook directory: %v", err)
	}
	if err := os.Symlink(target, managedHook); err != nil {
		t.Fatalf("create managed hook symlink: %v", err)
	}

	output, err := runMakeWithEnv(repoDir, "hooks-install")
	if err == nil || !strings.Contains(string(output), "Refusing to replace managed pre-commit hook symlink") {
		t.Fatalf("install with managed hook symlink = %v\n%s", err, output)
	}
	if info, err := os.Lstat(managedHook); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("managed hook symlink was modified before refusal: %v", err)
	}
	assertFileEquals(t, target, "#!/bin/sh\nexit 0\n")
	assertNoHooksPath(t, repoDir, "--local", "local")
}

func TestHooksInstallRefusesManagedHookDirectorySymlinkBeforeMutation(t *testing.T) {
	t.Parallel()
	repoDir := newHookTestRepository(t)
	commonDir := gitOutput(t, repoDir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	targetDir := filepath.Join(t.TempDir(), "outside")
	if err := os.Mkdir(targetDir, 0o700); err != nil {
		t.Fatalf("create target: %v", err)
	}
	if err := os.Symlink(targetDir, filepath.Join(commonDir, "lopper-hooks")); err != nil {
		t.Fatalf("symlink managed directory: %v", err)
	}
	output, err := runMakeWithEnv(repoDir, "hooks-install")
	if err == nil || !strings.Contains(string(output), "Refusing unsafe managed hook directory") {
		t.Fatalf("install with managed directory symlink = %v\n%s", err, output)
	}
	if _, err := os.Stat(filepath.Join(targetDir, "pre-commit")); !os.IsNotExist(err) {
		t.Fatalf("installer wrote through managed directory symlink: %v", err)
	}
	assertNoHooksPath(t, repoDir, "--local", "local")
}

func TestHooksUninstallRefusesManagedHookDirectorySymlinkBeforeMutation(t *testing.T) {
	t.Parallel()

	repoDir := newHookTestRepository(t)
	runCommand(t, repoDir, "make", "hooks-install")

	managedHook := managedHookPath(t, repoDir)
	managedDir := filepath.Dir(managedHook)
	configPath := filepath.Join(gitOutput(t, repoDir, "rev-parse", "--path-format=absolute", "--git-dir"), "config")
	configBefore, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read hook config before unsafe uninstall: %v", err)
	}
	if err := os.Remove(managedHook); err != nil {
		t.Fatalf("remove managed hook before symlink setup: %v", err)
	}
	if err := os.Remove(managedDir); err != nil {
		t.Fatalf("remove managed hook directory before symlink setup: %v", err)
	}
	victimDir := filepath.Join(t.TempDir(), "outside")
	if err := os.Mkdir(victimDir, 0o700); err != nil {
		t.Fatalf("create external hook directory: %v", err)
	}
	victimHook := filepath.Join(victimDir, "pre-commit")
	writeFileMode(t, victimHook, "#!/bin/sh\nexit 0\n", 0o755)
	if err := os.Symlink(victimDir, managedDir); err != nil {
		t.Fatalf("symlink managed hook directory: %v", err)
	}

	output, err := runMakeWithEnv(repoDir, "hooks-uninstall")
	if err == nil || !strings.Contains(string(output), "Refusing unsafe managed hook directory") {
		t.Fatalf("uninstall with managed directory symlink = %v\n%s", err, output)
	}
	assertFileEquals(t, victimHook, "#!/bin/sh\nexit 0\n")
	configAfter, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read hook config after unsafe uninstall: %v", err)
	}
	if string(configAfter) != string(configBefore) {
		t.Fatalf("uninstall modified config before unsafe-directory refusal")
	}
}

func TestHooksUninstallRefusesUnsafeManagedHookBeforeConfigMutation(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name    string
		prepare func(t *testing.T, path string)
		verify  func(t *testing.T, path string)
	}{
		{
			name: "directory",
			prepare: func(t *testing.T, path string) {
				t.Helper()
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatalf("create managed hook directory: %v", err)
				}
			},
			verify: func(t *testing.T, path string) {
				t.Helper()
				info, err := os.Lstat(path)
				if err != nil || !info.IsDir() {
					t.Fatalf("managed hook directory was modified before refusal: %v", err)
				}
			},
		},
		{
			name: "FIFO",
			prepare: func(t *testing.T, path string) {
				t.Helper()
				mkfifoPath, err := exec.LookPath("mkfifo")
				if err != nil {
					t.Skip("mkfifo is unavailable on this host")
				}
				if output, err := exec.Command(mkfifoPath, path).CombinedOutput(); err != nil {
					t.Fatalf("create managed hook FIFO: %v\n%s", err, output)
				}
			},
			verify: func(t *testing.T, path string) {
				t.Helper()
				info, err := os.Lstat(path)
				if err != nil || info.Mode()&os.ModeNamedPipe == 0 {
					t.Fatalf("managed hook FIFO was modified before refusal: %v", err)
				}
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			repoDir := newHookTestRepository(t)
			runCommand(t, repoDir, "make", "hooks-install")

			managedHook := managedHookPath(t, repoDir)
			configPath := filepath.Join(gitOutput(t, repoDir, "rev-parse", "--path-format=absolute", "--git-dir"), "config")
			configBefore, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatalf("read hook config before unsafe uninstall: %v", err)
			}
			if err := os.Remove(managedHook); err != nil {
				t.Fatalf("remove managed hook before unsafe setup: %v", err)
			}
			testCase.prepare(t, managedHook)

			output, err := runMakeWithEnv(repoDir, "hooks-uninstall")
			if err == nil || !strings.Contains(string(output), "Refusing unsafe managed pre-commit hook") {
				t.Fatalf("uninstall with unsafe managed hook = %v\n%s", err, output)
			}
			testCase.verify(t, managedHook)
			configAfter, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatalf("read hook config after unsafe uninstall: %v", err)
			}
			if string(configAfter) != string(configBefore) {
				t.Fatalf("uninstall modified config before unsafe-hook refusal")
			}
		})
	}
}

func TestHooksInstallRefusesManagedHookFIFOBeforeMutation(t *testing.T) {
	t.Parallel()

	mkfifoPath, err := exec.LookPath("mkfifo")
	if err != nil {
		t.Skip("mkfifo is unavailable on this host")
	}
	repoDir := newHookTestRepository(t)
	managedHook := managedHookPath(t, repoDir)
	if err := os.MkdirAll(filepath.Dir(managedHook), 0o700); err != nil {
		t.Fatalf("create managed hook directory: %v", err)
	}
	command := exec.Command(mkfifoPath, managedHook)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("create managed hook FIFO: %v\n%s", err, output)
	}

	output, err := runMakeWithEnv(repoDir, "hooks-install")
	if err == nil || !strings.Contains(string(output), "Refusing to replace non-regular managed pre-commit hook") {
		t.Fatalf("install with managed hook FIFO = %v\n%s", err, output)
	}
	info, err := os.Lstat(managedHook)
	if err != nil || info.Mode()&os.ModeNamedPipe == 0 {
		t.Fatalf("managed hook FIFO was modified before refusal: %v", err)
	}
	assertNoHooksPath(t, repoDir, "--local", "local")
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
	command.Env = hookTestEnv()
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
	command.Env = hookTestEnv()
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
	command.Env = hookTestEnv()
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

func TestManagedHookSkipsNonRegularStagedGoEntries(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name  string
		stage func(t *testing.T, repoDir string)
	}{
		{
			name: "symlink",
			stage: func(t *testing.T, repoDir string) {
				goPath := filepath.Join(repoDir, "replacement.go")
				writeFile(t, goPath, "package fixture\n")
				runCommand(t, repoDir, "git", "add", "replacement.go")
				runCommand(t, repoDir, "git", "-c", "core.hooksPath=/dev/null", "commit", "-m", "add regular Go file")
				if err := os.Remove(goPath); err != nil {
					t.Fatalf("remove regular Go file: %v", err)
				}
				if err := os.Symlink("tracked.txt", goPath); err != nil {
					t.Fatalf("replace Go file with symlink: %v", err)
				}
				runCommand(t, repoDir, "git", "add", "replacement.go")
			},
		},
		{
			name: "gitlink",
			stage: func(t *testing.T, repoDir string) {
				treeID := gitOutput(t, repoDir, "rev-parse", "HEAD^{tree}")
				runCommand(t, repoDir, "git", "update-index", "--add", "--cacheinfo", "160000,"+treeID+",gitlink.go")
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			repoDir := newHookTestRepository(t)
			runCommand(t, repoDir, "make", "hooks-install")
			testCase.stage(t, repoDir)
			runCommitWithHook(t, repoDir, "allow non-regular Go entry")
		})
	}
}

func TestManagedHookFailsClosedWhenStagedEntryQueryFails(t *testing.T) {
	t.Parallel()

	repoDir := newHookTestRepository(t)
	runCommand(t, repoDir, "make", "hooks-install")
	writeFile(t, filepath.Join(repoDir, "unformatted.go"), "package fixture\n\nfunc unformatted(){}\n")
	runCommand(t, repoDir, "git", "add", "unformatted.go")

	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("find git: %v", err)
	}
	wrapperDir := t.TempDir()
	writeFileMode(t, filepath.Join(wrapperDir, "git"), fmt.Sprintf(`#!/bin/sh
for arg do
	if [ "$arg" = "ls-files" ]; then
		echo "forced staged-entry query failure" >&2
		exit 73
	fi
done
exec %q "$@"
`, gitPath), 0o755)

	command := exec.Command(managedHookPath(t, repoDir))
	command.Dir = repoDir
	command.Env = append(hookTestEnv(), "PATH="+wrapperDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "forced staged-entry query failure") {
		t.Fatalf("hook with failed staged-entry query = %v\n%s", err, output)
	}
	if strings.Contains(string(output), "staged Go files must be gofmt-formatted") {
		t.Fatalf("hook treated the failed staged-entry query as a formatting result:\n%s", output)
	}
}

func TestManagedHookFailsClosedWhenGitQueriesFail(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name    string
		failure string
		want    string
	}{
		{name: "quiet", failure: "quiet", want: "forced quiet query failure"},
		{name: "names", failure: "names", want: "forced name query failure"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			repoDir := newHookTestRepository(t)
			runCommand(t, repoDir, "make", "hooks-install")
			writeFile(t, filepath.Join(repoDir, "unformatted.go"), "package fixture\n\nfunc unformatted(){}\n")
			runCommand(t, repoDir, "git", "add", "unformatted.go")

			gitPath, err := exec.LookPath("git")
			if err != nil {
				t.Fatalf("find git: %v", err)
			}
			wrapperDir := t.TempDir()
			writeFileMode(t, filepath.Join(wrapperDir, "git"), fmt.Sprintf(`#!/bin/sh
for arg do
	if [ "$HOOK_TEST_GIT_FAILURE" = quiet ] && [ "$arg" = "--quiet" ]; then
		echo "forced quiet query failure" >&2
		exit 73
	fi
	if [ "$HOOK_TEST_GIT_FAILURE" = names ] && [ "$arg" = "--name-only" ]; then
		echo "forced name query failure" >&2
		exit 74
	fi
done
exec %q "$@"
`, gitPath), 0o755)

			command := exec.Command(managedHookPath(t, repoDir))
			command.Dir = repoDir
			command.Env = append(hookTestEnv(), "PATH="+wrapperDir+string(os.PathListSeparator)+os.Getenv("PATH"), "HOOK_TEST_GIT_FAILURE="+testCase.failure)
			output, err := command.CombinedOutput()
			if err == nil || !strings.Contains(string(output), testCase.want) {
				t.Fatalf("hook with failed %s query = %v\n%s", testCase.name, err, output)
			}
			if strings.Contains(string(output), "staged Go files must be gofmt-formatted") {
				t.Fatalf("hook treated the failed %s query as a formatting result:\n%s", testCase.name, output)
			}
		})
	}
}

func TestManagedHookSkipsGofmtWhenNoGoFilesAreStaged(t *testing.T) {
	t.Parallel()

	repoDir := newHookTestRepository(t)
	runCommand(t, repoDir, "make", "hooks-install")
	writeFile(t, filepath.Join(repoDir, "only.txt"), "staged\n")
	runCommand(t, repoDir, "git", "add", "only.txt")
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("find git: %v", err)
	}
	wrapperDir := t.TempDir()
	writeFileMode(t, filepath.Join(wrapperDir, "git"), fmt.Sprintf(`#!/bin/sh
for arg do
	if [ "$arg" = "ls-files" ]; then
		echo "unexpected staged-entry query" >&2
		exit 75
	fi
done
exec %q "$@"
`, gitPath), 0o755)
	command := exec.Command(managedHookPath(t, repoDir))
	command.Dir = repoDir
	command.Env = append(hookTestEnv(), "PATH="+wrapperDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("hook without staged Go files = %v\n%s", err, output)
	}
}

func TestManagedHookUsesExplicitAlternateIndex(t *testing.T) {
	t.Parallel()
	repoDir := newHookTestRepository(t)
	runCommand(t, repoDir, "make", "hooks-install")
	writeFile(t, filepath.Join(repoDir, "alternate.go"), "package fixture\n\nfunc alternate() {}\n")
	runCommand(t, repoDir, "git", "add", "alternate.go")
	runCommand(t, repoDir, "git", "-c", "core.hooksPath=/dev/null", "commit", "-m", "formatted main")
	writeFile(t, filepath.Join(repoDir, "alternate.go"), "package fixture\n\nfunc alternate(){}\n")
	indexPath := filepath.Join(t.TempDir(), "alternate.index")
	runCommandWithEnv(t, repoDir, []string{"GIT_INDEX_FILE=" + indexPath}, "git", "read-tree", "HEAD")
	runCommandWithEnv(t, repoDir, []string{"GIT_INDEX_FILE=" + indexPath}, "git", "add", "alternate.go")
	command := exec.Command(managedHookPath(t, repoDir))
	command.Dir = repoDir
	command.Env = append(hookTestEnv(), "GIT_INDEX_FILE="+indexPath)
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "staged Go files must be gofmt-formatted") {
		t.Fatalf("hook with alternate unformatted index = %v\n%s", err, output)
	}
	command = exec.Command(managedHookPath(t, repoDir))
	command.Dir = repoDir
	command.Env = hookTestEnv()
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("hook with formatted main index = %v\n%s", err, output)
	}

	runCommand(t, repoDir, "git", "add", "alternate.go")
	writeFile(t, filepath.Join(repoDir, "alternate.go"), "package fixture\n\nfunc alternate() {}\n")
	formattedAlternateIndex := filepath.Join(t.TempDir(), "formatted-alternate.index")
	runCommandWithEnv(t, repoDir, []string{"GIT_INDEX_FILE=" + formattedAlternateIndex}, "git", "read-tree", "HEAD")
	runCommandWithEnv(t, repoDir, []string{"GIT_INDEX_FILE=" + formattedAlternateIndex}, "git", "add", "alternate.go")
	writeFile(t, filepath.Join(repoDir, "alternate.go"), "package fixture\n\nfunc alternate(){}\n")
	command = exec.Command(managedHookPath(t, repoDir))
	command.Dir = repoDir
	command.Env = append(hookTestEnv(), "GIT_INDEX_FILE="+formattedAlternateIndex)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("hook with formatted alternate index = %v\n%s", err, output)
	}
	command = exec.Command(managedHookPath(t, repoDir))
	command.Dir = repoDir
	command.Env = hookTestEnv()
	output, err = command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "staged Go files must be gofmt-formatted") {
		t.Fatalf("hook with unformatted main index = %v\n%s", err, output)
	}
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
	command.Env = hookTestEnv()
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

func TestHooksUninstallLeavesLocalConfigurationWhenWorktreeConfigIsLocked(t *testing.T) {
	t.Parallel()

	repoDir := newHookTestRepository(t)
	runCommand(t, repoDir, "git", "config", "extensions.worktreeConfig", "true")
	runCommand(t, repoDir, "make", "hooks-install")
	managedDir := filepath.Dir(managedHookPath(t, repoDir))
	runCommand(t, repoDir, "git", "config", "--worktree", "core.hooksPath", managedDir)
	gitDir := gitOutput(t, repoDir, "rev-parse", "--path-format=absolute", "--git-dir")
	writeFile(t, filepath.Join(gitDir, "config.worktree.lock"), "locked\n")

	output, err := runMakeWithEnv(repoDir, "hooks-uninstall")
	if err == nil || !strings.Contains(string(output), "Unable to remove managed current worktree core.hooksPath") {
		t.Fatalf("uninstall with locked worktree config = %v\n%s", err, output)
	}
	assertConfigValues(t, repoDir, "--local", managedDir)
	assertConfigValues(t, repoDir, "--worktree", managedDir)
	if _, err := os.Stat(managedHookPath(t, repoDir)); err != nil {
		t.Fatalf("uninstaller removed managed hook after worktree-config failure: %v", err)
	}
}

func TestHooksUninstallRemovesDormantCurrentWorktreeLegacyHookPath(t *testing.T) {
	t.Parallel()

	repoDir := newHookTestRepository(t)
	runCommand(t, repoDir, "make", "hooks-install")
	runCommand(t, repoDir, "git", "config", "extensions.worktreeConfig", "true")
	runCommand(t, repoDir, "git", "config", "--worktree", "core.hooksPath", ".githooks")
	runCommand(t, repoDir, "git", "config", "--local", "extensions.worktreeConfig", "false")
	runCommand(t, repoDir, "make", "hooks-uninstall")

	gitDir := gitOutput(t, repoDir, "rev-parse", "--path-format=absolute", "--git-dir")
	assertNoHooksPathInFile(t, filepath.Join(gitDir, "config.worktree"))
	if _, err := os.Stat(managedHookPath(t, repoDir)); !os.IsNotExist(err) {
		t.Fatalf("uninstall retained unreferenced managed hook: %v", err)
	}
	runCommand(t, repoDir, "git", "config", "--local", "extensions.worktreeConfig", "true")
	markerPath := filepath.Join(repoDir, "dormant-uninstall-marker")
	writeFileMode(t, filepath.Join(repoDir, ".githooks", "pre-commit"), "#!/bin/sh\ntouch "+markerPath+"\n", 0o755)
	writeFile(t, filepath.Join(repoDir, "dormant-uninstall.txt"), "safe\n")
	runCommand(t, repoDir, "git", "add", "dormant-uninstall.txt")
	runCommitWithHook(t, repoDir, "dormant worktree uninstall")
	if _, err := os.Stat(markerPath); !os.IsNotExist(err) {
		t.Fatalf("dormant worktree hook executed after uninstall: %v", err)
	}
}

func TestHooksUninstallPreservesStateWhenDormantCurrentWorktreeConfigIsLocked(t *testing.T) {
	t.Parallel()

	repoDir := newHookTestRepository(t)
	runCommand(t, repoDir, "make", "hooks-install")
	managedHook := managedHookPath(t, repoDir)
	managedDir := filepath.Dir(managedHook)
	runCommand(t, repoDir, "git", "config", "extensions.worktreeConfig", "true")
	runCommand(t, repoDir, "git", "config", "--worktree", "core.hooksPath", ".githooks")
	runCommand(t, repoDir, "git", "config", "--local", "extensions.worktreeConfig", "false")
	gitDir := gitOutput(t, repoDir, "rev-parse", "--path-format=absolute", "--git-dir")
	writeFile(t, filepath.Join(gitDir, "config.worktree.lock"), "locked\n")

	output, err := runMakeWithEnv(repoDir, "hooks-uninstall")
	if err == nil || !strings.Contains(string(output), "Unable to remove managed current worktree core.hooksPath") {
		t.Fatalf("uninstall with locked dormant worktree config = %v\n%s", err, output)
	}
	assertConfigValues(t, repoDir, "--local", managedDir)
	if got := gitOutput(t, repoDir, "config", "--file", filepath.Join(gitDir, "config.worktree"), "--get", "core.hooksPath"); got != ".githooks" {
		t.Fatalf("dormant worktree core.hooksPath = %q, want .githooks", got)
	}
	if _, err := os.Stat(managedHook); err != nil {
		t.Fatalf("uninstaller removed managed hook before dormant worktree failure: %v", err)
	}
}

func TestHooksUninstallPreservesManagedHookForDormantForeignWorktreeReference(t *testing.T) {
	t.Parallel()

	repoDir := newHookTestRepository(t)
	runCommand(t, repoDir, "make", "hooks-install")
	managedHook := managedHookPath(t, repoDir)
	managedDir := filepath.Dir(managedHook)
	linkedDir := filepath.Join(t.TempDir(), "linked")
	runCommand(t, repoDir, "git", "config", "extensions.worktreeConfig", "true")
	runCommand(t, repoDir, "git", "worktree", "add", linkedDir)
	runCommand(t, linkedDir, "git", "config", "--worktree", "core.hooksPath", managedDir)
	runCommand(t, repoDir, "git", "config", "--local", "extensions.worktreeConfig", "false")

	runCommand(t, repoDir, "make", "hooks-uninstall")
	assertNoHooksPath(t, repoDir, "--local", "local")
	if _, err := os.Stat(managedHook); err != nil {
		t.Fatalf("uninstaller removed managed hook used by dormant foreign worktree: %v", err)
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
	runCommand(t, repoDir, "git", "config", "--local", "extensions.worktreeConfig", "false")
	configPath := filepath.Join(gitOutput(t, repoDir, "rev-parse", "--path-format=absolute", "--git-dir"), "config")
	configBefore, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read local config before malformed foreign worktree refusal: %v", err)
	}

	output, err := runMakeWithEnv(repoDir, "hooks-uninstall")
	if err == nil {
		t.Fatalf("uninstall with malformed foreign worktree config unexpectedly succeeded:\n%s", output)
	}
	managedHook := managedHookPath(t, repoDir)
	assertConfigValues(t, repoDir, "--local", filepath.Dir(managedHook))
	if _, err := os.Stat(managedHook); err != nil {
		t.Fatalf("uninstaller removed managed hook before foreign-config refusal: %v", err)
	}
	configAfter, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read local config after malformed foreign worktree refusal: %v", err)
	}
	if string(configAfter) != string(configBefore) {
		t.Fatalf("uninstall modified config before malformed foreign worktree refusal")
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
	command.Env = hookTestEnv()
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
	command.Env = hookTestEnv()
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
	command.Env = hookTestEnv()
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

func TestHooksUninstallPreservesHookForForeignAbsoluteManagedPathAliases(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name      string
		hooksPath func(t *testing.T, managedDir string) string
	}{
		{
			name: "trailing slash",
			hooksPath: func(_ *testing.T, managedDir string) string {
				return managedDir + string(filepath.Separator)
			},
		},
		{
			name: "symlink alias",
			hooksPath: func(t *testing.T, managedDir string) string {
				aliasDir := filepath.Join(t.TempDir(), "managed-hooks-alias")
				if err := os.Symlink(managedDir, aliasDir); err != nil {
					t.Skipf("create managed hook alias: %v", err)
				}
				return aliasDir
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			repoDir := newHookTestRepository(t)
			runCommand(t, repoDir, "git", "config", "extensions.worktreeConfig", "true")
			runCommand(t, repoDir, "make", "hooks-install")
			managedHook := managedHookPath(t, repoDir)
			managedDir := filepath.Dir(managedHook)
			linkedDir := filepath.Join(t.TempDir(), "linked")
			runCommand(t, repoDir, "git", "worktree", "add", linkedDir)
			runCommand(t, linkedDir, "git", "config", "--worktree", "core.hooksPath", testCase.hooksPath(t, managedDir))

			runCommand(t, repoDir, "make", "hooks-uninstall")
			if _, err := os.Stat(managedHook); err != nil {
				t.Fatalf("uninstaller removed managed hook used through %s: %v", testCase.name, err)
			}
		})
	}
}

func TestHooksUninstallPreservesHookForForeignRelativeManagedPath(t *testing.T) {
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
	runCommand(t, linkedDir, "git", "config", "--worktree", "core.hooksPath", relativeManagedDir)

	runCommand(t, repoDir, "make", "hooks-uninstall")
	if _, err := os.Stat(managedHook); err != nil {
		t.Fatalf("uninstaller removed managed hook used through a relative foreign path: %v", err)
	}
}

func TestHooksUninstallRemovesUnreferencedHookWithUnrelatedGlobalAbsolutePath(t *testing.T) {
	t.Parallel()

	repoDir := newHookTestRepository(t)
	runCommand(t, repoDir, "make", "hooks-install")
	managedHook := managedHookPath(t, repoDir)
	globalConfig := filepath.Join(t.TempDir(), "global.gitconfig")
	customDir := filepath.Join(t.TempDir(), "custom-hooks")
	if err := os.Mkdir(customDir, 0o755); err != nil {
		t.Fatalf("create unrelated global hook directory: %v", err)
	}
	writeFile(t, globalConfig, "[core]\n\thooksPath = "+customDir+"\n")

	output, err := runMakeWithEnv(repoDir, "hooks-uninstall", "GIT_CONFIG_GLOBAL="+globalConfig, "GIT_CONFIG_NOSYSTEM=1")
	if err != nil {
		t.Fatalf("uninstall with unrelated global hook path = %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "Removed managed pre-commit hook") {
		t.Fatalf("uninstall did not report removal: %s", output)
	}
	if _, err := os.Stat(managedHook); !os.IsNotExist(err) {
		t.Fatalf("uninstaller preserved hook for unrelated global path: %v", err)
	}
}

func TestHooksUninstallReportsAbsentManagedHookNeutrally(t *testing.T) {
	t.Parallel()

	repoDir := newHookTestRepository(t)
	output, err := runMakeWithEnv(repoDir, "hooks-uninstall")
	if err != nil {
		t.Fatalf("uninstall without managed hook = %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "No managed pre-commit hook to remove") || strings.Contains(string(output), "Removed managed") || strings.Contains(string(output), "Preserved managed") {
		t.Fatalf("absent managed hook output = %q", output)
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
	command.Env = hookTestEnv()
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

func runMakeWithEnv(repoDir, target string, env ...string) ([]byte, error) {
	command := exec.Command("make", target)
	command.Dir = repoDir
	command.Env = append(hookTestEnv(), env...)
	return command.CombinedOutput()
}

func hookTestEnv() []string {
	return append(gitexec.SanitizedEnv(), "PATH="+os.Getenv("PATH"))
}

func runCommandWithEnv(t *testing.T, repoDir string, env []string, name string, args ...string) {
	t.Helper()
	command := exec.Command(name, args...)
	command.Dir = repoDir
	command.Env = append(hookTestEnv(), env...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("%s %s failed: %v\n%s", name, strings.Join(args, " "), err, output)
	}
}

func runMakeWithUmask(repoDir, target, mask string) ([]byte, error) {
	command := exec.Command("sh", "-c", "umask \"$1\"; make \"$2\"", "sh", mask, target)
	command.Dir = repoDir
	command.Env = hookTestEnv()
	return command.CombinedOutput()
}

func assertNoHooksPath(t *testing.T, repoDir, scope, scopeName string) {
	t.Helper()
	command := exec.Command("git", "config", scope, "--get", "core.hooksPath")
	command.Dir = repoDir
	command.Env = hookTestEnv()
	output, err := command.CombinedOutput()
	if err == nil || len(output) != 0 {
		t.Fatalf("%s core.hooksPath remains: %v\n%s", scopeName, err, output)
	}
}

func assertNoHooksPathInFile(t *testing.T, configPath string) {
	t.Helper()
	command := exec.Command("git", "config", "--file", configPath, "--get", "core.hooksPath")
	command.Env = hookTestEnv()
	output, err := command.CombinedOutput()
	if err == nil || len(output) != 0 {
		t.Fatalf("%s core.hooksPath remains: %v\n%s", configPath, err, output)
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
	command.Env = append(hookTestEnv(), env...)
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
	command.Env = hookTestEnv()
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git commit -m %s: %v\n%s", message, err, output)
	}
}
