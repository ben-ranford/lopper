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
	rootIdentity, err := prepareWritableAnalysisCacheRoot(options.Path)
	if err != nil {
		cache.cacheable = false
		cache.warn("analysis cache unavailable: " + err.Error())
		return cache
	}
	if err := validateAnalysisCacheRoot(options.Path, rootIdentity); err != nil {
		cache.cacheable = false
		cache.warn("analysis cache unavailable: " + err.Error())
		return cache
	}
	cache.rootIdentity = rootIdentity
	cache.cacheable = true
	return cache
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
	root, currentPath, missingParts, err := safeio.OpenRootExistingAncestorNoFollow(cachePath)
	if err != nil {
		return nil, err
	}
	roots := []safeio.Root{root}
	defer func() {
		for index := len(roots) - 1; index >= 0; index-- {
			returnErr = errors.Join(returnErr, roots[index].Close())
		}
	}()
	current := root
	for _, part := range missingParts {
		next, err := openOrCreatePinnedAnalysisCacheChild(current, currentPath, part)
		if err != nil {
			return nil, err
		}
		roots = append(roots, next)
		current = next
		currentPath = filepath.Join(currentPath, part)
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
		if closeErr := child.Close(); closeErr != nil {
			return nil, closeErr
		}
	}
	return info, nil
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

func openOrCreatePinnedAnalysisCacheChild(root safeio.Root, parentPath, name string) (safeio.Root, error) {
	if _, err := verifyPinnedAnalysisCacheDirectory(root, parentPath); err != nil {
		return nil, err
	}
	return safeio.OpenOrCreatePinnedDirectory(root, parentPath, name, 0o750)
}

func validateAnalysisCacheRoot(cachePath string, expected fs.FileInfo) error {
	return safeio.VerifyDirectoryIdentity(cachePath, expected)
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
