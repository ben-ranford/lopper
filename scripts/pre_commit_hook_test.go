package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPreCommitHookNormalizesGitEnvForLinkedWorktrees(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	repoDir := filepath.Join(tempDir, "repo")
	worktreeDir := filepath.Join(tempDir, "linked")
	binDir := filepath.Join(tempDir, "bin")
	logPath := filepath.Join(tempDir, "make-env.log")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin dir: %v", err)
	}

	runCommand(t, tempDir, "git", "init", "-b", "main", repoDir)
	runCommand(t, repoDir, "git", "config", "user.name", "Test User")
	runCommand(t, repoDir, "git", "config", "user.email", "test@example.com")
	writeFile(t, filepath.Join(repoDir, "tracked.txt"), "baseline\n")
	runCommand(t, repoDir, "git", "add", "tracked.txt")
	runCommand(t, repoDir, "git", "commit", "-m", "baseline")
	runCommand(t, repoDir, "git", "worktree", "add", worktreeDir)
	runCommand(t, repoDir, "git", "config", "core.bare", "true")

	hookDir := filepath.Join(worktreeDir, ".githooks")
	if err := os.MkdirAll(hookDir, 0o755); err != nil {
		t.Fatalf("mkdir hook dir: %v", err)
	}

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	hookSource := filepath.Join(cwd, "..", ".githooks", "pre-commit")
	hookData, err := os.ReadFile(hookSource)
	if err != nil {
		t.Fatalf("read hook: %v", err)
	}
	writeFileMode(t, filepath.Join(hookDir, "pre-commit"), string(hookData), 0o755)

	fakeMake := "#!/usr/bin/env sh\n" +
		"set -eu\n" +
		"printf 'GIT_DIR=%s\\nGIT_WORK_TREE=%s\\nGIT_INDEX_FILE=%s\\nGIT_PREFIX=%s\\nGIT_CONFIG_COUNT=%s\\nGIT_CONFIG_KEY_0=%s\\nGIT_CONFIG_VALUE_0=%s\\n' " +
		"\"${GIT_DIR-}\" \"${GIT_WORK_TREE-}\" \"${GIT_INDEX_FILE-}\" \"${GIT_PREFIX-}\" " +
		"\"${GIT_CONFIG_COUNT-}\" \"${GIT_CONFIG_KEY_0-}\" \"${GIT_CONFIG_VALUE_0-}\" >>\"" + logPath + "\"\n" +
		"git status --short >>\"" + logPath + "\"\n" +
		"repo_tmp=$(mktemp -d)\n" +
		"git init -b main \"$repo_tmp/fresh\" >/dev/null\n" +
		"(cd \"$repo_tmp/fresh\" && git status --short) >>\"" + logPath + "\"\n" +
		"printf '%s\\n' '--' >>\"" + logPath + "\"\n"
	writeFileMode(t, filepath.Join(binDir, "make"), fakeMake, 0o755)

	cmd := exec.Command(filepath.Join(hookDir, "pre-commit"))
	cmd.Dir = worktreeDir
	cmd.Env = withoutGitEnv()
	additionalEnv := []string{
		"PATH=" + binDir + string(os.PathListSeparator) + os.Getenv("PATH"),
		"GIT_DIR=/tmp/hook-git-dir",
		"GIT_WORK_TREE=/tmp/hook-worktree",
		"GIT_INDEX_FILE=/tmp/hook-index",
		"GIT_PREFIX=subdir/",
	}
	cmd.Env = append(cmd.Env, additionalEnv...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run pre-commit hook: %v\n%s", err, output)
	}

	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read fake make log: %v", err)
	}
	logOutput := string(logData)
	if strings.Count(logOutput, "--\n") != 2 {
		t.Fatalf("expected two make invocations, got log:\n%s", logOutput)
	}
	for _, forbidden := range []string{
		"GIT_DIR=/tmp/hook-git-dir",
		"GIT_WORK_TREE=/tmp/hook-worktree",
		"GIT_INDEX_FILE=/tmp/hook-index",
		"GIT_PREFIX=subdir/",
	} {
		if strings.Contains(logOutput, forbidden) {
			t.Fatalf("expected hook to clear %q before running make, got log:\n%s", forbidden, logOutput)
		}
	}
	for _, required := range []string{
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=core.bare",
		"GIT_CONFIG_VALUE_0=false",
	} {
		if !strings.Contains(logOutput, required) {
			t.Fatalf("expected hook to export %q before running make, got log:\n%s", required, logOutput)
		}
	}
	if strings.Contains(logOutput, "fatal: this operation must be run in a work tree") {
		t.Fatalf("expected linked worktree git commands to succeed with core.bare override, got log:\n%s", logOutput)
	}
	if strings.Contains(logOutput, "fatal: not a git repository") {
		t.Fatalf("expected temporary repo git commands to succeed with hook env, got log:\n%s", logOutput)
	}
}
