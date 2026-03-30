package js

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func dependencyRoot(repoPath, dependency string) (string, error) {
	if repoPath == "" {
		return "", errors.New("repo path is empty")
	}
	if dependency == "" {
		return "", errors.New("dependency is empty")
	}

	if err := validateDependencyName(dependency); err != nil {
		return "", err
	}

	if strings.HasPrefix(dependency, "@") {
		parts := strings.SplitN(dependency, "/", 2)
		return filepath.Join(repoPath, "node_modules", parts[0], parts[1]), nil
	}
	return filepath.Join(repoPath, "node_modules", dependency), nil
}

func validateDependencyName(dependency string) error {
	if strings.Contains(dependency, `\`) {
		return fmt.Errorf(invalidDependencyFormat, dependency)
	}
	if strings.HasPrefix(dependency, "@") {
		parts := strings.Split(dependency, "/")
		if len(parts) != 2 || !isValidDependencySegment(parts[0]) || !isValidDependencySegment(parts[1]) {
			return fmt.Errorf("invalid scoped dependency: %s", dependency)
		}
		return nil
	}
	if strings.Contains(dependency, "/") {
		return fmt.Errorf(invalidDependencyFormat, dependency)
	}
	if !isValidDependencySegment(dependency) {
		return fmt.Errorf(invalidDependencyFormat, dependency)
	}
	return nil
}

func isValidDependencySegment(segment string) bool {
	return segment != "" && segment != "." && segment != ".."
}

func collectExportPaths(value any, dest map[string]struct{}, surface *ExportSurface) {
	switch typed := value.(type) {
	case string:
		addEntrypoint(dest, typed)
	case []any:
		for _, item := range typed {
			collectExportPaths(item, dest, surface)
		}
	case map[string]any:
		for key, item := range typed {
			if surface != nil && looksLikeConditionKey(key) {
				if path, ok := item.(string); ok && !isLikelyCodeAsset(path) {
					surface.Warnings = append(surface.Warnings, fmt.Sprintf("skipping non-js export condition %q: %s", key, path))
					continue
				}
			}
			collectExportPaths(item, dest, surface)
		}
	}
}

func addEntrypoint(dest map[string]struct{}, entry string) {
	entry = strings.TrimSpace(entry)
	if entry == "" {
		return
	}
	dest[entry] = struct{}{}
}

func looksLikeConditionKey(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "browser", "node", "default", "import", "require", "development", "production", "types":
		return true
	default:
		return false
	}
}

func isLikelyCodeAsset(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".js", ".mjs", ".cjs", ".ts", ".tsx", ".cts", ".mts", ".d.ts":
		return true
	default:
		return false
	}
}

func resolveEntrypoint(root, entry string) (string, bool) {
	path := entry
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, entry)
	}

	if info, err := os.Stat(path); err == nil {
		if info.IsDir() {
			return resolveEntrypoint(root, filepath.Join(entry, "index"))
		}
		return path, true
	}

	if filepath.Ext(path) == "" {
		candidates := []string{".js", ".mjs", ".cjs", ".ts", ".tsx", ".d.ts"}
		for _, ext := range candidates {
			candidate := path + ext
			if _, err := os.Stat(candidate); err == nil {
				return candidate, true
			}
		}
	}

	return "", false
}
