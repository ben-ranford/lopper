package js

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ben-ranford/lopper/internal/pathutil"
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
	dependencyRootOpenReadyFn  = func() error { return nil }
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
	walk, ok := newDependencyRootWalk(repoPath, startDir, dependency)
	if !ok {
		return "", dependencyRootMissing
	}
	for {
		if root, status, found := walk.probe(); found {
			return root, status
		}
		if !walk.advance() {
			break
		}
	}
	if walk.sawUnsafe {
		return "", dependencyRootUnsafe
	}
	return "", dependencyRootMissing
}

type dependencyRootWalk struct {
	dependency     string
	rawRepo        string
	rawStart       string
	canonicalRepo  string
	canonicalStart string
	sawUnsafe      bool
	probed         map[string]struct{}
}

func newDependencyRootWalk(repoPath, startDir, dependency string) (dependencyRootWalk, bool) {
	if repoPath == "" || startDir == "" || dependency == "" {
		return dependencyRootWalk{}, false
	}
	rawRepo, err := absoluteDependencyPath(repoPath)
	if err != nil {
		return dependencyRootWalk{}, false
	}
	rawStart, err := absoluteDependencyPath(startDir)
	if err != nil {
		return dependencyRootWalk{}, false
	}
	canonicalRepo, err := resolvePinnedDependencyChainBase(repoPath)
	if err != nil {
		return dependencyRootWalk{}, false
	}
	canonicalStart, err := canonicalizeDependencyWalkStart(rawStart)
	if err != nil {
		return dependencyRootWalk{}, false
	}
	rawRepo, ok := normalizeDependencyWalkRepo(rawRepo, rawStart, canonicalRepo, canonicalStart)
	if !ok {
		return dependencyRootWalk{}, false
	}
	return dependencyRootWalk{
		dependency:     dependency,
		rawRepo:        rawRepo,
		rawStart:       rawStart,
		canonicalRepo:  canonicalRepo,
		canonicalStart: canonicalStart,
		probed:         make(map[string]struct{}, 8),
	}, true
}

func normalizeDependencyWalkRepo(rawRepo, rawStart, canonicalRepo, canonicalStart string) (string, bool) {
	if !isPathWithin(canonicalStart, canonicalRepo) {
		return "", false
	}
	if isPathWithin(rawStart, rawRepo) {
		return rawRepo, true
	}
	if !isPathWithin(rawStart, canonicalRepo) {
		return "", false
	}
	return canonicalRepo, true
}

func (w *dependencyRootWalk) probe() (string, dependencyRootResolutionStatus, bool) {
	for _, candidate := range []string{w.rawStart, w.canonicalStart} {
		root, status, found := w.probeCandidate(candidate)
		if found {
			return root, status, true
		}
	}
	return "", dependencyRootMissing, false
}

func (w *dependencyRootWalk) probeCandidate(candidate string) (string, dependencyRootResolutionStatus, bool) {
	if candidate == "" {
		return "", dependencyRootMissing, false
	}
	if _, seen := w.probed[candidate]; seen {
		return "", dependencyRootMissing, false
	}
	w.probed[candidate] = struct{}{}
	root, status := resolveDependencyRootAtDirDetailedWithinBoundary(candidate, w.dependency, w.canonicalRepo)
	if status == dependencyRootUnsafe {
		w.sawUnsafe = true
	}
	if status == dependencyRootFound {
		return root, dependencyRootFound, true
	}
	return "", dependencyRootMissing, false
}

func (w *dependencyRootWalk) advance() bool {
	rawDone := equalDependencyWalkPath(w.rawStart, w.rawRepo)
	canonicalDone := equalDependencyWalkPath(w.canonicalStart, w.canonicalRepo)
	if rawDone && canonicalDone {
		return false
	}
	progressed := false
	w.rawStart, progressed = advanceDependencyWalkPath(w.rawStart, rawDone, progressed)
	w.canonicalStart, progressed = advanceDependencyWalkPath(w.canonicalStart, canonicalDone, progressed)
	return progressed
}

func advanceDependencyWalkPath(current string, done, progressed bool) (string, bool) {
	if done {
		return current, progressed
	}
	next := filepath.Dir(current)
	if next == current {
		return current, progressed
	}
	return next, true
}

