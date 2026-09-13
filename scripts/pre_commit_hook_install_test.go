package scripts

import (
	"fmt"
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
	assertNoHookConfigBackups(t, repoDir)
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
	*.config.backup.*)
		echo "forced config rollback failure" >&2
		exit 74
		;;
	esac
done
exec %q "$@"
`, mvPath), 0o755)

	output, err := runMakeWithEnv(repoDir, "hooks-install", "PATH="+wrapperDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err == nil || !strings.Contains(string(output), "Unable to restore hook installation completely") {
		t.Fatalf("install with failed config rollback = %v\n%s", err, output)
	}
	managedHook := managedHookPath(t, repoDir)
	if _, err := os.Stat(managedHook); err != nil {
		t.Fatalf("installer removed reviewed hook after config rollback failure: %v", err)
	}
	if got := gitOutput(t, repoDir, "config", "--local", "--get", "core.hooksPath"); got != filepath.Dir(managedHook) {
		t.Fatalf("local core.hooksPath after failed rollback = %q, want %q", got, filepath.Dir(managedHook))
	}
	backups, err := filepath.Glob(filepath.Join(filepath.Dir(managedHook), ".config.backup.*"))
	if err != nil || len(backups) == 0 {
		t.Fatalf("config recovery backup was not retained: %v", err)
	}
}

func TestHooksInstallReportsManagedHookRollbackRemovalFailure(t *testing.T) {
	t.Parallel()

	repoDir := newHookTestRepository(t)
	gitDir := gitOutput(t, repoDir, "rev-parse", "--path-format=absolute", "--git-dir")
	configPath := filepath.Join(gitDir, "config")
	configBefore, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config before activation failure: %v", err)
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("find git: %v", err)
	}
	rmPath, err := exec.LookPath("rm")
	if err != nil {
		t.Fatalf("find rm: %v", err)
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
	writeFileMode(t, filepath.Join(wrapperDir, "rm"), fmt.Sprintf(`#!/bin/sh
for arg do
	case "$arg" in
	*/lopper-hooks/pre-commit)
		echo "forced managed hook rollback removal failure" >&2
		exit 74
		;;
	esac
done
exec %q "$@"
`, rmPath), 0o755)

	output, err := runMakeWithEnv(repoDir, "hooks-install", "PATH="+wrapperDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err == nil || !strings.Contains(string(output), "Unable to restore hook installation completely") || !strings.Contains(string(output), "forced managed hook rollback removal failure") {
		t.Fatalf("install with failed managed hook rollback removal = %v\n%s", err, output)
	}
	assertFileEquals(t, configPath, string(configBefore))
	assertNoHooksPath(t, repoDir, "--local", "local")
	if _, err := os.Stat(managedHookPath(t, repoDir)); err != nil {
		t.Fatalf("managed hook was not retained for recovery: %v", err)
	}
}

func TestHooksInstallCleansTemporaryHookAfterManagedHookMoveFailure(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name          string
		rollbackFails bool
	}{
		{name: "rollback succeeds"},
		{name: "rollback fails", rollbackFails: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			assertHooksInstallCleansTemporaryHookAfterManagedHookMoveFailure(t, test.rollbackFails)
		})
	}
}

func assertHooksInstallCleansTemporaryHookAfterManagedHookMoveFailure(t *testing.T, rollbackFails bool) {
	t.Helper()

	repoDir := newHookTestRepository(t)
	runCommand(t, repoDir, "make", "hooks-install")
	managedHook := managedHookPath(t, repoDir)
	managedDir := filepath.Dir(managedHook)
	configPath := filepath.Join(gitOutput(t, repoDir, "rev-parse", "--path-format=absolute", "--git-dir"), "config")
	configBefore, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config before install failure: %v", err)
	}
	hookBefore, err := os.ReadFile(managedHook)
	if err != nil {
		t.Fatalf("read hook before install failure: %v", err)
	}
	mvPath, err := exec.LookPath("mv")
	if err != nil {
		t.Fatalf("find mv: %v", err)
	}
	wrapperDir := t.TempDir()
	writeFileMode(t, filepath.Join(wrapperDir, "mv"), fmt.Sprintf(`#!/bin/sh
