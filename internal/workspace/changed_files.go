package workspace

import (
	"bytes"
	"errors"
	"sort"
	"strconv"
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

func ChangedFilesBetween(repoPath, baseRef, headRef string) ([]string, error) {
	normalized, err := normalizeRepoPath(repoPath)
	if err != nil {
		return nil, err
	}
	gitPath, err := resolveGitBinaryPath()
	if err != nil {
		return nil, err
	}
	diffOutput, err := runGit(gitPath, normalized, "diff", "--no-ext-diff", "--no-textconv", "--name-status", "-z", "--find-renames", "--find-copies", "--diff-filter=ACMRD", strings.TrimSpace(baseRef)+".."+strings.TrimSpace(headRef))
	if err != nil {
		return nil, err
	}
	return parseNameStatusChangedFiles(diffOutput), nil
}

func parseChangedFileLines(output []byte) []string {
	lines := strings.Split(strings.TrimRight(string(output), "\r\n"), "\n")
	return collectUniquePaths(lines, decodeGitQuotedPath)
}

func parseNameStatusChangedFiles(output []byte) []string {
	fields := bytes.Split(output, []byte{0})
	paths := make([]string, 0, len(fields))
	for i := 0; i < len(fields); {
		if len(fields[i]) == 0 {
			i++
			continue
		}
		status := string(fields[i])
		i++
		recordPaths, nextIndex, ok := nextNameStatusPaths(status, fields, i)
		if !ok {
			return collectUniquePaths(paths, func(v string) string { return v })
		}
		paths = append(paths, recordPaths...)
		i = nextIndex
	}
	return collectUniquePaths(paths, func(v string) string { return v })
}

func nextNameStatusPaths(status string, fields [][]byte, index int) ([]string, int, bool) {
	switch status[0] {
	case 'R', 'C':
		if index+1 >= len(fields) {
			return nil, len(fields), false
		}
		return []string{string(fields[index]), string(fields[index+1])}, index + 2, true
	case 'A', 'M', 'D':
		if index >= len(fields) {
			return nil, len(fields), false
		}
		return []string{string(fields[index])}, index + 1, true
	default:
		if index < len(fields) {
			return nil, index + 1, true
		}
		return nil, index, true
	}
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
		status := line[:2]
		// Porcelain format is positional (`XY PATH`); trimming line whitespace corrupts
		// unstaged entries that start with a leading status-space (`" M path"`).
		path := line[3:]
		if strings.ContainsAny(status, "RC") {
			if idx := findPorcelainRenameSeparator(path); idx >= 0 {
				path = path[idx+4:]
			}
		}
		return decodeGitQuotedPath(path)
	})
}

func findPorcelainRenameSeparator(path string) int {
	inQuotes := false
	escaped := false
	for i := 0; i+3 < len(path); i++ {
		if !inQuotes && strings.HasPrefix(path[i:], " -> ") {
			return i
		}
		if escaped {
			escaped = false
			continue
		}
		switch path[i] {
		case '\\':
			if inQuotes {
				escaped = true
			}
		case '"':
			inQuotes = !inQuotes
		}
	}
	return -1
}

func decodeGitQuotedPath(path string) string {
	if len(path) < 2 || path[0] != '"' || path[len(path)-1] != '"' {
		return path
	}

	decoded, err := strconv.Unquote(path)
	if err != nil {
		return path
	}
	return decoded
}
