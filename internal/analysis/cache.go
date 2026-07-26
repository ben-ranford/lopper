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
	"github.com/ben-ranford/lopper/internal/workspace"
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
	options           resolvedCacheOptions
	metadata          report.CacheMetadata
	warnings          []string
	cacheable         bool
	inputDigestMemo   map[cacheInputDigestMemoKey]string
	analysisRootPath  string
	stableKeyRepoPath string
	writeRootPath     string
	writeRootInfo     fs.FileInfo
	writeRootOpener   func() (*safeio.WriteRoot, error)
}

const analysisCacheUnavailablePrefix = "analysis cache unavailable: "

var cacheInitBeforeObjectsEnsureFn = func() error { return nil }

type trustedCachePathValidationKind uint8

const (
	trustedCachePathValidationExternal trustedCachePathValidationKind = iota + 1
	trustedCachePathValidationSymlinkEscape
)

const defaultAnalysisCacheDirName = ".lopper-cache"

// CachePathExternal reports whether validation authenticated a cache target
// that remains outside the canonical repository.
func CachePathExternal(err error) bool {
	return trustedCachePathErrorHasKind(err, trustedCachePathValidationExternal)
}

// CachePathSymlinkEscape reports whether a lexically in-repository
// cache target escaped the canonical repository through a symlink.
func CachePathSymlinkEscape(err error) bool {
	return trustedCachePathErrorHasKind(err, trustedCachePathValidationSymlinkEscape)
}

// AuthenticatedExternalCacheOptions verifies that options and an external-path
// classification came from the same trust-boundary resolution.
func AuthenticatedExternalCacheOptions(options *CacheOptions, err error) bool {
	if options == nil || options.trustedPin == nil || options.trustedPin.kind != trustedCachePathExternal {
		return false
	}
	var validationErr *trustedCachePathValidationError
	return CachePathExternal(err) &&
		errors.As(err, &validationErr) &&
		validationErr.trustedPin == options.trustedPin
}

// InRepoCacheOptions reports whether options carry an authenticated
// canonical cache target inside their bound repository.
func InRepoCacheOptions(options *CacheOptions) bool {
	return options != nil &&
		options.trustedPin != nil &&
		options.trustedPin.kind == trustedCachePathInRepo
}

func newAnalysisCache(req Request, repoPath string, repositoryViews ...*RepositoryView) *analysisCache {
	options := resolveCacheOptions(req.Cache, repoPath)
	cache := &analysisCache{
		options:          options,
		metadata:         newAnalysisCacheMetadata(options),
		warnings:         make([]string, 0),
		analysisRootPath: filepath.Clean(repoPath),
	}
	if !options.Enabled {
		cache.cacheable = false
		return cache
	}
	cache.configureStablePaths(req)
	if !cache.validateDefaultWritePath(req, repoPath) {
		return cache
	}
	var repositoryView *RepositoryView
	if len(repositoryViews) > 0 {
		repositoryView = repositoryViews[0]
	}
	cache.initializeWriteRoot(options.writePath(), req.Cache, repositoryView)
	return cache
}

func newAnalysisCacheMetadata(options resolvedCacheOptions) report.CacheMetadata {
	return report.CacheMetadata{
		Enabled:  options.Enabled,
		Path:     options.Path,
		ReadOnly: options.ReadOnly,
	}
}

func (c *analysisCache) configureStablePaths(req Request) {
	if c == nil {
		return
	}
	if repoRootPath := strings.TrimSpace(req.RepoPath); repoRootPath != "" {
		repoRootPath = filepath.Clean(repoRootPath)
		if repoRootPath != c.analysisRootPath {
			c.stableKeyRepoPath = repoRootPath
		}
	}
	if req.Cache.hasTrustedPin() && strings.TrimSpace(req.Cache.Path) == "" {
		c.metadata.Path = c.options.WritePath
	}
}

