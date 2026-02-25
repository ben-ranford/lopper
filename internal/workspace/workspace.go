package workspace

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

var fixedGitPaths = []string{
	"/usr/bin/git",
	"/bin/git",
}

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
	gitPath, err := resolveGitBinaryPath()
	if err != nil {
		return "", err
	}
	// #nosec G204 -- arguments are fixed and repoPath is normalized to an absolute directory.
	cmd := exec.Command(gitPath, "-C", normalized, "rev-parse", "--verify", "HEAD")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("resolve git commit sha: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(output)), nil
}

func resolveGitBinaryPath() (string, error) {
	for _, candidate := range fixedGitPaths {
		info, err := os.Stat(candidate)
		if err != nil {
			continue
		}
		if info.IsDir() {
			continue
		}
		if info.Mode()&0o111 == 0 {
			continue
		}
		return candidate, nil
	}
	return "", fmt.Errorf("git binary not found in fixed system paths")
}
