package gitexec

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const versionArg = "--version"
const attackerGlobalConfigEnvEntry = "GIT_CONFIG_GLOBAL=/tmp/attacker-global"
const keepMeEnvEntry = "KEEP_ME=1"

func TestSafeConfigArgsForcesNonExecutableGitConfig(t *testing.T) {
	args := SafeConfigArgs()
	for _, expected := range []string{"core.fsmonitor=false", "diff.external=", "maintenance.auto=false"} {
		if !containsArgPair(args, "-c", expected) {
			t.Fatalf("expected safe config arg %q in %#v", expected, args)
		}
	}
}

func TestSafeConfigEnvEntriesReturnsIndependentSlice(t *testing.T) {
	entries := SafeConfigEnvEntries()
	if len(entries) == 0 {
		t.Fatal("expected hardened git config env entries")
	}

	mutated := SafeConfigEnvEntries()
	mutated[0] = "GIT_CONFIG_COUNT=0"
	mutated = append(mutated, "GIT_CONFIG_KEY_99=attacker.override")
	if got, want := len(mutated), len(entries)+1; got != want {
		t.Fatalf("extended safe config env entries len = %d, want %d", got, want)
	}

	fresh := SafeConfigEnvEntries()
	if fresh[0] != entries[0] {
		t.Fatalf("fresh safe config env entries changed after caller mutation: got %q want %q", fresh[0], entries[0])
	}
	if containsEnv(fresh, "GIT_CONFIG_KEY_99=attacker.override") {
		t.Fatalf("fresh safe config env entries leaked caller append: %#v", fresh)
	}
}

func TestSafeConfigArgsAndEnvEntriesStaySynchronized(t *testing.T) {
	args := SafeConfigArgs()
	entries := SafeConfigEnvEntries()
	if len(entries) == 0 {
		t.Fatal("expected git config env entries")
	}
	if len(args)%2 != 0 {
		t.Fatalf("safe config args must be flag-value pairs: %#v", args)
	}

	wantCount := len(args) / 2
	wantCountEntry := "GIT_CONFIG_COUNT=" + strconv.Itoa(wantCount)
	if entries[0] != wantCountEntry {
		t.Fatalf("safe config env count = %q, want %q", entries[0], wantCountEntry)
	}

	for i := 0; i < wantCount; i++ {
		if args[i*2] != "-c" {
			t.Fatalf("safe config arg %d flag = %q, want -c", i, args[i*2])
		}
		pair := strings.SplitN(args[i*2+1], "=", 2)
		if len(pair) != 2 {
			t.Fatalf("safe config arg %d payload = %q, want key=value", i, args[i*2+1])
		}
		wantKey := "GIT_CONFIG_KEY_" + strconv.Itoa(i) + "=" + pair[0]
		wantValue := "GIT_CONFIG_VALUE_" + strconv.Itoa(i) + "=" + pair[1]
		if entries[1+i*2] != wantKey {
			t.Fatalf("safe config env key %d = %q, want %q", i, entries[1+i*2], wantKey)
		}
		if entries[2+i*2] != wantValue {
			t.Fatalf("safe config env value %d = %q, want %q", i, entries[2+i*2], wantValue)
		}
	}
}

func TestResolveBinaryPath(t *testing.T) {
	path, err := ResolveBinaryPath()
	if err != nil {
		t.Skip("git binary not available")
	}
	if !slices.Contains(TrustedExecutablePaths(), path) {
		t.Fatalf("expected trusted git path, got %q", path)
	}
}

func TestResolveBinaryPathBranches(t *testing.T) {
	for _, tc := range []struct {
		name       string
		candidates []string
		available  string
		want       string
	}{
		{name: "prefers first available", candidates: []string{"primary", "fallback"}, available: "primary", want: "primary"},
		{name: "falls back to later candidate", candidates: []string{"primary", "fallback"}, available: "fallback", want: "fallback"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, err := resolveBinaryPath(tc.candidates, func(path string) bool {
				return path == tc.available
			})
			if err != nil {
				t.Fatalf("resolve %s: %v", tc.available, err)
			}
			if path != tc.want {
				t.Fatalf("expected %s path, got %q", tc.want, path)
			}
		})
	}

	t.Run("returns error when unavailable", func(t *testing.T) {
		if _, err := resolveBinaryPath([]string{"primary", "fallback"}, func(string) bool { return false }); err == nil {
			t.Fatal("expected missing git executable error")
		}
	})
}