func (c *analysisCache) validateDefaultWritePath(req Request, repoPath string) bool {
	if c == nil {
		return false
	}
	if req.Cache != nil && (strings.TrimSpace(req.Cache.Path) != "" || req.Cache.hasTrustedPin()) {
		return true
	}
	if cachePathEscapesRepo(c.options.writePath(), repoPath) {
		c.cacheable = false
		c.warn(analysisCacheUnavailablePrefix + "cache path escapes repository root")
		return false
	}
	return true
}

func (c *analysisCache) initializeWriteRoot(writePath string, options *CacheOptions, repositoryView *RepositoryView) {
	var (
		writeRootPath string
		writeRootInfo fs.FileInfo
		err           error
	)
	switch {
	case InRepoCacheOptions(options) && repositoryView != nil && repositoryView.state != nil && repositoryView.state == options.trustedPin.repositoryState:
		writeRootPath = options.trustedPinnedPath()
		writeRootInfo, err = c.ensureRepositoryCacheLayout(repositoryView, options.trustedPin.repoRelativePath)
		if err == nil {
			c.writeRootOpener = func() (*safeio.WriteRoot, error) {
				return repositoryView.OpenWriteRoot(options.trustedPin.repoRelativePath, false)
			}
		}
	case options.hasTrustedPin():
		writeRootPath, writeRootInfo, err = ensureTrustedPinnedCacheLayout(writePath)
	default:
		writeRootPath, writeRootInfo, err = ensurePinnedCacheLayout(writePath)
	}
	if err != nil {
		c.cacheable = false
		c.warn(analysisCacheUnavailablePrefix + err.Error())
		return
	}
	c.writeRootPath = writeRootPath
	c.writeRootInfo = writeRootInfo
	c.cacheable = true
}

func (c *analysisCache) ensureRepositoryCacheLayout(repositoryView *RepositoryView, relativePath string) (_ fs.FileInfo, returnErr error) {
	root, err := repositoryView.OpenWriteRoot(relativePath, true)
	if err != nil {
		return nil, err
	}
	defer func() {
		returnErr = errors.Join(returnErr, root.Close())
	}()
	if err := root.EnsureDir("keys", 0o750); err != nil {
		return nil, err
	}
	if err := cacheInitBeforeObjectsEnsureFn(); err != nil {
		return nil, err
	}
	if err := root.EnsureDir("objects", 0o750); err != nil {
		return nil, err
	}
	return root.Lstat(".")
}

func (c *analysisCache) stableCacheRoot(normalizedRoot string) string {
	if c == nil || c.stableKeyRepoPath == "" {
		return normalizedRoot
	}
	rel, err := filepath.Rel(c.analysisRootPath, normalizedRoot)
	if err != nil {
		return normalizedRoot
	}
	if rel == "." {
		return c.stableKeyRepoPath
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return normalizedRoot
	}
	return filepath.Join(c.stableKeyRepoPath, rel)
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
		Path:      filepath.Join(repoPath, defaultAnalysisCacheDirName),
		WritePath: filepath.Join(repoPath, defaultAnalysisCacheDirName),
		ReadOnly:  false,
	}
	if req == nil {
		return options
	}
	options.Enabled = req.Enabled
	pinnedPath := req.trustedPinnedPath()
	if pinnedPath != "" {
		options.Path = pinnedPath
		options.WritePath = pinnedPath
	}
	if strings.TrimSpace(req.Path) != "" {
		options.Path = strings.TrimSpace(req.Path)
		switch {
		case pinnedPath != "":
			options.WritePath = pinnedPath
		case filepath.IsAbs(options.Path):
			options.WritePath = filepath.Clean(options.Path)
		default:
			options.WritePath = filepath.Join(repoPath, options.Path)
		}
	}
	options.ReadOnly = req.ReadOnly
	return options
}

func ResolveTrustedDefaultCacheOptions(repoPath string, readOnly bool) (*CacheOptions, error) {
	repository, err := ResolveTrustedRepository(repoPath)
	if err != nil {
		return nil, err
	}
	return ResolveTrustedDefaultCacheOptionsForRepository(repository, readOnly)
}

