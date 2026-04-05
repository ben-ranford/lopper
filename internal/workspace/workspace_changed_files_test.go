package workspace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/gitexec"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestChangedFilesErrorsForNonRepoPath(t *testing.T) {
	tmp := t.TempDir()

	_, err := ChangedFiles(filepath.Join(tmp, "missing"))
	if err == nil {
		t.Fatalf("expected changed-files lookup to fail for non-repo path")
	}
}

func TestChangedFilesReturnsResolverError(t *testing.T) {
	original := resolveGitBinaryPathFn
	resolveGitBinaryPathFn = func() (string, error) {
		return "", errors.New("git unavailable")
	}
	t.Cleanup(func() {
		resolveGitBinaryPathFn = original
	})

	_, err := ChangedFiles(t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "git unavailable") {
		t.Fatalf("expected resolver error, got %v", err)
	}
}

func TestChangedFilesParsesDiffAndStatusFallback(t *testing.T) {
	tests := []struct {
		name   string
		script string
		want   []string
	}{
		{
			name:   "diff_success",
			script: "#!/bin/sh\nif [ \"$3\" = \"diff\" ]; then\n  case \"$*\" in\n    *\"--diff-filter=ACMRD\"*) ;;\n    *)\n      echo \"missing deleted-files diff filter\" >&2\n      exit 4\n      ;;\n  esac\n  printf '%s\\n' \"  pkg/spaced.go\" \"pkg/deleted.go\"\n  exit 0\nfi\nexit 1\n",
			want:   []string{"  pkg/spaced.go", "pkg/deleted.go"},
		},
		{
			name:   "status_fallback",
			script: "#!/bin/sh\nif [ \"$3\" = \"diff\" ]; then\n  echo \"diff fail\" >&2\n  exit 2\nfi\nif [ \"$3\" = \"status\" ]; then\n  echo \"M  pkg/a.go\"\n  echo \"R  old.go -> pkg/b.go\"\n  exit 0\nfi\nexit 1\n",
			want:   []string{"pkg/a.go", "pkg/b.go"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			setupFakeGitResolver(t, tc.script)

			changed, err := ChangedFiles(t.TempDir())
			if err != nil {
				t.Fatalf("changed files lookup failed: %v", err)
			}
			if len(changed) != len(tc.want) {
				t.Fatalf("expected %d changed names, got %#v", len(tc.want), changed)
			}
			for i := range tc.want {
				if changed[i] != tc.want[i] {
					t.Fatalf("expected parsed changed names %#v, got %#v", tc.want, changed)
				}
			}
		})
	}
}

func TestChangedFilesReturnsJoinedGitErrors(t *testing.T) {
	setupFakeGitResolver(t, "#!/bin/sh\nif [ \"$3\" = \"diff\" ]; then\n  echo \"diff failed\" >&2\n  exit 2\nfi\nif [ \"$3\" = \"status\" ]; then\n  echo \"status failed\" >&2\n  exit 3\nfi\nexit 1\n")

	_, err := ChangedFiles(t.TempDir())
	if err == nil {
		t.Fatalf("expected joined git errors")
	}
	if !strings.Contains(err.Error(), "diff failed") || !strings.Contains(err.Error(), "status failed") {
		t.Fatalf("expected combined git stderr in error, got %v", err)
	}
}

func TestChangedFilesIgnoresCallerGitConfigEnvironment(t *testing.T) {
	_, err := gitexec.ResolveBinaryPath()
	if err != nil {
		t.Skip("git binary not available")
	}

	repo := t.TempDir()
	testutil.RunGit(t, repo, "init")
	testutil.RunGit(t, repo, "config", "user.name", "Workspace Test")
	testutil.RunGit(t, repo, "config", "user.email", "workspace-test@example.com")

	mustWrite(t, filepath.Join(repo, "tracked.txt"), "tracked\n")
	testutil.RunGit(t, repo, "add", "tracked.txt")
	testutil.RunGit(t, repo, "commit", "-m", "initial")
	mustWrite(t, filepath.Join(repo, "untracked.txt"), "untracked\n")

	attackDir := t.TempDir()
	markerPath := filepath.Join(attackDir, "fsmonitor.marker")
	helperPath := filepath.Join(attackDir, "fsmonitor.sh")
	mustWrite(t, helperPath, fmt.Sprintf("#!/bin/sh\necho fsmonitor-ran >> %q\nprintf \"version 2\\n\\n\"\nexit 0\n", markerPath))
	if err := os.Chmod(helperPath, 0o700); err != nil {
		t.Fatalf("chmod helper executable: %v", err)
	}

	globalConfigPath := filepath.Join(attackDir, "attacker.gitconfig")
	mustWrite(t, globalConfigPath, "[core]\n\tfsmonitor = "+helperPath+"\n")
	t.Setenv("GIT_CONFIG_GLOBAL", globalConfigPath)
	t.Setenv("HOME", attackDir)
	t.Setenv("XDG_CONFIG_HOME", attackDir)
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "core.fsmonitor")
	t.Setenv("GIT_CONFIG_VALUE_0", helperPath)

	changed, err := ChangedFiles(repo)
	if err != nil {
		t.Fatalf("changed files lookup failed: %v", err)
	}
	if len(changed) != 1 || changed[0] != "untracked.txt" {
		t.Fatalf("expected untracked file discovery without config influence, got %#v", changed)
	}
	if _, err := os.Stat(markerPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected fsmonitor helper to never execute, markerPath=%q statErr=%v", markerPath, err)
	}
}

