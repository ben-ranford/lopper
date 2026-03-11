package shared

import (
	"fmt"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/ben-ranford/lopper/internal/safeio"
)

func ReadYAMLUnderRepo[T any](repoPath, path string) (T, error) {
	var value T

	content, err := safeio.ReadFileUnder(repoPath, path)
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