// ResolveTrustedDefaultCacheOptionsForRepository pins the default cache path
// using an already-established repository authorization.
func ResolveTrustedDefaultCacheOptionsForRepository(repository *RepositoryAuthorization, readOnly bool) (*CacheOptions, error) {
	state := repository.authorizationState()
	if state == nil {
		return nil, errors.New("trusted repository authorization is required")
	}
	pin, err := pinTrustedCachePathForRepository(repository, filepath.Join(state.paths.requestedPath, defaultAnalysisCacheDirName), "cachePath")
	if err != nil {
		return nil, err
	}
	return &CacheOptions{
		Enabled:    true,
		ReadOnly:   readOnly,
		trustedPin: pin,
	}, nil
}

// ResolveTrustedCacheOptions pins enabled cache options to canonical paths.
// Genuine external targets return paired authenticated options and a
// CachePathExternal error; empty paths preserve default cache-path behavior.
func ResolveTrustedCacheOptions(repoPath string, req *CacheOptions) (*CacheOptions, error) {
	if req == nil {
		return nil, nil
	}
	if req.hasTrustedPin() {
		return useTrustedCacheOptions(repoPath, req)
	}
	repository, err := ResolveTrustedRepository(repoPath)
	if err != nil {
		return nil, err
	}
	return ResolveTrustedCacheOptionsForRepository(repository, req)
}

// ResolveTrustedCacheOptionsForRepository resolves cache options using an
// already-established repository authorization.
func ResolveTrustedCacheOptionsForRepository(repository *RepositoryAuthorization, req *CacheOptions) (*CacheOptions, error) {
	if req == nil {
		return nil, nil
	}
	state := repository.authorizationState()
	if state == nil {
		return nil, errors.New("trusted repository authorization is required")
	}
	options := *req
	options.Path = strings.TrimSpace(options.Path)
	if options.hasTrustedPin() {
		if options.trustedPin.repositoryState != repository.authorizationState() {
			return nil, errors.New("trusted cache pin does not match repository authorization")
		}
		return &options, nil
	}
	if !options.Enabled {
		return &options, nil
	}
	if options.Path == "" {
		return &options, nil
	}
	cachePath := options.Path
	if !filepath.IsAbs(cachePath) {
		cachePath = filepath.Join(state.paths.requestedPath, cachePath)
	}
	cachePath = filepath.Clean(cachePath)
	pin, err := pinTrustedCachePathForRepository(repository, cachePath, "cachePath")
	if err != nil {
		if CachePathExternal(err) && !filepath.IsAbs(options.Path) {
			return nil, err
		}
		if pin != nil {
			options.trustedPin = pin
			return &options, err
		}
		return nil, err
	}
	options.trustedPin = pin
	return &options, nil
}

func normalizeTrustedRepoPath(repoPath string) (string, error) {
	return workspace.NormalizeRepoPath(strings.TrimSpace(repoPath))
}

func useTrustedCacheOptions(repoPath string, req *CacheOptions) (*CacheOptions, error) {
	if req == nil || !req.hasTrustedPin() {
		return nil, errors.New("trusted cache options require an authenticated pin")
	}
	repository := newRepositoryAuthorization(req.trustedPin.repositoryState)
	if err := useTrustedRepository(repoPath, repository); err != nil {
		return nil, err
	}
	options := *req
	return &options, nil
}

func (o *CacheOptions) hasTrustedPin() bool {
	return o != nil && o.trustedPin != nil
}

