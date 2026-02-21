package runtime

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const defaultTraceRelPath = ".artifacts/lopper-runtime.ndjson"

type CaptureRequest struct {
	RepoPath  string
	TracePath string
	Command   string
}

func DefaultTracePath(repoPath string) string {
	return filepath.Join(repoPath, defaultTraceRelPath)
}

func Capture(ctx context.Context, req CaptureRequest) error {
	repoPath := strings.TrimSpace(req.RepoPath)
	tracePath := strings.TrimSpace(req.TracePath)
	command := strings.TrimSpace(req.Command)
	if repoPath == "" {
		return fmt.Errorf("repo path is required")
	}
	if command == "" {
		return fmt.Errorf("runtime test command is required")
	}
	if tracePath == "" {
		tracePath = DefaultTracePath(repoPath)
	}

	if err := os.MkdirAll(filepath.Dir(tracePath), 0o750); err != nil {
		return fmt.Errorf("create runtime trace directory: %w", err)
	}
	if err := os.Remove(tracePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove previous runtime trace: %w", err)
	}

	cmd, err := buildRuntimeCommand(ctx, command)
	if err != nil {
		return err
	}
	cmd.Dir = repoPath
	cmd.Env = withRuntimeTraceEnv(os.Environ(), tracePath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		trimmed := strings.TrimSpace(string(output))
		if trimmed == "" {
			return fmt.Errorf("runtime test command failed: %w", err)
		}
		return fmt.Errorf("runtime test command failed: %w: %s", err, trimmed)
	}

	return nil
}

func buildRuntimeCommand(ctx context.Context, command string) (*exec.Cmd, error) {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return nil, fmt.Errorf("runtime test command is required")
	}

	executable := fields[0]
	args := fields[1:]
	allowedExecutables := map[string]struct{}{
		"npm":    {},
		"pnpm":   {},
		"yarn":   {},
		"bun":    {},
		"npx":    {},
		"node":   {},
		"vitest": {},
		"jest":   {},
		"mocha":  {},
		"ava":    {},
		"deno":   {},
		"make":   {},
	}
	if _, ok := allowedExecutables[executable]; !ok {
		return nil, fmt.Errorf("unsupported runtime test executable %q; use a direct command like 'npm test'", executable)
	}
	// #nosec G204 -- executable is allowlisted and this path is only reached via explicit user opt-in flag.
	return exec.CommandContext(ctx, executable, args...), nil
}

func withRuntimeTraceEnv(base []string, tracePath string) []string {
	existing := readEnvValue(base, "NODE_OPTIONS")
	updates := map[string]string{
		"LOPPER_RUNTIME_TRACE": tracePath,
	}
	nodeOptions := strings.TrimSpace(existing)
	required := "--require=./scripts/runtime/require-hook.cjs --loader=./scripts/runtime/loader.mjs"
	if nodeOptions == "" {
		updates["NODE_OPTIONS"] = required
	} else {
		updates["NODE_OPTIONS"] = nodeOptions + " " + required
	}
	return mergeEnv(base, updates)
}

func mergeEnv(base []string, updates map[string]string) []string {
	merged := make(map[string]string, len(base)+len(updates))
	for _, item := range base {
		parts := strings.SplitN(item, "=", 2)
		if len(parts) != 2 {
			continue
		}
		merged[parts[0]] = parts[1]
	}
	for key, value := range updates {
		merged[key] = value
	}
	items := make([]string, 0, len(merged))
	for key, value := range merged {
		items = append(items, key+"="+value)
	}
	return items
}

func readEnvValue(env []string, key string) string {
	for _, item := range env {
		parts := strings.SplitN(item, "=", 2)
		if len(parts) != 2 {
			continue
		}
		if parts[0] == key {
			return parts[1]
		}
	}
	return ""
}
