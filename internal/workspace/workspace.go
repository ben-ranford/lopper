package workspace

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

const safeSystemPath = "PATH=/usr/bin:/bin:/usr/sbin:/sbin"
const gitExecutablePrimary = "/usr/bin/git"
const gitExecutableFallback = "/bin/git"

var gitExecutableAvailableFn = gitExecutableAvailable

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

func ChangedFiles(repoPath string) ([]string, error) {
	normalized, err := NormalizeRepoPath(repoPath)
	if err != nil {
		return nil, err
	}
	gitPath, err := resolveGitBinaryPath()
	if err != nil {
		return nil, err
	}
	diffOutput, diffErr := runGit(gitPath, normalized, "diff", "--name-only", "--diff-filter=ACMR", "HEAD~1..HEAD")
	if diffErr == nil {
		return parseChangedFileLines(diffOutput), nil
	}
	statusOutput, statusErr := runGit(gitPath, normalized, "status", "--porcelain")
	if statusErr != nil {
		return nil, errors.Join(diffErr, statusErr)
	}
	return parsePorcelainChangedFiles(statusOutput), nil
}

func parseChangedFileLines(output []byte) []string {
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	return collectUniquePaths(lines, func(line string) string {
		return strings.TrimSpace(line)
	})
}

func collectUniquePaths(lines []string, extractor func(string) string) []string {
	paths := make([]string, 0, len(lines))
	seen := make(map[string]struct{}, len(lines))
	for _, line := range lines {
		path := extractor(line)
		if path == "" {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

func parsePorcelainChangedFiles(output []byte) []string {
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	return collectUniquePaths(lines, func(line string) string {
		line = strings.TrimSpace(line)
		if len(line) < 4 {
			return ""
		}
		path := strings.TrimSpace(line[3:])
		if idx := strings.LastIndex(path, " -> "); idx >= 0 {
			path = strings.TrimSpace(path[idx+4:])
		}
		return path
	})
}

func resolveGitBinaryPath() (string, error) {
	switch {
	case gitExecutableAvailableFn(gitExecutablePrimary):
		return gitExecutablePrimary, nil
	case gitExecutableAvailableFn(gitExecutableFallback):
		return gitExecutableFallback, nil
	default:
		return "", fmt.Errorf("git executable not found")
	}
}

func runGit(gitPath, repoPath string, args ...string) ([]byte, error) {
	fullArgs := append([]string{"-C", repoPath}, args...)
	// #nosec G204 -- arguments are fixed and repoPath is normalized to an absolute directory.
	cmd := exec.Command(gitPath, fullArgs...)
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
	filtered = append(filtered, safeSystemPath)
	return filtered
}

func gitExecutableAvailable(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	return info.Mode()&0o111 != 0
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
	if sha, refErr := resolveLooseRefSHA(dirs, ref); sha != "" {
		return sha, nil
	} else if sha, packedErr := resolvePackedRefSHA(dirs, ref); sha != "" {
		return sha, nil
	} else {
		return "", resolveRefLookupError(ref, refErr, packedErr)
	}
}

func resolveLooseRefSHA(dirs []string, ref string) (string, error) {
	var firstErr error
	for _, dir := range dirs {
		refValue, err := readGitPath(dir, ref)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		sha := strings.TrimSpace(refValue)
		if validSHA(sha) {
			return sha, nil
		}
	}
	return "", firstErr
}

func resolvePackedRefSHA(dirs []string, ref string) (string, error) {
	var firstErr error
	for _, dir := range dirs {
		packedRefs, err := readGitPath(dir, "packed-refs")
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if sha := findPackedRefSHA(packedRefs, ref); sha != "" {
			return sha, nil
		}
	}
	return "", firstErr
}

func findPackedRefSHA(packedRefs, ref string) string {
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
			return fields[0]
		}
	}
	return ""
}

func resolveRefLookupError(ref string, refErr, packedErr error) error {
	if refErr != nil {
		return refErr
	}
	if packedErr != nil {
		return packedErr
	}
	return fmt.Errorf("ref %s not found", ref)
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