func (o *CacheOptions) trustedPinnedPath() string {
	if !o.hasTrustedPin() {
		return ""
	}
	return o.trustedPin.canonicalPath
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

func ensurePinnedCacheLayout(rootPath string) (_ string, _ fs.FileInfo, returnErr error) {
	canonicalRootPath, err := resolvePathWithinExistingTree(rootPath)
	if err != nil {
		return "", nil, err
	}
	return ensureCanonicalCacheLayout(canonicalRootPath)
}

func ensureTrustedPinnedCacheLayout(rootPath string) (_ string, _ fs.FileInfo, returnErr error) {
	return ensureCanonicalCacheLayout(filepath.Clean(rootPath))
}

func ensureCanonicalCacheLayout(rootPath string) (_ string, _ fs.FileInfo, returnErr error) {
	ancestorRoot, rootRel, err := safeio.OpenExistingCanonicalWriteRoot(rootPath, true)
	if err != nil {
		return "", nil, err
	}
	defer func() {
		if closeErr := ancestorRoot.Close(); closeErr != nil {
			returnErr = errors.Join(returnErr, closeErr)
		}
	}()
	if rootRel != "." {
		if err := ancestorRoot.EnsureDir(rootRel, 0o750); err != nil {
			return "", nil, err
		}
	}
	pinnedPath := ancestorRoot.CanonicalPath()
	if rootRel != "." {
		pinnedPath = filepath.Join(pinnedPath, rootRel)
	}
	keysRel := filepath.Join(rootRel, "keys")
	objectsRel := filepath.Join(rootRel, "objects")
	if rootRel == "." {
		keysRel = "keys"
		objectsRel = "objects"
	}
	if err := ancestorRoot.EnsureDir(keysRel, 0o750); err != nil {
		return "", nil, err
	}
	if err := cacheInitBeforeObjectsEnsureFn(); err != nil {
		return "", nil, err
	}
	if err := ancestorRoot.EnsureDir(objectsRel, 0o750); err != nil {
		return "", nil, err
	}
	info, err := ancestorRoot.Lstat(rootRel)
	if err != nil {
		return "", nil, err
	}
	return pinnedPath, info, nil
}

func (c *analysisCache) pinnedWritePath() string {
	if strings.TrimSpace(c.writeRootPath) != "" {
		return c.writeRootPath
	}
	return c.options.writePath()
}

func (c *analysisCache) validateWriteRoot() error {
	if c == nil || c.writeRootInfo == nil {
		return nil
	}
	if c.writeRootOpener != nil {
		return nil
	}
	writePath := c.pinnedWritePath()
	info, err := os.Lstat(writePath)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !os.SameFile(c.writeRootInfo, info) {
		return fmt.Errorf("cache root changed while pinned: %s", writePath)
	}
	return nil
}

func pinTrustedCachePath(repoPath, cachePath, field string) (*trustedCachePin, error) {
	if !validTrustedCacheCandidate(cachePath) {
		return nil, trustedCachePathError(field, trustedCachePathValidationSymlinkEscape)
	}
	repository, err := ResolveTrustedRepository(repoPath)
	if err != nil {
		return nil, err
	}
	return pinTrustedCachePathForRepository(repository, cachePath, field)
}

func pinTrustedCachePathForRepository(repository *RepositoryAuthorization, cachePath, field string) (*trustedCachePin, error) {
	state := repository.authorizationState()
	if state == nil || !validTrustedCacheCandidate(cachePath) {
		return nil, trustedCachePathError(field, trustedCachePathValidationSymlinkEscape)
	}
	repoPaths := state.paths
	candidatePath := normalizeTrustedCacheCandidate(repoPaths.requestedPath, cachePath)
	lexicallyInRepo := pathWithinTrustedRepresentations(repoPaths, candidatePath)
	resolution, err := resolveTrustedCacheCandidate(repoPaths, candidatePath)
	if err != nil {
		return nil, err
	}
	if resolution.escapedRepository {
		return nil, trustedCachePathError(field, trustedCachePathValidationSymlinkEscape)
	}
	canonicalPath := resolution.path
	if !pathWithinDir(repoPaths.canonicalPath, canonicalPath) {
		if lexicallyInRepo || resolution.enteredRepository {
			return nil, trustedCachePathError(field, trustedCachePathValidationSymlinkEscape)
		}
		pin := newTrustedCachePin(trustedCachePathExternal, repository, canonicalPath, "")
		return pin, trustedExternalCachePathError(field, pin)
	}
	relativePath, err := filepath.Rel(repoPaths.canonicalPath, canonicalPath)
	if err != nil {
		return nil, trustedCachePathError(field, trustedCachePathValidationSymlinkEscape)
	}
	return newTrustedCachePin(trustedCachePathInRepo, repository, canonicalPath, relativePath), nil
}

func newTrustedCachePin(kind trustedCachePathKind, repository *RepositoryAuthorization, canonicalPath, relativePath string) *trustedCachePin {
	if relativePath != "" {
		relativePath = filepath.Clean(relativePath)
	}
	return &trustedCachePin{
		kind:             kind,
		repositoryState:  repository.authorizationState(),
		canonicalPath:    filepath.Clean(canonicalPath),
		repoRelativePath: relativePath,
	}
}

type trustedCacheCandidateResolution struct {
	path              string
	enteredRepository bool
	escapedRepository bool
	missingPath       bool
	symlinkExpansions int
}

const maxTrustedCacheSymlinkExpansions = 255

func resolveTrustedCacheCandidate(repoPaths trustedRepoPaths, candidatePath string) (trustedCacheCandidateResolution, error) {
	return resolveTrustedAbsolutePath(filepath.Clean(candidatePath), repoPaths, 0)
}

func resolveTrustedAbsolutePath(path string, repoPaths trustedRepoPaths, symlinkExpansions int) (trustedCacheCandidateResolution, error) {
	if symlinkExpansions > maxTrustedCacheSymlinkExpansions {
		return trustedCacheCandidateResolution{}, errors.New("too many symlinks while resolving cachePath")
	}
	volumeRoot := filepath.VolumeName(path) + string(os.PathSeparator)
	relativePath, err := filepath.Rel(volumeRoot, path)
	if err != nil {
		return trustedCacheCandidateResolution{}, err
	}
	walk := trustedCachePathWalk{
		repoPaths:         repoPaths,
		segments:          strings.Split(relativePath, string(filepath.Separator)),
		currentPath:       volumeRoot,
		enteredRepository: pathWithinTrustedRepresentations(repoPaths, volumeRoot),
		symlinkExpansions: symlinkExpansions,
	}
	for index, segment := range walk.segments {
		resolution, done, err := walk.resolveSegment(index, segment)
		if err != nil || done {
			return resolution, err
		}
	}
	return trustedCacheCandidateResolution{
		path:              filepath.Clean(walk.currentPath),
		enteredRepository: walk.enteredRepository,
		symlinkExpansions: walk.symlinkExpansions,
	}, nil
}

type trustedCachePathWalk struct {
	repoPaths         trustedRepoPaths
	segments          []string
	currentPath       string
	enteredRepository bool
	symlinkExpansions int
}

func (w *trustedCachePathWalk) resolveSegment(index int, segment string) (trustedCacheCandidateResolution, bool, error) {
	if segment == "" || segment == "." {
		return trustedCacheCandidateResolution{}, false, nil
	}
	nextPath := filepath.Join(w.currentPath, segment)
	info, err := os.Lstat(nextPath)
	if errors.Is(err, os.ErrNotExist) {
		return w.missingPathResolution(index, nextPath), true, nil
	}
	if err != nil {
		return trustedCacheCandidateResolution{}, false, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return w.resolveSymlink(nextPath)
	}
	w.currentPath = nextPath
	w.enteredRepository = w.enteredRepository || pathWithinTrustedRepresentations(w.repoPaths, nextPath)
	return trustedCacheCandidateResolution{}, false, nil
}

func (w *trustedCachePathWalk) missingPathResolution(index int, nextPath string) trustedCacheCandidateResolution {
	resolvedPath := filepath.Join(append([]string{nextPath}, w.segments[index+1:]...)...)
	return trustedCacheCandidateResolution{
		path:              resolvedPath,
		enteredRepository: w.enteredRepository || pathWithinTrustedRepresentations(w.repoPaths, resolvedPath),
		missingPath:       true,
		symlinkExpansions: w.symlinkExpansions,
	}
}

func (w *trustedCachePathWalk) resolveSymlink(path string) (trustedCacheCandidateResolution, bool, error) {
	linkTarget, err := os.Readlink(path)
	if err != nil {
		return trustedCacheCandidateResolution{}, false, err
	}
	if !filepath.IsAbs(linkTarget) {
		linkTarget = filepath.Join(filepath.Dir(path), linkTarget)
	}
	target, err := resolveTrustedAbsolutePath(filepath.Clean(linkTarget), w.repoPaths, w.symlinkExpansions+1)
	if err != nil {
		return trustedCacheCandidateResolution{}, false, err
	}
	targetInside := pathWithinTrustedRepresentations(w.repoPaths, target.path)
	if target.missingPath || target.escapedRepository || w.enteredRepository && !targetInside {
		target.escapedRepository = true
		return target, true, nil
	}
	w.enteredRepository = w.enteredRepository || target.enteredRepository || targetInside
	w.symlinkExpansions = target.symlinkExpansions
	w.currentPath = target.path
	return trustedCacheCandidateResolution{}, false, nil
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
	_, err := pinTrustedCachePath(repoPath, cachePath, field)
	return err
}

func pathWithinTrustedRoot(repoPath, candidatePath string) bool {
	if !validTrustedCacheCandidate(candidatePath) {
		return false
	}
	pin, err := pinTrustedCachePath(repoPath, candidatePath, "cachePath")
	return err == nil && pin != nil && pin.kind == trustedCachePathInRepo
}

type trustedRepoPaths struct {
	requestedPath string
	canonicalPath string
	canonicalInfo fs.FileInfo
}

func resolveTrustedRepoPaths(repoPath string) (trustedRepoPaths, error) {
	requestedPath, err := normalizeTrustedRepoPath(repoPath)
	if err != nil {
		return trustedRepoPaths{}, err
	}
	canonicalPath, err := resolvePathWithinExistingTree(requestedPath)
	if err != nil {
		return trustedRepoPaths{}, err
	}
	canonicalPath = filepath.Clean(canonicalPath)
	canonicalInfo, err := os.Lstat(canonicalPath)
	if err != nil {
		return trustedRepoPaths{}, fmt.Errorf("stat canonical repoPath: %w", err)
	}
	if canonicalInfo.Mode()&os.ModeSymlink != 0 || !canonicalInfo.IsDir() {
		return trustedRepoPaths{}, errors.New("repoPath must resolve to a directory")
	}
	return trustedRepoPaths{
		requestedPath: filepath.Clean(requestedPath),
		canonicalPath: canonicalPath,
		canonicalInfo: canonicalInfo,
	}, nil
}

func normalizeTrustedCacheCandidate(requestedRepoPath, candidatePath string) string {
	candidatePath = strings.TrimSpace(candidatePath)
	if !filepath.IsAbs(candidatePath) {
		candidatePath = filepath.Join(requestedRepoPath, candidatePath)
	}
	return filepath.Clean(candidatePath)
}

func validTrustedCacheCandidate(candidatePath string) bool {
	return !strings.ContainsRune(candidatePath, '\x00')
}

func pathWithinTrustedRepresentations(repoPaths trustedRepoPaths, candidatePath string) bool {
	return pathWithinDir(repoPaths.requestedPath, candidatePath) ||
		pathWithinDir(repoPaths.canonicalPath, candidatePath)
}

func validateNoSymlinkEscape(repoPath, candidatePath, field string) error {
	return validateTrustedCachePath(repoPath, candidatePath, field)
}

func trustedCachePathError(field string, kind trustedCachePathValidationKind) error {
	return &trustedCachePathValidationError{
		field: field,
		kind:  kind,
	}
}

func trustedExternalCachePathError(field string, pin *trustedCachePin) error {
	return &trustedCachePathValidationError{
		field:      field,
		kind:       trustedCachePathValidationExternal,
		trustedPin: pin,
	}
}

type trustedCachePathValidationError struct {
	field      string
	kind       trustedCachePathValidationKind
	trustedPin *trustedCachePin
}

func (e *trustedCachePathValidationError) Error() string {
	return fmt.Sprintf("%s must stay within repoPath", e.field)
}

func trustedCachePathErrorHasKind(err error, kind trustedCachePathValidationKind) bool {
	var validationErr *trustedCachePathValidationError
	return errors.As(err, &validationErr) && validationErr.kind == kind
}

func pathWithinDir(root, child string) bool {
	rel, err := filepath.Rel(root, child)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}
