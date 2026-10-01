package main

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"strings"
	"testing"
)

func TestProveMergeBaseOperandBoundary(t *testing.T) {
	testMergeBaseOperandBoundary(t)
}

func testMergeBaseOperandBoundary(t *testing.T) {
	t.Helper()
	repo := newMergeBaseOperandRepo(t)
	for _, tc := range []struct {
		name, operand string
		invalid       bool
	}{
		{name: "independent mode", operand: "--independent", invalid: true},
		{name: "octopus mode", operand: "--octopus", invalid: true},
		{name: "symbolic ref", operand: "refs/heads/proof-base"},
		{name: "relative revision", operand: "HEAD~1"},
		{name: "full SHA", operand: repo.baseSHA},
	} {
		t.Run(tc.name, func(t *testing.T) {
			executor := &execRunner{}
			calls := 0
			var mergeBase string
			r := &runner{execCommand: func(ctx context.Context, name string, args []string, dir string, env []string) ([]byte, error) {
				calls++
				output, err := executor.Run(ctx, name, args, dir, env)
				if calls == 1 {
					mergeBase = strings.TrimSpace(string(output))
				}
				return output, err
			}}
			err := r.prove(context.Background(), repo.path, tc.operand, nil, io.Discard)
			if tc.invalid {
				var exitErr *exec.ExitError
				if err == nil || !strings.HasPrefix(err.Error(), "resolve merge base:") || !errors.As(err, &exitErr) || calls != 1 {
					t.Fatalf("option operand must fail at merge-base before later commands: calls=%d err=%v", calls, err)
				}
				return
			}
			if mergeBase != repo.baseSHA {
				t.Fatalf("merge base = %q, want %q", mergeBase, repo.baseSHA)
			}
			if err == nil || !strings.HasPrefix(err.Error(), "regression proof requires at least one changed") || calls != 2 {
				t.Fatalf("valid revision must reach changed-file selection without a worktree or Go command: calls=%d err=%v", calls, err)
			}
		})
	}
}

func newMergeBaseOperandRepo(t *testing.T) regressionProofRepo {
	t.Helper()
	repo := regressionProofRepo{path: t.TempDir()}
	r := &runner{execCommand: (&execRunner{}).Run}
	git := func(args ...string) string {
		t.Helper()
		output, err := r.gitOutput(context.Background(), repo.path, args...)
		if err != nil {
			t.Fatalf("prepare merge-base fixture: %v", err)
		}
		return strings.TrimSpace(output)
	}
	git("init", "--template=")
	git("config", "user.name", "Test User")
	git("config", "user.email", "test@example.com")
	git("config", "commit.gpgsign", "false")
	git("config", "core.hooksPath", t.TempDir())
	for _, content := range []string{"base\n", "head\n"} {
		writeFiles(t, repo.path, map[string]string{"README.md": content})
		git("add", "--", "README.md")
		git("commit", "-m", "fixture")
		if repo.baseSHA == "" {
			repo.baseSHA = git("rev-parse", "HEAD")
			git("branch", "proof-base")
		}
	}
	return repo
}
