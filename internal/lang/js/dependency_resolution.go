package js

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ben-ranford/lopper/internal/safeio"
)

type dependencyRootResolutionStatus int

const (
	dependencyRootMissing dependencyRootResolutionStatus = iota
	dependencyRootUnsafe
	dependencyRootFound
)

var (
	absoluteDependencyPath     = filepath.Abs
	evaluateDependencySymlinks = filepath.EvalSymlinks
	relativeDependencyPath     = filepath.Rel
	openDependencyRootNoFollow = safeio.OpenRootNoFollow
)

type dependencyResolutionRequest struct {
	RepoPath     string
	ImporterPath string
	Dependency   string
}

func resolveDependencyRootFromImporter(req dependencyResolutionRequest) string {
	if req.RepoPath == "" || req.ImporterPath == "" || req.Dependency == "" {
		return ""
	}
	return resolveDependencyRootFromDir(req.RepoPath, filepath.Dir(req.ImporterPath), req.Dependency)
}

func resolveDependencyRootFromDir(repoPath, startDir, dependency string) string {
	root, status := resolveDependencyRootFromDirDetailed(repoPath, startDir, dependency)
	if status != dependencyRootFound {
		return ""
	}
	return root
}

func resolveDependencyRootFromDirDetailed(repoPath, startDir, dependency string) (string, dependencyRootResolutionStatus) {
	if repoPath == "" || startDir == "" || dependency == "" {
		return "", dependencyRootMissing
	}
	absRepo, err := absoluteDependencyPath(repoPath)
	if err != nil {
		return "", dependencyRootMissing
	}
	absStart, err := absoluteDependencyPath(startDir)
	if err != nil {
		return "", dependencyRootMissing
	}
	if !isPathWithin(absStart, absRepo) {
		return "", dependencyRootMissing
	}

	sawUnsafe := false
	for {
		root, status := resolveDependencyRootAtDirDetailed(absStart, dependency)
		if status == dependencyRootFound {
			return root, dependencyRootFound
		}
		if status == dependencyRootUnsafe {
			sawUnsafe = true
		}
		if absStart == absRepo {
			break
		}
		parent := filepath.Dir(absStart)
		if parent == absStart {
			break
		}
		absStart = parent
	}
	if sawUnsafe {
		return "", dependencyRootUnsafe
	}
	return "", dependencyRootMissing
}

func resolveDependencyRootsFromDeclarationDirs(repoPath string, dependency string, declarationDirs map[string]struct{}) []string {
	rootsSet := make(map[string]struct{})
	for dir := range declarationDirs {
		if resolved := resolveDependencyRootFromDir(repoPath, dir, dependency); resolved != "" {
			rootsSet[resolved] = struct{}{}
		}
	}

	roots := make([]string, 0, len(rootsSet))
	for root := range rootsSet {
		roots = append(roots, root)
	}
	sort.Strings(roots)
	return roots
}

func resolveDependencyRootsFromScan(repoPath string, dependency string, scanResult ScanResult) []string {
	rootsSet := make(map[string]struct{})
	for _, file := range scanResult.Files {
		for _, imp := range file.Imports {
			if !matchesDependency(imp.Module, dependency) {
				continue
			}
			importerPath := filepath.Join(repoPath, file.Path)
			if resolved := resolveDependencyRootFromImporter(dependencyResolutionRequest{
				RepoPath:     repoPath,
				ImporterPath: importerPath,
				Dependency:   dependency,
			}); resolved != "" {
				rootsSet[resolved] = struct{}{}
			}
		}
	}
	roots := make([]string, 0, len(rootsSet))
	for root := range rootsSet {
		roots = append(roots, root)
	}
	sort.Strings(roots)
	return roots
}

func firstResolvedDependencyRoot(roots []string) string {
	if len(roots) == 0 {
		return ""
	}
	return roots[0]
}

func resolveDependencyRootAtDir(rootDir, dependency string) (string, bool) {
	root, status := resolveDependencyRootAtDirDetailed(rootDir, dependency)
	if status != dependencyRootFound {
		return "", false
	}
	return root, true
}

func resolveDependencyRootAtDirDetailed(rootDir, dependency string) (string, dependencyRootResolutionStatus) {
	root, err := validatedDependencyRootAtDir(rootDir, dependency)
	if err == nil {
		return root, dependencyRootFound
	}
	if dependencyRootErrorIsUnsafe(err) {
		return "", dependencyRootUnsafe
	}
	return "", dependencyRootMissing
}

