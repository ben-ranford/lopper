package main

import (
	"bytes"
	"context"
	"os/exec"
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
