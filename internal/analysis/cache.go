package analysis

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/safeio"
)

type resolvedCacheOptions struct {
	Enabled   bool
	Path      string
	WritePath string
	ReadOnly  bool
}

func (o *resolvedCacheOptions) writePath() string {
	if o.WritePath != "" {
		return o.WritePath
	}
	return o.Path
}

type analysisCache struct {
	options         resolvedCacheOptions
	metadata        report.CacheMetadata
	warnings        []string
	cacheable       bool
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
	writePath := options.writePath()
	if req.Cache == nil || strings.TrimSpace(req.Cache.Path) == "" {
		if cachePathEscapesRepo(writePath, repoPath) {
			cache.cacheable = false
			cache.warn("analysis cache unavailable: cache path escapes repository root")
			return cache
		}
	}
	if err := ensureConfinedDirectory(writePath, "keys", 0o750); err != nil {
		cache.cacheable = false
		cache.warn("analysis cache unavailable: " + err.Error())
		return cache
	}
	if err := ensureConfinedDirectory(writePath, "objects", 0o750); err != nil {
		cache.cacheable = false
		cache.warn("analysis cache unavailable: " + err.Error())
		return cache
	}
	cache.cacheable = true
	return cache
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
		Enabled:   true,
		Path:      filepath.Join(repoPath, ".lopper-cache"),
		WritePath: filepath.Join(repoPath, ".lopper-cache"),
		ReadOnly:  false,
	}
	if req == nil {
		return options
	}
	options.Enabled = req.Enabled
	if strings.TrimSpace(req.Path) != "" {
		options.Path = strings.TrimSpace(req.Path)
		switch {
		case req.PinnedPath != "":
			options.WritePath = req.PinnedPath
		case filepath.IsAbs(options.Path):
			options.WritePath = filepath.Clean(options.Path)
		default:
			options.WritePath = filepath.Join(repoPath, options.Path)
		}
	}
	options.ReadOnly = req.ReadOnly
	return options
}

// ResolveTrustedCacheOptions normalizes enabled cache options so writes stay
// within the trusted repository root. Empty paths are left empty so callers can
// preserve default cache-path behavior.
func ResolveTrustedCacheOptions(repoPath string, req *CacheOptions) (*CacheOptions, error) {
	if req == nil {
		return nil, nil
	}
	options := *req
	options.Path = strings.TrimSpace(options.Path)
	if !options.Enabled || options.Path == "" {
		return &options, nil
	}
	if strings.TrimSpace(options.PinnedPath) != "" {
		options.PinnedPath = filepath.Clean(strings.TrimSpace(options.PinnedPath))
		if err := validateTrustedCachePath(repoPath, options.PinnedPath, "cachePath"); err != nil {
			return nil, err
		}
		return &options, nil
	}
	cachePath := options.Path
	if !filepath.IsAbs(cachePath) {
		cachePath = filepath.Join(repoPath, cachePath)
	}
	cachePath = filepath.Clean(cachePath)
	pinnedPath, err := pinTrustedCachePath(repoPath, cachePath, "cachePath")
	if err != nil {
		return nil, err
	}
	options.PinnedPath = pinnedPath
	return &options, nil
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

func ensureConfinedDirectory(rootPath, name string, perm os.FileMode) (returnErr error) {
	targetPath := filepath.Join(rootPath, name)
	root, rel, err := openConfinedWriteRootUnder(rootPath, targetPath)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			returnErr = errors.Join(returnErr, closeErr)
		}
	}()
	return root.EnsureDir(rel, perm)
}

func pinTrustedCachePath(repoPath, cachePath, field string) (string, error) {
	if err := validateTrustedCachePath(repoPath, cachePath, field); err != nil {
		return "", err
	}
	return resolvePathWithinExistingTree(cachePath)
}

