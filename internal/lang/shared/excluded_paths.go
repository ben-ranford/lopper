package shared

import (
	"path/filepath"
	"strings"
)

// ExcludedPathsForRepo resolves the analysis cache's directory/file
// exclusions into an absolute-path set scoped to repoPath, so cache-excluded
// inputs (for example the default runtime trace directory) do not affect
// fresh scans.
func ExcludedPathsForRepo(repoPath string, directories, files []string) map[string]struct{} {
	if len(directories) == 0 && len(files) == 0 {
		return nil
	}
	repoPath = filepath.Clean(repoPath)
	paths := append(append([]string(nil), directories...), files...)
	excludedPaths := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		if strings.TrimSpace(path) == "" {
			continue
		}
		cleanedPath := filepath.Clean(path)
		relativePath, err := filepath.Rel(repoPath, cleanedPath)
		if err != nil || relativePath == "." || relativePath == ".." || strings.HasPrefix(relativePath, ".."+string(filepath.Separator)) {
			continue
		}
		excludedPaths[cleanedPath] = struct{}{}
	}
	return excludedPaths
}

// IsExcludedPath reports whether path is present in the exclusion set
// produced by ExcludedPathsForRepo.
func IsExcludedPath(paths map[string]struct{}, path string) bool {
	_, excluded := paths[filepath.Clean(path)]
	return excluded
}
