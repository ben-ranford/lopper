package gitexec

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

const SafeSystemPath = "PATH=/usr/bin:/bin:/usr/sbin:/sbin"
const ExecutablePrimary = "/usr/bin/git"
const ExecutableFallback = "/bin/git"
const safeGitNoSystemConfig = "GIT_CONFIG_NOSYSTEM=1"
const safeGitGlobalConfig = "GIT_CONFIG_GLOBAL=/dev/null"

type gitConfigOverride struct {
	key   string
	value string
}

var forcedGitConfigOverrides = []gitConfigOverride{
	{key: "core.fsmonitor", value: "false"},
	{key: "diff.external", value: ""},
	{key: "interactive.diffFilter", value: ""},
	{key: "core.pager", value: "cat"},
}

func ResolveBinaryPath() (string, error) {
	return resolveBinaryPath(ExecutablePrimary, ExecutableFallback, ExecutableAvailable)
}

func resolveBinaryPath(primary, fallback string, available func(string) bool) (string, error) {
	switch {
	case available(primary):
		return primary, nil
	case available(fallback):
		return fallback, nil
	default:
		return "", fmt.Errorf("git executable not found")
	}
}

func Command(path string, args ...string) (*exec.Cmd, error) {
	switch path {
	case ExecutablePrimary:
		return exec.Command(ExecutablePrimary, args...), nil
	case ExecutableFallback:
		return exec.Command(ExecutableFallback, args...), nil
	default:
		return nil, fmt.Errorf("unsupported git executable path: %q", path)
	}
}

func CommandContext(ctx context.Context, path string, args ...string) (*exec.Cmd, error) {
	switch path {
	case ExecutablePrimary:
		return exec.CommandContext(ctx, ExecutablePrimary, args...), nil
	case ExecutableFallback:
		return exec.CommandContext(ctx, ExecutableFallback, args...), nil
	default:
		return nil, fmt.Errorf("unsupported git executable path: %q", path)
	}
}

func SanitizedEnv() []string {
	env := os.Environ()
	filtered := make([]string, 0, len(env)+2+1+len(forcedGitConfigOverrides)*2)
	for _, entry := range env {
		key, _, hasKey := strings.Cut(entry, "=")
		if !hasKey {
			filtered = append(filtered, entry)
			continue
		}
		if shouldStripGitEnvKey(key) {
			continue
		}
		filtered = append(filtered, entry)
	}
	filtered = append(filtered, SafeSystemPath, safeGitNoSystemConfig, safeGitGlobalConfig)
	filtered = append(filtered, safeGitConfigEnvEntries()...)
	return filtered
}

func shouldStripGitEnvKey(key string) bool {
	if strings.HasPrefix(key, "GIT_") {
		return true
	}
	switch key {
	case "PATH", "HOME", "XDG_CONFIG_HOME", "XDG_CONFIG_DIRS", "PAGER", "EDITOR", "VISUAL":
		return true
	default:
		return false
	}
}

func safeGitConfigEnvEntries() []string {
	entries := make([]string, 0, 1+len(forcedGitConfigOverrides)*2)
	entries = append(entries, fmt.Sprintf("GIT_CONFIG_COUNT=%d", len(forcedGitConfigOverrides)))
	for index, override := range forcedGitConfigOverrides {
		entries = append(
			entries,
			fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", index, override.key),
			fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", index, override.value),
		)
	}
	return entries
}

func ExecutableAvailable(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	return info.Mode()&0o111 != 0
}
