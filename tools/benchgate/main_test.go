package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/gitexec"
	"github.com/ben-ranford/lopper/internal/safeio"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestMainSuccessAndFailureArtifacts(t *testing.T) {
	if runBenchgateMainIfRequested(t) {
		return
	}

	t.Run("success", func(t *testing.T) {
		repo := initGitRepo(t)
		baseCommit := gitCommitFile(t, repo, "base.txt", "base")
		gitCommitFile(t, repo, "head.txt", "head")

		output, exitCode := runBenchgateHelper(t, repo, "TestMainSuccessAndFailureArtifacts", "-base-ref", baseCommit)
		if exitCode != 0 {
			t.Fatalf("exit code = %d, want 0\n%s", exitCode, output)
		}
		if !strings.Contains(string(output), baseCommit) {
			t.Fatalf("output = %q, want it to contain %q", output, baseCommit)
		}
	})

	t.Run("missing ref writes status two", func(t *testing.T) {
		repo := initGitRepo(t)
		gitCommitFile(t, repo, "head.txt", "head")
		summaryPath := filepath.Join(repo, ".artifacts", "memory-bench-summary.md")
		statusPath := filepath.Join(repo, ".artifacts", "memory-bench-status.txt")

		output, exitCode := runBenchgateHelper(t, repo, "TestMainSuccessAndFailureArtifacts", "-base-ref", "refs/heads/missing", "-summary-out", summaryPath, "-status-out", statusPath)
		if exitCode != 2 {
			t.Fatalf("exit code = %d, want 2\n%s", exitCode, output)
		}
		if !strings.Contains(string(output), `does not resolve to a commit`) {
			t.Fatalf("expected missing ref diagnostic, got %q", output)
		}
		assertBenchgateFailureArtifacts(t, summaryPath, statusPath, `does not resolve to a commit`)
	})

	t.Run("missing base-ref required", func(t *testing.T) {
		repo := initGitRepo(t)
		summaryPath := filepath.Join(repo, ".artifacts", "memory-bench-summary.md")
		statusPath := filepath.Join(repo, ".artifacts", "memory-bench-status.txt")

		output, exitCode := runBenchgateHelper(t, repo, "TestMainSuccessAndFailureArtifacts", "-summary-out", summaryPath, "-status-out", statusPath)
		if exitCode != 2 {
			t.Fatalf("exit code = %d, want 2\n%s", exitCode, output)
		}
		if !strings.Contains(string(output), `-base-ref is required`) {
			t.Fatalf("expected missing base-ref diagnostic, got %q", output)
		}
		assertBenchgateFailureArtifacts(t, summaryPath, statusPath, `-base-ref is required`)
	})

	t.Run("unrelated ref writes status two", func(t *testing.T) {
		repo := initGitRepo(t)
		gitCommitFile(t, repo, "main.txt", "main")
		branch := strings.TrimSpace(runGit(t, repo, "branch", "--show-current"))
		runGit(t, repo, "checkout", "--orphan", "other-root")
		removeTrackedFiles(t, repo)
		otherCommit := gitCommitFile(t, repo, "other.txt", "other")
		runGit(t, repo, "checkout", branch)

		summaryPath := filepath.Join(repo, ".artifacts", "memory-bench-summary.md")
		statusPath := filepath.Join(repo, ".artifacts", "memory-bench-status.txt")
		output, exitCode := runBenchgateHelper(t, repo, "TestMainSuccessAndFailureArtifacts", "-base-ref", otherCommit, "-summary-out", summaryPath, "-status-out", statusPath)
		if exitCode != 2 {
			t.Fatalf("exit code = %d, want 2\n%s", exitCode, output)
		}
		if !strings.Contains(string(output), `is unrelated to HEAD`) {
			t.Fatalf("expected unrelated diagnostic, got %q", output)
		}
		assertBenchgateFailureArtifacts(t, summaryPath, statusPath, `is unrelated to HEAD`)
	})

	t.Run("artifact write failure surfaces clearly", func(t *testing.T) {
		repo := initGitRepo(t)
		blocker := filepath.Join(repo, "blocker")
		if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
			t.Fatalf("write blocker: %v", err)
		}

		output, exitCode := runBenchgateHelper(t, repo, "TestMainSuccessAndFailureArtifacts", "-base-ref", "refs/heads/missing", "-summary-out", filepath.Join(blocker, "memory-bench-summary.md"), "-status-out", filepath.Join(repo, ".artifacts", "memory-bench-status.txt"))
		if exitCode != 2 {
			t.Fatalf("exit code = %d, want 2\n%s", exitCode, output)
		}
		if !strings.Contains(string(output), `write failure artifacts`) {
			t.Fatalf("expected artifact write failure output, got %q", output)
		}
	})
}

func TestResolveBaseCommit(t *testing.T) {
	repo := initGitRepo(t)
	baseCommit := gitCommitFile(t, repo, "base.txt", "base")
	headCommit := gitCommitFile(t, repo, "head.txt", "head")
	gitPath := mustResolveCallerGitPath(t)

	got, err := resolveBaseCommit(repo, gitPath, baseCommit, "HEAD")
	if err != nil {
		t.Fatalf("resolve base commit: %v", err)
	}
	if got != baseCommit {
		t.Fatalf("base commit = %q, want %q (head %q)", got, baseCommit, headCommit)
	}
}

func TestRunMain(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		repo := initGitRepo(t)
		baseCommit := gitCommitFile(t, repo, "base.txt", "base")
		gitCommitFile(t, repo, "head.txt", "head")

		var stdout, stderr bytes.Buffer
		exitCode := runMain([]string{"-base-ref", baseCommit}, repo, &stdout, &stderr)
		if exitCode != 0 {
			t.Fatalf("exit code = %d, want 0", exitCode)
		}
		if strings.TrimSpace(stdout.String()) != baseCommit {
			t.Fatalf("stdout = %q, want %q", stdout.String(), baseCommit)
		}
		if stderr.Len() != 0 {
			t.Fatalf("stderr = %q, want empty", stderr.String())
		}
	})

	t.Run("failure", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		exitCode := runMain([]string{"-base-ref", "refs/heads/missing"}, benchgateCanonicalTempDir(t), &stdout, &stderr)
		if exitCode != 2 {
			t.Fatalf("exit code = %d, want 2", exitCode)
		}
		if stdout.Len() != 0 {
			t.Fatalf("stdout = %q, want empty", stdout.String())
		}
		if !strings.Contains(stderr.String(), `does not resolve to a commit`) {
			t.Fatalf("stderr = %q, want missing ref diagnostic", stderr.String())
		}
	})

	t.Run("stdout write failure", func(t *testing.T) {
		repo := initGitRepo(t)
		baseCommit := gitCommitFile(t, repo, "base.txt", "base")
		gitCommitFile(t, repo, "head.txt", "head")

		stdout := &errorWriter{err: errors.New("stdout write failed")}
		var stderr bytes.Buffer
		exitCode := runMain([]string{"-base-ref", baseCommit}, repo, stdout, &stderr)
		if exitCode != 2 {
			t.Fatalf("exit code = %d, want 2", exitCode)
		}
		if stdout.calls != 1 {
			t.Fatalf("stdout writes = %d, want 1", stdout.calls)
		}
	})

	t.Run("stderr write failure", func(t *testing.T) {
		var stdout bytes.Buffer
		stderr := &errorWriter{err: errors.New("stderr write failed")}
		exitCode := runMain([]string{"-base-ref", "refs/heads/missing"}, benchgateCanonicalTempDir(t), &stdout, stderr)
		if exitCode != 2 {
			t.Fatalf("exit code = %d, want 2", exitCode)
		}
		if stderr.calls != 1 {
			t.Fatalf("stderr writes = %d, want 1", stderr.calls)
		}
	})
}

func TestResolveBaseCommitFromArgs(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		repo := initGitRepo(t)
		baseCommit := gitCommitFile(t, repo, "base.txt", "base")
		gitCommitFile(t, repo, "head.txt", "head")
		gitPathOut := filepath.Join(repo, "git-path.txt")

		got, err := resolveBaseCommitFromArgs([]string{"-base-ref", baseCommit, "-git-path-out", gitPathOut}, repo)
		if err != nil {
			t.Fatalf("resolve from args: %v", err)
		}
		if got != baseCommit {
			t.Fatalf("base commit = %q, want %q", got, baseCommit)
		}
		gitPathData, err := os.ReadFile(gitPathOut)
		if err != nil {
			t.Fatalf("read git path output: %v", err)
		}
		gitPath := strings.TrimSpace(string(gitPathData))
		if !filepath.IsAbs(gitPath) {
			t.Fatalf("git path output = %q, want absolute path", gitPath)
		}
	})

	t.Run("missing base-ref writes artifacts", func(t *testing.T) {
		dir := benchgateCanonicalTempDir(t)
		summaryPath := filepath.Join(dir, "summary.md")
		statusPath := filepath.Join(dir, "status.txt")

		_, err := resolveBaseCommitFromArgs([]string{"-summary-out", summaryPath, "-status-out", statusPath}, dir)
		if err == nil || !strings.Contains(err.Error(), "-base-ref is required") {
			t.Fatalf("expected missing base-ref error, got %v", err)
		}
		assertBenchgateFailureArtifacts(t, summaryPath, statusPath, `-base-ref is required`)
	})

	t.Run("invalid flag returns parse error", func(t *testing.T) {
		if _, err := resolveBaseCommitFromArgs([]string{"-nope"}, benchgateCanonicalTempDir(t)); err == nil || !strings.Contains(err.Error(), "flag provided but not defined") {
			t.Fatalf("expected invalid flag error, got %v", err)
		}
	})

	t.Run("status artifact write failure", func(t *testing.T) {
		repo := initGitRepo(t)
		blocker := filepath.Join(repo, "blocker")
		if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
			t.Fatalf("write blocker: %v", err)
		}

		_, err := resolveBaseCommitFromArgs([]string{"-base-ref", "refs/heads/missing", "-summary-out", filepath.Join(repo, ".artifacts", "summary.md"), "-status-out", filepath.Join(blocker, "status.txt")}, repo)
		if err == nil || !strings.Contains(err.Error(), "write failure artifacts") {
			t.Fatalf("expected artifact write failure, got %v", err)
		}
	})

	t.Run("failure message writes artifacts without base ref", func(t *testing.T) {
		dir := benchgateCanonicalTempDir(t)
		summaryPath := filepath.Join(dir, "summary.md")
		statusPath := filepath.Join(dir, "status.txt")

		_, err := resolveBaseCommitFromArgs([]string{"-summary-out", summaryPath, "-status-out", statusPath, "-failure-message", "memory benchmark head execution failed; comparison could not be evaluated"}, dir)
		if err == nil || !strings.Contains(err.Error(), "memory benchmark head execution failed") {
			t.Fatalf("expected failure message error, got %v", err)
		}
		assertBenchgateFailureArtifacts(t, summaryPath, statusPath, "memory benchmark head execution failed")
	})

	t.Run("publish mode writes artifacts without stdout payload", func(t *testing.T) {
		dir := benchgateCanonicalTempDir(t)
		baseInput := filepath.Join(dir, "base-input.out")
		headInput := filepath.Join(dir, "head-input.out")
		summaryInput := filepath.Join(dir, "summary-input.md")
		writeBenchgateInputFile(t, baseInput, "base\n")
		writeBenchgateInputFile(t, headInput, "head\n")
		writeBenchgateInputFile(t, summaryInput, "summary\n")

		args := []string{
			"-bench-base-input", baseInput,
			"-bench-base-out", filepath.Join(dir, ".artifacts", "bench-base.out"),
			"-bench-head-input", headInput,
			"-bench-head-out", filepath.Join(dir, ".artifacts", "bench-head.out"),
			"-summary-input", summaryInput,
			"-summary-out", filepath.Join(dir, ".artifacts", "memory-bench-summary.md"),
			"-status-code", "0",
			"-status-out", filepath.Join(dir, ".artifacts", "memory-bench-status.txt"),
		}
		got, err := resolveBaseCommitFromArgs(args, dir)
		if err != nil {
			t.Fatalf("publish mode resolve: %v", err)
		}
		if got != "" {
			t.Fatalf("publish mode output = %q, want empty", got)
		}
	})

	t.Run("invalid caller git resolution fails closed", func(t *testing.T) {
		restoreBenchgateSeams(t)
		dir := benchgateCanonicalTempDir(t)
		summaryPath := filepath.Join(dir, "summary.md")
		statusPath := filepath.Join(dir, "status.txt")
		resolveGitBinaryPath = func() (string, error) { return "", errors.New("git executable not found in trusted locations") }

		_, err := resolveBaseCommitFromArgs([]string{"-base-ref", "HEAD", "-summary-out", summaryPath, "-status-out", statusPath}, dir)
		if err == nil || !strings.Contains(err.Error(), "trusted locations") {
			t.Fatalf("expected invalid git resolution error, got %v", err)
		}
		assertBenchgateFailureArtifacts(t, summaryPath, statusPath, "trusted locations")
	})
}

