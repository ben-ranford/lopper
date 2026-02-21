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

var runtimeExecutableAllowlist = map[string]struct{}{
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
	executablePath, err := resolveRuntimeExecutablePath(executable, os.Getenv("PATH"))
	if err != nil {
		return nil, err
	}

	cmd, err := newAllowlistedRuntimeCommand(ctx, executable)
	if err != nil {
		return nil, err
	}
	cmd.Path = executablePath
	cmd.Args = append([]string{executablePath}, args...)
	return cmd, nil
}

func resolveRuntimeExecutablePath(executable string, pathValue string) (string, error) {
	if _, ok := runtimeExecutableAllowlist[executable]; !ok {
		return "", fmt.Errorf("unsupported runtime test executable %q; use a direct command like 'npm test'", executable)
	}

	for _, dir := range trustedPathDirs(pathValue) {
		candidate := filepath.Join(dir, executable)
		info, err := os.Stat(candidate)
		if err != nil || info.IsDir() {
			continue
		}
		if info.Mode().Perm()&0o111 == 0 {
			continue
		}
		return candidate, nil
	}

	return "", fmt.Errorf("runtime test executable %q not found in trusted PATH directories", executable)
}

func newAllowlistedRuntimeCommand(ctx context.Context, executable string) (*exec.Cmd, error) {
	_, ok := runtimeExecutableAllowlist[executable]
	if !ok {
		return nil, fmt.Errorf("unsupported runtime test executable %q; use a direct command like 'npm test'", executable)
	}
	_ = ctx
	return &exec.Cmd{}, nil
}

func trustedPathDirs(pathValue string) []string {
	seen := make(map[string]struct{})
	dirs := make([]string, 0)
	for _, raw := range filepath.SplitList(pathValue) {
		dir := filepath.Clean(strings.TrimSpace(raw))
		if dir == "" || !filepath.IsAbs(dir) {
			continue
		}
		if _, ok := seen[dir]; ok {
			continue
		}

		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			continue
		}
		// Reject group/other-writable PATH entries.
		if info.Mode().Perm()&0o022 != 0 {
			continue
		}

		seen[dir] = struct{}{}
		dirs = append(dirs, dir)
	}
	return dirs
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
