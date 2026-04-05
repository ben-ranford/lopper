package workspace

import (
	"errors"
	"sort"
	"strings"
)

func ChangedFiles(repoPath string) ([]string, error) {
	normalized, err := normalizeRepoPath(repoPath)
	if err != nil {
		return nil, err
	}
	gitPath, err := resolveGitBinaryPath()
	if err != nil {
		return nil, err
	}
	diffOutput, diffErr := runGit(gitPath, normalized, "diff", "--no-ext-diff", "--no-textconv", "--name-only", "--diff-filter=ACMRD", "HEAD~1..HEAD")
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
	lines := strings.Split(strings.TrimRight(string(output), "\r\n"), "\n")
	return collectUniquePaths(lines, func(line string) string { return line })
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
	lines := strings.Split(strings.TrimRight(string(output), "\r\n"), "\n")
	return collectUniquePaths(lines, func(line string) string {
		if len(line) < 4 {
			return ""
		}
		path := line[3:]
		if idx := strings.LastIndex(path, " -> "); idx >= 0 {
			path = path[idx+4:]
		}
		return path
	})
}
