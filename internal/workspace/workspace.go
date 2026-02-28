package workspace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

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
	gitDir, err := resolveGitDir(normalized)
	if err != nil {
		return "", err
	}
	head, err := readGitPath(gitDir, "HEAD")
	if err != nil {
		return "", err
	}
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

func resolveGitDir(repoPath string) (string, error) {
	searchDir := filepath.Clean(repoPath)
	var lastErr error

	for {
		gitDir, found, err := inspectGitDir(searchDir)
		if err != nil {
			return "", err
		}
		if found {
			return gitDir, nil
		}
		lastErr = os.ErrNotExist

		parent := filepath.Dir(searchDir)
		if parent == searchDir {
			if lastErr != nil {
				return "", lastErr
			}
			return "", os.ErrNotExist
		}
		searchDir = parent
	}
}

func inspectGitDir(searchDir string) (_ string, _ bool, returnErr error) {
	repoRoot, err := os.OpenRoot(searchDir)
	if err != nil {
		return "", false, err
	}
	defer func() {
		if closeErr := repoRoot.Close(); closeErr != nil {
			if returnErr == nil {
				returnErr = closeErr
				return
			}
			returnErr = errors.Join(returnErr, closeErr)
		}
	}()

	info, err := repoRoot.Stat(".git")
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", false, nil
		}
		return "", false, err
	}
	if info.IsDir() {
		return filepath.Join(searchDir, ".git"), true, nil
	}

	data, err := repoRoot.ReadFile(".git")
	if err != nil {
		return "", false, err
	}
	line := strings.TrimSpace(string(data))
	if !strings.HasPrefix(line, "gitdir:") {
		return "", false, fmt.Errorf("invalid .git file format")
	}
	dirPath := strings.TrimSpace(strings.TrimPrefix(line, "gitdir:"))
	if dirPath == "" {
		return "", false, fmt.Errorf("empty gitdir path")
	}
	if !filepath.IsAbs(dirPath) {
		dirPath = filepath.Clean(filepath.Join(searchDir, dirPath))
	}
	return dirPath, true, nil
}

func resolveRefSHA(gitDir, ref string) (string, error) {
	dirs := candidateGitDirs(gitDir)
	var refErr error

	for _, dir := range dirs {
		refValue, err := readGitPath(dir, ref)
		if err != nil {
			if refErr == nil {
				refErr = err
			}
			continue
		}
		sha := strings.TrimSpace(refValue)
		if validSHA(sha) {
			return sha, nil
		}
	}

	var packedErr error
	for _, dir := range dirs {
		packedRefs, err := readGitPath(dir, "packed-refs")
		if err != nil {
			if packedErr == nil {
				packedErr = err
			}
			continue
		}

		for _, line := range strings.Split(packedRefs, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "^") {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) != 2 || fields[1] != ref {
				continue
			}
			if validSHA(fields[0]) {
				return fields[0], nil
			}
		}
	}

	if refErr != nil {
		return "", refErr
	}
	if packedErr != nil {
		return "", packedErr
	}
	return "", fmt.Errorf("ref %s not found", ref)
}

func candidateGitDirs(gitDir string) []string {
	dirs := []string{gitDir}
	commonDir, err := resolveCommonGitDir(gitDir)
	if err != nil || commonDir == "" || commonDir == gitDir {
		return dirs
	}
	return append(dirs, commonDir)
}

func resolveCommonGitDir(gitDir string) (string, error) {
	value, err := readGitPath(gitDir, "commondir")
	if err != nil {
		return gitDir, nil
	}
	commonDir := strings.TrimSpace(value)
	if commonDir == "" {
		return gitDir, nil
	}
	if filepath.IsAbs(commonDir) {
		return commonDir, nil
	}
	return filepath.Clean(filepath.Join(gitDir, commonDir)), nil
}

func readGitPath(gitDir, name string) (string, error) {
	root, err := os.OpenRoot(gitDir)
	if err != nil {
		return "", err
	}
	defer root.Close()

	data, err := root.ReadFile(filepath.Clean(name))
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func validSHA(value string) bool {
	if len(value) != 40 {
		return false
	}
	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}
