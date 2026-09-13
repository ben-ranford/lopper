package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestHooksInstallUsesImmutableSnapshot(t *testing.T) {
	repoDir := newHookFixture(t)
	sentinel := filepath.Join(repoDir, "branch-command-ran")
	writeFileMode(t, filepath.Join(repoDir, ".githooks", "pre-commit"), "#!/bin/sh\nprintf branch >"+sentinel+"\n", 0o755)
	writeFile(t, filepath.Join(repoDir, "Makefile"), "fmt:\n\t@printf branch >"+sentinel+"\nci:\n\t@printf branch >"+sentinel+"\nhooks-install:\n\t@printf branch >"+sentinel+"\n")
	writeFile(t, filepath.Join(repoDir, "sample.go"), "package sample\n\nfunc Value() int { return 2 }\n")
	runCommand(t, repoDir, "git", "add", "Makefile", ".githooks/pre-commit", "sample.go")
	output, err := hookCommand(repoDir, "git", "commit", "-m", "revision B")
	if err != nil {
		t.Fatalf("commit revision B: %v\n%s", err, output)
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatalf("checkout-controlled command ran: %v", err)
	}
}

func TestInstalledPreCommitRejectsUnformattedStagedGo(t *testing.T) {
	assertInstalledPreCommitRejects(t, "sample.go", "package sample\n\nfunc Value() int {return 2}\n", "unformatted", "gofmt-formatted")
}

func TestInstalledPreCommitRejectsStagedWhitespace(t *testing.T) {
	assertInstalledPreCommitRejects(t, "notes.txt", "trailing space \n", "whitespace", "trailing whitespace")
}

func assertInstalledPreCommitRejects(t *testing.T, path, contents, message, expected string) {
	t.Helper()

	repoDir := newHookFixture(t)
	writeFile(t, filepath.Join(repoDir, path), contents)
	runCommand(t, repoDir, "git", "add", path)
	output, err := hookCommand(repoDir, "git", "commit", "-m", message)
	if err == nil || !strings.Contains(output, expected) {
		t.Fatalf("expected staged %s rejection, got %v:\n%s", expected, err, output)
	}
}

func TestInstalledPreCommitUsesStagedGoContent(t *testing.T) {
	repoDir := newHookFixture(t)
	writeFile(t, filepath.Join(repoDir, "sample.go"), "package sample\n\nfunc Value() int { return 2 }\n")
	runCommand(t, repoDir, "git", "add", "sample.go")
	writeFile(t, filepath.Join(repoDir, "sample.go"), "package sample\n\nfunc Value() int {return 3}\n")
	output, err := hookCommand(repoDir, "git", "commit", "-m", "staged formatting")
	if err != nil {
		t.Fatalf("commit staged formatting: %v\n%s", err, output)
	}
}

func TestInstalledPreCommitDoesNotRunCheckoutControlledGofmt(t *testing.T) {
	repoDir := newHookFixture(t)
	sentinel := filepath.Join(repoDir, "branch-gofmt-ran")
	toolsDir := filepath.Join(repoDir, "tools")
	writeFileMode(t, filepath.Join(toolsDir, "gofmt"), "#!/bin/sh\nprintf branch >"+sentinel+"\n", 0o755)
	writeFile(t, filepath.Join(repoDir, "sample.go"), "package sample\n\nfunc Value() int { return 2 }\n")
	runCommand(t, repoDir, "git", "add", "sample.go")
	hookDir, err := hookCommand(repoDir, "git", "config", "--get", "core.hooksPath")
	if err != nil {
		t.Fatalf("read managed hook path: %v", err)
	}
	env := []string{"PATH=" + toolsDir + ":/usr/bin:/bin"}
	selectedGofmt, err := hookCommandWithEnv(repoDir, env, "/bin/sh", "-c", "PATH=/usr/bin:/bin:$PATH; export PATH; command -v gofmt")
	if err != nil {
		t.Fatalf("resolve normalized gofmt: %v", err)
	}
	output, err := hookCommandWithEnv(repoDir, env, filepath.Join(strings.TrimSpace(hookDir), "pre-commit"))
	if strings.TrimSpace(selectedGofmt) == filepath.Join(toolsDir, "gofmt") {
		if err == nil || !strings.Contains(output, "checkout-controlled hook tool") {
			t.Fatalf("expected checkout gofmt refusal, got %v:\n%s", err, output)
		}
	} else if err != nil {
		t.Fatalf("expected safe normalized gofmt to run, got %v:\n%s", err, output)
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatalf("checkout-controlled gofmt ran: %v", err)
	}
}

func TestHooksInstallPreservesCustomPathAndManagedUninstall(t *testing.T) {
	repoDir := newHookFixture(t)
	runCommand(t, repoDir, "make", "hooks-uninstall")
	runCommand(t, repoDir, "git", "config", "core.hooksPath", "/custom/hooks")
	output, err := hookCommand(repoDir, "make", "hooks-install")
	if err == nil || !strings.Contains(output, "Refusing to replace") {
		t.Fatalf("expected custom hook path refusal, got %v:\n%s", err, output)
	}
	got, getErr := hookCommand(repoDir, "git", "config", "--get-all", "core.hooksPath")
	if getErr != nil || got != "/custom/hooks\n" {
		t.Fatalf("custom hook path changed: %v, %q", getErr, got)
	}
	runCommand(t, repoDir, "git", "config", "--unset", "core.hooksPath")
	runCommand(t, repoDir, "make", "hooks-install")
	runCommand(t, repoDir, "make", "hooks-uninstall")
	output, err = hookCommand(repoDir, "git", "config", "--get", "core.hooksPath")
	if err == nil || output != "" {
		t.Fatalf("expected managed hook path to be removed, got %v: %q", err, output)
	}
}