func canonicalizeDependencyWalkStart(path string) (string, error) {
	canonicalPath, err := evaluateDependencySymlinks(path)
	if err == nil {
		return canonicalPath, nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}
	return canonicalizeNoFollowParentPath(path)
}

func resolveDependencyRootsFromDeclarationDirs(repoPath, dependency string, declarationDirs map[string]struct{}) []string {
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

func resolveDependencyRootsFromScan(repoPath, dependency string, scanResult ScanResult) []string {
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
	return resolveDependencyRootAtDirDetailedWithinBoundary(rootDir, dependency, rootDir)
}

func resolveDependencyRootAtDirDetailedWithinBoundary(rootDir, dependency, allowedRoot string) (string, dependencyRootResolutionStatus) {
	root, err := validatedDependencyRootAtDirWithinBoundary(rootDir, dependency, allowedRoot)
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
	return validatedDependencyRootAtDirWithinBoundary(rootDir, dependency, rootDir)
}

func validatedDependencyRootAtDirWithinBoundary(rootDir, dependency, allowedRoot string) (string, error) {
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
	root, _, err := openValidatedRootNoFollowWithinBoundary(dependencyRootPath, allowedRoot)
	if err != nil {
		return "", err
	}
	if err := validateRegularFileWithinRoot(root, dependencyRootPath, jsPackageFile); err != nil {
		if closeErr := root.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
		return "", err
	}
	if err := root.Close(); err != nil {
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
	return openValidatedRootNoFollowWithinBoundary(path, "")
}

func openValidatedRootNoFollowWithinBoundary(path, allowedRoot string) (safeio.Root, string, error) {
	validatedPath, err := resolvePinnedRootPathWithinBoundary(path, allowedRoot)
	if err != nil {
		return nil, "", err
	}
	root, err := openPinnedDependencyRootNoFollow(validatedPath)
	if err != nil {
		return nil, "", err
	}
	return root, validatedPath, nil
}

func openConstrainedRoot(path string) (safeio.Root, error) {
	validatedPath, err := resolvePinnedRootPathWithinBoundary(path, "")
	if err != nil {
		return nil, err
	}
	return openPinnedDependencyRootNoFollow(validatedPath)
}

func openPinnedDependencyRootNoFollow(path string) (safeio.Root, error) {
	expectedInfo, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !expectedInfo.IsDir() {
		return nil, fmt.Errorf("path is not a directory: %s", path)
	}
	if err := dependencyRootOpenReadyFn(); err != nil {
		return nil, err
	}

	root, err := openDependencyRootNoFollow(path)
	if err != nil {
		return nil, err
	}
	openedInfo, err := root.Lstat(".")
	if err != nil {
		return nil, closeRootWithErr(root, err)
	}
	if !openedInfo.IsDir() || !os.SameFile(expectedInfo, openedInfo) {
		return nil, closeRootWithErr(root, fmt.Errorf("path changed while opening: %s", path))
	}
	return root, nil
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
	return resolvePinnedRootPathWithinBoundary(path, "")
}

func resolvePinnedRootPathWithinBoundary(path, allowedRoot string) (string, error) {
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
	return resolvePinnedDependencyChainPath(basePath, relParts, allowedRoot)
}

func resolvePinnedDependencyChainPath(basePath string, relParts []string, allowedRoot string) (string, error) {
	chain, err := newPinnedDependencyChain(basePath, allowedRoot)
	if err != nil {
		return "", err
	}
	for _, part := range relParts {
		if err := chain.advance(part); err != nil {
			return "", err
		}
	}
	return chain.result()
}

type pinnedDependencyChain struct {
	current         string
	allowedBoundary string
	ancestorParts   []string
	bounded         bool
}

func newPinnedDependencyChain(basePath, allowedRoot string) (pinnedDependencyChain, error) {
	current, err := resolvePinnedDependencyChainBase(basePath)
	if err != nil {
		return pinnedDependencyChain{}, err
	}
	chain := pinnedDependencyChain{
		current:         current,
		allowedBoundary: current,
	}
	if allowedRoot == "" {
		return chain, nil
	}
	chain.allowedBoundary, err = resolvePinnedDependencyChainBase(allowedRoot)
	if err != nil {
		return pinnedDependencyChain{}, err
	}
	chain.ancestorParts, err = dependencyBoundaryAncestorParts(current, chain.allowedBoundary)
	if err != nil {
		return pinnedDependencyChain{}, err
	}
	chain.bounded = true
	return chain, nil
}

func (c *pinnedDependencyChain) advance(part string) error {
	if part == "" || part == "." {
		return nil
	}
	nextPath := filepath.Join(c.current, part)
	nextPath, err := resolvePinnedDependencyComponent(nextPath, c.componentBoundary())
	if err != nil {
		return err
	}
	if err := c.consumeAncestorPart(nextPath); err != nil {
		return err
	}
	c.current = nextPath
	return nil
}

func (c *pinnedDependencyChain) componentBoundary() string {
	if c.bounded && len(c.ancestorParts) != 0 {
		return dependencyBoundaryRoot(c.current, c.allowedBoundary)
	}
	return c.allowedBoundary
}

func (c *pinnedDependencyChain) consumeAncestorPart(nextPath string) error {
	if !c.bounded || len(c.ancestorParts) == 0 {
		return nil
	}
	if equalDependencyWalkPath(nextPath, c.allowedBoundary) {
		c.ancestorParts = nil
		return nil
	}
	expectedPath := filepath.Join(c.current, c.ancestorParts[0])
	if !equalDependencyWalkPath(nextPath, expectedPath) {
		return fmt.Errorf("path escapes root: %s", nextPath)
	}
	c.ancestorParts = c.ancestorParts[1:]
	return nil
}

func (c *pinnedDependencyChain) result() (string, error) {
	if c.bounded {
		if err := enforceDependencyPathWithinRoot(c.current, c.allowedBoundary); err != nil {
			return "", err
		}
	}
	return c.current, nil
}

func dependencyBoundaryRoot(basePath, allowedRoot string) string {
	if allowedRoot == "" {
		return basePath
	}
	if isPathWithin(basePath, allowedRoot) {
		return allowedRoot
	}
	candidate := basePath
	for !isPathWithin(allowedRoot, candidate) {
		next := filepath.Dir(candidate)
		if equalDependencyWalkPath(next, candidate) {
			return candidate
		}
		candidate = next
	}
	return candidate
}

func resolvePinnedDependencyChainBase(basePath string) (string, error) {
	baseRoot, baseParts, ok := dependencyChainBase(basePath)
	if !ok {
		if _, err := validateDirectoryPathNoFollow(basePath); err != nil {
			return "", err
		}
		return canonicalizeNoFollowParentPath(basePath)
	}
	current, err := resolvePinnedDependencyChainPath(baseRoot, baseParts, "")
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
	resolvedPath, err := requirePinnedDependencyDirectory(path, info)
	if err != nil {
		return "", err
	}
	if err := enforceDependencyPathWithinRoot(resolvedPath, allowedRoot); err != nil {
		return "", err
	}
	return resolvedPath, nil
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
	if err := enforceDependencyPathWithinRoot(resolvedPath, allowedRoot); err != nil {
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
	return pathutil.WithinRoot(root, path)
}

func enforceDependencyPathWithinRoot(path, allowedRoot string) error {
	if allowedRoot == "" || isPathWithin(path, allowedRoot) {
		return nil
	}
	return fmt.Errorf("path escapes root: %s", path)
}

func equalDependencyWalkPath(left, right string) bool {
	return pathutil.Equal(left, right)
}

func dependencyBoundaryAncestorParts(basePath, allowedRoot string) ([]string, error) {
	if allowedRoot == "" || isPathWithin(basePath, allowedRoot) {
		return nil, nil
	}
	rel, err := relativeDependencyPath(basePath, allowedRoot)
	if err != nil {
		return nil, err
	}
	if !pathContainedByRelativePath(rel) {
		return nil, fmt.Errorf("path escapes root: %s", allowedRoot)
	}
	return splitDependencyRelativePath(rel), nil
}

func pathContainedByRelativePath(rel string) bool {
	if rel == "." {
		return true
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return false
	}
	return true
}

func splitDependencyRelativePath(rel string) []string {
	if rel == "." || rel == "" {
		return nil
	}
	return strings.Split(rel, string(filepath.Separator))
}