func TestResolveBaseCommitMissingRef(t *testing.T) {
	repo := initGitRepo(t)
	gitCommitFile(t, repo, "head.txt", "head")
	gitPath := mustResolveCallerGitPath(t)

	_, err := resolveBaseCommit(repo, gitPath, "refs/heads/does-not-exist", "HEAD")
	if err == nil || !strings.Contains(err.Error(), "does not resolve to a commit") {
		t.Fatalf("expected missing ref error, got %v", err)
	}
}

func TestResolveBaseCommitUnrelatedHistory(t *testing.T) {
	repo := initGitRepo(t)
	gitCommitFile(t, repo, "main.txt", "main")
	branch := strings.TrimSpace(runGit(t, repo, "branch", "--show-current"))
	runGit(t, repo, "checkout", "--orphan", "other-root")
	removeTrackedFiles(t, repo)
	otherCommit := gitCommitFile(t, repo, "other.txt", "other")
	runGit(t, repo, "checkout", branch)
	gitPath := mustResolveCallerGitPath(t)

	_, err := resolveBaseCommit(repo, gitPath, otherCommit, "HEAD")
	if err == nil || !strings.Contains(err.Error(), "is unrelated to HEAD") {
		t.Fatalf("expected unrelated history error, got %v", err)
	}
}

func TestResolveBaseCommitIgnoresCallerGitEnvironment(t *testing.T) {
	repo := initGitRepo(t)
	want := gitCommitFile(t, repo, "base.txt", "base")
	gitCommitFile(t, repo, "head.txt", "head")
	gitPath := mustResolveCallerGitPath(t)

	attackerRepo := initGitRepo(t)
	gitCommitFile(t, attackerRepo, "only.txt", "only")

	t.Setenv("GIT_DIR", filepath.Join(attackerRepo, ".git"))
	t.Setenv("GIT_WORK_TREE", attackerRepo)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(benchgateCanonicalTempDir(t), "attacker.index"))
	t.Setenv("GIT_OBJECT_DIRECTORY", filepath.Join(benchgateCanonicalTempDir(t), "attacker.objects"))
	t.Setenv("GIT_ALTERNATE_OBJECT_DIRECTORIES", filepath.Join(benchgateCanonicalTempDir(t), "attacker.alternates"))
	t.Setenv("GIT_COMMON_DIR", filepath.Join(benchgateCanonicalTempDir(t), "attacker.common"))
	t.Setenv("GIT_NAMESPACE", "attacker")

	got, err := resolveBaseCommit(repo, gitPath, "HEAD~1", "HEAD")
	if err != nil {
		t.Fatalf("resolve base commit with polluted git env: %v", err)
	}
	if got != want {
		t.Fatalf("base commit = %q, want %q", got, want)
	}
}

func TestResolveBaseCommitRejectsOptionLikeRefs(t *testing.T) {
	for _, tc := range []struct {
		name    string
		baseRef string
		headRef string
		want    string
	}{
		{name: "base ref long option", baseRef: "--help", headRef: "HEAD", want: "base-ref must not start with '-'"},
		{name: "base ref short option", baseRef: "-C/tmp/attacker", headRef: "HEAD", want: "base-ref must not start with '-'"},
		{name: "head ref option", baseRef: "HEAD", headRef: "--help", want: "head-ref must not start with '-'"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			restoreBenchgateSeams(t)
			calls := 0
			gitCommandContext = func(context.Context, string, ...string) (*exec.Cmd, error) {
				calls++
				return exec.Command("sh", "-c", ":"), nil
			}

			_, err := resolveBaseCommit(benchgateCanonicalTempDir(t), "/usr/bin/git", tc.baseRef, tc.headRef)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q, got %v", tc.want, err)
			}
			if calls != 0 {
				t.Fatalf("git command calls = %d, want 0", calls)
			}
		})
	}
}

func TestResolveBaseCommitAndFixturesIgnorePATHGitSentinel(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("path sentinel shell script is POSIX-only")
	}

	sentinelDir := filepath.Join(benchgateCanonicalTempDir(t), "fake-bin")
	if err := os.MkdirAll(sentinelDir, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	sentinelPath := filepath.Join(benchgateCanonicalTempDir(t), "fake-git-invoked")
	fakeGitPath := filepath.Join(sentinelDir, "git")
	fakeGit := "#!/bin/sh\n" +
		"echo invoked >" + shellQuotePath(sentinelPath) + "\n" +
		"exit 99\n"
	if err := os.WriteFile(fakeGitPath, []byte(fakeGit), 0o755); err != nil {
		t.Fatalf("write fake git: %v", err)
	}
	t.Setenv("PATH", sentinelDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	repo := initGitRepo(t)
	baseCommit := gitCommitFile(t, repo, "base.txt", "base")
	gitCommitFile(t, repo, "head.txt", "head")
	gitPath := mustResolveCallerGitPath(t)

	got, err := resolveBaseCommit(repo, gitPath, baseCommit, "HEAD")
	if err != nil {
		t.Fatalf("resolve base commit with fake PATH git: %v", err)
	}
	if got != baseCommit {
		t.Fatalf("base commit = %q, want %q", got, baseCommit)
	}
	if _, err := os.Stat(sentinelPath); !os.IsNotExist(err) {
		t.Fatalf("expected fake PATH git to remain unused, stat err = %v", err)
	}
}

func TestWorktreeMode(t *testing.T) {
	t.Run("rejects invalid combinations", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			args []string
			want string
		}{
			{
				name: "requires commit for add",
				args: []string{"-worktree-add", "/tmp/base-tree"},
				want: "worktree add requires worktree-commit",
			},
			{
				name: "commit requires add",
				args: []string{"-worktree-commit", "HEAD"},
				want: "worktree-commit requires worktree-add",
			},
			{
				name: "add and remove cannot be combined",
				args: []string{"-worktree-add", "/tmp/base-tree", "-worktree-commit", "HEAD", "-worktree-remove", "/tmp/base-tree"},
				want: "worktree add and remove cannot be combined",
			},
			{
				name: "worktree mode cannot be combined with base-ref",
				args: []string{"-base-ref", "HEAD", "-worktree-remove", "/tmp/base-tree"},
				want: "worktree mode cannot be combined",
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				_, err := resolveBaseCommitFromArgs(tc.args, benchgateCanonicalTempDir(t))
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("expected %q, got %v", tc.want, err)
				}
			})
		}
	})

	t.Run("add uses trusted git binary", func(t *testing.T) {
		restoreBenchgateSeams(t)
		repo := benchgateCanonicalTempDir(t)
		calls := 0
		resolveGitBinaryPath = func() (string, error) { return "/usr/bin/git", nil }
		gitCommandContext = func(_ context.Context, path string, args ...string) (*exec.Cmd, error) {
			calls++
			if path != "/usr/bin/git" {
				t.Fatalf("git path = %q, want trusted git", path)
			}
			if !reflect.DeepEqual(args, []string{"worktree", "add", "--detach", "--", "/tmp/base-tree", "deadbeef"}) {
				t.Fatalf("git args = %#v", args)
			}
			return exec.Command("sh", "-c", ":"), nil
		}

		if _, err := resolveBaseCommitFromArgs([]string{"-worktree-add", "/tmp/base-tree", "-worktree-commit", "deadbeef"}, repo); err != nil {
			t.Fatalf("worktree add: %v", err)
		}
		if calls != 1 {
			t.Fatalf("git command calls = %d, want 1", calls)
		}
	})

	t.Run("remove propagates git stderr", func(t *testing.T) {
		restoreBenchgateSeams(t)
		resolveGitBinaryPath = func() (string, error) { return "/usr/bin/git", nil }
		gitCommandContext = func(_ context.Context, path string, args ...string) (*exec.Cmd, error) {
			if path != "/usr/bin/git" {
				t.Fatalf("git path = %q, want trusted git", path)
			}
			if !reflect.DeepEqual(args, []string{"worktree", "remove", "--force", "--", "/tmp/base-tree"}) {
				t.Fatalf("git args = %#v", args)
			}
			return exec.Command("sh", "-c", "echo removal failed >&2; exit 3"), nil
		}

		_, err := resolveBaseCommitFromArgs([]string{"-worktree-remove", "/tmp/base-tree"}, benchgateCanonicalTempDir(t))
		if err == nil || !strings.Contains(err.Error(), "removal failed") {
			t.Fatalf("expected propagated git stderr, got %v", err)
		}
	})

	t.Run("leading dash path stays an operand", func(t *testing.T) {
		restoreBenchgateSeams(t)
		resolveGitBinaryPath = func() (string, error) { return "/usr/bin/git", nil }
		gitCommandContext = func(_ context.Context, path string, args ...string) (*exec.Cmd, error) {
			if path != "/usr/bin/git" {
				t.Fatalf("git path = %q, want trusted git", path)
			}
			if !reflect.DeepEqual(args, []string{"worktree", "add", "--detach", "--", "-tmp/base-tree", "deadbeef"}) {
				t.Fatalf("git args = %#v", args)
			}
			return exec.Command("sh", "-c", ":"), nil
		}

		if _, err := resolveBaseCommitFromArgs([]string{"-worktree-add", "-tmp/base-tree", "-worktree-commit", "deadbeef"}, benchgateCanonicalTempDir(t)); err != nil {
			t.Fatalf("worktree add with leading dash path: %v", err)
		}
	})

	t.Run("rejects option-like commit", func(t *testing.T) {
		restoreBenchgateSeams(t)
		resolveGitBinaryPath = func() (string, error) { return "/usr/bin/git", nil }
		calls := 0
		gitCommandContext = func(context.Context, string, ...string) (*exec.Cmd, error) {
			calls++
			return exec.Command("sh", "-c", ":"), nil
		}

		_, err := resolveBaseCommitFromArgs([]string{"-worktree-add", "/tmp/base-tree", "-worktree-commit", "--help"}, benchgateCanonicalTempDir(t))
		if err == nil || !strings.Contains(err.Error(), "worktree-commit must not start with '-'") {
			t.Fatalf("expected worktree commit validation error, got %v", err)
		}
		if calls != 0 {
			t.Fatalf("git command calls = %d, want 0", calls)
		}
	})

	t.Run("resolve trusted git failure propagates", func(t *testing.T) {
		restoreBenchgateSeams(t)
		expectedErr := errors.New("git executable not found in trusted locations")
		resolveGitBinaryPath = func() (string, error) { return "", expectedErr }

		_, err := resolveBaseCommitFromArgs([]string{"-worktree-remove", "/tmp/base-tree"}, benchgateCanonicalTempDir(t))
		if !errors.Is(err, expectedErr) {
			t.Fatalf("expected resolver error, got %v", err)
		}
	})

	t.Run("direct executeWorktree requires an operation", func(t *testing.T) {
		restoreBenchgateSeams(t)
		resolveGitBinaryPath = func() (string, error) { return "/usr/bin/git", nil }

		err := executeWorktree(benchgateCanonicalTempDir(t), config{statusCode: -1})
		if err == nil || !strings.Contains(err.Error(), "worktree mode requires worktree-add or worktree-remove") {
			t.Fatalf("expected missing worktree operation error, got %v", err)
		}
	})
}

