package analysis

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/safeio"
)

var (
	analysisCacheAbsFn          = filepath.Abs
	analysisCacheMkdirAllFn     = os.MkdirAll
	analysisCacheEvalSymlinksFn = filepath.EvalSymlinks
	analysisCacheStatFn         = os.Stat
	analysisCacheOpenRootFn     = safeio.OpenCanonicalWriteRoot
	analysisCacheCloseRootFn    = func(root *safeio.WriteRoot) error { return root.Close() }
	analysisCacheRootLstatFn    = func(root *safeio.WriteRoot, path string) (fs.FileInfo, error) {
		return root.Lstat(path)
	}
	analysisCacheSameFileFn = os.SameFile
)

type resolvedCacheOptions struct {
	Enabled      bool
	Path         string
	RawPath      string
	ReadOnly     bool
	ExplicitPath bool
}

type analysisCache struct {
	options         resolvedCacheOptions
	metadata        report.CacheMetadata
	warnings        []string
	cacheable       bool
	authKey         []byte
	repoRoot        string
	storageRoot     string
	storageRootInfo fs.FileInfo
	inputDigestMemo map[cacheInputDigestMemoKey]string
}

func newAnalysisCache(req Request, repoPath string) *analysisCache {
	options := resolveCacheOptions(req.Cache, repoPath)
	metadata := report.CacheMetadata{
		Enabled:  options.Enabled,
		Path:     options.Path,
		ReadOnly: options.ReadOnly,
	}
	cache := &analysisCache{
		options:  options,
		metadata: metadata,
		warnings: make([]string, 0),
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
	} else if err := validateExplicitCachePath(options.rawExplicitPath()); err != nil {
		cache.cacheable = false
		cache.warn("analysis cache unavailable: " + err.Error())
		return cache
	}
	if err := cache.initializeStorage(repoPath); err != nil {
		cache.cacheable = false
		cache.warn("analysis cache unavailable: " + err.Error())
		return cache
	}
	authKey, err := cache.resolveAuthKey()
	if err != nil {
		cache.cacheable = false
		cache.warn("analysis cache unavailable: " + err.Error())
		return cache
	}
	cache.authKey = authKey
	cache.cacheable = true
	return cache
}

func (c *analysisCache) initializeStorage(repoPath string) (returnErr error) {
	canonicalRepo, err := analysisCacheEvalSymlinksFn(repoPath)
	if err != nil {
		return fmt.Errorf("resolve repository root: %w", err)
	}
	c.repoRoot = canonicalRepo
	storageRoot, err := resolveCacheStorageRoot(c.options, repoPath, canonicalRepo)
	if err != nil {
		return err
	}
	c.storageRoot = storageRoot
	if c.options.ReadOnly {
		if _, err := analysisCacheStatFn(storageRoot); os.IsNotExist(err) {
			return nil
		} else if err != nil {
			return err
		}
	}
	root, err := analysisCacheOpenRootFn(storageRoot)
	if err != nil {
		return err
	}
	defer func() {
		returnErr = errors.Join(returnErr, root.Close())
	}()
	info, err := analysisCacheRootLstatFn(root, ".")
	if err != nil {
		return err
	}
	c.storageRootInfo = info
	if c.options.ReadOnly {
		return nil
	}
	if err := root.MkdirAll("keys", 0o750); err != nil {
		return err
	}
	return root.MkdirAll("objects", 0o750)
}

func resolveCacheStorageRoot(options resolvedCacheOptions, repoPath, canonicalRepo string) (string, error) {
	if options.ExplicitPath {
		return resolveExplicitCacheStorageRoot(options)
	}
	return resolveRepoLocalCacheStorageRoot(options, repoPath, canonicalRepo)
}

func resolveExplicitCacheStorageRoot(options resolvedCacheOptions) (string, error) {
	if err := validateExplicitCachePath(options.rawExplicitPath()); err != nil {
		return "", err
	}
	cacheRoot, err := analysisCacheAbsFn(options.Path)
	if err != nil {
		return "", fmt.Errorf("resolve cache root: %w", err)
	}
	if err := ensureCacheStorageRoot(cacheRoot, options.ReadOnly); err != nil {
		return "", err
	}
	return canonicalizeExplicitCacheStorageRoot(cacheRoot, options.ReadOnly)
}

func resolveRepoLocalCacheStorageRoot(options resolvedCacheOptions, repoPath, canonicalRepo string) (string, error) {
	relativeCachePath, err := filepath.Rel(filepath.Clean(repoPath), filepath.Clean(options.Path))
	if err != nil {
		return "", fmt.Errorf("resolve cache path relative to repository: %w", err)
	}
	if relativeCachePath == ".." || strings.HasPrefix(relativeCachePath, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("cache path escapes repository root")
	}
	cacheRoot := filepath.Join(canonicalRepo, relativeCachePath)
	if err := ensureCacheStorageRoot(cacheRoot, options.ReadOnly); err != nil {
		return "", err
	}
	return cacheRoot, nil
}

func ensureCacheStorageRoot(cacheRoot string, readOnly bool) error {
	if readOnly {
		return nil
	}
	return analysisCacheMkdirAllFn(cacheRoot, 0o750)
}

func canonicalizeExplicitCacheStorageRoot(cacheRoot string, readOnly bool) (string, error) {
	canonicalRoot, err := analysisCacheEvalSymlinksFn(cacheRoot)
	if err == nil {
		return canonicalRoot, nil
	}
	if readOnly && os.IsNotExist(err) {
		return cacheRoot, nil
	}
	return "", err
}

func (c *analysisCache) canonicalStorageRoot() (string, error) {
	if strings.TrimSpace(c.storageRoot) != "" {
		root, err := analysisCacheEvalSymlinksFn(c.storageRoot)
		if err == nil {
			return root, nil
		}
		if os.IsNotExist(err) {
			canonicalParent, parentErr := analysisCacheEvalSymlinksFn(filepath.Dir(c.storageRoot))
			if parentErr == nil {
				return filepath.Join(canonicalParent, filepath.Base(c.storageRoot)), nil
			}
		}
		if c.options.ReadOnly && os.IsNotExist(err) {
			return c.storageRoot, nil
		}
		return "", err
	}
	root, err := analysisCacheEvalSymlinksFn(c.options.Path)
	if err != nil {
		return "", err
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
		options.RawPath = req.Path
		options.Path = strings.TrimSpace(req.Path)
		options.ExplicitPath = true
	}
	options.ReadOnly = req.ReadOnly
	return options
}

func (o *resolvedCacheOptions) rawExplicitPath() string {
	if strings.TrimSpace(o.RawPath) != "" {
		return o.RawPath
	}
	return o.Path
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
