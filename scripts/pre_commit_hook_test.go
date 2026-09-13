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

func assertNoHookConfigBackups(t *testing.T, repoDir string) {
	t.Helper()
	commonDir := gitOutput(t, repoDir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	backups, err := filepath.Glob(filepath.Join(commonDir, ".lopper-hooks-config.*"))
	if err != nil || len(backups) != 0 {
		t.Fatalf("hook configuration backups = %#v err=%v", backups, err)
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

func TestHooksUninstallTerminatesDuringFirstSnapshot(t *testing.T) {
	t.Parallel()
	repoDir := newHookTestRepository(t)
	runCommand(t, repoDir, "make", "hooks-install")
	gitDir := gitOutput(t, repoDir, "rev-parse", "--path-format=absolute", "--git-dir")
	configPath := filepath.Join(gitDir, "config")
	configBefore, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config before signal: %v", err)
	}
	cpPath, err := exec.LookPath("cp")
	if err != nil {
		t.Fatalf("find cp: %v", err)
	}
	wrapperDir := t.TempDir()
	writeFileMode(t, filepath.Join(wrapperDir, "cp"), fmt.Sprintf("#!/bin/sh\nkill -TERM \"$PPID\"\nexec %q \"$@\"\n", cpPath), 0o755)
	if _, err := runMakeWithEnv(repoDir, "hooks-uninstall", "PATH="+wrapperDir+string(os.PathListSeparator)+os.Getenv("PATH")); err == nil {
		t.Fatal("uninstall interrupted during first snapshot succeeded")
	}
	assertFileEquals(t, configPath, string(configBefore))
	assertNoHookConfigBackups(t, repoDir)
	if _, err := os.Stat(managedHookPath(t, repoDir)); err != nil {
		t.Fatalf("managed hook missing after snapshot signal: %v", err)
	}
}

func TestHooksInstallRefusesConditionalForeignGlobalHooksPathBeforeMutation(t *testing.T) {
	t.Parallel()
	repoDir := newHookTestRepository(t)
	linkedDir := filepath.Join(t.TempDir(), "linked")
	runCommand(t, repoDir, "git", "worktree", "add", linkedDir)
	commonConfig := filepath.Join(gitOutput(t, repoDir, "rev-parse", "--path-format=absolute", "--git-common-dir"), "config")
	commonBefore := readHookConfigBytes(t, commonConfig)
	linkedGitDir := gitOutput(t, linkedDir, "rev-parse", "--path-format=absolute", "--git-dir")
	customDir := filepath.Join(t.TempDir(), "custom-hooks")
	includePath := filepath.Join(t.TempDir(), "linked-hooks.gitconfig")
	writeFile(t, includePath, "[core]\n\thooksPath = "+customDir+"\n")
	globalConfig := filepath.Join(t.TempDir(), "global.gitconfig")
	writeFile(t, globalConfig, "[includeIf \"gitdir:"+filepath.ToSlash(linkedGitDir)+"\"]\n\tpath = "+includePath+"\n")
	env := []string{"GIT_CONFIG_GLOBAL=" + globalConfig, "GIT_CONFIG_NOSYSTEM=1"}
	assertNoHooksPath(t, repoDir, "--local", "local")
	command := exec.Command("git", "config", "--get", "core.hooksPath")
	command.Dir = linkedDir
	command.Env = append(hookTestEnv(), env...)
	output, err := command.CombinedOutput()
	if err != nil || strings.TrimSpace(string(output)) != customDir {
		t.Fatalf("linked conditional hooksPath=%q err=%v", output, err)
	}
	output, err = runMakeWithEnv(repoDir, "hooks-install", env...)
	if err == nil || !strings.Contains(string(output), "another worktree") {
		t.Fatalf("install with foreign conditional hooksPath=%v\n%s", err, output)
	}
	assertFileEquals(t, commonConfig, string(commonBefore))
	assertNoHooksPath(t, repoDir, "--local", "local")
	if _, err := os.Stat(filepath.Dir(managedHookPath(t, repoDir))); !os.IsNotExist(err) {
		t.Fatalf("installer created hook state: %v", err)
	}
	command = exec.Command("git", "config", "--get", "core.hooksPath")
	command.Dir = linkedDir
	command.Env = append(hookTestEnv(), env...)
	output, err = command.CombinedOutput()
	if err != nil || strings.TrimSpace(string(output)) != customDir {
		t.Fatalf("linked hooksPath changed=%q err=%v", output, err)
	}
}
