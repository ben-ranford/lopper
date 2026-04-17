package python

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/safeio"
	toml "github.com/pelletier/go-toml/v2"
)

func readOptionalTOMLDocument(repoPath, path string) (map[string]any, []string, error) {
	content, err := readOptionalTOMLContent(repoPath, path)
	switch {
	case err == nil:
	case errors.Is(err, os.ErrNotExist):
		return nil, nil, nil
	default:
		return nil, nil, fmt.Errorf("read %s: %w", relativePackagingPath(repoPath, path), err)
	}

	document := make(map[string]any)
	if err := toml.Unmarshal(content, &document); err != nil {
		return make(map[string]any), []string{fmt.Sprintf("%s: skipped TOML parsing after decode error: %v", relativePackagingPath(repoPath, path), err)}, nil
	}
	return document, nil, nil
}

func readOptionalTOMLContent(repoPath, path string) ([]byte, error) {
	return readOptionalFileContent(repoPath, path)
}

func readOptionalJSONContent(repoPath, path string) ([]byte, error) {
	return readOptionalFileContent(repoPath, path)
}

func readOptionalFileContent(repoPath, path string) ([]byte, error) {
	return safeio.ReadFileUnder(repoPath, path)
}

func stringSlice(value any) ([]string, bool) {
	switch typed := value.(type) {
	case []string:
		return typed, true
	case []any:
		items := make([]string, 0, len(typed))
		for _, entry := range typed {
			item, ok := entry.(string)
			if !ok {
				return nil, false
			}
			items = append(items, item)
		}
		return items, true
	default:
		return nil, false
	}
}

func nestedMap(document map[string]any, keys ...string) map[string]any {
	current := document
	for _, key := range keys {
		if len(current) == 0 {
			return nil
		}
		next, ok := current[key].(map[string]any)
		if !ok {
			return nil
		}
		current = next
	}
	return current
}

func isTrue(value any) bool {
	flag, ok := value.(bool)
	return ok && flag
}

func hasFile(files map[string]struct{}, name string) bool {
	_, ok := files[name]
	return ok
}

func addDependencySet(destination, source map[string]struct{}) {
	for dependency := range source {
		destination[dependency] = struct{}{}
	}
}

func addDependencyKeys(dependencies map[string]struct{}, table map[string]any, _ string) {
	for _, key := range sortedMapKeySlice(table) {
		if dependency := normalizeDependencyID(key); dependency != "" {
			dependencies[dependency] = struct{}{}
		}
	}
}

func sortedDependencyUnion(values ...map[string]struct{}) []string {
	set := make(map[string]struct{})
	for _, value := range values {
		for dependency := range value {
			set[dependency] = struct{}{}
		}
	}
	return shared.SortedKeys(set)
}

func sortedMapKeys(values map[string]any) string {
	return strings.Join(sortedMapKeySlice(values), ", ")
}

func sortedMapKeySlice(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func relativePackagingPath(repoPath, path string) string {
	relative, err := filepath.Rel(repoPath, path)
	if err != nil {
		return filepath.Base(path)
	}
	if relative == "." {
		return "."
	}
	return filepath.ToSlash(relative)
}
