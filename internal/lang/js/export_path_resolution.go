package js

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/ben-ranford/lopper/internal/safeio"
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

func resolveEntrypoint(depPath, entry string) (string, bool, error) {
	return resolveEntrypointUnderRoot(depPath, depPath, entry)
}

func resolveEntrypointUnderRoot(rootPath, depPath, entry string) (resolved string, ok bool, err error) {
	root, err := openConstrainedRoot(rootPath)
	if err != nil {
		return "", false, err
	}
	defer func() {
		err = errors.Join(err, closeRootResetResolution(root, &resolved, &ok, "failed to close dependency root after entrypoint resolution"))
	}()

	resolved, ok = resolveEntrypointWithinRoot(root, rootPath, depPath, entry)
	return resolved, ok, err
}

func resolveEntrypointWithinRoot(root safeio.Root, rootPath, depPath, entry string) (string, bool) {
	entryPath := entry
	if !filepath.IsAbs(entryPath) {
		entryPath = filepath.Join(depPath, entry)
	}

	if info, ok := lstatWithinRoot(root, rootPath, entryPath); ok {
		if info.Mode()&os.ModeSymlink != 0 {
			return "", false
		}
		if info.IsDir() {
			return resolveEntrypointWithinRoot(root, rootPath, depPath, filepath.Join(entry, "index"))
		}
		return entryPath, true
	}

	if filepath.Ext(entryPath) == "" {
		candidates := []string{".js", ".mjs", ".cjs", ".ts", ".tsx", ".d.ts"}
		for _, ext := range candidates {
			candidate := entryPath + ext
			if info, ok := lstatWithinRoot(root, rootPath, candidate); ok && info.Mode()&os.ModeSymlink == 0 && !info.IsDir() {
				return candidate, true
			}
		}
	}

	return "", false
}

func lstatWithinRoot(root safeio.Root, rootPath, targetPath string) (fs.FileInfo, bool) {
	rootAbs, err := filepath.Abs(rootPath)
	if err != nil {
		return nil, false
	}
	targetAbs, err := filepath.Abs(targetPath)
	if err != nil {
		return nil, false
	}
	rel, err := filepath.Rel(rootAbs, targetAbs)
	if err != nil {
		return nil, false
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		return nil, false
	}
	info, err := root.Lstat(rel)
	if err != nil {
		return nil, false
	}
	return info, true
}
