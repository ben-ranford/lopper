package workspace

import "path/filepath"

func NormalizeRepoPath(path string) (string, error) {
	if path == "" {
		path = "."
	}
	return filepath.Abs(path)
}
