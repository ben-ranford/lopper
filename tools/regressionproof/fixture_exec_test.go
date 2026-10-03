package main

import (
	"bytes"
	"context"
	"os/exec"

	"github.com/ben-ranford/lopper/internal/gitexec"
)

// Fixtures deliberately have broader process capabilities than production proof.
type execRunner struct{}

func (*execRunner) Run(ctx context.Context, name string, args []string, dir string, env []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	if len(env) > 0 {
		cmd.Env = env
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	output := stdout.Bytes()
	if err != nil {
		output = append(append([]byte(nil), output...), stderr.Bytes()...)
		return output, &commandError{name: name, args: args, output: output, err: err}
	}
	return output, nil
}

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