func TestGitOutputPropagatesCommandErrors(t *testing.T) {
	t.Run("command context", func(t *testing.T) {
		expectedErr := errors.New("command creation failure")
		restoreBenchgateSeams(t)
		gitCommandContext = func(context.Context, string, ...string) (*exec.Cmd, error) {
			return nil, expectedErr
		}

		_, err := gitOutput(benchgateCanonicalTempDir(t), "/custom/git", "status")
		if !errors.Is(err, expectedErr) {
			t.Fatalf("expected command creation error, got %v", err)
		}
	})
}

func TestRunGitCommandPropagatesEmptyOutputErrors(t *testing.T) {
	restoreBenchgateSeams(t)
	expectedErr := errors.New("command creation failure")
	gitCommandContext = func(context.Context, string, ...string) (*exec.Cmd, error) {
		return nil, expectedErr
	}

	err := runGitCommand(benchgateCanonicalTempDir(t), "/usr/bin/git", "worktree", "remove", "--force", "/tmp/base-tree")
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected command creation failure, got %v", err)
	}

	restoreBenchgateSeams(t)
	gitCommandContext = func(_ context.Context, path string, args ...string) (*exec.Cmd, error) {
		if path != "/usr/bin/git" {
			t.Fatalf("git path = %q, want trusted git", path)
		}
		return exec.Command("sh", "-c", "exit 4"), nil
	}

	err = runGitCommand(benchgateCanonicalTempDir(t), "/usr/bin/git", "worktree", "remove", "--force", "/tmp/base-tree")
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected raw exit error without wrapped stderr, got %T %v", err, err)
	}
}

func TestWriteFailureArtifacts(t *testing.T) {
	dir := benchgateCanonicalTempDir(t)
	summaryPath := filepath.Join(dir, "artifacts", "summary.md")
	statusPath := filepath.Join(dir, "artifacts", "status.txt")

	if err := writeFailureArtifacts(summaryPath, statusPath, "memory benchmark base ref \"missing\" does not resolve to a commit"); err != nil {
		t.Fatalf("write failure artifacts: %v", err)
	}

	summary, err := os.ReadFile(summaryPath)
	if err != nil {
		t.Fatalf("read summary: %v", err)
	}
	if !strings.Contains(string(summary), "Comparison could not be evaluated.") || strings.Contains(string(summary), "Result: memory benchmark gate passed.") {
		t.Fatalf("unexpected summary: %q", summary)
	}

	status, err := os.ReadFile(statusPath)
	if err != nil {
		t.Fatalf("read status: %v", err)
	}
	if string(status) != "2\n" {
		t.Fatalf("status = %q, want %q", status, "2\n")
	}
}

func TestWriteFailureArtifactsCreatesRestrictedArtifactDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory permission bits are not stable on Windows")
	}

	dir := benchgateCanonicalTempDir(t)
	artifactDir := filepath.Join(dir, "artifacts")
	summaryPath := filepath.Join(artifactDir, "summary.md")
	statusPath := filepath.Join(artifactDir, "status.txt")

	if err := writeFailureArtifacts(summaryPath, statusPath, "missing base ref"); err != nil {
		t.Fatalf("write failure artifacts: %v", err)
	}

	info, err := os.Stat(artifactDir)
	if err != nil {
		t.Fatalf("stat artifact dir: %v", err)
	}
	if got := info.Mode().Perm() & 0o007; got != 0 {
		t.Fatalf("artifact dir perms = %o, want no world access", info.Mode().Perm())
	}
}

func TestWriteFailureArtifactsNormalizesPreexistingArtifactPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits are not stable on Windows")
	}

	dir := benchgateCanonicalTempDir(t)
	artifactDir := filepath.Join(dir, ".artifacts")
	summaryPath := filepath.Join(artifactDir, "memory-bench-summary.md")
	statusPath := filepath.Join(artifactDir, "memory-bench-status.txt")
	if err := os.MkdirAll(artifactDir, 0o755); err != nil {
		t.Fatalf("mkdir artifact dir: %v", err)
	}
	if err := os.Chmod(artifactDir, 0o777); err != nil {
		t.Fatalf("chmod artifact dir: %v", err)
	}
	if err := os.WriteFile(summaryPath, []byte("stale summary\n"), 0o666); err != nil {
		t.Fatalf("write stale summary: %v", err)
	}
	if err := os.Chmod(summaryPath, 0o666); err != nil {
		t.Fatalf("chmod stale summary: %v", err)
	}
	if err := os.WriteFile(statusPath, []byte("1\n"), 0o666); err != nil {
		t.Fatalf("write stale status: %v", err)
	}
	if err := os.Chmod(statusPath, 0o666); err != nil {
		t.Fatalf("chmod stale status: %v", err)
	}

	if err := writeFailureArtifacts(summaryPath, statusPath, "missing base ref"); err != nil {
		t.Fatalf("write failure artifacts: %v", err)
	}

	assertBenchgateFailureArtifacts(t, summaryPath, statusPath, "missing base ref")
	assertPathMode(t, artifactDir, artifactDirMode)
	assertPathMode(t, summaryPath, artifactFileMode)
	assertPathMode(t, statusPath, artifactFileMode)
}

func TestWriteFailureArtifactsCreatesExactPermissionsUnderRestrictiveUmask(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits are not stable on Windows")
	}
	if !supportsProcessUmask() {
		t.Skip("process umask control unavailable on this platform")
	}

	dir := benchgateCanonicalTempDir(t)
	artifactDir := filepath.Join(dir, ".artifacts")
	summaryPath := filepath.Join(artifactDir, "memory-bench-summary.md")
	statusPath := filepath.Join(artifactDir, "memory-bench-status.txt")

	withProcessUmask(t, 0o077, func() {
		if err := writeFailureArtifacts(summaryPath, statusPath, "missing base ref"); err != nil {
			t.Fatalf("write failure artifacts under restrictive umask: %v", err)
		}
	})

	assertBenchgateFailureArtifacts(t, summaryPath, statusPath, "missing base ref")
	assertPathMode(t, artifactDir, artifactDirMode)
	assertPathMode(t, summaryPath, artifactFileMode)
	assertPathMode(t, statusPath, artifactFileMode)
}

func TestWriteFailureArtifactsStatusOnly(t *testing.T) {
	statusPath := filepath.Join(benchgateCanonicalTempDir(t), "status.txt")
	if err := writeFailureArtifacts("", statusPath, "no summary"); err != nil {
		t.Fatalf("write status-only failure artifacts: %v", err)
	}

	status, err := os.ReadFile(statusPath)
	if err != nil {
		t.Fatalf("read status: %v", err)
	}
	if string(status) != "2\n" {
		t.Fatalf("status = %q, want %q", status, "2\n")
	}
}

func TestWriteFailureArtifactsSummaryOnly(t *testing.T) {
	summaryPath := filepath.Join(benchgateCanonicalTempDir(t), "summary.md")
	if err := writeFailureArtifacts(summaryPath, "", "summary only"); err != nil {
		t.Fatalf("write summary-only failure artifacts: %v", err)
	}

	summary, err := os.ReadFile(summaryPath)
	if err != nil {
		t.Fatalf("read summary: %v", err)
	}
	if !strings.Contains(string(summary), "summary only") {
		t.Fatalf("summary = %q, want it to contain %q", summary, "summary only")
	}
}

func TestWriteFailureArtifactsStatusWriteError(t *testing.T) {
	dir := benchgateCanonicalTempDir(t)
	statusPath := filepath.Join(dir, "status-dir")
	if err := os.Mkdir(statusPath, 0o755); err != nil {
		t.Fatalf("mkdir status path: %v", err)
	}

	err := writeFailureArtifacts("", statusPath, "status write error")
	if err == nil {
		t.Fatal("expected status write error")
	}
}

func TestWriteFailureArtifactsSummaryWriteErrorStillWritesStatus(t *testing.T) {
	dir := benchgateCanonicalTempDir(t)
	blocker := filepath.Join(dir, "summary-blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	summaryPath := filepath.Join(blocker, "summary.md")
	statusPath := filepath.Join(dir, "status.txt")

	err := writeFailureArtifacts(summaryPath, statusPath, "summary write error")
	if err == nil {
		t.Fatal("expected summary write error")
	}
	if !strings.Contains(err.Error(), "summary artifact:") {
		t.Fatalf("error = %v, want summary artifact failure", err)
	}
	status, readErr := os.ReadFile(statusPath)
	if readErr != nil {
		t.Fatalf("read status: %v", readErr)
	}
	if string(status) != "2\n" {
		t.Fatalf("status = %q, want %q", status, "2\n")
	}
}

func TestWriteFailureArtifactsStatusWriteErrorStillWritesSummary(t *testing.T) {
	dir := benchgateCanonicalTempDir(t)
	summaryPath := filepath.Join(dir, "summary.md")
	statusPath := filepath.Join(dir, "status-dir")
	if err := os.Mkdir(statusPath, 0o755); err != nil {
		t.Fatalf("mkdir status path: %v", err)
	}

	err := writeFailureArtifacts(summaryPath, statusPath, "status write error")
	if err == nil {
		t.Fatal("expected status write error")
	}
	if !strings.Contains(err.Error(), "status artifact:") {
		t.Fatalf("error = %v, want status artifact failure", err)
	}
	summary, readErr := os.ReadFile(summaryPath)
	if readErr != nil {
		t.Fatalf("read summary: %v", readErr)
	}
	if !strings.Contains(string(summary), "status write error") {
		t.Fatalf("summary = %q, want it to contain %q", summary, "status write error")
	}
}

func TestWriteFailureArtifactsAggregatesWriteErrors(t *testing.T) {
	dir := benchgateCanonicalTempDir(t)
	summaryBlocker := filepath.Join(dir, "summary-blocker")
	statusBlocker := filepath.Join(dir, "status-blocker")
	if err := os.WriteFile(summaryBlocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("write summary blocker: %v", err)
	}
	if err := os.WriteFile(statusBlocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("write status blocker: %v", err)
	}

	err := writeFailureArtifacts(filepath.Join(summaryBlocker, "summary.md"), filepath.Join(statusBlocker, "status.txt"), "both writes fail")
	if err == nil {
		t.Fatal("expected aggregate write error")
	}
	if !strings.Contains(err.Error(), "summary artifact:") {
		t.Fatalf("error = %v, want summary artifact failure", err)
	}
	if !strings.Contains(err.Error(), "status artifact:") {
		t.Fatalf("error = %v, want status artifact failure", err)
	}
}

func TestWriteFailureArtifactsRejectsSymlinkArtifactDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink behavior is environment-dependent on Windows")
	}

	dir := benchgateCanonicalTempDir(t)
	realDir := filepath.Join(dir, "real-artifacts")
	if err := os.Mkdir(realDir, 0o755); err != nil {
		t.Fatalf("mkdir real artifact dir: %v", err)
	}
	linkDir := filepath.Join(dir, "link-artifacts")
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Fatalf("symlink artifact dir: %v", err)
	}

	err := writeFailureArtifacts(filepath.Join(linkDir, "summary.md"), filepath.Join(linkDir, "status.txt"), "blocked by symlink")
	if err == nil {
		t.Fatal("expected symlink artifact directory error")
	}
	if !strings.Contains(err.Error(), "artifact parent contains symlink") {
		t.Fatalf("error = %v, want symlink parent failure", err)
	}
}

