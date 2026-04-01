package workspace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func resolveGitDir(repoPath string) (string, error) {
	searchDir := filepath.Clean(repoPath)

	for {
		gitDir, found, err := inspectGitDir(searchDir)
		if err != nil {
			return "", err
		}
		if found {
			return gitDir, nil
		}

		parent := filepath.Dir(searchDir)
		if parent == searchDir {
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
		joinCloseError(&returnErr, repoRoot.Close())
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

func readGitPath(gitDir, name string) (data string, err error) {
	root, err := os.OpenRoot(gitDir)
	if err != nil {
		return "", err
	}
	defer func() {
		joinCloseError(&err, root.Close())
	}()

	bytes, err := root.ReadFile(filepath.Clean(name))
	if err != nil {
		return "", err
	}
	return string(bytes), nil
}

func joinCloseError(target *error, closeErr error) {
	if closeErr == nil {
		return
	}
	if *target == nil {
		*target = closeErr
		return
	}
	*target = errors.Join(*target, closeErr)
}
