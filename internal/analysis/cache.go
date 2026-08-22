package analysis

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/safeio"
)

type resolvedCacheOptions struct {
	Enabled      bool
	Path         string
	ReadOnly     bool
	ExplicitPath bool
}

type analysisCache struct {
	options          resolvedCacheOptions
	metadata         report.CacheMetadata
	warnings         []string
	cacheable        bool
	rootIdentity     fs.FileInfo
	rejectReadHits   bool
	inputDigestMemo  map[cacheInputDigestMemoKey]string
	stableRepoPath   string
	analysisRepoPath string
}

var (
	openAnalysisCacheAncestor = safeio.OpenRootExistingAncestorNoFollow
	mkdirAnalysisCacheDir     = func(root safeio.Root, name string, perm os.FileMode) (fs.FileInfo, error) {
		if err := root.Mkdir(name, perm); err != nil {
			return nil, err
		}
		return root.Lstat(name)
	}
	closeAnalysisCacheRoot    = func(root safeio.Root) error { return root.Close() }
	validateAnalysisCacheRoot = safeio.VerifyDirectoryIdentity
)

type openedAnalysisCacheRoot struct {
	root           safeio.Root
	parent         safeio.Root
	rollbackParent safeio.Root
	name           string
	info           fs.FileInfo
	created        bool
}

func newAnalysisCache(req Request, repoPath string, analysisRepoPaths ...string) *analysisCache {
	options := resolveCacheOptions(req.Cache, repoPath)
	metadata := report.CacheMetadata{
		Enabled:  options.Enabled,
		Path:     options.Path,
		ReadOnly: options.ReadOnly,
	}
	cache := &analysisCache{
		options:          options,
		metadata:         metadata,
		warnings:         make([]string, 0),
		rejectReadHits:   !options.ExplicitPath,
		stableRepoPath:   filepath.Clean(repoPath),
		analysisRepoPath: filepath.Clean(repoPath),
	}
	if len(analysisRepoPaths) > 0 && strings.TrimSpace(analysisRepoPaths[0]) != "" {
		cache.analysisRepoPath = filepath.Clean(analysisRepoPaths[0])
	}
	if !options.Enabled {
		cache.cacheable = false
		return cache
	}
	if req.Cache == nil || strings.TrimSpace(req.Cache.Path) == "" {
		if cachePathEscapesRepo(options.Path, repoPath) {
			cache.cacheable = false
			cache.warn("analysis cache unavailable: cache path escapes repository root")
			return cache
		}
	}
	var (
		rootIdentity fs.FileInfo
		err          error
	)
	rootIdentity, err = prepareAnalysisCacheRoot(options)
	if err != nil {
		cache.cacheable = false
		cache.warn("analysis cache unavailable: " + err.Error())
		return cache
	}
	cache.rootIdentity = rootIdentity
	cache.cacheable = true
	return cache
}

func prepareAnalysisCacheRoot(options resolvedCacheOptions) (fs.FileInfo, error) {
	if options.ReadOnly {
		identity, err := prepareReadableAnalysisCacheRoot(options.Path)
		if err != nil {
			return nil, err
		}
		if err := validateAnalysisCacheRoot(options.Path, identity); err != nil {
			return nil, err
		}
		return identity, nil
	}
	return prepareWritableAnalysisCacheRoot(options.Path)
}

func prepareReadableAnalysisCacheRoot(cachePath string) (identity fs.FileInfo, returnErr error) {
	root, currentPath, missingParts, err := openAnalysisCacheAncestor(cachePath)
	if err != nil {
		return nil, err
	}
	defer func() {
		returnErr = errors.Join(returnErr, root.Close())
	}()
	if len(missingParts) > 0 {
		return nil, os.ErrNotExist
	}
	return verifyPinnedAnalysisCacheDirectory(root, currentPath)
}

func (c *analysisCache) stableCacheRoot(rootPath string) string {
	if c == nil {
		return rootPath
	}
	rootPath = filepath.Clean(rootPath)
	rel, err := filepath.Rel(c.analysisRepoPath, rootPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return rootPath
	}
	return filepath.Join(c.stableRepoPath, rel)
}

func prepareWritableAnalysisCacheRoot(cachePath string) (identity fs.FileInfo, returnErr error) {
	root, currentPath, missingParts, err := openAnalysisCacheAncestor(cachePath)
	if err != nil {
		return nil, err
	}
	openedRoots := []openedAnalysisCacheRoot{{root: root}}
	defer func() {
		returnErr = cleanupOpenedAnalysisCacheRoots(openedRoots, returnErr)
	}()

	current := root
	for _, name := range missingParts {
		child, err := openOrCreatePinnedAnalysisCacheChild(current, currentPath, name)
		if err != nil {
			return nil, err
		}
		openedRoots = append(openedRoots, child)
		current = child.root
		currentPath = filepath.Join(currentPath, name)
		if _, err := verifyPinnedAnalysisCacheDirectory(current, currentPath); err != nil {
			return nil, err
		}
	}

	info, err := verifyPinnedAnalysisCacheDirectory(current, currentPath)
	if err != nil {
		return nil, err
	}
	for _, name := range []string{"keys", "objects"} {
		child, err := openOrCreatePinnedAnalysisCacheChild(current, currentPath, name)
		if err != nil {
			return nil, err
		}
		openedRoots = append(openedRoots, child)
	}
	if err := validateAnalysisCacheRoot(cachePath, info); err != nil {
		return nil, err
	}
	return info, nil
}