func TestWriteFailureArtifactsRejectsSymlinkSummaryTargetStillWritesStatus(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink behavior is environment-dependent on Windows")
	}

	dir := benchgateCanonicalTempDir(t)
	summaryPath := filepath.Join(dir, "summary.md")
	statusPath := filepath.Join(dir, "status.txt")
	outsidePath := filepath.Join(dir, "outside-summary.md")
	if err := os.WriteFile(outsidePath, []byte("outside\n"), 0o644); err != nil {
		t.Fatalf("write outside target: %v", err)
	}
	if err := os.Symlink(outsidePath, summaryPath); err != nil {
		t.Fatalf("symlink summary target: %v", err)
	}

	err := writeFailureArtifacts(summaryPath, statusPath, "blocked by symlink target")
	if err == nil {
		t.Fatal("expected symlink summary target error")
	}
	if !strings.Contains(err.Error(), "summary artifact:") {
		t.Fatalf("error = %v, want summary artifact failure", err)
	}
	if !strings.Contains(err.Error(), "target path is a symlink") {
		t.Fatalf("error = %v, want symlink target failure", err)
	}

	status, readErr := os.ReadFile(statusPath)
	if readErr != nil {
		t.Fatalf("read status: %v", readErr)
	}
	if string(status) != "2\n" {
		t.Fatalf("status = %q, want %q", status, "2\n")
	}

	outside, readErr := os.ReadFile(outsidePath)
	if readErr != nil {
		t.Fatalf("read outside target: %v", readErr)
	}
	if string(outside) != "outside\n" {
		t.Fatalf("outside target = %q, want unchanged content", outside)
	}
}

func TestPublishArtifactsWritesBenchOutputsSummaryAndStatus(t *testing.T) {
	dir := benchgateCanonicalTempDir(t)
	baseInput := filepath.Join(dir, "base-input.out")
	headInput := filepath.Join(dir, "head-input.out")
	summaryInput := filepath.Join(dir, "summary-input.md")
	writeBenchgateInputFile(t, baseInput, "base\n")
	writeBenchgateInputFile(t, headInput, "head\n")
	writeBenchgateInputFile(t, summaryInput, "summary\n")

	cfg := config{
		benchBaseInput: baseInput,
		benchBaseOut:   filepath.Join(dir, ".artifacts", "bench-base.out"),
		benchHeadInput: headInput,
		benchHeadOut:   filepath.Join(dir, ".artifacts", "bench-head.out"),
		summaryInput:   summaryInput,
		summaryOut:     filepath.Join(dir, ".artifacts", "memory-bench-summary.md"),
		statusCode:     1,
		statusOut:      filepath.Join(dir, ".artifacts", "memory-bench-status.txt"),
	}

	if err := publishArtifacts(cfg); err != nil {
		t.Fatalf("publish artifacts: %v", err)
	}

	assertBenchgateFileContent(t, cfg.benchBaseOut, "base\n")
	assertBenchgateFileContent(t, cfg.benchHeadOut, "head\n")
	assertBenchgateFileContent(t, cfg.summaryOut, "summary\n")
	assertBenchgateFileContent(t, cfg.statusOut, "1\n")
}

func TestPublishArtifactsRejectsSymlinkTargetBeforeWritingOthers(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink behavior is environment-dependent on Windows")
	}

	dir := benchgateCanonicalTempDir(t)
	baseInput := filepath.Join(dir, "base-input.out")
	headInput := filepath.Join(dir, "head-input.out")
	summaryInput := filepath.Join(dir, "summary-input.md")
	writeBenchgateInputFile(t, baseInput, "base\n")
	writeBenchgateInputFile(t, headInput, "head\n")
	writeBenchgateInputFile(t, summaryInput, "summary\n")

	artifactDir := filepath.Join(dir, ".artifacts")
	if err := os.MkdirAll(artifactDir, 0o755); err != nil {
		t.Fatalf("mkdir artifact dir: %v", err)
	}
	outsideSummary := filepath.Join(dir, "outside-summary.md")
	writeBenchgateInputFile(t, outsideSummary, "outside summary\n")
	summaryOut := filepath.Join(artifactDir, "memory-bench-summary.md")
	if err := os.Symlink(outsideSummary, summaryOut); err != nil {
		t.Fatalf("symlink summary target: %v", err)
	}

	cfg := config{
		benchBaseInput: baseInput,
		benchBaseOut:   filepath.Join(artifactDir, "bench-base.out"),
		benchHeadInput: headInput,
		benchHeadOut:   filepath.Join(artifactDir, "bench-head.out"),
		summaryInput:   summaryInput,
		summaryOut:     summaryOut,
		statusCode:     0,
		statusOut:      filepath.Join(artifactDir, "memory-bench-status.txt"),
	}

	err := publishArtifacts(cfg)
	if err == nil || !strings.Contains(err.Error(), "summary artifact: artifact target is a symlink") {
		t.Fatalf("expected publish symlink rejection, got %v", err)
	}
	assertBenchgateFileContent(t, outsideSummary, "outside summary\n")
	assertBenchgatePathAbsent(t, cfg.benchBaseOut)
	assertBenchgatePathAbsent(t, cfg.benchHeadOut)
	assertBenchgatePathAbsent(t, cfg.statusOut)
}

func TestBuildArtifactSpecsValidation(t *testing.T) {
	dir := benchgateCanonicalTempDir(t)
	baseInput := filepath.Join(dir, "base-input.out")
	headInput := filepath.Join(dir, "head-input.out")
	summaryInput := filepath.Join(dir, "summary-input.md")
	writeBenchgateInputFile(t, baseInput, "base\n")
	writeBenchgateInputFile(t, headInput, "head\n")
	writeBenchgateInputFile(t, summaryInput, "summary\n")

	t.Run("success", func(t *testing.T) {
		specs, err := buildArtifactSpecs(config{
			benchBaseInput: baseInput,
			benchBaseOut:   filepath.Join(dir, "bench-base.out"),
			benchHeadInput: headInput,
			benchHeadOut:   filepath.Join(dir, "bench-head.out"),
			summaryInput:   summaryInput,
			summaryOut:     filepath.Join(dir, "summary.md"),
			statusCode:     2,
			statusOut:      filepath.Join(dir, "status.txt"),
		})
		if err != nil {
			t.Fatalf("build artifact specs: %v", err)
		}
		if len(specs) != 4 {
			t.Fatalf("spec count = %d, want 4", len(specs))
		}
		if string(specs[3].content) != "2\n" {
			t.Fatalf("status content = %q, want %q", specs[3].content, "2\n")
		}
	})

	for _, tt := range []struct {
		name string
		cfg  config
		want string
	}{
		{
			name: "publish with base ref",
			cfg: config{
				baseRef:      "HEAD~1",
				summaryInput: summaryInput,
				summaryOut:   filepath.Join(dir, "summary.md"),
				statusCode:   0,
				statusOut:    filepath.Join(dir, "status.txt"),
			},
			want: "publish mode cannot be combined",
		},
		{
			name: "publish with failure message",
			cfg: config{
				failureMessage: "failed",
				summaryInput:   summaryInput,
				summaryOut:     filepath.Join(dir, "summary.md"),
			},
			want: "publish mode cannot be combined",
		},
		{
			name: "base missing output",
			cfg: config{
				benchBaseInput: baseInput,
			},
			want: "bench-base publish requires both input and output paths",
		},
		{
			name: "head missing input",
			cfg: config{
				benchHeadOut: filepath.Join(dir, "bench-head.out"),
			},
			want: "bench-head publish requires both input and output paths",
		},
		{
			name: "summary missing output",
			cfg: config{
				summaryInput: summaryInput,
			},
			want: "summary publish requires both input and output paths",
		},
		{
			name: "status missing code",
			cfg: config{
				statusOut:  filepath.Join(dir, "status.txt"),
				statusCode: -1,
			},
			want: "status publish requires both status-code and status-out",
		},
		{
			name: "status missing output",
			cfg: config{
				statusCode: 0,
			},
			want: "status publish requires both status-code and status-out",
		},
		{
			name: "status code too large",
			cfg: config{
				statusCode: 3,
				statusOut:  filepath.Join(dir, "status.txt"),
			},
			want: "status-code must be between 0 and 2",
		},
		{
			name: "summary input read error",
			cfg: config{
				summaryInput: filepath.Join(dir, "missing-summary.md"),
				summaryOut:   filepath.Join(dir, "summary.md"),
				statusCode:   -1,
			},
			want: "read summary input",
		},
		{
			name: "no artifacts",
			cfg:  config{statusCode: -1},
			want: "publish mode requires at least one artifact",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := buildArtifactSpecs(tt.cfg)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("expected %q, got %v", tt.want, err)
			}
		})
	}
}

func TestReadArtifactInputErrorPaths(t *testing.T) {
	t.Run("open error", func(t *testing.T) {
		expectedErr := errors.New("open failure")
		restoreBenchgateSeams(t)
		openArtifactInputFile = func(string) (io.ReadCloser, error) {
			return nil, expectedErr
		}

		_, err := readArtifactInput("artifact.txt")
		if !errors.Is(err, expectedErr) {
			t.Fatalf("expected open error, got %v", err)
		}
	})

	t.Run("read and close errors join", func(t *testing.T) {
		readErr := errors.New("read failure")
		closeErr := errors.New("close failure")
		restoreBenchgateSeams(t)
		openArtifactInputFile = func(string) (io.ReadCloser, error) {
			return &benchgateStubFile{
				read:  func([]byte) (int, error) { return 0, readErr },
				close: func() error { return closeErr },
			}, nil
		}

		_, err := readArtifactInput("artifact.txt")
		if !errors.Is(err, readErr) || !errors.Is(err, closeErr) {
			t.Fatalf("expected joined read and close errors, got %v", err)
		}
	})

	t.Run("close error after successful read", func(t *testing.T) {
		closeErr := errors.New("close failure")
		restoreBenchgateSeams(t)
		openArtifactInputFile = func(string) (io.ReadCloser, error) {
			readCalls := 0
			return &benchgateStubFile{
				read: func(p []byte) (int, error) {
					if readCalls > 0 {
						return 0, io.EOF
					}
					readCalls++
					copy(p, []byte("ok"))
					return 2, io.EOF
				},
				close: func() error { return closeErr },
			}, nil
		}

		_, err := readArtifactInput("artifact.txt")
		if !errors.Is(err, closeErr) {
			t.Fatalf("expected close error, got %v", err)
		}
	})
}

