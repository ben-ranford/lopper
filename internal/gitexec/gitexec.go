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

func ResolveBinaryPath() (string, error) {
	switch {
	case ExecutableAvailable(ExecutablePrimary):
		return ExecutablePrimary, nil
	case ExecutableAvailable(ExecutableFallback):
		return ExecutableFallback, nil
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
	filtered := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if strings.HasPrefix(entry, "GIT_DIR=") ||
			strings.HasPrefix(entry, "GIT_WORK_TREE=") ||
			strings.HasPrefix(entry, "GIT_INDEX_FILE=") ||
			strings.HasPrefix(entry, "PATH=") {
			continue
		}
		filtered = append(filtered, entry)
	}
	filtered = append(filtered, SafeSystemPath)
	return filtered
}

func ExecutableAvailable(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	return info.Mode()&0o111 != 0
}