func cleanupOpenedAnalysisCacheRoots(openedRoots []openedAnalysisCacheRoot, operationErr error) error {
	if operationErr != nil {
		operationErr = errors.Join(operationErr, removeCreatedAnalysisCacheRoots(openedRoots, false))
	}
	closeErr := closeOpenedAnalysisCacheRoots(openedRoots)
	if operationErr == nil && closeErr != nil {
		closeErr = errors.Join(closeErr, removeCreatedAnalysisCacheRoots(openedRoots, true))
	}
	return errors.Join(operationErr, closeErr, closeRollbackAnalysisCacheRoots(openedRoots))
}

func removeCreatedAnalysisCacheRoots(openedRoots []openedAnalysisCacheRoot, useRollbackParents bool) error {
	var rollbackErr error
	for index := len(openedRoots) - 1; index >= 0; index-- {
		opened := openedRoots[index]
		if !opened.created {
			continue
		}
		parent := opened.parent
		if useRollbackParents {
			parent = opened.rollbackParent
		}
		rollbackErr = errors.Join(rollbackErr, removeCreatedAnalysisCacheChild(parent, opened.name, opened.info))
	}
	return rollbackErr
}

func closeOpenedAnalysisCacheRoots(openedRoots []openedAnalysisCacheRoot) error {
	var closeErr error
	for index := len(openedRoots) - 1; index >= 0; index-- {
		closeErr = errors.Join(closeErr, closeAnalysisCacheRoot(openedRoots[index].root))
	}
	return closeErr
}

func closeRollbackAnalysisCacheRoots(openedRoots []openedAnalysisCacheRoot) error {
	var closeErr error
	for index := len(openedRoots) - 1; index >= 0; index-- {
		if openedRoots[index].rollbackParent != nil {
			closeErr = errors.Join(closeErr, openedRoots[index].rollbackParent.Close())
		}
	}
	return closeErr
}

func verifyPinnedAnalysisCacheDirectory(root safeio.Root, path string) (fs.FileInfo, error) {
	info, err := root.Lstat(".")
	if err != nil {
		return nil, err
	}
	if err := safeio.VerifyDirectoryIdentity(path, info); err != nil {
		return nil, err
	}
	return info, nil
}

func openOrCreatePinnedAnalysisCacheChild(root safeio.Root, parentPath, name string) (openedAnalysisCacheRoot, error) {
	if _, err := verifyPinnedAnalysisCacheDirectory(root, parentPath); err != nil {
		return openedAnalysisCacheRoot{}, err
	}
	childPath := filepath.Join(parentPath, name)
	created := false
	var createdInfo fs.FileInfo
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		var mkdirErr error
		createdInfo, mkdirErr = mkdirAnalysisCacheDir(root, name, 0o750)
		if mkdirErr != nil {
			if !errors.Is(mkdirErr, fs.ErrExist) {
				return openedAnalysisCacheRoot{}, errors.Join(mkdirErr, removeCreatedAnalysisCacheChild(root, name, createdInfo))
			}
			createdInfo = nil
		} else {
			created = true
		}
		info, err = root.Lstat(name)
		if err != nil {
			return openedAnalysisCacheRoot{}, errors.Join(err, removeCreatedAnalysisCacheChild(root, name, createdInfo))
		}
		if created && (createdInfo == nil || !os.SameFile(createdInfo, info)) {
			return openedAnalysisCacheRoot{}, errors.Join(errors.New("directory changed after creation: "+childPath), removeCreatedAnalysisCacheChild(root, name, createdInfo))
		}
	}
	if err != nil {
		return openedAnalysisCacheRoot{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return openedAnalysisCacheRoot{}, errors.New("directory contains symlink: " + childPath)
	}
	if !info.IsDir() {
		return openedAnalysisCacheRoot{}, errors.New("directory is not a directory: " + childPath)
	}
	child, err := root.OpenRoot(name)
	if err != nil {
		return openedAnalysisCacheRoot{}, errors.Join(err, removeCreatedAnalysisCacheChild(root, name, createdInfo))
	}
	openedInfo, err := child.Lstat(".")
	if err != nil {
		return openedAnalysisCacheRoot{}, cleanupCreatedAnalysisCacheChild(root, name, child, createdInfo, err)
	}
	if !os.SameFile(info, openedInfo) {
		return openedAnalysisCacheRoot{}, cleanupCreatedAnalysisCacheChild(root, name, child, createdInfo, errors.New("directory changed while opening: "+childPath))
	}
	if _, err := verifyPinnedAnalysisCacheDirectory(root, parentPath); err != nil {
		return openedAnalysisCacheRoot{}, cleanupCreatedAnalysisCacheChild(root, name, child, createdInfo, err)
	}
	if err := safeio.VerifyDirectoryIdentity(childPath, openedInfo); err != nil {
		return openedAnalysisCacheRoot{}, cleanupCreatedAnalysisCacheChild(root, name, child, createdInfo, err)
	}
	rollbackInfo := openedInfo
	if created {
		rollbackInfo = createdInfo
	}
	opened := openedAnalysisCacheRoot{root: child, parent: root, name: name, info: rollbackInfo, created: created}
	if !opened.created {
		return opened, nil
	}
	rollbackParent, err := root.OpenRoot(".")
	if err != nil {
		return openedAnalysisCacheRoot{}, cleanupCreatedAnalysisCacheChild(root, name, child, createdInfo, err)
	}
	opened.rollbackParent = rollbackParent
	return opened, nil
}

