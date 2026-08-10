package analysis

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/ben-ranford/lopper/internal/report"
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
	if err := os.MkdirAll(filepath.Join(options.Path, "keys"), 0o750); err != nil {
		cache.cacheable = false
		cache.warn("analysis cache unavailable: " + err.Error())
		return cache
	}
	if err := os.MkdirAll(filepath.Join(options.Path, "objects"), 0o750); err != nil {
		cache.cacheable = false
		cache.warn("analysis cache unavailable: " + err.Error())
		return cache
	}
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
