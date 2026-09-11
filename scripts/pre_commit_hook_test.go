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

	stagedGo := filepath.Join(repoDir, "unformatted.go")
	writeFile(t, stagedGo, "package fixture\n\nfunc unformatted(){}\n")
	runCommand(t, repoDir, "git", "add", "unformatted.go")
	writeFile(t, stagedGo, "package fixture\n\nfunc unformatted() {}\n")
	assertCommitFails(t, repoDir, "staged Go files must be gofmt-formatted")
	runCommand(t, repoDir, "git", "reset", "--", "unformatted.go")

	for _, name := range []string{": staged.go", "0:unformatted.go"} {
		writeFile(t, filepath.Join(repoDir, name), "package fixture\n\nfunc adversarialName(){}\n")
		runCommand(t, repoDir, "git", "add", "--", "./"+name)
		assertCommitFails(t, repoDir, "staged Go files must be gofmt-formatted")
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
	if err == nil || !strings.Contains(string(output), "Refusing to replace local") {
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

func assertNoHooksPath(t *testing.T, repoDir, scope, scopeName string) {
	t.Helper()
	command := exec.Command("git", "config", scope, "--get", "core.hooksPath")
	command.Dir = repoDir
	output, err := command.CombinedOutput()
	if err == nil || len(output) != 0 {
		t.Fatalf("%s core.hooksPath remains: %v\n%s", scopeName, err, output)
	}
}

func assertCommitFails(t *testing.T, repoDir, wantOutput string) {
	t.Helper()
	command := exec.Command("git", "commit", "-m", "must fail")
	command.Dir = repoDir
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), wantOutput) {
		t.Fatalf("commit = %v\n%s", err, output)
	}
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
