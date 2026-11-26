package shared

import (
	"fmt"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/ben-ranford/lopper/internal/safeio"
)

// ReadYAMLUnderRepoLimit reads and parses a regular YAML file under repoPath.
// maxBytes must be positive and bounds the file content before parsing.
func ReadYAMLUnderRepoLimit[T any](repoPath, path string, maxBytes int64) (T, error) {
	var value T

	content, err := safeio.ReadRegularFileUnderLimit(repoPath, path, maxBytes)
	if err != nil {
		return value, err
	}
	if err := yaml.Unmarshal(content, &value); err != nil {
		return value, fmt.Errorf("parse %s: %w", yamlDisplayPath(repoPath, path), err)
	}
	return value, nil
}

func yamlDisplayPath(repoPath, path string) string {
	if !filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	if rel, err := filepath.Rel(repoPath, path); err == nil {
		cleanRel := filepath.Clean(rel)
		if cleanRel != ".." && !strings.HasPrefix(cleanRel, ".."+string(filepath.Separator)) {
			return cleanRel
		}
	}
	return filepath.Base(path)
}