func TestPublishArtifactsJoinsWriteAndCloseErrors(t *testing.T) {
	dir := benchgateCanonicalTempDir(t)
	input := filepath.Join(dir, "summary-input.md")
	writeBenchgateInputFile(t, input, "summary\n")

	writeErr := errors.New("write failure")
	closeErr := errors.New("close failure")
	restoreBenchgateSeams(t)
	openArtifactDirFn = func(string) (safeio.Root, string, error) {
		return &benchgateStubRoot{
			lstat: func(string) (fs.FileInfo, error) { return nil, os.ErrNotExist },
			close: func() error { return closeErr },
		}, "summary.md", nil
	}
	writeArtifactWithinRoot = func(safeio.Root, string, []byte, os.FileMode) error {
		return writeErr
	}

	err := publishArtifacts(config{
		summaryInput: input,
		summaryOut:   filepath.Join(dir, "summary.md"),
		statusCode:   -1,
	})
	if !errors.Is(err, writeErr) || !errors.Is(err, closeErr) {
		t.Fatalf("expected joined write and close errors, got %v", err)
	}
}

func TestValidateArtifactTarget(t *testing.T) {
	t.Run("missing target", func(t *testing.T) {
		root := &benchgateStubRoot{
			lstat: func(string) (fs.FileInfo, error) { return nil, os.ErrNotExist },
		}
		err := validateArtifactTarget(root, "artifact.txt", "/tmp/artifact.txt")
		if err != nil {
			t.Fatalf("expected missing target to be allowed, got %v", err)
		}
	})

	t.Run("non-regular target", func(t *testing.T) {
		dirInfo := statBenchgatePath(t, benchgateCanonicalTempDir(t))
		root := &benchgateStubRoot{
			lstat: func(string) (fs.FileInfo, error) { return dirInfo, nil },
		}
		err := validateArtifactTarget(root, "artifact.txt", "/tmp/artifact.txt")
		if err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Fatalf("expected non-regular target error, got %v", err)
		}
	})

	t.Run("regular existing file", func(t *testing.T) {
		path := filepath.Join(benchgateCanonicalTempDir(t), "artifact.txt")
		writeBenchgateInputFile(t, path, "artifact\n")
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatalf("lstat regular file: %v", err)
		}
		root := &benchgateStubRoot{
			lstat: func(string) (fs.FileInfo, error) { return info, nil },
		}
		err = validateArtifactTarget(root, "artifact.txt", path)
		if err != nil {
			t.Fatalf("expected regular file validation success, got %v", err)
		}
	})

	t.Run("symlink target", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlink behavior is environment-dependent on Windows")
		}
		dir := benchgateCanonicalTempDir(t)
		targetPath := filepath.Join(dir, "target.txt")
		linkPath := filepath.Join(dir, "link.txt")
		writeBenchgateInputFile(t, targetPath, "target\n")
		if err := os.Symlink(targetPath, linkPath); err != nil {
			t.Fatalf("symlink target: %v", err)
		}
		info, err := os.Lstat(linkPath)
		if err != nil {
			t.Fatalf("lstat symlink: %v", err)
		}
		root := &benchgateStubRoot{
			lstat: func(string) (fs.FileInfo, error) { return info, nil },
		}
		err = validateArtifactTarget(root, "artifact.txt", linkPath)
		if err == nil || !strings.Contains(err.Error(), "artifact target is a symlink") {
			t.Fatalf("expected symlink target error, got %v", err)
		}
	})

	t.Run("lstat error", func(t *testing.T) {
		expectedErr := errors.New("lstat failure")
		root := &benchgateStubRoot{
			lstat: func(string) (fs.FileInfo, error) { return nil, expectedErr },
		}
		err := validateArtifactTarget(root, "artifact.txt", "/tmp/artifact.txt")
		if !errors.Is(err, expectedErr) {
			t.Fatalf("expected lstat error, got %v", err)
		}
	})
}

func TestArtifactWriterWriteAndClosePaths(t *testing.T) {
	t.Run("chmod error", func(t *testing.T) {
		restoreBenchgateSeams(t)
		writer := &artifactWriter{
			roots:   make(map[string]safeio.Root),
			targets: make(map[string]artifactHandle),
		}
		openArtifactDirFn = func(string) (safeio.Root, string, error) {
			return &benchgateStubRoot{
				lstat: func(string) (fs.FileInfo, error) { return nil, os.ErrNotExist },
				chmod: func(name string, perm os.FileMode) error {
					if name != "artifact.txt" || perm != artifactFileMode {
						t.Fatalf("unexpected chmod %q %#o", name, perm)
					}
					return errors.New("chmod failure")
				},
			}, "artifact.txt", nil
		}
		writeArtifactWithinRoot = func(safeio.Root, string, []byte, os.FileMode) error { return nil }

		err := writer.Write(artifactSpec{
			path:    filepath.Join(benchgateCanonicalTempDir(t), "artifact.txt"),
			content: []byte("artifact"),
		})
		if err == nil || !strings.Contains(err.Error(), "chmod failure") {
			t.Fatalf("expected chmod failure, got %v", err)
		}
	})

	t.Run("success", func(t *testing.T) {
		restoreBenchgateSeams(t)
		writer := &artifactWriter{
			roots:   make(map[string]safeio.Root),
			targets: make(map[string]artifactHandle),
		}
		chmodCalls := 0
		openArtifactDirFn = func(string) (safeio.Root, string, error) {
			return &benchgateStubRoot{
				lstat: func(string) (fs.FileInfo, error) { return nil, os.ErrNotExist },
				chmod: func(string, os.FileMode) error {
					chmodCalls++
					return nil
				},
			}, "artifact.txt", nil
		}
		writeArtifactWithinRoot = func(safeio.Root, string, []byte, os.FileMode) error { return nil }

		if err := writer.Write(artifactSpec{
			path:    filepath.Join(benchgateCanonicalTempDir(t), "artifact.txt"),
			content: []byte("artifact"),
		}); err != nil {
			t.Fatalf("write success path: %v", err)
		}
		if chmodCalls != 1 {
			t.Fatalf("chmod calls = %d, want 1", chmodCalls)
		}
	})

	t.Run("prepare error", func(t *testing.T) {
		expectedErr := errors.New("open artifact dir failure")
		restoreBenchgateSeams(t)
		writer := &artifactWriter{
			roots:   make(map[string]safeio.Root),
			targets: make(map[string]artifactHandle),
		}
		openArtifactDirFn = func(string) (safeio.Root, string, error) {
			return nil, "", expectedErr
		}

		err := writer.Write(artifactSpec{
			path:    filepath.Join(benchgateCanonicalTempDir(t), "artifact.txt"),
			content: []byte("artifact"),
		})
		if !errors.Is(err, expectedErr) {
			t.Fatalf("expected prepare error, got %v", err)
		}
	})

	t.Run("close joins non-nil roots", func(t *testing.T) {
		err := (&artifactWriter{
			roots: map[string]safeio.Root{
				"/tmp/ok":  nil,
				"/tmp/bad": &benchgateStubRoot{close: func() error { return errors.New("close failure") }},
			},
		}).Close()
		if err == nil || !strings.Contains(err.Error(), "/tmp/bad") {
			t.Fatalf("expected close failure with path context, got %v", err)
		}
	})
}

func TestArtifactWriterPrepareCacheAndMismatch(t *testing.T) {
	t.Run("cache hit reuses handle", func(t *testing.T) {
		restoreBenchgateSeams(t)
		writer := &artifactWriter{
			roots:   make(map[string]safeio.Root),
			targets: make(map[string]artifactHandle),
		}
		opened := 0
		openArtifactDirFn = func(string) (safeio.Root, string, error) {
			opened++
			return &benchgateStubRoot{
				lstat: func(string) (fs.FileInfo, error) { return nil, os.ErrNotExist },
			}, "artifact.txt", nil
		}

		path := filepath.Join(benchgateCanonicalTempDir(t), "artifact.txt")
		first, err := writer.prepare(path)
		if err != nil {
			t.Fatalf("first prepare: %v", err)
		}
		second, err := writer.prepare(path)
		if err != nil {
			t.Fatalf("second prepare: %v", err)
		}
		if opened != 1 {
			t.Fatalf("open count = %d, want 1", opened)
		}
		if first.fileName != second.fileName || first.root != second.root {
			t.Fatal("expected cached prepare result")
		}
	})

	t.Run("mismatched target name closes root", func(t *testing.T) {
		closeErr := errors.New("close failure")
		restoreBenchgateSeams(t)
		writer := &artifactWriter{
			roots:   make(map[string]safeio.Root),
			targets: make(map[string]artifactHandle),
		}
		openArtifactDirFn = func(string) (safeio.Root, string, error) {
			return &benchgateStubRoot{close: func() error { return closeErr }}, "wrong.txt", nil
		}

		_, err := writer.prepare(filepath.Join(benchgateCanonicalTempDir(t), "artifact.txt"))
		if err == nil || !strings.Contains(err.Error(), "artifact target changed while opening") || !errors.Is(err, closeErr) {
			t.Fatalf("expected mismatched target error with close failure, got %v", err)
		}
	})
}

func TestPublishArtifactsReturnsCloseErrorAfterSuccessfulWrite(t *testing.T) {
	dir := benchgateCanonicalTempDir(t)
	input := filepath.Join(dir, "summary-input.md")
	writeBenchgateInputFile(t, input, "summary\n")

	closeErr := errors.New("close failure")
	restoreBenchgateSeams(t)
	openArtifactDirFn = func(string) (safeio.Root, string, error) {
		return &benchgateStubRoot{
			lstat: func(string) (fs.FileInfo, error) { return nil, os.ErrNotExist },
			close: func() error { return closeErr },
		}, "summary.md", nil
	}
	writeArtifactWithinRoot = func(safeio.Root, string, []byte, os.FileMode) error { return nil }

	err := publishArtifacts(config{
		summaryInput: input,
		summaryOut:   filepath.Join(dir, "summary.md"),
		statusCode:   -1,
	})
	if !errors.Is(err, closeErr) {
		t.Fatalf("expected close error after successful publish, got %v", err)
	}
}

