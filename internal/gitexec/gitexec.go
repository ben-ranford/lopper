package gitexec

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
)

const SafeSystemPath = "PATH=/usr/bin:/bin:/usr/sbin:/sbin"
const safeGitNoSystemConfig = "GIT_CONFIG_NOSYSTEM=1"
const safeGitGlobalConfig = "GIT_CONFIG_GLOBAL=/dev/null"
const ExecutablePrimary = "/usr/bin/git"
const ExecutableFallback = "/bin/git"

var trustedExecutablePaths = []string{
	ExecutablePrimary,
	ExecutableFallback,
	"/usr/local/bin/git",
	"/opt/homebrew/bin/git",
}

type gitConfigOverride struct {
	key   string
	value string
}

var forcedGitConfigOverrides = []gitConfigOverride{
	{key: "core.fsmonitor", value: "false"},
	{key: "core.quotePath", value: "false"},
	{key: "diff.external", value: ""},
	{key: "interactive.diffFilter", value: ""},
	// Keep git commit from detaching background maintenance into temp repos.
	{key: "maintenance.auto", value: "false"},
	{key: "core.pager", value: "cat"},
}

func SafeConfigArgs() []string {
	return configArgs(forcedGitConfigOverrides)
}

func SafeConfigEnvEntries() []string {
	return gitConfigEnvEntries(forcedGitConfigOverrides)
}

func ResolveBinaryPath() (string, error) {
	return resolveBinaryPath(trustedExecutablePaths, ExecutableAvailable)
}

func TrustedExecutablePaths() []string {
	return slices.Clone(trustedExecutablePaths)
}

func resolveBinaryPath(candidates []string, available func(string) bool) (string, error) {
	for _, candidate := range candidates {
		if available(candidate) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("git executable not found in trusted locations")
}

func isTrustedBinaryPath(path string) bool {
	return slices.Contains(trustedExecutablePaths, path)
}

func Command(path string, args ...string) (*exec.Cmd, error) {
	if !isTrustedBinaryPath(path) {
		return nil, fmt.Errorf("unsupported git executable path: %q", path)
	}
	return exec.Command(path, args...), nil
}

func CommandContext(ctx context.Context, path string, args ...string) (*exec.Cmd, error) {
	if !isTrustedBinaryPath(path) {
		return nil, fmt.Errorf("unsupported git executable path: %q", path)
	}
	return exec.CommandContext(ctx, path, args...), nil
}

func SanitizedEnv() []string {
	return sanitizedEnvEntries(os.Environ())
}

func sanitizedEnvEntries(env []string) []string {
	filtered := make([]string, 0, len(env)+3+1+len(forcedGitConfigOverrides)*2)
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
	if strings.HasPrefix(key, "LD_") || strings.HasPrefix(key, "DYLD_") {
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
	return SafeConfigEnvEntries()
}

func configArgs(overrides []gitConfigOverride) []string {
	args := make([]string, 0, len(overrides)*2)
	for _, override := range overrides {
		args = append(args, "-c", fmt.Sprintf("%s=%s", override.key, override.value))
	}
	return args
}

func gitConfigEnvEntries(overrides []gitConfigOverride) []string {
	entries := make([]string, 0, 1+len(overrides)*2)
	entries = append(entries, fmt.Sprintf("GIT_CONFIG_COUNT=%d", len(overrides)))
	for index, override := range overrides {
		entries = append(entries, fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", index, override.key), fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", index, override.value))
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
