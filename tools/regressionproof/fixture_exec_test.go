package main

import (
	"context"

	"github.com/ben-ranford/lopper/internal/gitexec"
)

func (r *runner) fixtureGitOutput(ctx context.Context, repoRoot string, args ...string) (string, error) {
	gitPath, err := resolveGitBinaryPath()
	if err != nil {
		return "", err
	}
	fullArgs := append(gitexec.SafeConfigArgs(), "-C", repoRoot)
	fullArgs = append(fullArgs, args...)
	output, err := r.execCommand(ctx, gitPath, fullArgs, repoRoot, proofGitEnv())
	return string(output), err
}
