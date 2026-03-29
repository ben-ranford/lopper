package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/gitexec"
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
	}{
		{
			name:   "diff_success",
			script: "#!/bin/sh\nif [ \"$3\" = \"diff\" ]; then\n  echo \"pkg/a.go\"\n  echo \"pkg/b.go\"\n  exit 0\nfi\nexit 1\n",
		},
		{
			name:   "status_fallback",
			script: "#!/bin/sh\nif [ \"$3\" = \"diff\" ]; then\n  echo \"diff fail\" >&2\n  exit 2\nfi\nif [ \"$3\" = \"status\" ]; then\n  echo \"M  pkg/a.go\"\n  echo \"R  old.go -> pkg/b.go\"\n  exit 0\nfi\nexit 1\n",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			setupFakeGitResolver(t, tc.script)

			changed, err := ChangedFiles(t.TempDir())
			if err != nil {
				t.Fatalf("changed files lookup failed: %v", err)
			}
			if len(changed) != 2 || changed[0] != "pkg/a.go" || changed[1] != "pkg/b.go" {
				t.Fatalf("expected parsed changed names, got %#v", changed)
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

func TestParseChangedFileHelpers(t *testing.T) {
	changed := parseChangedFileLines([]byte("packages/a/file.ts\npackages/a/file.ts\n"))
	if len(changed) != 1 || changed[0] != "packages/a/file.ts" {
		t.Fatalf("expected deduped changed lines, got %#v", changed)
	}

	porcelain := parsePorcelainChangedFiles([]byte("M  packages/a/file.ts\nR  old.ts -> packages/b/new.ts\n"))
	if len(porcelain) != 2 || porcelain[0] != "packages/a/file.ts" || porcelain[1] != "packages/b/new.ts" {
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
	t.Setenv("PATH", "/tmp/fake-path")

	env := sanitizedGitEnv()
	for _, entry := range env {
		if strings.HasPrefix(entry, "GIT_DIR=") || strings.HasPrefix(entry, "GIT_WORK_TREE=") || strings.HasPrefix(entry, "GIT_INDEX_FILE=") {
			t.Fatalf("expected git override env vars to be removed, found %q", entry)
		}
	}
	if !containsEnvEntry(env, gitexec.SafeSystemPath) {
		t.Fatalf("expected safe PATH entry in sanitized env, got %#v", env)
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
