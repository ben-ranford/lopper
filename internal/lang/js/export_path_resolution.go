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

var pinnedPathStatReadyFn = func() error { return nil }

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
		collectExportMapPaths(typed, dest, surface)
	}
}

func collectExportMapPaths(entries map[string]any, dest map[string]struct{}, surface *ExportSurface) {
	for key, item := range entries {
		if shouldSkipExportConditionPath(surface, key, item) {
			continue
		}
		collectExportPaths(item, dest, surface)
	}
}

func shouldSkipExportConditionPath(surface *ExportSurface, key string, item any) bool {
	if surface == nil || !looksLikeConditionKey(key) {
		return false
	}
	path, ok := item.(string)
	if !ok || isLikelyCodeAsset(path) {
		return false
	}
	surface.Warnings = append(surface.Warnings, fmt.Sprintf("skipping non-js export condition %q: %s", key, path))
	return true
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

	if resolvedPath, info, ok := statWithinRoot(root, rootPath, entryPath); ok {
		if info.IsDir() {
			return resolveEntrypointWithinRoot(root, rootPath, depPath, filepath.Join(entry, "index"))
		}
		return resolvedPath, true
	}

	if filepath.Ext(entryPath) == "" {
		candidates := []string{".js", ".mjs", ".cjs", ".ts", ".tsx", ".d.ts"}
		for _, ext := range candidates {
			candidate := entryPath + ext
			if resolvedPath, info, ok := statWithinRoot(root, rootPath, candidate); ok && !info.IsDir() {
				return resolvedPath, true
			}
		}
	}

	return "", false
}

func lstatWithinRoot(root safeio.Root, rootPath, targetPath string) (fs.FileInfo, bool) {
	_, info, ok := statWithinRoot(root, rootPath, targetPath)
	return info, ok
}

func statWithinRoot(root safeio.Root, rootPath, targetPath string) (string, fs.FileInfo, bool) {
	validatedRoot, err := resolvePinnedRootPathWithinBoundary(rootPath, "")
	if err != nil {
		return "", nil, false
	}
	resolvedPath, err := resolvePinnedPathWithinBoundary(targetPath, validatedRoot)
	if err != nil {
		return "", nil, false
	}
	if err := pinnedPathStatReadyFn(); err != nil {
		return "", nil, false
	}
	info, err := lstatPinnedPathWithinRoot(root, validatedRoot, resolvedPath)
	if err != nil {
		return "", nil, false
	}
	return resolvedPath, info, true
}

func lstatPinnedPathWithinRoot(root safeio.Root, rootPath, targetPath string) (_ fs.FileInfo, err error) {
	targetRel, err := relativePathWithinRoot(rootPath, targetPath)
	if err != nil {
		return nil, err
	}

	parent, closeParent, err := openPathParentWithinRoot(root, rootPath, filepath.Dir(targetRel))
	if err != nil {
		return nil, err
	}
	if closeParent {
		defer func() {
			if closeErr := parent.Close(); closeErr != nil {
				err = errors.Join(err, closeErr)
			}
		}()
	}

	info, err := parent.Lstat(filepath.Base(targetRel))
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("symlinked path component: %s", targetPath)
	}
	return info, nil
}

func openPathParentWithinRoot(root safeio.Root, rootPath, parentRel string) (safeio.Root, bool, error) {
	if parentRel == "." {
		return root, false, nil
	}

	current := root
	currentOwned := false
	currentPath := rootPath
	for _, part := range strings.Split(parentRel, string(os.PathSeparator)) {
		if part == "" || part == "." {
			continue
		}
		nextPath := filepath.Join(currentPath, part)
		next, err := openRootChildNoFollow(current, part, nextPath)
		if err != nil {
			return nil, false, closeJSOwnedRoot(current, currentOwned, err)
		}
		if currentOwned {
			if err := current.Close(); err != nil {
				return nil, false, closeRootWithErr(next, err)
			}
		}
		current = next
		currentOwned = true
		currentPath = nextPath
	}
	return current, currentOwned, nil
}

func closeJSOwnedRoot(root safeio.Root, owned bool, err error) error {
	if !owned {
		return err
	}
	return closeRootWithErr(root, err)
}

func closeRootWithErr(root safeio.Root, err error) error {
	if closeErr := root.Close(); closeErr != nil {
		return errors.Join(err, closeErr)
	}
	return err
}

func resolvePinnedPathWithinBoundary(targetPath, allowedRoot string) (string, error) {
	if !filepath.IsAbs(targetPath) {
		targetPath = filepath.Join(allowedRoot, targetPath)
	}

	targetAbs, err := absoluteDependencyPath(targetPath)
	if err != nil {
		return "", err
	}
	validatedParent, err := resolvePinnedRootPath(filepath.Dir(targetAbs))
	if err != nil {
		return "", err
	}
	if err := enforceDependencyPathWithinRoot(validatedParent, allowedRoot); err != nil {
		return "", err
	}

	pinnedPath := filepath.Join(validatedParent, filepath.Base(targetAbs))
	info, err := os.Lstat(pinnedPath)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		if !isPathWithin(pinnedPath, allowedRoot) {
			return "", fmt.Errorf("path escapes root: %s", targetAbs)
		}
		return pinnedPath, nil
	}

	resolvedPath, err := evaluateDependencySymlinks(pinnedPath)
	if err != nil {
		return "", err
	}
	if !isPathWithin(resolvedPath, allowedRoot) {
		return "", fmt.Errorf("symlinked path component: %s", targetAbs)
	}
	return resolvedPath, nil
}