for arg do
	case "$arg" in
	*.pre-commit.tmp.*)
		echo "forced managed hook move failure" >&2
		exit 73
		;;
	*.config.backup.*)
		if [ %t = true ]; then
			echo "forced config rollback failure" >&2
			exit 74
		fi
		;;
	esac
done
exec %q "$@"
`, rollbackFails, mvPath), 0o755)

	output, err := runMakeWithEnv(repoDir, "hooks-install", "PATH="+wrapperDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err == nil || !strings.Contains(string(output), "forced managed hook move failure") {
		t.Fatalf("install with managed hook move failure = %v\n%s", err, output)
	}
	temporaryHooks, err := filepath.Glob(filepath.Join(managedDir, ".pre-commit.tmp.*"))
	if err != nil || len(temporaryHooks) != 0 {
		t.Fatalf("temporary hooks after failure = %#v err=%v", temporaryHooks, err)
	}
	if !rollbackFails {
		assertFileEquals(t, configPath, string(configBefore))
		assertFileEquals(t, managedHook, string(hookBefore))
		return
	}
	backups, err := filepath.Glob(filepath.Join(managedDir, ".config.backup.*"))
	if err != nil || len(backups) == 0 {
		t.Fatalf("config recovery backups after rollback failure = %#v err=%v", backups, err)
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

func TestHooksInstallCleansSnapshotsWhenBackupCopyFails(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name, backupPattern string
	}{
		{name: "common config", backupPattern: "*.config.backup.*"},
		{name: "managed hook", backupPattern: "*.pre-commit.backup.*"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newHookInstallSnapshot(t)
			cpPath, err := exec.LookPath("cp")
			if err != nil {
				t.Fatalf("find cp: %v", err)
			}
			wrapperDir := t.TempDir()
			writeFileMode(t, filepath.Join(wrapperDir, "cp"), fmt.Sprintf(`#!/bin/sh
for arg do
	case "$arg" in
	%s) echo "forced backup copy failure" >&2; exit 73 ;;
	esac
done
exec %q "$@"
`, testCase.backupPattern, cpPath), 0o755)

			output, err := runMakeWithEnv(fixture.repoDir, "hooks-install", "PATH="+wrapperDir+string(os.PathListSeparator)+os.Getenv("PATH"))
			if err == nil || !strings.Contains(string(output), "forced backup copy failure") {
				t.Fatalf("install with failed backup copy = %v\n%s", err, output)
			}
			fixture.assertPreserved(t)
		})
	}
}

func TestHooksInstallCleansSnapshotsWhenBackupCopyGetsTerm(t *testing.T) {
	t.Parallel()

	fixture := newHookInstallSnapshot(t)
	cpPath, err := exec.LookPath("cp")
	if err != nil {
		t.Fatalf("find cp: %v", err)
	}
	wrapperDir := t.TempDir()
	writeFileMode(t, filepath.Join(wrapperDir, "cp"), fmt.Sprintf(`#!/bin/sh
for arg do
	case "$arg" in
	*.pre-commit.backup.*) kill -TERM "$PPID"; exit 73 ;;
	esac
done
exec %q "$@"
`, cpPath), 0o755)

	if output, err := runMakeWithEnv(fixture.repoDir, "hooks-install", "PATH="+wrapperDir+string(os.PathListSeparator)+os.Getenv("PATH")); err == nil || strings.Contains(string(output), "Installed reviewed pre-commit hook") {
		t.Fatalf("install interrupted during backup copy = %v\n%s", err, output)
	}
	fixture.assertPreserved(t)
}

func TestHooksInstallRemovesNewManagedDirectoryWhenInitialHookCopyFails(t *testing.T) {
	t.Parallel()

	repoDir := newHookTestRepository(t)
	wrapperDir := t.TempDir()
	writeFileMode(t, filepath.Join(wrapperDir, "cp"), `#!/bin/sh