func TestTrustedExecutablePathsReturnsIndependentSlice(t *testing.T) {
	paths := TrustedExecutablePaths()
	if len(paths) == 0 {
		t.Skip("no trusted Git executable available")
	}
	mutated := TrustedExecutablePaths()
	mutated[0] = "/tmp/hijacked-git"
	if got := TrustedExecutablePaths()[0]; got != paths[0] {
		t.Fatalf("trusted git paths changed after caller mutation: got %q want %q", got, paths[0])
	}
}

func TestTrustedExecutablePathsHaveValidatedAbsoluteProvenance(t *testing.T) {
	paths := TrustedExecutablePaths()
	for _, path := range paths {
		if !filepath.IsAbs(path) {
			t.Fatalf("trusted Git path is not absolute: %q", path)
		}
		if !ExecutableAvailable(path) {
			t.Fatalf("trusted Git path failed provenance revalidation: %q", path)
		}
	}
	for _, rejected := range []string{"/opt/homebrew/bin/git", filepath.Join(t.TempDir(), "git")} {
		if slices.Contains(paths, rejected) {
			t.Fatalf("trusted Git paths contain user-managed path %q: %#v", rejected, paths)
		}
	}
}

func TestSanitizedEnv(t *testing.T) {
	setSanitizedEnvTestVars(t)

	env := SanitizedEnv()
	assertSanitizedEnvCoreEntries(t, env)
	assertSanitizedEnvForcedGitConfig(t, env)
}

