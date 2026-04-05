package gitexec

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const versionArg = "--version"
const attackerGlobalConfigEnvEntry = "GIT_CONFIG_GLOBAL=/tmp/attacker-global"
const keepMeEnvEntry = "KEEP_ME=1"

func TestResolveBinaryPath(t *testing.T) {
	path, err := ResolveBinaryPath()
	if err != nil {
		t.Skip("git binary not available")
	}
	if path != ExecutablePrimary && path != ExecutableFallback {
		t.Fatalf("expected known git path, got %q", path)
	}
}

func TestResolveBinaryPathBranches(t *testing.T) {
	t.Run("prefers primary", func(t *testing.T) {
		path, err := resolveBinaryPath("primary", "fallback", func(path string) bool {
			return path == "primary"
		})
		if err != nil {
			t.Fatalf("resolve primary: %v", err)
		}
		if path != "primary" {
			t.Fatalf("expected primary path, got %q", path)
		}
	})

	t.Run("falls back", func(t *testing.T) {
		path, err := resolveBinaryPath("primary", "fallback", func(path string) bool {
			return path == "fallback"
		})
		if err != nil {
			t.Fatalf("resolve fallback: %v", err)
		}
		if path != "fallback" {
			t.Fatalf("expected fallback path, got %q", path)
		}
	})

	t.Run("returns error when unavailable", func(t *testing.T) {
		if _, err := resolveBinaryPath("primary", "fallback", func(string) bool { return false }); err == nil {
			t.Fatal("expected missing git executable error")
		}
	})
}

func TestSanitizedEnv(t *testing.T) {
	t.Setenv("PATH", "/tmp/custom-bin")
	t.Setenv("GIT_DIR", "/tmp/fake-git-dir")
	t.Setenv("GIT_WORK_TREE", "/tmp/fake-worktree")
	t.Setenv("GIT_INDEX_FILE", "/tmp/fake-index")
	t.Setenv("GIT_CONFIG_GLOBAL", "/tmp/attacker-global")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "core.fsmonitor")
	t.Setenv("GIT_CONFIG_VALUE_0", "/tmp/attacker-helper")
	t.Setenv("HOME", "/tmp/attacker-home")
	t.Setenv("XDG_CONFIG_HOME", "/tmp/attacker-xdg")
	t.Setenv("PAGER", "/tmp/attacker-pager")
	t.Setenv("KEEP_ME", "1")

	env := SanitizedEnv()
	if !containsEnv(env, SafeSystemPath) {
		t.Fatalf("expected safe path %q in env, got %#v", SafeSystemPath, env)
	}
	if containsEnvPrefix(env, "GIT_DIR=") || containsEnvPrefix(env, "GIT_WORK_TREE=") || containsEnvPrefix(env, "GIT_INDEX_FILE=") {
		t.Fatalf("expected git override vars to be stripped, got %#v", env)
	}
	if containsEnv(env, attackerGlobalConfigEnvEntry) ||
		containsEnv(env, "GIT_CONFIG_COUNT=1") ||
		containsEnv(env, "GIT_CONFIG_VALUE_0=/tmp/attacker-helper") ||
		containsEnv(env, "HOME=/tmp/attacker-home") ||
		containsEnv(env, "XDG_CONFIG_HOME=/tmp/attacker-xdg") ||
		containsEnv(env, "PAGER=/tmp/attacker-pager") {
		t.Fatalf("expected caller-controlled git config env vars to be stripped, got %#v", env)
	}
	if !containsEnv(env, safeGitNoSystemConfig) || !containsEnv(env, safeGitGlobalConfig) {
		t.Fatalf("expected hardened git config env entries, got %#v", env)
	}
	for _, expected := range safeGitConfigEnvEntries() {
		if !containsEnv(env, expected) {
			t.Fatalf("expected forced git config entry %q in env, got %#v", expected, env)
		}
	}
	if !containsEnv(env, keepMeEnvEntry) {
		t.Fatalf("expected unrelated env vars to be preserved, got %#v", env)
	}
}

func TestSanitizedEnvEntriesPreservesMalformedEntries(t *testing.T) {
	env := sanitizedEnvEntries([]string{
		"BROKEN",
		keepMeEnvEntry,
		"PATH=/tmp/custom-bin",
		attackerGlobalConfigEnvEntry,
	})

	if !containsEnv(env, "BROKEN") {
		t.Fatalf("expected malformed env entry to be preserved, got %#v", env)
	}
	if containsEnv(env, "PATH=/tmp/custom-bin") || containsEnv(env, attackerGlobalConfigEnvEntry) {
		t.Fatalf("expected sanitized env entries to strip caller overrides, got %#v", env)
	}
	if !containsEnv(env, keepMeEnvEntry) {
		t.Fatalf("expected unrelated env entry to be preserved, got %#v", env)
	}
}

func TestCommandUsesKnownGitPaths(t *testing.T) {
	testKnownGitPaths(t, func(gitPath string) (*exec.Cmd, error) {
		return Command(gitPath, versionArg)
	})
}

func TestCommandContextUsesKnownGitPaths(t *testing.T) {
	testKnownGitPaths(t, func(gitPath string) (*exec.Cmd, error) {
		return CommandContext(context.Background(), gitPath, versionArg)
	})
}

func TestCommandRejectsUnknownGitPath(t *testing.T) {
	if _, err := Command("/tmp/fake-git", versionArg); err == nil {
		t.Fatalf("expected unknown path error")
	}
	if _, err := CommandContext(context.Background(), "/tmp/fake-git", versionArg); err == nil {
		t.Fatalf("expected unknown path error for context command")
	}
}

func TestExecutableAvailable(t *testing.T) {
	if ExecutableAvailable(t.TempDir()) {
		t.Fatalf("expected directory to be unavailable")
	}

	filePath := filepath.Join(t.TempDir(), "git")
	if err := os.WriteFile(filePath, []byte("#!/bin/sh\n"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if ExecutableAvailable(filePath) {
		t.Fatalf("expected non-executable file to be unavailable")
	}
	if err := os.Chmod(filePath, 0o700); err != nil {
		t.Fatalf("chmod file executable: %v", err)
	}
	if !ExecutableAvailable(filePath) {
		t.Fatalf("expected executable file to be available")
	}
}

func containsEnv(env []string, expected string) bool {
	for _, entry := range env {
		if entry == expected {
			return true
		}
	}
	return false
}

func containsEnvPrefix(env []string, prefix string) bool {
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			return true
		}
	}
	return false
}

func testKnownGitPaths(t *testing.T, build func(string) (*exec.Cmd, error)) {
	t.Helper()

	for _, gitPath := range []string{ExecutablePrimary, ExecutableFallback} {
		t.Run(gitPath, func(t *testing.T) {
			command, err := build(gitPath)
			if err != nil {
				t.Fatalf("build command for %s: %v", gitPath, err)
			}
			if command.Path != gitPath {
				t.Fatalf("expected command path %q, got %q", gitPath, command.Path)
			}
		})
	}
}
