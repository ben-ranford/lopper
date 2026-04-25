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
	statusOutput, statusErr := runGit(gitPath, normalized, "status", "--porcelain")

	if statusErr != nil && diffErr != nil {
		return nil, errors.Join(diffErr, statusErr)
	}
	if diffErr != nil {
		statusFiles := parsePorcelainChangedFiles(statusOutput)
		if len(statusFiles) == 0 {
			return nil, diffErr
		}
		return statusFiles, nil
	}
	if statusErr != nil {
		return parseChangedFileLines(diffOutput), nil
	}

	diffFiles := parseChangedFileLines(diffOutput)
	statusFiles := parsePorcelainChangedFiles(statusOutput)
	changed := make([]string, 0, len(diffFiles)+len(statusFiles))
	changed = append(changed, diffFiles...)
	changed = append(changed, statusFiles...)
	return collectUniquePaths(changed, func(v string) string { return v }), nil
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
		// Porcelain format is positional (`XY PATH`); trimming line whitespace corrupts
		// unstaged entries that start with a leading status-space (`" M path"`).
		path := line[3:]
		if idx := strings.LastIndex(path, " -> "); idx >= 0 {
			path = path[idx+4:]
		}
		return path
	})
}
