package workspace

import "path/filepath"

var normalizeRepoPath = NormalizeRepoPath

func NormalizeRepoPath(path string) (string, error) {
	if path == "" {
		path = "."
	}
	return filepath.Abs(path)
}