func cleanupCreatedAnalysisCacheChild(root safeio.Root, name string, child safeio.Root, childInfo fs.FileInfo, err error) error {
	return errors.Join(err, closeAnalysisCacheRoot(child), removeCreatedAnalysisCacheChild(root, name, childInfo))
}

func removeCreatedAnalysisCacheChild(root safeio.Root, name string, childInfo fs.FileInfo) error {
	if childInfo == nil {
		return nil
	}
	currentInfo, err := root.Lstat(name)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if !os.SameFile(currentInfo, childInfo) {
		return nil
	}
	if err := root.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (c *analysisCache) openWriteRoot() (*safeio.WriteRoot, error) {
	if c.rootIdentity == nil {
		identity, err := prepareWritableAnalysisCacheRoot(c.options.Path)
		if err != nil {
			return nil, err
		}
		c.rootIdentity = identity
	}
	if err := validateAnalysisCacheRoot(c.options.Path, c.rootIdentity); err != nil {
		return nil, err
	}
	root, err := safeio.OpenCanonicalWriteRoot(c.options.Path)
	if err != nil {
		return nil, err
	}
	if err := root.VerifyIdentity(c.rootIdentity); err != nil {
		return nil, errors.Join(err, root.Close())
	}
	if err := validateAnalysisCacheRoot(c.options.Path, c.rootIdentity); err != nil {
		return nil, errors.Join(err, root.Close())
	}
	return root, nil
}

func cachePathEscapesRepo(cachePath, repoPath string) bool {
	if info, err := os.Lstat(cachePath); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return true
	}
	resolvedCachePath, err := filepath.EvalSymlinks(cachePath)
	if err != nil {
		return false
	}
	resolvedRepoPath, err := filepath.EvalSymlinks(repoPath)
	if err != nil {
		resolvedRepoPath = filepath.Clean(repoPath)
	}
	rel, err := filepath.Rel(resolvedRepoPath, resolvedCachePath)
	if err != nil {
		return true
	}
	return rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func resolveCacheOptions(req *CacheOptions, repoPath string) resolvedCacheOptions {
	options := resolvedCacheOptions{
		Enabled:  true,
		Path:     filepath.Join(repoPath, ".lopper-cache"),
		ReadOnly: false,
	}
	if req == nil {
		return options
	}
	options.Enabled = req.Enabled
	if strings.TrimSpace(req.Path) != "" {
		options.Path = strings.TrimSpace(req.Path)
		options.ExplicitPath = true
	}
	if strings.TrimSpace(req.ResolvedPath) != "" {
		options.Path = strings.TrimSpace(req.ResolvedPath)
	}
	options.ReadOnly = req.ReadOnly
	return options
}

func normalizeCacheOptionsForRepository(req *CacheOptions, repoPath string) *CacheOptions {
	if req == nil {
		return nil
	}

	options := *req
	options.Path = strings.TrimSpace(options.Path)
	options.ResolvedPath = resolveCachePathForRepository(repoPath, options.ResolvedPath)
	if options.ResolvedPath == "" {
		options.ResolvedPath = resolveCachePathForRepository(repoPath, options.Path)
	}
	return &options
}

func resolveCachePathForRepository(repoPath, path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Join(repoPath, path)
}

func (c *analysisCache) warn(message string) {
	if strings.TrimSpace(message) == "" {
		return
	}
	c.warnings = append(c.warnings, message)
}

func (c *analysisCache) takeWarnings() []string {
	if len(c.warnings) == 0 {
		return nil
	}
	out := append([]string(nil), c.warnings...)
	c.warnings = c.warnings[:0]
	return out
}

func (c *analysisCache) metadataSnapshot() *report.CacheMetadata {
	if c == nil {
		return nil
	}
	snapshot := c.metadata
	if len(c.metadata.Invalidations) > 0 {
		snapshot.Invalidations = append([]report.CacheInvalidation(nil), c.metadata.Invalidations...)
	}
	return &snapshot
}
