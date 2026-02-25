package workspace

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const safeExecPath = "PATH=/usr/bin:/bin:/usr/sbin:/sbin"

func NormalizeRepoPath(path string) (string, error) {
	if path == "" {
		path = "."
	}
	return filepath.Abs(path)
}

func CurrentCommitSHA(repoPath string) (string, error) {
	normalized, err := NormalizeRepoPath(repoPath)
	if err != nil {
		return "", err
	}
	// #nosec G204 -- arguments are fixed and repoPath is normalized to an absolute directory.
	cmd := exec.Command("git", "-C", normalized, "rev-parse", "--verify", "HEAD")
	cmd.Env = append(nonPathEnv(os.Environ()), safeExecPath)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("resolve git commit sha: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(output)), nil
}

func nonPathEnv(env []string) []string {
	filtered := make([]string, 0, len(env))
	for _, entry := range env {
		if strings.HasPrefix(entry, "PATH=") {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}