func TestSanitizedEnvEntriesPreservesMalformedEntries(t *testing.T) {
	input := []string{
		"BROKEN",
		keepMeEnvEntry,
		"PATH=/tmp/custom-bin",
		attackerGlobalConfigEnvEntry,
	}
	env := sanitizedEnvEntries(input)

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

func TestSanitizedEnvEntriesWindowsCaseInsensitiveFiltering(t *testing.T) {
	original := windowsEnvKeyMatching
	windowsEnvKeyMatching = true
	t.Cleanup(func() {
		windowsEnvKeyMatching = original
	})

	input := []string{
		"git_dir=/tmp/attacker.git",
		"Path=C:\\attacker\\bin",
		"home=C:\\attacker",
		"DyLd_Insert_Libraries=evil.dll",
		keepMeEnvEntry,
	}
	env := sanitizedEnvEntries(input)

	for _, filteredEntry := range []string{
		"git_dir=/tmp/attacker.git",
		"Path=C:\\attacker\\bin",
		"home=C:\\attacker",
		"DyLd_Insert_Libraries=evil.dll",
	} {
		if containsEnv(env, filteredEntry) {
			t.Fatalf("expected Windows case-insensitive filter to strip %q, got %#v", filteredEntry, env)
		}
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
	for _, path := range []string{"/tmp/fake-git", "git"} {
		if _, err := Command(path, versionArg); err == nil {
			t.Fatalf("expected %q path error", path)
		}
		if _, err := CommandContext(context.Background(), path, versionArg); err == nil {
			t.Fatalf("expected %q path error for context command", path)
		}
	}
}

func TestCommandRejectsUserManagedInstallPaths(t *testing.T) {
	userLocalPath := filepath.Join(t.TempDir(), "bin", "git")
	if err := os.MkdirAll(filepath.Dir(userLocalPath), 0o755); err != nil {
		t.Fatalf("mkdir user-local bin: %v", err)
	}
	if err := os.WriteFile(userLocalPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write user-local Git: %v", err)
	}

	for _, path := range []string{userLocalPath, "/opt/homebrew/bin/git"} {
		if _, err := Command(path, versionArg); err == nil {
			t.Fatalf("expected %q to be rejected", path)
		}
		if _, err := CommandContext(context.Background(), path, versionArg); err == nil {
			t.Fatalf("expected %q to be rejected for context command", path)
		}
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
	if ExecutableAvailable(filePath) {
		t.Fatalf("expected user-owned executable to fail provenance validation")
	}
}

func TestSanitizedEnvPreventsDetachedGitMaintenanceOnCommit(t *testing.T) {
	gitPath, err := ResolveBinaryPath()
	if err != nil {
		t.Skip("git binary not available")
	}

	repo := t.TempDir()
	tracePath := filepath.Join(t.TempDir(), "trace2.json")
	run := func(args ...string) {
		t.Helper()

		command, err := CommandContext(context.Background(), gitPath, append([]string{"-C", repo}, args...)...)
		if err != nil {
			t.Fatalf("construct git %s: %v", strings.Join(args, " "), err)
		}
		command.Env = append(SanitizedEnv(), "GIT_TRACE2_EVENT="+tracePath)
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, string(output))
		}
	}

	run("init")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("hi\n"), 0o600); err != nil {
		t.Fatalf("write tracked file: %v", err)
	}
	run("add", ".")
	run("commit", "-m", "init")

	trace, err := os.ReadFile(tracePath)
	if err != nil {
		t.Fatalf("read trace2 output: %v", err)
	}
	traceText := string(trace)
	if strings.Contains(traceText, `"hierarchy":"commit/maintenance"`) {
		t.Fatalf("expected sanitized env to suppress detached git maintenance, trace=%s", traceText)
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

func setSanitizedEnvTestVars(t *testing.T) {
	t.Helper()

	t.Setenv("PATH", "/tmp/custom-bin")
	t.Setenv("GIT_DIR", "/tmp/fake-git-dir")
	t.Setenv("GIT_WORK_TREE", "/tmp/fake-worktree")
	t.Setenv("GIT_INDEX_FILE", "/tmp/fake-index")
	t.Setenv("LD_PRELOAD", "/tmp/malicious.so")
	t.Setenv("LD_LIBRARY_PATH", "/tmp/malicious-lib")
	t.Setenv("DYLD_FOO", "/tmp/malicious-dyld")
	t.Setenv("GIT_CONFIG_GLOBAL", "/tmp/attacker-global")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "core.fsmonitor")
	t.Setenv("GIT_CONFIG_VALUE_0", "/tmp/attacker-helper")
	t.Setenv("HOME", "/tmp/attacker-home")
	t.Setenv("XDG_CONFIG_HOME", "/tmp/attacker-xdg")
	t.Setenv("PAGER", "/tmp/attacker-pager")
	t.Setenv("KEEP_ME", "1")
}

func assertSanitizedEnvCoreEntries(t *testing.T, env []string) {
	t.Helper()

	if !containsEnv(env, SafeSystemPath) {
		t.Fatalf("expected safe path %q in env, got %#v", SafeSystemPath, env)
	}
	if containsEnvPrefix(env, "GIT_DIR=") || containsEnvPrefix(env, "GIT_WORK_TREE=") || containsEnvPrefix(env, "GIT_INDEX_FILE=") {
		t.Fatalf("expected git override vars to be stripped, got %#v", env)
	}
	if containsEnvPrefix(env, "LD_") || containsEnvPrefix(env, "DYLD_") {
		t.Fatalf("expected dynamic loader vars to be stripped, got %#v", env)
	}
	if !containsEnv(env, keepMeEnvEntry) {
		t.Fatalf("expected unrelated env vars to be preserved, got %#v", env)
	}
}

func assertSanitizedEnvForcedGitConfig(t *testing.T, env []string) {
	t.Helper()

	for _, forbidden := range []string{
		attackerGlobalConfigEnvEntry,
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_VALUE_0=/tmp/attacker-helper",
		"HOME=/tmp/attacker-home",
		"XDG_CONFIG_HOME=/tmp/attacker-xdg",
		"PAGER=/tmp/attacker-pager",
	} {
		if containsEnv(env, forbidden) {
			t.Fatalf("expected caller-controlled git config env vars to be stripped, got %#v", env)
		}
	}
	if !containsEnv(env, safeGitNoSystemConfig) || !containsEnv(env, safeGitGlobalConfig) {
		t.Fatalf("expected hardened git config env entries, got %#v", env)
	}
	for _, expected := range safeGitConfigEnvEntries() {
		if !containsEnv(env, expected) {
			t.Fatalf("expected forced git config entry %q in env, got %#v", expected, env)
		}
	}
}

func testKnownGitPaths(t *testing.T, build func(string) (*exec.Cmd, error)) {
	t.Helper()

	for _, gitPath := range TrustedExecutablePaths() {
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

func containsArgPair(args []string, key, value string) bool {
	for index := 0; index+1 < len(args); index++ {
		if args[index] == key && args[index+1] == value {
			return true
		}
	}
	return false
}
