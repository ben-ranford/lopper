package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestHooksCleanupRetainsShadowedDurablePaths(t *testing.T) {
	for _, scope := range []string{"system", "global", "local", "worktree"} {
		t.Run(scope, func(t *testing.T) {
			t.Parallel()
			repo, managed, env := shadowedDurableHookFixture(t, scope, false)
			assertDurableHookRetention(t, repo, managed, env)
		})
	}
}

func TestHooksCleanupRetainsAmbiguousDurableRelativePaths(t *testing.T) {
	for _, scope := range []string{"system", "global"} {
		t.Run(scope, func(t *testing.T) {
			repo, managed, env := shadowedDurableHookFixture(t, scope, true)
			assertDurableHookRetention(t, repo, managed, env)
		})
	}
}

func TestHooksCleanupRetainsConditionalDurablePaths(t *testing.T) {
	for _, scope := range []string{"system", "global"} {
		t.Run(scope, func(t *testing.T) {
			repo, managed, env := shadowedDurableHookFixture(t, scope, false)
			var config string
			for _, setting := range env {
				if value, ok := strings.CutPrefix(setting, "TEST_DURABLE_CONFIG="); ok {
					config = value
				}
			}
			runCommand(t, repo, "git", "config", "--file", config, "--unset-all", "core.hooksPath")
			other := t.TempDir()
			runCommand(t, other, "git", "init")
			included := filepath.Join(t.TempDir(), "include.cfg")
			runCommand(t, repo, "git", "config", "--file", included, "core.hooksPath", managed)
			condition := "includeIf.gitdir:" + testutil.GitOutput(t, other, "rev-parse", "--absolute-git-dir") + ".path"
			runCommand(t, repo, "git", "config", "--file", config, condition, included)
			output, err := hookCommandWithEnv(other, env, "sh", "-c", "git config --get core.hooksPath")
			if err != nil || strings.TrimSpace(output) != managed {
				t.Fatalf("unrelated repository did not match durable condition: %v\n%s", err, output)
			}
			assertDurableHookRetention(t, repo, managed, env)
		})
	}
}

func assertDurableHookRetention(t *testing.T, repo, managed string, env []string) {
	t.Helper()
	output, err := hookCommandWithEnv(repo, env, "sh", "scripts/cleanup-hook-snapshot.sh")
	if err != nil {
		t.Fatalf("cleanup with durable reference: %v\n%s", err, output)
	}
	assertHookSnapshotRetention(t, managed, hookReferenceCase{alias: true})
}

func shadowedDurableHookFixture(t *testing.T, scope string, relative bool) (string, string, []string) {
	t.Helper()
	repo, managed, linked := newHookReferenceWorktree(t, hookReferenceCase{})
	custom := t.TempDir()
	runCommand(t, repo, "git", "config", "--local", "core.hooksPath", custom)
	for _, root := range []string{repo, linked} {
		runCommand(t, root, "git", "config", "--worktree", "core.hooksPath", custom)
		if err := os.Mkdir(filepath.Join(root, "custom hooks"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	config := filepath.Join(repo, ".git", "config")
	env := []string{"XDG_CONFIG_HOME="}
	switch scope {
	case "worktree":
		config = testutil.GitOutput(t, linked, "rev-parse", "--path-format=absolute", "--git-path", "config.worktree")
	case "system", "global":
		config = filepath.Join(t.TempDir(), "durable.cfg")
		realGit, err := exec.LookPath("git")
		if err != nil {
			t.Fatal(err)
		}
		bin := t.TempDir()
		// Model installed durable configuration without changing the host's files.
		writeFileMode(t, filepath.Join(bin, "git"), "#!/bin/sh\nGIT_CONFIG_"+strings.ToUpper(scope)+"=\"$TEST_DURABLE_CONFIG\" exec \"$TEST_REAL_GIT\" \"$@\"\n", 0o755)
		env = append(env, "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "TEST_DURABLE_CONFIG="+config, "TEST_REAL_GIT="+realGit)
	}
	reference := managed
	if relative {
		// It resolves to unrelated directories here, but other repositories may
		// resolve the same durable value to the managed snapshot.
		reference = "custom hooks"
	}
	runCommand(t, repo, "git", "config", "--file", config, "--add", "core.hooksPath", reference)
	if scope == "local" || scope == "worktree" {
		runCommand(t, repo, "git", "config", "--file", config, "--add", "core.hooksPath", custom)
	} else {
		other := t.TempDir()
		runCommand(t, other, "git", "init")
		output, err := hookCommandWithEnv(other, env, "sh", "-c", "git config --get core.hooksPath")
		if err != nil || strings.TrimSpace(output) != reference {
			t.Fatalf("unrelated repository lost durable reference: %v\n%s", err, output)
		}
	}
	for _, root := range []string{repo, linked} {
		output, err := hookCommandWithEnv(root, env, "sh", "-c", "git config --get core.hooksPath")
		if err != nil || strings.TrimSpace(output) != custom {
			t.Fatalf("fixture did not shadow durable reference: %v\n%s", err, output)
		}
	}
	return repo, managed, env
}