func dependencyRootErrorIsUnsafe(err error) bool {
	switch {
	case err == nil:
		return false
	case os.IsNotExist(err):
		return false
	case strings.Contains(err.Error(), "symlinked"),
		strings.Contains(err.Error(), "not a directory"),
		strings.Contains(err.Error(), "not a regular file"),
		strings.Contains(err.Error(), "invalid dependency"):
		return true
	default:
		return true
	}
}

func validatedDependencyRootAtDir(rootDir, dependency string) (string, error) {
	if !isSafeDependencyName(dependency) {
		return "", fmt.Errorf(invalidDependencyFormat, dependency)
	}

	rootDirInfo, err := os.Stat(rootDir)
	if err != nil {
		return "", err
	}
	if !rootDirInfo.IsDir() {
		return "", fmt.Errorf("path is not a directory: %s", rootDir)
	}

	dependencyRootPath, err := dependencyRoot(rootDir, dependency)
	if err != nil {
		return "", err
	}
	root, _, err := openValidatedRootNoFollow(dependencyRootPath)
	if err != nil {
		return "", err
	}
	defer func() {
		err = errors.Join(err, root.Close())
	}()
	if err := validateRegularFileWithinRoot(root, dependencyRootPath, jsPackageFile); err != nil {
		return "", err
	}
	return dependencyRootPath, nil
}

func validateDirectoryPathNoFollowFromBase(baseDir string, relParts ...string) (string, error) {
	current, err := canonicalizeNoFollowParentPath(baseDir)
	if err != nil {
		return "", err
	}
	currentRaw, err := absoluteDependencyPath(baseDir)
	if err != nil {
		return "", err
	}
	for _, part := range relParts {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		currentRaw = filepath.Join(currentRaw, part)
		info, err := os.Lstat(current)
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("symlinked path component: %s", currentRaw)
		}
		if !info.IsDir() {
			return "", fmt.Errorf("path is not a directory: %s", currentRaw)
		}
	}
	return currentRaw, nil
}

func validateDirectoryPathNoFollow(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("path is empty")
	}
	absPath, err := absoluteDependencyPath(path)
	if err != nil {
		return "", err
	}
	if basePath, relParts, ok := dependencyChainBase(absPath); ok {
		canonicalBase, err := evaluateDependencySymlinks(basePath)
		if err != nil {
			return "", err
		}
		return validateDirectoryPathNoFollowFromBase(canonicalBase, relParts...)
	}
	canonicalPath, err := canonicalizeNoFollowParentPath(absPath)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(canonicalPath)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("symlinked path component: %s", absPath)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("path is not a directory: %s", absPath)
	}
	return absPath, nil
}

func validateRegularFileNoFollow(path string) error {
	canonicalPath, err := canonicalizeNoFollowParentPath(path)
	if err != nil {
		return err
	}
	info, err := os.Lstat(canonicalPath)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("symlinked file path: %s", path)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("path is not a regular file: %s", path)
	}
	return nil
}

func validateRegularFileWithinRoot(root safeio.Root, rootPath, relPath string) error {
	info, err := root.Lstat(relPath)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("symlinked file path: %s", filepath.Join(rootPath, relPath))
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("path is not a regular file: %s", filepath.Join(rootPath, relPath))
	}
	return nil
}