func TestWriteArtifactFileJoinsWriteAndCloseErrors(t *testing.T) {
	writeErr := errors.New("write failure")
	closeErr := errors.New("close failure")
	restoreBenchgateSeams(t)
	openArtifactDirFn = func(string) (safeio.Root, string, error) {
		return &benchgateStubRoot{close: func() error { return closeErr }}, "artifact.txt", nil
	}
	writeArtifactWithinRoot = func(safeio.Root, string, []byte, os.FileMode) error {
		return writeErr
	}

	err := writeArtifactFile(filepath.Join(benchgateCanonicalTempDir(t), "artifact.txt"), []byte("artifact"))
	if !errors.Is(err, writeErr) || !errors.Is(err, closeErr) {
		t.Fatalf("expected joined write and close errors, got %v", err)
	}
}

func TestWriteArtifactFileJoinsChmodAndCloseErrors(t *testing.T) {
	chmodErr := errors.New("chmod failure")
	closeErr := errors.New("close failure")
	restoreBenchgateSeams(t)
	openArtifactDirFn = func(string) (safeio.Root, string, error) {
		return &benchgateStubRoot{
			chmod: func(name string, perm os.FileMode) error {
				if name != "artifact.txt" || perm != artifactFileMode {
					t.Fatalf("unexpected chmod %q %#o", name, perm)
				}
				return chmodErr
			},
			close: func() error { return closeErr },
		}, "artifact.txt", nil
	}
	writeArtifactWithinRoot = func(safeio.Root, string, []byte, os.FileMode) error { return nil }

	err := writeArtifactFile(filepath.Join(benchgateCanonicalTempDir(t), "artifact.txt"), []byte("artifact"))
	if !errors.Is(err, chmodErr) || !errors.Is(err, closeErr) {
		t.Fatalf("expected joined chmod and close errors, got %v", err)
	}
}

func TestResolveArtifactTargetNormalizesRelativePath(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	dir := benchgateCanonicalTempDir(t)
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir %s: %v", dir, err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(wd); err != nil {
			t.Fatalf("restore wd %s: %v", wd, err)
		}
	})
	wantDir, err := filepath.Abs("nested")
	if err != nil {
		t.Fatalf("resolve expected dir: %v", err)
	}

	target, err := resolveArtifactTarget(filepath.Join("nested", "summary.md"))
	if err != nil {
		t.Fatalf("resolve artifact target: %v", err)
	}
	if target.dir != wantDir {
		t.Fatalf("target dir = %q, want %q", target.dir, wantDir)
	}
	if target.fileName != "summary.md" {
		t.Fatalf("target file = %q, want summary.md", target.fileName)
	}
}

func TestResolveArtifactTargetRejectsRootPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("root-path semantics vary on Windows volumes")
	}

	_, err := resolveArtifactTarget(string(os.PathSeparator))
	if err == nil || !strings.Contains(err.Error(), "must name a file") {
		t.Fatalf("expected root-path rejection, got %v", err)
	}
}

func TestResolveArtifactTargetPropagatesAbsError(t *testing.T) {
	expectedErr := errors.New("absolute path failure")
	restoreBenchgateSeams(t)
	resolveArtifactPathAbs = func(string) (string, error) {
		return "", expectedErr
	}

	_, err := resolveArtifactTarget("summary.md")
	if err == nil || !strings.Contains(err.Error(), "resolve artifact path") {
		t.Fatalf("expected absolute-path resolution error, got %v", err)
	}
}

func TestOpenArtifactDirCreatesMissingParents(t *testing.T) {
	rootDir := benchgateCanonicalTempDir(t)
	path := filepath.Join(rootDir, "nested", "artifacts", "summary.md")

	root, fileName, err := openArtifactDir(path)
	if err != nil {
		t.Fatalf("open artifact dir: %v", err)
	}
	if fileName != "summary.md" {
		t.Fatalf("fileName = %q, want summary.md", fileName)
	}
	if err := root.Close(); err != nil {
		t.Fatalf("close artifact root: %v", err)
	}
	assertPathMode(t, filepath.Join(rootDir, "nested"), artifactDirMode)
	assertPathMode(t, filepath.Join(rootDir, "nested", "artifacts"), artifactDirMode)
}

func TestOpenArtifactDirErrorPaths(t *testing.T) {
	t.Run("target resolution error", func(t *testing.T) {
		expectedErr := errors.New("resolve artifact target failure")
		restoreBenchgateSeams(t)
		resolveArtifactPathAbs = func(string) (string, error) {
			return "", expectedErr
		}

		_, _, err := openArtifactDir("summary.md")
		if !errors.Is(err, expectedErr) {
			t.Fatalf("expected target-resolution error, got %v", err)
		}
	})

	t.Run("ancestor error", func(t *testing.T) {
		expectedErr := errors.New("ancestor root failure")
		restoreBenchgateSeams(t)
		openArtifactAncestorFn = func(string) (safeio.Root, string, []string, error) {
			return nil, "", nil, expectedErr
		}

		_, _, err := openArtifactDir(filepath.Join(benchgateCanonicalTempDir(t), "summary.md"))
		if !errors.Is(err, expectedErr) {
			t.Fatalf("expected ancestor-root error, got %v", err)
		}
	})

	t.Run("open child error closes ancestor root", func(t *testing.T) {
		openErr := errors.New("open child failure")
		closeErr := errors.New("ancestor close failure")
		restoreBenchgateSeams(t)
		openArtifactAncestorFn = func(string) (safeio.Root, string, []string, error) {
			return &benchgateStubRoot{close: func() error { return closeErr }}, "/tmp", []string{"nested"}, nil
		}
		openOrCreateArtifactFn = func(safeio.Root, string, string) (safeio.Root, error) {
			return nil, openErr
		}

		_, _, err := openArtifactDir(filepath.Join(benchgateCanonicalTempDir(t), "summary.md"))
		if !errors.Is(err, openErr) || !errors.Is(err, closeErr) {
			t.Fatalf("expected joined child-open and ancestor-close errors, got %v", err)
		}
	})

	t.Run("close current after child open", func(t *testing.T) {
		currentCloseErr := errors.New("current close failure")
		nextCloseErr := errors.New("next close failure")
		restoreBenchgateSeams(t)
		openArtifactAncestorFn = func(string) (safeio.Root, string, []string, error) {
			return &benchgateStubRoot{close: func() error { return currentCloseErr }}, "/tmp", []string{"nested"}, nil
		}
		openOrCreateArtifactFn = func(safeio.Root, string, string) (safeio.Root, error) {
			return &benchgateStubRoot{close: func() error { return nextCloseErr }}, nil
		}

		_, _, err := openArtifactDir(filepath.Join(benchgateCanonicalTempDir(t), "summary.md"))
		if !errors.Is(err, currentCloseErr) || !errors.Is(err, nextCloseErr) {
			t.Fatalf("expected joined current and next close errors, got %v", err)
		}
	})

	t.Run("chmod error closes current root", func(t *testing.T) {
		chmodErr := errors.New("chmod failure")
		closeErr := errors.New("close failure")
		restoreBenchgateSeams(t)
		openArtifactAncestorFn = func(string) (safeio.Root, string, []string, error) {
			return &benchgateStubRoot{
				chmod: func(name string, perm os.FileMode) error {
					if name != "." || perm != artifactDirMode {
						t.Fatalf("unexpected chmod %q %#o", name, perm)
					}
					return chmodErr
				},
				close: func() error { return closeErr },
			}, "/tmp", nil, nil
		}

		_, _, err := openArtifactDir(filepath.Join(benchgateCanonicalTempDir(t), "summary.md"))
		if !errors.Is(err, chmodErr) || !errors.Is(err, closeErr) {
			t.Fatalf("expected joined chmod and close errors, got %v", err)
		}
	})
}

func TestOpenArtifactAncestorRootRejectsFileAndSymlinkParents(t *testing.T) {
	t.Run("file", func(t *testing.T) {
		dir := benchgateCanonicalTempDir(t)
		filePath := filepath.Join(dir, "file")
		if err := os.WriteFile(filePath, []byte("x"), 0o600); err != nil {
			t.Fatalf("write file parent: %v", err)
		}

		_, _, _, err := openArtifactAncestorRoot(filePath)
		if err == nil || !strings.Contains(err.Error(), "artifact parent is not a directory") {
			t.Fatalf("expected file-parent rejection, got %v", err)
		}
	})

	t.Run("symlink", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlink behavior is environment-dependent on Windows")
		}

		dir := benchgateCanonicalTempDir(t)
		realDir := filepath.Join(dir, "real")
		if err := os.Mkdir(realDir, 0o755); err != nil {
			t.Fatalf("mkdir real dir: %v", err)
		}
		linkDir := filepath.Join(dir, "link")
		if err := os.Symlink(realDir, linkDir); err != nil {
			t.Fatalf("symlink parent: %v", err)
		}

		_, _, _, err := openArtifactAncestorRoot(linkDir)
		if err == nil || !strings.Contains(err.Error(), "artifact parent contains symlink") {
			t.Fatalf("expected symlink-parent rejection, got %v", err)
		}
	})

	t.Run("symlink ancestor on original artifact path", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlink behavior is environment-dependent on Windows")
		}

		dir := benchgateCanonicalTempDir(t)
		outsideDir := filepath.Join(dir, "outside")
		if err := os.MkdirAll(filepath.Join(outsideDir, "existing"), 0o755); err != nil {
			t.Fatalf("mkdir outside existing dir: %v", err)
		}
		linkDir := filepath.Join(dir, "link")
		if err := os.Symlink(outsideDir, linkDir); err != nil {
			t.Fatalf("symlink ancestor: %v", err)
		}

		_, _, err := openArtifactDir(filepath.Join(linkDir, "existing", "file"))
		if err == nil || !strings.Contains(err.Error(), "artifact parent contains symlink") {
			t.Fatalf("expected symlink-ancestor rejection, got %v", err)
		}
	})
}

func TestOpenArtifactAncestorRootReturnsVolumeRootForRootPath(t *testing.T) {
	volumeRoot := filepath.VolumeName(benchgateCanonicalTempDir(t)) + string(os.PathSeparator)

	root, rootAbs, relParts, err := openArtifactAncestorRoot(volumeRoot)
	if err != nil {
		t.Fatalf("open artifact ancestor root: %v", err)
	}
	if err := root.Close(); err != nil {
		t.Fatalf("close artifact root: %v", err)
	}
	if rootAbs != volumeRoot {
		t.Fatalf("rootAbs = %q, want %q", rootAbs, volumeRoot)
	}
	if len(relParts) != 0 {
		t.Fatalf("relParts = %v, want none", relParts)
	}
}

