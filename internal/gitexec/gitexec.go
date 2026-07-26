package gitexec

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	goruntime "runtime"
	"strings"
)

// SafeSystemPath restricts Git subprocess PATH lookup to OS-managed locations.
const SafeSystemPath = platformSafeSystemPath

const safeGitNoSystemConfig = "GIT_CONFIG_NOSYSTEM=1"
const safeGitGlobalConfig = "GIT_CONFIG_GLOBAL=/dev/null"

var windowsEnvKeyMatching = goruntime.GOOS == "windows"

// ExecutablePrimary is the preferred conventional Unix Git executable path.
const ExecutablePrimary = "/usr/bin/git"

// ExecutableFallback is the conventional Unix Git executable fallback path.
const ExecutableFallback = "/bin/git"

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

// SafeConfigArgs returns forced Git config overrides that disable executable helpers.
func SafeConfigArgs() []string {
	return configArgs(forcedGitConfigOverrides)
}

// SafeConfigEnvEntries returns forced Git config overrides in environment form.
func SafeConfigEnvEntries() []string {
	return gitConfigEnvEntries(forcedGitConfigOverrides)
}

// ResolveBinaryPath returns the first trusted Git executable available on the host.
func ResolveBinaryPath() (string, error) {
	return resolveBinaryPath(platformExecutableCandidates(), ExecutableAvailable)
}

// TrustedExecutablePaths returns currently available Git executables with trusted provenance.
func TrustedExecutablePaths() []string {
	candidates := platformExecutableCandidates()
	trusted := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if ExecutableAvailable(candidate) {
			trusted = append(trusted, candidate)
		}
	}
	return trusted
}

func resolveBinaryPath(candidates []string, available func(string) bool) (string, error) {
	for _, candidate := range candidates {
		if available(candidate) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("git executable not found in trusted locations")
}

// Command constructs a Git command only for trusted executable paths.
func Command(path string, args ...string) (*exec.Cmd, error) {
	if err := validatePlatformExecutable(path); err != nil {
		return nil, fmt.Errorf("unsupported git executable path %q: %w", path, err)
	}
	return exec.Command(path, args...), nil
}

// CommandContext constructs a context-aware Git command for trusted executable paths.
func CommandContext(ctx context.Context, path string, args ...string) (*exec.Cmd, error) {
	if err := validatePlatformExecutable(path); err != nil {
		return nil, fmt.Errorf("unsupported git executable path %q: %w", path, err)
	}
	return exec.CommandContext(ctx, path, args...), nil
}

// SanitizedEnv returns a hardened environment for Git subprocess execution.
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
	if envKeyHasPrefix(key, "GIT_") {
		return true
	}
	if envKeyHasPrefix(key, "LD_") || envKeyHasPrefix(key, "DYLD_") {
		return true
	}
	for _, sensitiveKey := range []string{"PATH", "HOME", "XDG_CONFIG_HOME", "XDG_CONFIG_DIRS", "PAGER", "EDITOR", "VISUAL"} {
		if envKeyEquals(key, sensitiveKey) {
			return true
		}
	}
	return false
}

func envKeyHasPrefix(key, prefix string) bool {
	if windowsEnvKeyMatching {
		return strings.HasPrefix(strings.ToUpper(key), prefix)
	}
	return strings.HasPrefix(key, prefix)
}

func envKeyEquals(key, expected string) bool {
	if windowsEnvKeyMatching {
		return strings.EqualFold(key, expected)
	}
	return key == expected
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

// ExecutableAvailable reports whether a Git path is executable and has trusted provenance.
func ExecutableAvailable(path string) bool {
	return validatePlatformExecutable(path) == nil
}
