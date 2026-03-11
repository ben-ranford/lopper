package shared

import (
	"fmt"

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
		return value, fmt.Errorf("parse %s: %w", path, err)
	}
	return value, nil
}