func TestHooksInstallWorksFromLinkedWorktree(t *testing.T) {
	repoDir := newHookFixture(t)
	runCommand(t, repoDir, "make", "hooks-uninstall")
	linkedDir := filepath.Join(filepath.Dir(repoDir), "linked")
	runCommand(t, repoDir, "git", "worktree", "add", linkedDir)
	runCommand(t, linkedDir, "make", "hooks-install")
	runCommand(t, repoDir, "git", "config", "core.bare", "true")
	writeFile(t, filepath.Join(linkedDir, "sample.go"), "package sample\n\nfunc Value() int { return 2 }\n")
	runCommand(t, linkedDir, "git", "add", "sample.go")
	output, err := hookCommandWithEnv(linkedDir, []string{"GIT_CONFIG_COUNT=01", "GIT_CONFIG_KEY_0=advice.detachedHead", "GIT_CONFIG_VALUE_0=false"}, "git", "-c", "core.bare=false", "commit", "-m", "linked hook")
	if err != nil {
		t.Fatalf("commit from linked worktree: %v\n%s", err, output)
	}
}

func TestInstalledPreCommitUsesAlternateIndex(t *testing.T) {
	repoDir := newHookFixture(t)
	indexPath := filepath.Join(repoDir, "alternate-index")
	_, err := hookCommandWithEnv(repoDir, []string{"GIT_INDEX_FILE=" + indexPath}, "git", "read-tree", "HEAD")
	if err != nil {
		t.Fatalf("prepare alternate index: %v", err)
	}
	writeFile(t, filepath.Join(repoDir, "sample.go"), "package sample\n\nfunc Value() int {return 2}\n")
	_, err = hookCommandWithEnv(repoDir, []string{"GIT_INDEX_FILE=" + indexPath}, "git", "add", "sample.go")
	if err != nil {
		t.Fatalf("stage alternate index: %v", err)
	}
	hookPath, err := hookCommand(repoDir, "git", "config", "--get", "core.hooksPath")
	if err != nil {
		t.Fatalf("read managed hook path: %v", err)
	}
	hookPath = filepath.Join(strings.TrimSpace(hookPath), "pre-commit")
	output, err := hookCommandWithEnv(repoDir, []string{"GIT_INDEX_FILE=" + indexPath, "GIT_CONFIG_COUNT=01", "GIT_CONFIG_KEY_0=advice.detachedHead", "GIT_CONFIG_VALUE_0=false"}, hookPath)
	if err == nil || !strings.Contains(output, "gofmt-formatted") {
		t.Fatalf("expected alternate index formatting rejection, got %v:\n%s", err, output)
	}
}

func newHookFixture(t *testing.T) string {
	t.Helper()
	repoDir := filepath.Join(t.TempDir(), "repo")
	runCommand(t, filepath.Dir(repoDir), "git", "init", "-b", "main", repoDir)
	runCommand(t, repoDir, "git", "config", "user.name", "Hook Test")
	runCommand(t, repoDir, "git", "config", "user.email", "hook-test@example.com")
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	copyHookFixtureFile(t, filepath.Join(filepath.Dir(cwd), "Makefile"), filepath.Join(repoDir, "Makefile"), 0o644)
	copyHookFixtureFile(t, filepath.Join(filepath.Dir(cwd), ".githooks", "pre-commit"), filepath.Join(repoDir, ".githooks", "pre-commit"), 0o755)
	writeFile(t, filepath.Join(repoDir, "sample.go"), "package sample\n\nfunc Value() int { return 1 }\n")
	runCommand(t, repoDir, "git", "add", ".")
	runCommand(t, repoDir, "git", "-c", "core.hooksPath=/dev/null", "commit", "-m", "revision A")
	runCommand(t, repoDir, "make", "hooks-install")
	return repoDir
}

func copyHookFixtureFile(t *testing.T, source, destination string, mode os.FileMode) {
	t.Helper()
	contents, err := os.ReadFile(source)
	if err != nil {
		t.Fatalf("read fixture source %s: %v", source, err)
	}
	writeFileMode(t, destination, string(contents), mode)
}

func hookCommand(dir, name string, args ...string) (string, error) {
	return hookCommandWithEnv(dir, nil, name, args...)
}

func hookCommandWithEnv(dir string, env []string, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = withoutGitEnv()
	for _, setting := range env {
		name, _, _ := strings.Cut(setting, "=")
		for index, existing := range cmd.Env {
			if strings.HasPrefix(existing, name+"=") {
				cmd.Env[index] = setting
				setting = ""
				break
			}
		}
		if setting != "" {
			cmd.Env = append(cmd.Env, setting)
		}
	}
	output, err := cmd.CombinedOutput()
	return string(output), err
}