echo "forced initial hook copy failure" >&2
exit 73
`, 0o755)

	output, err := runMakeWithEnv(repoDir, "hooks-install", "PATH="+wrapperDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err == nil || !strings.Contains(string(output), "forced initial hook copy failure") {
		t.Fatalf("install with failed initial hook copy = %v\n%s", err, output)
	}
	assertNoHooksPath(t, repoDir, "--local", "local")
	if _, err := os.Stat(filepath.Dir(managedHookPath(t, repoDir))); !os.IsNotExist(err) {
		t.Fatalf("installer retained newly created managed hook directory: %v", err)
	}
}

func TestHooksInstallPreservesExistingEmptyManagedDirectoryOnActivationRollback(t *testing.T) {
	t.Parallel()

	repoDir := newHookTestRepository(t)
	managedDir := filepath.Dir(managedHookPath(t, repoDir))
	if err := os.MkdirAll(managedDir, 0o700); err != nil {
		t.Fatalf("create managed hook directory: %v", err)
	}
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
		t.Fatalf("install with activation failure = %v\n%s", err, output)
	}
	assertNoHooksPath(t, repoDir, "--local", "local")
	entries, err := os.ReadDir(managedDir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("managed hook directory entries = %#v err=%v", entries, err)
	}
}

func TestHooksInstallRetriesCompletedTransactionCleanup(t *testing.T) {
	t.Parallel()

	repoDir := newHookTestRepository(t)
	rmPath, err := exec.LookPath("rm")
	if err != nil {
		t.Fatalf("find rm: %v", err)
	}
	wrapperDir := t.TempDir()
	markerPath := filepath.Join(wrapperDir, "cleanup-failed")
	writeFileMode(t, filepath.Join(wrapperDir, "rm"), fmt.Sprintf(`#!/bin/sh
for arg do
	case "$arg" in
	*.backup.*)
		if [ ! -e %q ]; then : > %q; echo "forced completed cleanup failure" >&2; exit 73; fi
		;;
	esac
done
exec %q "$@"
`, markerPath, markerPath, rmPath), 0o755)

	output, err := runMakeWithEnv(repoDir, "hooks-install", "PATH="+wrapperDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err == nil || !strings.Contains(string(output), "forced completed cleanup failure") {
		t.Fatalf("install with completed cleanup failure = %v\n%s", err, output)
	}
	managedHook := managedHookPath(t, repoDir)
	assertConfigEquals(t, repoDir, filepath.Dir(managedHook))
	assertFileEquals(t, managedHook, readRepositoryHook(t))
	assertNoHookInstallArtifacts(t, filepath.Dir(managedHook))
}

type hookInstallSnapshot struct {
	repoDir, configPath, managedDir, managedHook string
	config, hook                                 []byte
}

func newHookInstallSnapshot(t *testing.T) *hookInstallSnapshot {
	t.Helper()

	repoDir := newHookTestRepository(t)
	runCommand(t, repoDir, "make", "hooks-install")
	managedHook := managedHookPath(t, repoDir)
	configPath := filepath.Join(gitOutput(t, repoDir, "rev-parse", "--path-format=absolute", "--git-dir"), "config")
	return &hookInstallSnapshot{
		repoDir: repoDir, configPath: configPath, managedDir: filepath.Dir(managedHook), managedHook: managedHook,
		config: readHookConfigBytes(t, configPath), hook: readHookConfigBytes(t, managedHook),
	}
}

func (hi *hookInstallSnapshot) assertPreserved(t *testing.T) {
	t.Helper()

	assertFileEquals(t, hi.configPath, string(hi.config))
	assertFileEquals(t, hi.managedHook, string(hi.hook))
	assertNoHookInstallArtifacts(t, hi.managedDir)
}

func assertNoHookInstallArtifacts(t *testing.T, managedDir string) {
	t.Helper()

	for _, pattern := range []string{".pre-commit.tmp.*", ".pre-commit.backup.*", ".config.backup.*"} {
		artifacts, err := filepath.Glob(filepath.Join(managedDir, pattern))
		if err != nil || len(artifacts) != 0 {
			t.Fatalf("snapshot artifacts for %s = %#v err=%v", pattern, artifacts, err)
		}
	}
}
