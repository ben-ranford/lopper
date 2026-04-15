package runtime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const defaultTraceRelPath = ".artifacts/lopper-runtime.ndjson"

type CaptureRequest struct {
	RepoPath         string
	TracePath        string
	Command          string
	ReuseIfUnchanged bool
}

type capturePlan struct {
	repoPath  string
	tracePath string
	command   string
}

func DefaultTracePath(repoPath string) string {
	return filepath.Join(repoPath, defaultTraceRelPath)
}

func Capture(ctx context.Context, req CaptureRequest) error {
	plan, err := resolveCapturePlan(req)
	if err != nil {
		return err
	}

	if req.ReuseIfUnchanged {
		reused, err := reuseRuntimeTraceIfPossible(plan.tracePath, plan.command)
		if err == nil && reused {
			return nil
		}
	}

	if err := prepareTracePath(plan.tracePath); err != nil {
		return err
	}

	cmd, err := buildRuntimeCommand(ctx, plan.command)
	if err != nil {
		return err
	}
	cmd.Dir = plan.repoPath
	cmd.Env, err = withRuntimeTraceEnv(os.Environ(), plan.tracePath)
	if err != nil {
		return err
	}

	output, err := cmd.CombinedOutput()
	if err != nil {
		return formatRuntimeCommandError(err, output)
	}
	if err := writeRuntimeTraceState(plan.tracePath, plan.command); err != nil {
		return fmt.Errorf("write runtime trace state: %w", err)
	}

	return nil
}

func resolveCapturePlan(req CaptureRequest) (capturePlan, error) {
	plan := capturePlan{
		repoPath:  strings.TrimSpace(req.RepoPath),
		tracePath: strings.TrimSpace(req.TracePath),
		command:   strings.TrimSpace(req.Command),
	}
	if plan.repoPath == "" {
		return capturePlan{}, fmt.Errorf("repo path is required")
	}
	if plan.command == "" {
		return capturePlan{}, fmt.Errorf("runtime test command is required")
	}
	if plan.tracePath == "" {
		plan.tracePath = DefaultTracePath(plan.repoPath)
	}
	return plan, nil
}

func prepareTracePath(tracePath string) error {
	if err := os.MkdirAll(filepath.Dir(tracePath), 0o750); err != nil {
		return fmt.Errorf("create runtime trace directory: %w", err)
	}
	if err := os.Remove(tracePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove previous runtime trace: %w", err)
	}
	statePath := runtimeTraceStatePath(tracePath)
	if err := os.Remove(statePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove previous runtime trace state: %w", err)
	}
	return nil
}

func formatRuntimeCommandError(runErr error, output []byte) error {
	trimmed := strings.TrimSpace(string(output))
	if trimmed == "" {
		return fmt.Errorf("runtime test command failed: %w", runErr)
	}
	return fmt.Errorf("runtime test command failed: %w: %s", runErr, trimmed)
}