func openConfinedWriteRootUnder(rootPath, targetPath string) (*safeio.WriteRoot, string, error) {
	rootPath = filepath.Clean(rootPath)
	targetPath = filepath.Clean(targetPath)
	targetRel, err := filepath.Rel(rootPath, targetPath)
	if err != nil {
		return nil, "", err
	}
	canonicalRootPath, err := resolvePathWithinExistingTree(rootPath)
	if err != nil {
		return nil, "", err
	}
	root, rootRel, err := safeio.OpenExistingCanonicalWriteRoot(canonicalRootPath, true)
	if err != nil {
		return nil, "", err
	}
	if rootRel == "." {
		return root, targetRel, nil
	}
	return root, filepath.Join(rootRel, targetRel), nil
}

func resolvePathWithinExistingTree(path string) (string, error) {
	cleanPath := filepath.Clean(path)
	missingSegments := make([]string, 0, 4)
	currentPath := cleanPath
	for {
		resolvedPath, err := filepath.EvalSymlinks(currentPath)
		if err == nil {
			if len(missingSegments) == 0 {
				return resolvedPath, nil
			}
			parts := []string{resolvedPath}
			for i := len(missingSegments) - 1; i >= 0; i-- {
				parts = append(parts, missingSegments[i])
			}
			return filepath.Join(parts...), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parentPath := filepath.Dir(currentPath)
		if parentPath == currentPath {
			return "", err
		}
		missingSegments = append(missingSegments, filepath.Base(currentPath))
		currentPath = parentPath
	}
}

func validateTrustedCachePath(repoPath, cachePath, field string) error {
	cachePath = strings.TrimSpace(cachePath)
	if cachePath == "" {
		return nil
	}
	if !pathWithinTrustedRoot(repoPath, cachePath) {
		return fmt.Errorf("%s must stay within repoPath", field)
	}
	return validateNoSymlinkEscape(repoPath, cachePath, field)
}

func pathWithinTrustedRoot(repoPath, candidatePath string) bool {
	cleanRepoPath := filepath.Clean(repoPath)
	cleanCandidatePath := filepath.Clean(candidatePath)
	if pathWithinDir(cleanRepoPath, cleanCandidatePath) {
		return true
	}
	resolvedRepoPath, err := filepath.EvalSymlinks(cleanRepoPath)
	if err != nil {
		return false
	}
	resolvedCandidatePath, err := resolvePathWithinExistingTree(cleanCandidatePath)
	if err != nil {
		return false
	}
	return pathWithinDir(resolvedRepoPath, resolvedCandidatePath)
}

func validateNoSymlinkEscape(repoPath, candidatePath, field string) error {
	cleanRepoPath := filepath.Clean(repoPath)
	cleanCandidatePath := filepath.Clean(candidatePath)
	resolvedRepoPath, err := filepath.EvalSymlinks(cleanRepoPath)
	if err != nil {
		resolvedRepoPath = cleanRepoPath
	}
	walkRoot := cleanRepoPath
	walkTarget := cleanCandidatePath
	if !pathWithinDir(walkRoot, walkTarget) {
		resolvedCandidatePath, resolveErr := resolvePathWithinExistingTree(cleanCandidatePath)
		if resolveErr != nil || !pathWithinDir(resolvedRepoPath, resolvedCandidatePath) {
			return fmt.Errorf("%s must stay within repoPath", field)
		}
		walkRoot = resolvedRepoPath
		walkTarget = resolvedCandidatePath
	}
	relativePath, err := filepath.Rel(walkRoot, walkTarget)
	if err != nil {
		return fmt.Errorf("%s must stay within repoPath", field)
	}
	if relativePath == "." {
		return nil
	}
	currentPath := walkRoot
	for _, segment := range strings.Split(relativePath, string(filepath.Separator)) {
		currentPath = filepath.Join(currentPath, segment)
		info, statErr := os.Lstat(currentPath)
		if errors.Is(statErr, os.ErrNotExist) {
			return nil
		}
		if statErr != nil {
			return fmt.Errorf("stat %s: %w", field, statErr)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			continue
		}
		resolvedPath, resolveErr := filepath.EvalSymlinks(currentPath)
		if resolveErr != nil || !pathWithinDir(resolvedRepoPath, resolvedPath) {
			return fmt.Errorf("%s must stay within repoPath", field)
		}
	}
	return nil
}

func pathWithinDir(root, child string) bool {
	rel, err := filepath.Rel(root, child)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}