func TestParseChangedFileHelpers(t *testing.T) {
	changed := parseChangedFileLines([]byte("  packages/a/file.ts\n  packages/a/file.ts\n"))
	if len(changed) != 1 || changed[0] != "  packages/a/file.ts" {
		t.Fatalf("expected deduped changed lines, got %#v", changed)
	}

	porcelain := parsePorcelainChangedFiles([]byte("M   packages/a/file.ts\nR  old.ts ->  packages/b/new.ts\n"))
	if len(porcelain) != 2 || porcelain[0] != " packages/a/file.ts" || porcelain[1] != " packages/b/new.ts" {
		t.Fatalf("expected parsed porcelain paths, got %#v", porcelain)
	}

	withShort := parsePorcelainChangedFiles([]byte("\n??\nA  file.txt\n"))
	if len(withShort) != 1 || withShort[0] != "file.txt" {
		t.Fatalf("expected short lines to be ignored, got %#v", withShort)
	}
}

func TestResolveGitBinaryPathMissing(t *testing.T) {
	original := resolveGitBinaryPathFn
	originalExec := execGitCommandFn
	resolveGitBinaryPathFn = func() (string, error) {
		return "", errors.New("git executable not found")
	}
	t.Cleanup(func() {
		resolveGitBinaryPathFn = original
		execGitCommandFn = originalExec
	})

	_, err := resolveGitBinaryPath()
	if err == nil {
		t.Fatalf("expected git lookup to fail when configured binaries are unavailable")
	}
}

func TestResolveGitBinaryPathUsesResolver(t *testing.T) {
	original := resolveGitBinaryPathFn
	originalExec := execGitCommandFn
	t.Cleanup(func() {
		resolveGitBinaryPathFn = original
		execGitCommandFn = originalExec
	})

	const expectedPath = "/tmp/fake-git"
	resolveGitBinaryPathFn = func() (string, error) {
		return expectedPath, nil
	}

	path, err := resolveGitBinaryPath()
	if err != nil || path != expectedPath {
		t.Fatalf("expected resolver path, got path=%q err=%v", path, err)
	}
}

func TestRunGitReturnsStderrInError(t *testing.T) {
	gitPath, err := resolveGitBinaryPath()
	if err != nil {
		t.Skip("git binary not available")
	}

	_, err = runGit(gitPath, t.TempDir(), "hash-object", "--definitely-invalid-option")
	if err == nil {
		t.Fatalf("expected runGit to fail for invalid git option")
	}
	if !strings.Contains(err.Error(), "option") {
		t.Fatalf("expected stderr context in runGit error, got %v", err)
	}
}

func TestSanitizedGitEnvStripsGitOverrides(t *testing.T) {
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
	t.Setenv("PATH", "/tmp/fake-path")

	env := sanitizedGitEnv()
	for _, entry := range env {
		if strings.HasPrefix(entry, "GIT_DIR=") || strings.HasPrefix(entry, "GIT_WORK_TREE=") || strings.HasPrefix(entry, "GIT_INDEX_FILE=") {
			t.Fatalf("expected git override env vars to be removed, found %q", entry)
		}
	}
	if containsEnvEntry(env, "GIT_CONFIG_GLOBAL=/tmp/attacker-global") ||
		containsEnvEntry(env, "GIT_CONFIG_COUNT=1") ||
		containsEnvEntry(env, "GIT_CONFIG_VALUE_0=/tmp/attacker-helper") ||
		containsEnvEntry(env, "HOME=/tmp/attacker-home") ||
		containsEnvEntry(env, "XDG_CONFIG_HOME=/tmp/attacker-xdg") ||
		containsEnvEntry(env, "PAGER=/tmp/attacker-pager") {
		t.Fatalf("expected caller-provided git config env vars to be removed, got %#v", env)
	}
	if !containsEnvEntry(env, gitexec.SafeSystemPath) {
		t.Fatalf("expected safe PATH entry in sanitized env, got %#v", env)
	}
	if !containsEnvEntry(env, "GIT_CONFIG_GLOBAL=/dev/null") || !containsEnvEntry(env, "GIT_CONFIG_NOSYSTEM=1") {
		t.Fatalf("expected sanitized env to pin global/system git config, got %#v", env)
	}
}

func TestGitExecutableAvailable(t *testing.T) {
	if gitexec.ExecutableAvailable(t.TempDir()) {
		t.Fatalf("expected directory path to be non-executable")
	}

	file := filepath.Join(t.TempDir(), "git")
	mustWrite(t, file, "#!/bin/sh\n")
	if gitexec.ExecutableAvailable(file) {
		t.Fatalf("expected non-executable file to be unavailable")
	}
	if err := os.Chmod(file, 0o700); err != nil {
		t.Fatalf("chmod executable: %v", err)
	}
	if !gitexec.ExecutableAvailable(file) {
		t.Fatalf("expected executable file to be available")
	}
}

func containsEnvEntry(env []string, target string) bool {
	for _, entry := range env {
		if entry == target {
			return true
		}
	}
	return false
}