func canonicalizeNoFollowParentPath(path string) (string, error) {
	absPath, err := absoluteDependencyPath(path)
	if err != nil {
		return "", err
	}
	parent := filepath.Dir(absPath)
	canonicalParent, err := evaluateDependencySymlinks(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(canonicalParent, filepath.Base(absPath)), nil
}

func openValidatedRootNoFollow(path string) (safeio.Root, string, error) {
	validatedPath, err := resolvePinnedRootPath(path)
	if err != nil {
		return nil, "", err
	}
	root, err := openDependencyRootNoFollow(validatedPath)
	if err != nil {
		return nil, "", err
	}
	return root, validatedPath, nil
}

func openConstrainedRoot(path string) (safeio.Root, error) {
	validatedPath, err := resolvePinnedRootPath(path)
	if err != nil {
		return nil, err
	}
	return openDependencyRootNoFollow(validatedPath)
}

func dependencyChainBase(path string) (string, []string, bool) {
	cleanPath := filepath.Clean(path)
	parts, separator := splitDependencyChainPath(cleanPath)
	nodeModulesIndex := -1
	for i, part := range parts {
		if part == "node_modules" {
			nodeModulesIndex = i
		}
	}
	if nodeModulesIndex <= 0 || nodeModulesIndex >= len(parts)-1 {
		return "", nil, false
	}

	basePath := joinPathPrefix(parts[:nodeModulesIndex], separator)
	return basePath, parts[nodeModulesIndex:], true
}

func splitDependencyChainPath(path string) ([]string, string) {
	separator := string(os.PathSeparator)
	if strings.Contains(path, `\`) && !strings.Contains(path, `/`) {
		separator = `\`
	}
	return strings.Split(path, separator), separator
}

func joinPathPrefix(parts []string, separator string) string {
	if len(parts) == 0 {
		return string(os.PathSeparator)
	}
	if len(parts) == 1 && isWindowsVolume(parts[0]) {
		return parts[0] + separator
	}
	basePath := strings.Join(parts, separator)
	if basePath == "" {
		return string(os.PathSeparator)
	}
	return basePath
}

func isWindowsVolume(part string) bool {
	if len(part) != 2 || part[1] != ':' {
		return false
	}
	return (part[0] >= 'A' && part[0] <= 'Z') || (part[0] >= 'a' && part[0] <= 'z')
}

func openRootChildNoFollow(root safeio.Root, name, path string) (safeio.Root, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("symlinked path component: %s", path)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("path is not a directory: %s", path)
	}

	next, err := root.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	openedInfo, err := next.Lstat(".")
	if err != nil {
		if closeErr := next.Close(); closeErr != nil {
			return nil, errors.Join(err, closeErr)
		}
		return nil, err
	}
	if !os.SameFile(info, openedInfo) {
		err = fmt.Errorf("path changed while opening: %s", path)
		if closeErr := next.Close(); closeErr != nil {
			return nil, errors.Join(err, closeErr)
		}
		return nil, err
	}
	return next, nil
}

func resolvePinnedRootPath(path string) (string, error) {
	absPath, err := absoluteDependencyPath(path)
	if err != nil {
		return "", err
	}
	basePath, relParts, ok := dependencyChainBase(absPath)
	if !ok {
		if _, err := validateDirectoryPathNoFollow(absPath); err != nil {
			return "", err
		}
		return canonicalizeNoFollowParentPath(absPath)
	}
	return resolvePinnedDependencyChainPath(basePath, relParts)
}

func resolvePinnedDependencyChainPath(basePath string, relParts []string) (string, error) {
	current, err := resolvePinnedDependencyChainBase(basePath)
	if err != nil {
		return "", err
	}
	allowedRoot := current
	for _, part := range relParts {
		if part == "" || part == "." {
			continue
		}
		nextPath := filepath.Join(current, part)
		nextPath, err = resolvePinnedDependencyComponent(nextPath, allowedRoot)
		if err != nil {
			return "", err
		}
		current = nextPath
	}
	return current, nil
}

func resolvePinnedDependencyChainBase(basePath string) (string, error) {
	baseRoot, baseParts, ok := dependencyChainBase(basePath)
	if !ok {
		if _, err := validateDirectoryPathNoFollow(basePath); err != nil {
			return "", err
		}
		return canonicalizeNoFollowParentPath(basePath)
	}
	current, err := resolvePinnedDependencyChainPath(baseRoot, baseParts)
	if err != nil {
		return "", err
	}
	return current, nil
}

func resolvePinnedDependencyComponent(path, allowedRoot string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return resolvePinnedSymlinkDependencyComponent(path, allowedRoot)
	}
	return requirePinnedDependencyDirectory(path, info)
}

func requirePinnedDependencyDirectory(path string, info os.FileInfo) (string, error) {
	if !info.IsDir() {
		return "", fmt.Errorf("path is not a directory: %s", path)
	}
	return path, nil
}

func resolvePinnedSymlinkDependencyComponent(path, allowedRoot string) (string, error) {
	resolvedPath, err := evaluateDependencySymlinks(path)
	if err != nil {
		return "", err
	}
	if !isPathWithin(resolvedPath, allowedRoot) {
		return "", fmt.Errorf("symlinked path component: %s", path)
	}
	resolvedInfo, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !resolvedInfo.IsDir() {
		return "", fmt.Errorf("path is not a directory: %s", path)
	}
	return resolvedPath, nil
}

func relativePathWithinRoot(rootPath, targetPath string) (string, error) {
	rootAbs, err := canonicalizeNoFollowParentPath(rootPath)
	if err != nil {
		return "", err
	}
	targetAbs, err := canonicalizeNoFollowParentPath(targetPath)
	if err != nil {
		return "", err
	}
	rel, err := relativeDependencyPath(rootAbs, targetAbs)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("path escapes root: %s", targetPath)
	}
	return rel, nil
}

func isPathWithin(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	if rel == ".." {
		return false
	}
	return !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
