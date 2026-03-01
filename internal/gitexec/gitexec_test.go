package gitexec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveBinaryPath(t *testing.T) {
	path, err := ResolveBinaryPath()
	if err != nil {
		t.Skip("git binary not available")
	}
	if path != ExecutablePrimary && path != ExecutableFallback {
		t.Fatalf("expected known git path, got %q", path)
	}
}

func TestSanitizedEnv(t *testing.T) {
	t.Setenv("PATH", "/tmp/custom-bin")
	t.Setenv("GIT_DIR", "/tmp/fake-git-dir")
	t.Setenv("GIT_WORK_TREE", "/tmp/fake-worktree")
	t.Setenv("GIT_INDEX_FILE", "/tmp/fake-index")
	t.Setenv("KEEP_ME", "1")

	env := SanitizedEnv()
	if !containsEnv(env, SafeSystemPath) {
		t.Fatalf("expected safe path %q in env, got %#v", SafeSystemPath, env)
	}
	if containsEnvPrefix(env, "GIT_DIR=") || containsEnvPrefix(env, "GIT_WORK_TREE=") || containsEnvPrefix(env, "GIT_INDEX_FILE=") {
		t.Fatalf("expected git override vars to be stripped, got %#v", env)
	}
	if !containsEnv(env, "KEEP_ME=1") {
		t.Fatalf("expected unrelated env vars to be preserved, got %#v", env)
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