func TestOpenArtifactAncestorRootInternalErrorPaths(t *testing.T) {
	volumeRoot := filepath.VolumeName(benchgateCanonicalTempDir(t)) + string(os.PathSeparator)
	dirInfo := statBenchgatePath(t, benchgateCanonicalTempDir(t))

	t.Run("open child error", func(t *testing.T) {
		expectedErr := errors.New("open child failure")
		restoreBenchgateSeams(t)
		openCanonicalArtifactRoot = func(path string) (safeio.Root, error) {
			if path != volumeRoot {
				t.Fatalf("open root path = %q, want %q", path, volumeRoot)
			}
			return &benchgateStubRoot{
				lstat: func(name string) (fs.FileInfo, error) {
					if name != "tmp" {
						t.Fatalf("unexpected lstat %q", name)
					}
					return dirInfo, nil
				},
				openRoot: func(name string) (safeio.Root, error) {
					if name != "tmp" {
						t.Fatalf("unexpected open root %q", name)
					}
					return nil, expectedErr
				},
			}, nil
		}

		_, _, _, err := openArtifactAncestorRoot(filepath.Join(volumeRoot, "tmp"))
		if !errors.Is(err, expectedErr) {
			t.Fatalf("expected open-child error, got %v", err)
		}
	})

	t.Run("child stat error", func(t *testing.T) {
		expectedErr := errors.New("child stat failure")
		restoreBenchgateSeams(t)
		openCanonicalArtifactRoot = stubArtifactRootWithChild(dirInfo, func(string) (fs.FileInfo, error) {
			return nil, expectedErr
		})

		_, _, _, err := openArtifactAncestorRoot(filepath.Join(volumeRoot, "tmp"))
		if !errors.Is(err, expectedErr) {
			t.Fatalf("expected child-stat error, got %v", err)
		}
	})

	t.Run("child changed while opening", func(t *testing.T) {
		restoreBenchgateSeams(t)
		changedInfo := statBenchgatePath(t, filepath.Dir(benchgateCanonicalTempDir(t)))
		openCanonicalArtifactRoot = stubArtifactRootWithChild(dirInfo, func(string) (fs.FileInfo, error) {
			return changedInfo, nil
		})

		_, _, _, err := openArtifactAncestorRoot(filepath.Join(volumeRoot, "tmp"))
		if err == nil || !strings.Contains(err.Error(), "artifact parent changed while opening") {
			t.Fatalf("expected changed-while-opening error, got %v", err)
		}
	})

	t.Run("current close error", func(t *testing.T) {
		expectedErr := errors.New("current close failure")
		restoreBenchgateSeams(t)
		openCanonicalArtifactRoot = func(string) (safeio.Root, error) {
			return &benchgateStubRoot{
				lstat: func(string) (fs.FileInfo, error) { return dirInfo, nil },
				openRoot: func(string) (safeio.Root, error) {
					return &benchgateStubRoot{
						lstat: func(string) (fs.FileInfo, error) { return dirInfo, nil },
					}, nil
				},
				close: func() error { return expectedErr },
			}, nil
		}

		_, _, _, err := openArtifactAncestorRoot(filepath.Join(volumeRoot, "tmp"))
		if !errors.Is(err, expectedErr) {
			t.Fatalf("expected current-close error, got %v", err)
		}
	})
}

func TestOpenArtifactAncestorRootWrapsOpenError(t *testing.T) {
	expectedErr := errors.New("open canonical root failure")
	restoreBenchgateSeams(t)
	openCanonicalArtifactRoot = func(string) (safeio.Root, error) {
		return nil, expectedErr
	}

	_, _, _, err := openArtifactAncestorRoot(benchgateCanonicalTempDir(t))
	if err == nil || !errors.Is(err, expectedErr) || !strings.Contains(err.Error(), "open artifact root") {
		t.Fatalf("expected wrapped canonical-root error, got %v", err)
	}
}

func TestOpenArtifactAncestorRootPropagatesLstatError(t *testing.T) {
	_, _, _, err := openArtifactAncestorRoot(string([]byte{0}))
	if err == nil {
		t.Fatal("expected invalid-path lstat error")
	}
}

func TestSplitArtifactPathFiltersEmptyAndDotComponents(t *testing.T) {
	parts := splitArtifactPath(filepath.Join("nested", ".", "artifacts") + string(os.PathSeparator) + string(os.PathSeparator) + "summary.md")
	if !reflect.DeepEqual(parts, []string{"nested", "artifacts", "summary.md"}) {
		t.Fatalf("parts = %v, want %v", parts, []string{"nested", "artifacts", "summary.md"})
	}
	if got := splitArtifactPath("."); len(got) != 0 {
		t.Fatalf("root-relative dot path = %v, want empty", got)
	}
}

func TestOpenOrCreateArtifactDirErrorPaths(t *testing.T) {
	t.Run("mkdir error", func(t *testing.T) {
		mkdirErr := errors.New("mkdir failure")
		root := &benchgateStubRoot{
			lstat: func(string) (fs.FileInfo, error) { return nil, os.ErrNotExist },
			mkdir: func(string, os.FileMode) error { return mkdirErr },
		}

		next, err := openOrCreateArtifactDir(root, "artifacts", "/tmp/artifacts")
		if next != nil {
			t.Fatal("expected mkdir failure to keep child root nil")
		}
		if !errors.Is(err, mkdirErr) {
			t.Fatalf("expected mkdir error, got %v", err)
		}
	})

	t.Run("mkdir raced with concurrent create", func(t *testing.T) {
		dirInfo := statBenchgatePath(t, benchgateCanonicalTempDir(t))
		child := &benchgateStubRoot{
			lstat: func(string) (fs.FileInfo, error) { return dirInfo, nil },
		}
		lstatCalls := 0
		root := &benchgateStubRoot{
			lstat: func(string) (fs.FileInfo, error) {
				lstatCalls++
				if lstatCalls == 1 {
					return nil, os.ErrNotExist
				}
				return dirInfo, nil
			},
			mkdir:    func(string, os.FileMode) error { return fs.ErrExist },
			openRoot: func(string) (safeio.Root, error) { return child, nil },
		}

		next, err := openOrCreateArtifactDir(root, "artifacts", "/tmp/artifacts")
		if err != nil {
			t.Fatalf("expected concurrent create success, got %v", err)
		}
		if next == nil {
			t.Fatal("expected child root on concurrent create")
		}
		if err := next.Close(); err != nil {
			t.Fatalf("close child root: %v", err)
		}
	})

	t.Run("lookup after mkdir error", func(t *testing.T) {
		expectedErr := errors.New("lookup after mkdir failure")
		lstatCalls := 0
		root := &benchgateStubRoot{
			lstat: func(string) (fs.FileInfo, error) {
				lstatCalls++
				if lstatCalls == 1 {
					return nil, os.ErrNotExist
				}
				return nil, expectedErr
			},
			mkdir: func(string, os.FileMode) error { return nil },
		}

		next, err := openOrCreateArtifactDir(root, "artifacts", "/tmp/artifacts")
		if next != nil {
			t.Fatal("expected lookup-after-mkdir failure to keep child root nil")
		}
		if err == nil || !strings.Contains(err.Error(), "lookup after mkdir failure") {
			t.Fatalf("expected lookup-after-mkdir error, got %v", err)
		}
	})

	t.Run("symlink", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlink behavior is environment-dependent on Windows")
		}
		dir := benchgateCanonicalTempDir(t)
		targetDir := filepath.Join(dir, "target")
		if err := os.Mkdir(targetDir, 0o755); err != nil {
			t.Fatalf("mkdir target dir: %v", err)
		}
		linkPath := filepath.Join(dir, "link")
		if err := os.Symlink(targetDir, linkPath); err != nil {
			t.Fatalf("symlink child: %v", err)
		}
		linkInfo, err := os.Lstat(linkPath)
		if err != nil {
			t.Fatalf("lstat link: %v", err)
		}
		root := &benchgateStubRoot{
			lstat: func(string) (fs.FileInfo, error) { return linkInfo, nil },
		}

		next, err := openOrCreateArtifactDir(root, "link", linkPath)
		if next != nil {
			t.Fatal("expected symlink child root to remain nil")
		}
		if err == nil || !strings.Contains(err.Error(), "artifact parent contains symlink") {
			t.Fatalf("expected symlink rejection, got %v", err)
		}
	})

	t.Run("non-directory", func(t *testing.T) {
		dir := benchgateCanonicalTempDir(t)
		filePath := filepath.Join(dir, "file")
		if err := os.WriteFile(filePath, []byte("x"), 0o600); err != nil {
			t.Fatalf("write child file: %v", err)
		}
		fileInfo, err := os.Lstat(filePath)
		if err != nil {
			t.Fatalf("lstat file: %v", err)
		}
		root := &benchgateStubRoot{
			lstat: func(string) (fs.FileInfo, error) { return fileInfo, nil },
		}

		next, err := openOrCreateArtifactDir(root, "file", filePath)
		if next != nil {
			t.Fatal("expected non-directory child root to remain nil")
		}
		if err == nil || !strings.Contains(err.Error(), "artifact parent is not a directory") {
			t.Fatalf("expected non-directory rejection, got %v", err)
		}
	})

	t.Run("open root error", func(t *testing.T) {
		expectedInfo := statBenchgatePath(t, benchgateCanonicalTempDir(t))
		openErr := errors.New("open root failure")
		root := &benchgateStubRoot{
			lstat:    func(string) (fs.FileInfo, error) { return expectedInfo, nil },
			openRoot: func(string) (safeio.Root, error) { return nil, openErr },
		}

		next, err := openOrCreateArtifactDir(root, "artifacts", "/tmp/artifacts")
		if next != nil {
			t.Fatal("expected open-root failure to keep child root nil")
		}
		if !errors.Is(err, openErr) {
			t.Fatalf("expected open-root error, got %v", err)
		}
	})

	t.Run("changed while opening", func(t *testing.T) {
		expectedInfo := statBenchgatePath(t, benchgateCanonicalTempDir(t))
		changedInfo := statBenchgatePath(t, benchgateCanonicalTempDir(t))
		childClosed := false
		root := &benchgateStubRoot{
			lstat: func(string) (fs.FileInfo, error) { return expectedInfo, nil },
			openRoot: func(string) (safeio.Root, error) {
				return &benchgateStubRoot{
					lstat: func(string) (fs.FileInfo, error) { return changedInfo, nil },
					close: func() error {
						childClosed = true
						return nil
					},
				}, nil
			},
		}

		next, err := openOrCreateArtifactDir(root, "artifacts", "/tmp/artifacts")
		if next != nil {
			t.Fatal("expected changed directory rejection")
		}
		if err == nil || !strings.Contains(err.Error(), "artifact parent changed while opening") {
			t.Fatalf("expected changed-directory error, got %v", err)
		}
		if !childClosed {
			t.Fatal("expected changed child root to be closed")
		}
	})

	t.Run("child stat and close errors join", func(t *testing.T) {
		expectedInfo := statBenchgatePath(t, benchgateCanonicalTempDir(t))
		childStatErr := errors.New("child stat failure")
		childCloseErr := errors.New("child close failure")
		root := &benchgateStubRoot{
			lstat: func(string) (fs.FileInfo, error) { return expectedInfo, nil },
			openRoot: func(string) (safeio.Root, error) {
				return &benchgateStubRoot{
					lstat: func(string) (fs.FileInfo, error) { return nil, childStatErr },
					close: func() error { return childCloseErr },
				}, nil
			},
		}

		next, err := openOrCreateArtifactDir(root, "artifacts", "/tmp/artifacts")
		if next != nil {
			t.Fatal("expected failed child root to remain nil")
		}
		if !errors.Is(err, childStatErr) || !errors.Is(err, childCloseErr) {
			t.Fatalf("expected joined child stat and close errors, got %v", err)
		}
	})
}

func TestCloseRootWithErrorJoinsCloseFailure(t *testing.T) {
	rootErr := errors.New("root failure")
	closeErr := errors.New("close failure")
	err := closeRootWithError(&benchgateStubRoot{close: func() error { return closeErr }}, rootErr)
	if !errors.Is(err, rootErr) || !errors.Is(err, closeErr) {
		t.Fatalf("expected joined root and close errors, got %v", err)
	}
}

