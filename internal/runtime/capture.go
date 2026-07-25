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
	RepoPath             string
	TracePath            string
	Command              string
	Provider             CaptureProvider
	ReuseIfUnchanged     bool
	TrustedInputDigest   string
	PythonRunnerProfiles bool
}

// ValidatedTraceSnapshot contains the exact bounded bytes validated during capture.
type ValidatedTraceSnapshot struct {
	data []byte
}

type CaptureProvider string

const (
	CaptureProviderNode   CaptureProvider = "node"
	CaptureProviderPython CaptureProvider = "python"
)

type capturePlan struct {
	repoPath             string
	tracePath            string
	command              string
	provider             CaptureProvider
	pythonRunnerProfiles bool
}

func DefaultTracePath(repoPath string) string {
	return filepath.Join(repoPath, defaultTraceRelPath)
}

func Capture(ctx context.Context, req CaptureRequest) error {
	_, err := CaptureValidatedTrace(ctx, req)
	return err
}

// CaptureValidatedTrace captures or reuses a trace and returns its validated bytes.
func CaptureValidatedTrace(ctx context.Context, req CaptureRequest) (*ValidatedTraceSnapshot, error) {
	plan, err := resolveCapturePlan(req)
	if err != nil {
		return nil, err
	}
	commandOptions := CommandOptions{PythonRunnerProfiles: plan.pythonRunnerProfiles}
	if err := ValidateCommand(plan.command, commandOptions); err != nil {
		return nil, err
	}

	if req.ReuseIfUnchanged {
		snapshot, reused, err := reusableRuntimeTraceSnapshot(plan.tracePath, plan.command, plan.provider, req.TrustedInputDigest)
		if err == nil && reused {
			return &ValidatedTraceSnapshot{data: snapshot.data}, nil
		}
	}

	if err := prepareTracePath(plan.tracePath); err != nil {
		return nil, err
	}

	cmd, err := buildRuntimeCommand(ctx, plan.command, commandOptions)
	if err != nil {
		return nil, err
	}
	cmd.Dir = plan.repoPath
	cmd.Env, err = withRuntimeTraceEnv(os.Environ(), plan.tracePath, plan.provider)
	if err != nil {
		return nil, err
	}

	output := newRuntimeCommandOutput()
	cmd.Stdout = output
	cmd.Stderr = output
	err = cmd.Run()
	if err != nil {
		return nil, formatRuntimeCommandError(err, output.diagnostic())
	}
	snapshot, err := writeRuntimeTraceStateAndSnapshot(plan.tracePath, plan.command, plan.provider, req.TrustedInputDigest)
	if err != nil {
		return nil, fmt.Errorf("write runtime trace state: %w", err)
	}
	if snapshot == nil {
		return nil, nil
	}
	return &ValidatedTraceSnapshot{data: snapshot.data}, nil
}

func resolveCapturePlan(req CaptureRequest) (capturePlan, error) {
	plan := capturePlan{
		repoPath:             strings.TrimSpace(req.RepoPath),
		tracePath:            strings.TrimSpace(req.TracePath),
		command:              strings.TrimSpace(req.Command),
		provider:             normalizeCaptureProvider(req.Provider),
		pythonRunnerProfiles: req.PythonRunnerProfiles,
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
	if plan.provider == "" {
		return capturePlan{}, fmt.Errorf("unsupported runtime capture provider %q", req.Provider)
	}
	return plan, nil
}

func normalizeCaptureProvider(provider CaptureProvider) CaptureProvider {
	switch provider {
	case "", CaptureProviderNode:
		return CaptureProviderNode
	case CaptureProviderPython:
		return CaptureProviderPython
	default:
		return ""
	}
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
