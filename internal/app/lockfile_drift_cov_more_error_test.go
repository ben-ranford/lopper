package app

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

func TestDetectLockfileDriftPropagatesGitContextErrors(t *testing.T) {
	originalResolve := resolveGitBinaryPathFn
	originalExec := execGitCommandContextFn
	defer func() {
		resolveGitBinaryPathFn = originalResolve
		execGitCommandContextFn = originalExec
	}()

	resolveGitBinaryPathFn = func() (string, error) { return writeFakeGitBinary(t), nil }
	execGitCommandContextFn = func(ctx context.Context, gitPath string, args ...string) (*exec.Cmd, error) {
		return exec.CommandContext(ctx, gitPath, args...), nil
	}

	cases := []struct {
		name    string
		mode    string
		wantSub string
		run     func(context.Context, string) error
	}{
		{
			name:    "detect lockfile drift propagates git context errors",
			mode:    "lsfail",
			wantSub: "ls-files",
			run: func(ctx context.Context, repo string) error {
				_, err := detectLockfileDrift(ctx, repo, false)
				return err
			},
		},
		{
			name:    "git changed files propagates tracked change failures",
			mode:    "difffail-head",
			wantSub: "run git",
			run: func(ctx context.Context, repo string) error {
				_, _, err := gitChangedFiles(ctx, repo)
				return err
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("FAKE_GIT_MODE", tc.mode)
			err := tc.run(context.Background(), t.TempDir())
			if err == nil || !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("expected %q error, got %v", tc.wantSub, err)
			}
		})
	}
}