func TestParseArgs(t *testing.T) {
	cfg, err := parseArgs([]string{
		"-base-ref", "origin/main",
		"-head-ref", "HEAD~1",
		"-summary-out", "summary.md",
		"-status-out", "status.txt",
	})
	if err != nil {
		t.Fatalf("parse args: %v", err)
	}
	if cfg.baseRef != "origin/main" || cfg.headRef != "HEAD~1" || cfg.summaryOut != "summary.md" || cfg.statusOut != "status.txt" {
		t.Fatalf("unexpected config: %#v", cfg)
	}
}

func TestParseArgsFailureMessage(t *testing.T) {
	cfg, err := parseArgs([]string{
		"-summary-out", "summary.md",
		"-status-out", "status.txt",
		"-failure-message", "memory benchmark base execution failed",
	})
	if err != nil {
		t.Fatalf("parse args: %v", err)
	}
	if cfg.failureMessage != "memory benchmark base execution failed" {
		t.Fatalf("failure message = %q, want %q", cfg.failureMessage, "memory benchmark base execution failed")
	}
}

func initGitRepo(t *testing.T) string {
	t.Helper()
	repo := benchgateCanonicalTempDir(t)
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.name", "Test User")
	runGit(t, repo, "config", "user.email", "test@example.com")
	return repo
}

func gitCommitFile(t *testing.T, repo, name, content string) string {
	t.Helper()
	path := filepath.Join(repo, name)
	if err := os.WriteFile(path, []byte(content+"\n"), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	runGit(t, repo, "add", name)
	runGit(t, repo, "commit", "-m", "add "+name)
	return strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
}

func removeTrackedFiles(t *testing.T, repo string) {
	t.Helper()
	entries, err := os.ReadDir(repo)
	if err != nil {
		t.Fatalf("read repo dir: %v", err)
	}
	for _, entry := range entries {
		if entry.Name() == ".git" {
			continue
		}
		path := filepath.Join(repo, entry.Name())
		if err := os.RemoveAll(path); err != nil {
			t.Fatalf("remove %s: %v", entry.Name(), err)
		}
	}
}

func runGit(t *testing.T, repo string, args ...string) string {
	t.Helper()
	gitPath, err := gitexec.ResolveBinaryPath()
	if err != nil {
		t.Fatalf("resolve git path: %v", err)
	}
	cmd, err := gitexec.CommandContext(context.Background(), gitPath, args...)
	if err != nil {
		t.Fatalf("construct git %s: %v", strings.Join(args, " "), err)
	}
	cmd.Dir = repo
	cmd.Env = isolatedGitEnv(t)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
}

func shellQuotePath(path string) string {
	return "'" + strings.ReplaceAll(path, "'", `'\''`) + "'"
}

func runBenchgateMainIfRequested(t *testing.T) bool {
	t.Helper()
	if os.Getenv("GO_WANT_BENCHGATE_HELPER") != "1" {
		return false
	}

	argsIndex := slices.Index(os.Args, "--")
	if argsIndex < 0 {
		t.Fatal("missing helper args separator")
	}
	oldArgs := os.Args
	defer func() { os.Args = oldArgs }()
	os.Args = append([]string{oldArgs[0]}, os.Args[argsIndex+1:]...)
	main()
	return true
}

func runBenchgateHelper(t *testing.T, repo, testName string, args ...string) ([]byte, int) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run="+testName, "--")
	cmd.Args = append(cmd.Args, args...)
	cmd.Dir = repo
	cmd.Env = make([]string, 0, len(os.Environ())+1)
	for _, entry := range isolatedGitEnv(t) {
		if strings.HasPrefix(entry, "GO_WANT_BENCHGATE_HELPER=") {
			continue
		}
		cmd.Env = append(cmd.Env, entry)
	}
	cmd.Env = append(cmd.Env, "GO_WANT_BENCHGATE_HELPER=1")
	output, err := cmd.CombinedOutput()
	if err == nil {
		return output, 0
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected subprocess exit error or success, got %v\n%s", err, output)
	}
	return output, exitErr.ExitCode()
}

func benchgateCanonicalTempDir(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	canonicalDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("canonicalize temp dir %s: %v", dir, err)
	}
	return canonicalDir
}

type errorWriter struct {
	err   error
	calls int
}

type benchgateStubRoot struct {
	open     func(string) (safeio.File, error)
	openFile func(string, int, os.FileMode) (safeio.File, error)
	openRoot func(string) (safeio.Root, error)
	lstat    func(string) (fs.FileInfo, error)
	mkdir    func(string, os.FileMode) error
	chmod    func(string, os.FileMode) error
	mkdirAll func(string, os.FileMode) error
	rename   func(string, string) error
	remove   func(string) error
	close    func() error
}

type benchgateStubFile struct {
	read  func([]byte) (int, error)
	write func([]byte) (int, error)
	close func() error
	stat  func() (fs.FileInfo, error)
	chmod func(os.FileMode) error
}

func (f *benchgateStubFile) Read(p []byte) (int, error) {
	if f.read != nil {
		return f.read(p)
	}
	return 0, io.EOF
}

func (f *benchgateStubFile) Write(p []byte) (int, error) {
	if f.write != nil {
		return f.write(p)
	}
	return len(p), nil
}

func (f *benchgateStubFile) Close() error {
	if f.close != nil {
		return f.close()
	}
	return nil
}

func (f *benchgateStubFile) Stat() (fs.FileInfo, error) {
	if f.stat != nil {
		return f.stat()
	}
	return nil, errors.New("unexpected Stat call")
}

func (f *benchgateStubFile) Chmod(perm os.FileMode) error {
	if f.chmod != nil {
		return f.chmod(perm)
	}
	return nil
}

func (r *benchgateStubRoot) Open(name string) (safeio.File, error) {
	if r.open != nil {
		return r.open(name)
	}
	return nil, errors.New("unexpected Open call")
}

func (r *benchgateStubRoot) OpenFile(name string, flag int, perm os.FileMode) (safeio.File, error) {
	if r.openFile != nil {
		return r.openFile(name, flag, perm)
	}
	return nil, errors.New("unexpected OpenFile call")
}

func (r *benchgateStubRoot) OpenRoot(name string) (safeio.Root, error) {
	if r.openRoot != nil {
		return r.openRoot(name)
	}
	return nil, errors.New("unexpected OpenRoot call")
}

func (r *benchgateStubRoot) Lstat(name string) (fs.FileInfo, error) {
	if r.lstat != nil {
		return r.lstat(name)
	}
	return nil, errors.New("unexpected Lstat call")
}

func (r *benchgateStubRoot) Mkdir(name string, perm os.FileMode) error {
	if r.mkdir != nil {
		return r.mkdir(name, perm)
	}
	return errors.New("unexpected Mkdir call")
}

func (r *benchgateStubRoot) Chmod(name string, perm os.FileMode) error {
	if r.chmod != nil {
		return r.chmod(name, perm)
	}
	return nil
}

func (r *benchgateStubRoot) MkdirAll(name string, perm os.FileMode) error {
	if r.mkdirAll != nil {
		return r.mkdirAll(name, perm)
	}
	return errors.New("unexpected MkdirAll call")
}

func (r *benchgateStubRoot) Rename(oldName, newName string) error {
	if r.rename != nil {
		return r.rename(oldName, newName)
	}
	return errors.New("unexpected Rename call")
}

func (r *benchgateStubRoot) Remove(name string) error {
	if r.remove != nil {
		return r.remove(name)
	}
	return errors.New("unexpected Remove call")
}

func (r *benchgateStubRoot) Close() error {
	if r.close != nil {
		return r.close()
	}
	return nil
}

func (w *errorWriter) Write(_ []byte) (int, error) {
	w.calls++
	return 0, w.err
}

func restoreBenchgateSeams(t *testing.T) {
	t.Helper()
	originalResolveGitBinaryPath := resolveGitBinaryPath
	originalWriteGitPathOut := writeGitPathOut
	originalGitCommandContext := gitCommandContext
	originalOpenArtifactDirFn := openArtifactDirFn
	originalOpenArtifactAncestorFn := openArtifactAncestorFn
	originalOpenOrCreateArtifactFn := openOrCreateArtifactFn
	originalOpenCanonicalArtifactRoot := openCanonicalArtifactRoot
	originalOpenArtifactInputFile := openArtifactInputFile
	originalResolveArtifactPathAbs := resolveArtifactPathAbs
	originalWriteArtifactWithinRoot := writeArtifactWithinRoot
	t.Cleanup(func() {
		resolveGitBinaryPath = originalResolveGitBinaryPath
		writeGitPathOut = originalWriteGitPathOut
		gitCommandContext = originalGitCommandContext
		openArtifactDirFn = originalOpenArtifactDirFn
		openArtifactAncestorFn = originalOpenArtifactAncestorFn
		openOrCreateArtifactFn = originalOpenOrCreateArtifactFn
		openCanonicalArtifactRoot = originalOpenCanonicalArtifactRoot
		openArtifactInputFile = originalOpenArtifactInputFile
		resolveArtifactPathAbs = originalResolveArtifactPathAbs
		writeArtifactWithinRoot = originalWriteArtifactWithinRoot
	})
}

func mustResolveCallerGitPath(t *testing.T) string {
	t.Helper()
	gitPath, err := resolveGitBinaryPath()
	if err != nil {
		t.Fatalf("resolve caller git path: %v", err)
	}
	return gitPath
}

func statBenchgatePath(t *testing.T, path string) fs.FileInfo {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info
}

func stubArtifactRootWithChild(dirInfo fs.FileInfo, childLstat func(string) (fs.FileInfo, error)) func(string) (safeio.Root, error) {
	return func(string) (safeio.Root, error) {
		return &benchgateStubRoot{
			lstat: func(string) (fs.FileInfo, error) { return dirInfo, nil },
			openRoot: func(string) (safeio.Root, error) {
				return &benchgateStubRoot{lstat: childLstat}, nil
			},
		}, nil
	}
}

func writeBenchgateInputFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func assertBenchgateFileContent(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(data) != want {
		t.Fatalf("%s = %q, want %q", path, string(data), want)
	}
}

func assertBenchgatePathAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("expected %s to remain absent, got %v", path, err)
	}
}

func isolatedGitEnv(t *testing.T) []string {
	t.Helper()

	return testutil.IsolatedGitEnv(t)
}

func assertBenchgateFailureArtifacts(t *testing.T, summaryPath, statusPath, message string) {
	t.Helper()
	summary, err := os.ReadFile(summaryPath)
	if err != nil {
		t.Fatalf("read summary: %v", err)
	}
	if !strings.Contains(string(summary), "Comparison could not be evaluated.") || !strings.Contains(string(summary), message) {
		t.Fatalf("unexpected summary: %q", summary)
	}
	if strings.Contains(string(summary), "Result: memory benchmark gate passed.") || strings.Contains(string(summary), "Result: memory benchmark regression detected.") {
		t.Fatalf("invalid comparison summary must not contain threshold result: %q", summary)
	}

	status, err := os.ReadFile(statusPath)
	if err != nil {
		t.Fatalf("read status: %v", err)
	}
	if string(status) != "2\n" {
		t.Fatalf("status = %q, want %q", status, "2\n")
	}
}

func assertPathMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s perms = %o, want %o", path, got, want)
	}
}
