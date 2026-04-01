package workspace

import (
	"fmt"
	"strings"
)

func CurrentCommitSHA(repoPath string) (string, error) {
	normalized, err := NormalizeRepoPath(repoPath)
	if err != nil {
		return "", err
	}
	gitDir, err := resolveGitDir(normalized)
	if err != nil {
		return "", err
	}
	head, err := readGitPath(gitDir, "HEAD")
	if err != nil {
		return "", err
	}
	return currentCommitSHAFromHEAD(gitDir, head)
}

func currentCommitSHAFromHEAD(gitDir, head string) (string, error) {
	head = strings.TrimSpace(head)
	if strings.HasPrefix(head, "ref: ") {
		ref := strings.TrimSpace(strings.TrimPrefix(head, "ref: "))
		return resolveRefSHA(gitDir, ref)
	}
	if validSHA(head) {
		return head, nil
	}
	return "", fmt.Errorf("invalid HEAD value")
}
