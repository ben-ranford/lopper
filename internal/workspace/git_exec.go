package workspace

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/ben-ranford/lopper/internal/gitexec"
)

var resolveGitBinaryPathFn = gitexec.ResolveBinaryPath
var execGitCommandFn = gitexec.Command

func resolveGitBinaryPath() (string, error) {
	return resolveGitBinaryPathFn()
}

func runGit(gitPath, repoPath string, args ...string) ([]byte, error) {
	fullArgs := append([]string{"-C", repoPath}, args...)
	cmd, err := execGitCommandFn(gitPath, fullArgs...)
	if err != nil {
		return nil, err
	}
	cmd.Env = sanitizedGitEnv()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return output, nil
}

func sanitizedGitEnv() []string {
	return gitexec.SanitizedEnv()
}
