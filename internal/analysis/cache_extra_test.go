package analysis

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/safeio"
)

const (
	cacheDirName          = ".lopper-cache"
	cacheKeysDirName      = "keys"
	cacheObjectsDirName   = "objects"
	cacheTestGoModName    = "go.mod"
	cacheTestGoModContent = "module demo\n"
	cacheMissingFileName  = "missing.txt"
)

type analysisCacheLookupCase struct {
	name         string
	setup        func(*testing.T)
	wantReason   string
	wantHit      bool
	wantRepoPath string
}

type cacheLookupRootSwapCase struct {
	name         string
	swapOnRead   int
	outsideRepo  string
	outsideObjID string
}

func TestAnalysisCacheWarningLifecycleAndSnapshot(t *testing.T) {
	cache := &analysisCache{
		metadata: report.CacheMetadata{Invalidations: []report.CacheInvalidation{{Key: "k", Reason: "reason"}}},
		warnings: []string{},
	}

	cache.warn("")
	cache.warn("cache warning")
	warnings := cache.takeWarnings()
	if len(warnings) != 1 || warnings[0] != "cache warning" {
		t.Fatalf("unexpected warnings: %#v", warnings)
	}
	if len(cache.takeWarnings()) != 0 {
		t.Fatalf("expected warnings to be drained")
	}

	snapshot := cache.metadataSnapshot()
	if snapshot == nil || len(snapshot.Invalidations) != 1 {
		t.Fatalf("unexpected snapshot: %#v", snapshot)
	}
	cache.metadata.Invalidations[0].Reason = "mutated"
	if snapshot.Invalidations[0].Reason == "mutated" {
		t.Fatalf("expected snapshot invalidations to be copied")
	}

	var nilCache *analysisCache
	if nilCache.metadataSnapshot() != nil {
		t.Fatalf("expected nil snapshot for nil cache")
	}
}

func TestNewAnalysisCacheUnavailablePathAddsWarning(t *testing.T) {
	repo := t.TempDir()
	blockingPath := filepath.Join(repo, "not-a-dir")
	if err := os.WriteFile(blockingPath, []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocking file: %v", err)
	}

	cache := newAnalysisCache(Request{Cache: &CacheOptions{Enabled: true, Path: blockingPath}}, repo)
	if cache.cacheable {
		t.Fatalf("expected cache to be non-cacheable when path is invalid")
	}
	if len(cache.takeWarnings()) == 0 {
		t.Fatalf("expected warning when cache directory init fails")
	}
}

func TestNewAnalysisCacheObjectsDirInitFailureAddsWarning(t *testing.T) {
	repo := t.TempDir()
	cachePath := filepath.Join(repo, cacheDirName)
	keysDir := filepath.Join(cachePath, cacheKeysDirName)
	objectsPath := filepath.Join(cachePath, cacheObjectsDirName)
	if err := os.MkdirAll(keysDir, 0o750); err != nil {
		t.Fatalf("mkdir keys dir: %v", err)
	}
	if err := os.WriteFile(objectsPath, []byte("not-a-directory"), 0o600); err != nil {
		t.Fatalf("write blocking objects file: %v", err)
	}

	cache := newAnalysisCache(Request{Cache: &CacheOptions{Enabled: true, Path: cachePath}}, repo)
	if cache.cacheable {
		t.Fatalf("expected cache to be non-cacheable when objects dir init fails")
	}
	if len(cache.takeWarnings()) == 0 {
		t.Fatalf("expected warning when objects dir init fails")
	}
}

func TestNewAnalysisCacheRejectsSymlinkedDefaultPathOutsideRepo(t *testing.T) {
	repo := t.TempDir()
	outside := t.TempDir()
	assertSymlinkedDefaultCachePathRejected(t, repo, outside, "symlinked")
}

func TestNewAnalysisCacheRejectsBrokenSymlinkedDefaultPathOutsideRepo(t *testing.T) {
	repo := t.TempDir()
	outside := filepath.Join(t.TempDir(), "missing-target")
	assertSymlinkedDefaultCachePathRejected(t, repo, outside, "broken symlinked")
}

func TestResolveTrustedCacheOptionsRejectsPathOutsideRepo(t *testing.T) {
	repo := t.TempDir()
	outside := filepath.Join(t.TempDir(), "cache")

	options, err := ResolveTrustedCacheOptions(repo, &CacheOptions{
		Enabled: true,
		Path:    outside,
	})
	if !AuthenticatedExternalCacheOptions(options, err) {
		t.Fatalf("expected rejected options to carry an authenticated external pin, options=%#v err=%v", options, err)
	}
	if err == nil || !strings.Contains(err.Error(), "cachePath must stay within repoPath") {
		t.Fatalf("expected outside path rejection, got %v", err)
	}
}

func TestResolveTrustedCacheOptionsRejectsSymlinkAncestorEscape(t *testing.T) {
	repo := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(repo, "tmp")); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	options, err := ResolveTrustedCacheOptions(repo, &CacheOptions{
		Enabled: true,
		Path:    filepath.Join("tmp", "cache"),
	})
	if options != nil {
		t.Fatalf("expected rejected options to remain nil, got %#v", options)
	}
	if err == nil || !strings.Contains(err.Error(), "cachePath must stay within repoPath") {
		t.Fatalf("expected symlink ancestor rejection, got %v", err)
	}
	if !CachePathSymlinkEscape(err) {
		t.Fatalf("expected symlink escape sentinel, got %v", err)
	}
}

func TestResolveTrustedCacheOptionsRejectsAbsoluteSymlinkAncestorEscape(t *testing.T) {
	repo := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(repo, "tmp")); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	options, err := ResolveTrustedCacheOptions(repo, &CacheOptions{
		Enabled: true,
		Path:    filepath.Join(repo, "tmp", "cache"),
	})
	if options != nil {
		t.Fatalf("expected rejected options to remain nil, got %#v", options)
	}
	if err == nil || !strings.Contains(err.Error(), "cachePath must stay within repoPath") {
		t.Fatalf("expected absolute symlink ancestor rejection, got %v", err)
	}
	if !CachePathSymlinkEscape(err) {
		t.Fatalf("expected absolute symlink escape sentinel, got %v", err)
	}
}

func TestResolveTrustedCacheOptionsRejectsCanonicalSymlinkEscapeUnderRequestedRepoAlias(t *testing.T) {
	requestedRepo, canonicalCache, outsideCache := canonicalAliasCacheEscapeFixture(t)

	options, err := ResolveTrustedCacheOptions(requestedRepo, &CacheOptions{
		Enabled: true,
		Path:    canonicalCache,
	})
	if options != nil {
		t.Fatalf("expected canonical-form symlink escape rejection, got %#v", options)
	}
	if !CachePathSymlinkEscape(err) {
		t.Fatalf("expected symlink escape sentinel, got %v", err)
	}
	if CachePathExternal(err) {
		t.Fatalf("expected in-repo canonical form not to be classified as external, got %v", err)
	}
	if _, statErr := os.Stat(outsideCache); !os.IsNotExist(statErr) {
		t.Fatalf("expected no external cache directory, stat err=%v", statErr)
	}
}

func TestResolveTrustedCacheOptionsRejectsAlternateAbsoluteRepoAliasesThatLaterEscape(t *testing.T) {
	for _, fixture := range alternateAbsoluteRepoAliasEscapeFixtures(t) {
		t.Run(fixture.name, func(t *testing.T) {
			options, err := ResolveTrustedCacheOptions(fixture.requestedRepo, &CacheOptions{
				Enabled: true,
				Path:    fixture.cachePath,
			})
			if options != nil {
				t.Fatalf("expected alternate-alias escape rejection, got %#v", options)
			}
			if !CachePathSymlinkEscape(err) || CachePathExternal(err) {
				t.Fatalf("expected authenticated symlink escape classification, got %v", err)
			}
			if _, statErr := os.Stat(fixture.outsideCache); !os.IsNotExist(statErr) {
				t.Fatalf("expected no outside cache directory, stat err=%v", statErr)
			}
		})
	}
}

func TestResolveTrustedCacheOptionsClassifiesExternalAliasesIntoRepoWithoutMutation(t *testing.T) {
	repo := t.TempDir()
	cacheSubdir := filepath.Join(repo, ".cache", "lopper")
	if err := os.MkdirAll(cacheSubdir, 0o750); err != nil {
		t.Fatalf("mkdir cache subdir: %v", err)
	}

	for _, tc := range []struct {
		name         string
		target       string
		wantRelative string
	}{
		{name: "repo root", target: repo, wantRelative: "."},
		{name: "repo subdir", target: cacheSubdir, wantRelative: filepath.Join(".cache", "lopper")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			aliasPath := filepath.Join(t.TempDir(), "external-cache-alias")
			if err := os.Symlink(tc.target, aliasPath); err != nil {
				t.Skipf("symlink not supported: %v", err)
			}

			options, err := ResolveTrustedCacheOptions(repo, &CacheOptions{
				Enabled: true,
				Path:    aliasPath,
			})
			if err != nil {
				t.Fatalf("resolve external alias into repo: %v", err)
			}
			if !InRepoCacheOptions(options) {
				t.Fatalf("expected external alias to mint an in-repo pin, got %#v", options)
			}
			if options.trustedPin.repoRelativePath != tc.wantRelative {
				t.Fatalf("expected repo-relative pin %q, got %q", tc.wantRelative, options.trustedPin.repoRelativePath)
			}
			assertCacheLayoutAbsent(t, tc.target)
		})
	}
}

func TestTrustedCacheClassificationRejectsForgedErrorsAndMutablePathRebinding(t *testing.T) {
	repo := t.TempDir()
	outside := filepath.Join(t.TempDir(), "cache")
	options, err := ResolveTrustedCacheOptions(repo, &CacheOptions{
		Enabled: true,
		Path:    outside,
	})
	if !AuthenticatedExternalCacheOptions(options, err) {
		t.Fatalf("expected genuine external classification, options=%#v err=%v", options, err)
	}
	wrappedErr := fmt.Errorf("wrapped external classification: %w", err)
	if !CachePathExternal(wrappedErr) || !AuthenticatedExternalCacheOptions(options, wrappedErr) {
		t.Fatalf("expected wrapped authenticated classification to remain recognizable, err=%v", wrappedErr)
	}

	lookalike := errors.New("trusted cache path is external to repoPath")
	if CachePathExternal(lookalike) {
		t.Fatalf("expected matching mutable error text not to authorize an external cache")
	}
	if AuthenticatedExternalCacheOptions(options, lookalike) {
		t.Fatalf("expected a forged classification error not to authenticate options")
	}

	repoAlias := filepath.Join(t.TempDir(), "repo-alias")
	if err := os.Symlink(repo, repoAlias); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	options.Path = repoAlias
	if InRepoCacheOptions(options) {
		t.Fatalf("expected exported path mutation not to reclassify the opaque external pin")
	}
	canonicalOutside, resolveErr := resolvePathWithinExistingTree(outside)
	if resolveErr != nil {
		t.Fatalf("resolve expected external pin: %v", resolveErr)
	}
	if options.trustedPinnedPath() != canonicalOutside {
		t.Fatalf("expected opaque pin to remain bound to %q, got %q", canonicalOutside, options.trustedPinnedPath())
	}
	assertCacheLayoutAbsent(t, repo)
}

func TestResolveTrustedCacheOptionsBypassesNilDisabledAndEmptyPath(t *testing.T) {
	repo := t.TempDir()

	options, err := ResolveTrustedCacheOptions(repo, nil)
	if err != nil || options != nil {
		t.Fatalf("expected nil cache options to bypass unchanged, got %#v err=%v", options, err)
	}

	disabled, err := ResolveTrustedCacheOptions(repo, &CacheOptions{
		Enabled: false,
		Path:    filepath.Join(t.TempDir(), "cache"),
	})
	if err != nil {
		t.Fatalf("resolve disabled cache options: %v", err)
	}
	if disabled == nil || disabled.Enabled || disabled.Path == "" {
		t.Fatalf("expected disabled cache options to round-trip, got %#v", disabled)
	}

	emptyPath, err := ResolveTrustedCacheOptions(repo, &CacheOptions{Enabled: true})
	if err != nil {
		t.Fatalf("resolve empty-path cache options: %v", err)
	}
	if emptyPath == nil || emptyPath.Path != "" || emptyPath.hasTrustedPin() {
		t.Fatalf("expected empty cache path to preserve default-path behavior, got %#v", emptyPath)
	}
}

func TestTrustedCacheBoundaryGuardBranches(t *testing.T) {
	repo := t.TempDir()
	repository, err := ResolveTrustedRepository(repo)
	if err != nil {
		t.Fatalf("authorize repository: %v", err)
	}
	assertTrustedCacheNilAndInvalidGuards(t, repo, repository)
	cacheOptions := assertTrustedCacheOptionReuse(t, repo, repository)
	assertTrustedCacheRepositoryMismatch(t, repo, cacheOptions)
	assertTrustedDefaultCacheSymlinkRejection(t, repo, repository)
}

func assertTrustedCacheNilAndInvalidGuards(t *testing.T, repo string, repository *RepositoryAuthorization) {
	t.Helper()
	if CachePathExternal(nil) || CachePathSymlinkEscape(nil) {
		t.Fatal("nil errors must not carry trusted cache classifications")
	}
	if AuthenticatedExternalCacheOptions(nil, errors.New("external")) {
		t.Fatal("nil cache options must not authenticate an external classification")
	}
	if (*RepositoryAuthorization)(nil).matchesPath(repo) {
		t.Fatal("nil repository authorization must not match a path")
	}
	if _, err := ResolveTrustedDefaultCacheOptionsForRepository(nil, false); err == nil {
		t.Fatal("expected missing repository authorization rejection")
	}
	if _, err := ResolveTrustedDefaultCacheOptions(filepath.Join(repo, "missing"), false); err == nil {
		t.Fatal("expected missing repository rejection for default cache options")
	}
	if _, err := ResolveTrustedCacheOptions(filepath.Join(repo, "missing"), &CacheOptions{Enabled: true, Path: ".cache"}); err == nil {
		t.Fatal("expected missing repository rejection for explicit cache options")
	}
	if options, err := ResolveTrustedCacheOptionsForRepository(repository, nil); err != nil || options != nil {
		t.Fatalf("nil cache options = %#v, %v; want nil, nil", options, err)
	}
	if _, err := ResolveTrustedCacheOptionsForRepository(nil, &CacheOptions{Enabled: true}); err == nil {
		t.Fatal("expected cache resolution without repository authorization to fail")
	}
	if _, err := useTrustedCacheOptions(repo, nil); err == nil {
		t.Fatal("expected unauthenticated trusted-cache use to fail")
	}
	if _, err := pinTrustedCachePathForRepository(nil, filepath.Join(repo, "cache"), "cachePath"); !CachePathSymlinkEscape(err) {
		t.Fatalf("expected nil repository pin attempt to be a symlink-escape rejection, got %v", err)
	}
	repoPaths := trustedRepoPaths{requestedPath: repo, canonicalPath: repo}
	if _, err := resolveTrustedAbsolutePath(repo, repoPaths, maxTrustedCacheSymlinkExpansions+1); err == nil {
		t.Fatal("expected excessive cache-path symlink expansion rejection")
	}
	if _, err := resolveTrustedAbsolutePath(string([]byte{0}), repoPaths, 0); err == nil {
		t.Fatal("expected invalid absolute cache-path resolution to fail")
	}
}

func assertTrustedCacheOptionReuse(t *testing.T, repo string, repository *RepositoryAuthorization) *CacheOptions {
	t.Helper()
	cacheOptions, err := ResolveTrustedCacheOptionsForRepository(repository, &CacheOptions{Enabled: true, Path: filepath.Join(".cache", "lopper")})
	if err != nil {
		t.Fatalf("resolve in-repository cache options: %v", err)
	}
	reused, err := ResolveTrustedCacheOptions(repo, cacheOptions)
	if err != nil || reused.trustedPin != cacheOptions.trustedPin {
		t.Fatalf("reuse trusted cache options: options=%#v err=%v", reused, err)
	}
	reused, err = ResolveTrustedCacheOptionsForRepository(repository, cacheOptions)
	if err != nil || reused.trustedPin != cacheOptions.trustedPin {
		t.Fatalf("reuse trusted cache options at repository boundary: options=%#v err=%v", reused, err)
	}
	return cacheOptions
}

func assertTrustedCacheRepositoryMismatch(t *testing.T, repo string, cacheOptions *CacheOptions) {
	t.Helper()
	otherRepository, err := ResolveTrustedRepository(t.TempDir())
	if err != nil {
		t.Fatalf("authorize second repository: %v", err)
	}
	if _, err := ResolveTrustedCacheOptionsForRepository(otherRepository, cacheOptions); err == nil {
		t.Fatal("expected cache pin/repository authorization mismatch")
	}
	if _, err := ResolveTrustedCacheOptions(repo, cacheOptions); err != nil {
		t.Fatalf("reuse trusted cache options for matching path: %v", err)
	}
	if _, err := ResolveTrustedCacheOptions(TrustedRepositoryPath(otherRepository), cacheOptions); err == nil {
		t.Fatal("expected trusted cache options to reject a different repository path")
	}
}

func assertTrustedDefaultCacheSymlinkRejection(t *testing.T, repo string, repository *RepositoryAuthorization) {
	t.Helper()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(repo, defaultAnalysisCacheDirName)); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	if _, err := ResolveTrustedDefaultCacheOptionsForRepository(repository, false); !CachePathSymlinkEscape(err) {
		t.Fatalf("expected symlinked default cache rejection, got %v", err)
	}
}

func TestTrustedCacheIdentityNormalizesRelativeCandidatesAndRejectsInvalidRepositories(t *testing.T) {
	repo := t.TempDir()
	if got := normalizeTrustedCacheCandidate(repo, filepath.Join(".cache", "lopper")); got != filepath.Join(repo, ".cache", "lopper") {
		t.Fatalf("normalized relative cache path = %q", got)
	}
	if _, err := resolveTrustedRepoPaths(string([]byte{0})); err == nil {
		t.Fatal("expected invalid repository path rejection")
	}

	brokenLink := filepath.Join(t.TempDir(), "broken-repository")
	if err := os.Symlink("missing-target", brokenLink); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	if _, err := resolveTrustedRepoPaths(brokenLink); err == nil {
		t.Fatal("expected broken repository symlink rejection")
	}
}

func TestRepositoryBackedCacheLayoutUsesRetainedRootAndFailsClosed(t *testing.T) {
	repo := t.TempDir()
	repository, err := ResolveTrustedRepository(repo)
	if err != nil {
		t.Fatalf("authorize repository: %v", err)
	}
	cacheOptions, err := ResolveTrustedCacheOptionsForRepository(repository, &CacheOptions{
		Enabled: true,
		Path:    filepath.Join(".cache", "lopper"),
	})
	if err != nil {
		t.Fatalf("resolve in-repository cache options: %v", err)
	}
	view, err := OpenTrustedRepository(context.Background(), repository, repo, cacheOptions)
	if err != nil {
		t.Fatalf("open trusted repository: %v", err)
	}

	expectedHookErr := errors.New("objects init blocked")
	previousHook := cacheInitBeforeObjectsEnsureFn
	cacheInitBeforeObjectsEnsureFn = func() error { return expectedHookErr }
	t.Cleanup(func() {
		cacheInitBeforeObjectsEnsureFn = previousHook
	})
	cache := newAnalysisCache(Request{RepoPath: repo, Repository: repository, Cache: cacheOptions}, view.ExecutionPath(), view)
	if cache.cacheable || len(cache.takeWarnings()) == 0 {
		t.Fatal("expected retained-root cache initialization hook failure")
	}
	cacheRoot := filepath.Join(repo, ".cache", "lopper")
	if _, err := os.Stat(filepath.Join(cacheRoot, cacheKeysDirName)); err != nil {
		t.Fatalf("expected keys directory before hook failure: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cacheRoot, cacheObjectsDirName)); !os.IsNotExist(err) {
		t.Fatalf("expected objects directory to remain absent, got %v", err)
	}

	cacheInitBeforeObjectsEnsureFn = previousHook
	cache = newAnalysisCache(Request{RepoPath: repo, Repository: repository, Cache: cacheOptions}, view.ExecutionPath(), view)
	if !cache.cacheable || cache.writeRootOpener == nil {
		t.Fatalf("expected retained-root cache initialization success, warnings=%#v", cache.takeWarnings())
	}
	root, err := cache.openPinnedWriteRoot()
	if err != nil {
		t.Fatalf("open retained cache root: %v", err)
	}
	if err := root.Close(); err != nil {
		t.Fatalf("close retained cache root: %v", err)
	}

	if err := view.Close(); err != nil {
		t.Fatalf("close trusted repository: %v", err)
	}
	cache = newAnalysisCache(Request{RepoPath: repo, Repository: repository, Cache: cacheOptions}, repo, view)
	if cache.cacheable {
		t.Fatal("expected closed retained root to reject cache initialization")
	}

	var nilCache *analysisCache
	nilCache.configureStablePaths(Request{})
	if nilCache.validateDefaultWritePath(Request{}, repo) {
		t.Fatal("nil cache must not validate a default write path")
	}
}

func TestTrustedCanonicalCacheLayoutRejectsBlockingEntries(t *testing.T) {
	for _, entry := range []string{cacheKeysDirName, cacheObjectsDirName} {
		t.Run(entry, func(t *testing.T) {
			root := mustEvalSymlinks(t, t.TempDir())
			if entry == cacheObjectsDirName {
				if err := os.Mkdir(filepath.Join(root, cacheKeysDirName), 0o750); err != nil {
					t.Fatalf("create keys directory: %v", err)
				}
			}
			writeFile(t, filepath.Join(root, entry), "blocking file\n")
			if _, _, err := ensureTrustedPinnedCacheLayout(root); err == nil {
				t.Fatalf("expected blocking %s entry rejection", entry)
			}
		})
	}
}

func TestAnalysisCachePinnedWriteRootGuardBranches(t *testing.T) {
	var nilCache *analysisCache
	if _, err := nilCache.openPinnedWriteRoot(); err == nil {
		t.Fatal("expected nil analysis cache rejection")
	}

	expectedOpenErr := errors.New("retained cache root unavailable")
	cache := &analysisCache{
		writeRootOpener: func() (*safeio.WriteRoot, error) {
			return nil, expectedOpenErr
		},
	}
	if _, err := cache.openPinnedWriteRoot(); !errors.Is(err, expectedOpenErr) {
		t.Fatalf("expected retained cache-root opener error, got %v", err)
	}

	rootA := mustEvalSymlinks(t, t.TempDir())
	rootB := mustEvalSymlinks(t, t.TempDir())
	rootAInfo, err := os.Lstat(rootA)
	if err != nil {
		t.Fatalf("stat root A: %v", err)
	}
	cache = &analysisCache{
		writeRootInfo: rootAInfo,
		writeRootOpener: func() (*safeio.WriteRoot, error) {
			return safeio.OpenCanonicalWriteRoot(rootB)
		},
	}
	if _, err := cache.openPinnedWriteRoot(); err == nil || !strings.Contains(err.Error(), "cache root changed while pinned") {
		t.Fatalf("expected retained cache-root identity mismatch, got %v", err)
	}

	closedRoot, err := safeio.OpenCanonicalWriteRoot(rootA)
	if err != nil {
		t.Fatalf("open root to close: %v", err)
	}
	if err := closedRoot.Close(); err != nil {
		t.Fatalf("close root before validation: %v", err)
	}
	if err := cache.validateOpenedWriteRoot(closedRoot); err == nil {
		t.Fatal("expected closed cache-root validation failure")
	}
}

func TestAnalysisCacheStableRootAndDefaultValidationBranches(t *testing.T) {
	repo := t.TempDir()
	cache := &analysisCache{
		options:           resolvedCacheOptions{WritePath: filepath.Join(repo, defaultAnalysisCacheDirName)},
		analysisRootPath:  repo,
		stableKeyRepoPath: filepath.Join(string(os.PathSeparator), "stable", "repository"),
	}
	if !cache.validateDefaultWritePath(Request{}, repo) {
		t.Fatal("expected safe missing default cache path")
	}
	outside := t.TempDir()
	if got := cache.stableCacheRoot(outside); got != outside {
		t.Fatalf("outside stable cache root = %q, want %q", got, outside)
	}
}

func TestResolveCacheOptionsUsesPinnedWritePath(t *testing.T) {
	cacheOptions := &CacheOptions{
		Enabled:    true,
		Path:       filepath.Join(".cache", "lopper"),
		ReadOnly:   true,
		trustedPin: &trustedCachePin{canonicalPath: "/trusted/cache"},
	}
	options := resolveCacheOptions(cacheOptions, "/repo")
	if options.Path != filepath.Join(".cache", "lopper") {
		t.Fatalf("expected display path to remain unchanged, got %#v", options)
	}
	if options.WritePath != "/trusted/cache" || !options.ReadOnly {
		t.Fatalf("expected pinned write path to be preferred, got %#v", options)
	}
}

func TestResolveCacheOptionsUsesRelativeWritePath(t *testing.T) {
	options := resolveCacheOptions(&CacheOptions{Enabled: true, Path: filepath.Join(".cache", "lopper")}, "/repo")
	if options.WritePath != filepath.Join("/repo", ".cache", "lopper") {
		t.Fatalf("expected repository-relative cache write path, got %#v", options)
	}
}

func TestResolveCacheOptionsUsesAbsolutePathAsWritePath(t *testing.T) {
	options := resolveCacheOptions(&CacheOptions{Enabled: true, Path: "/trusted/cache"}, "/repo")
	if options.WritePath != filepath.Clean("/trusted/cache") {
		t.Fatalf("expected absolute cache path to be used as write path, got %#v", options)
	}
}

func TestValidateTrustedCachePathAllowsEmptyPath(t *testing.T) {
	if err := validateTrustedCachePath("/repo", "", "cachePath"); err != nil {
		t.Fatalf("expected empty cache path to be allowed, got %v", err)
	}
}

func TestPathWithinDirRejectsTraversal(t *testing.T) {
	if pathWithinDir("/repo", "/outside") {
		t.Fatalf("expected outside path to be rejected")
	}
}

func TestResolveTrustedCacheOptionsPinsRelativePathWithoutRewritingUserValue(t *testing.T) {
	repo := t.TempDir()
	cacheOptions, err := ResolveTrustedCacheOptions(repo, &CacheOptions{
		Enabled: true,
		Path:    filepath.Join(".cache", "lopper"),
	})
	if err != nil {
		t.Fatalf("resolve trusted cache options: %v", err)
	}
	if cacheOptions.Path != filepath.Join(".cache", "lopper") {
		t.Fatalf("expected user path to remain relative, got %q", cacheOptions.Path)
	}
	expectedPinnedPath, err := resolvePathWithinExistingTree(filepath.Join(repo, ".cache", "lopper"))
	if err != nil {
		t.Fatalf("resolve expected pinned path: %v", err)
	}
	if cacheOptions.trustedPinnedPath() != expectedPinnedPath {
		t.Fatalf("expected pinned path inside repo, got %q", cacheOptions.trustedPinnedPath())
	}
}

func TestResolveTrustedCacheOptionsPinsRelativePathFromCanonicalDotRepoPath(t *testing.T) {
	repo := t.TempDir()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(repo); err != nil {
		t.Fatalf("chdir repo: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(cwd); err != nil {
			t.Errorf("restore cwd: %v", err)
		}
	})

	cacheOptions, err := ResolveTrustedCacheOptions(".", &CacheOptions{
		Enabled: true,
		Path:    filepath.Join(".cache", "lopper"),
	})
	if err != nil {
		t.Fatalf("resolve trusted cache options from dot repo path: %v", err)
	}
	expectedPinnedPath, err := resolvePathWithinExistingTree(filepath.Join(repo, ".cache", "lopper"))
	if err != nil {
		t.Fatalf("resolve expected pinned path: %v", err)
	}
	if cacheOptions.trustedPinnedPath() != expectedPinnedPath {
		t.Fatalf("expected canonical pinned path %q, got %q", expectedPinnedPath, cacheOptions.trustedPinnedPath())
	}
}

func TestResolveTrustedCacheOptionsPinsCanonicalPathAcrossRepoAlias(t *testing.T) {
	realRepo := t.TempDir()
	canonicalRepo, err := filepath.EvalSymlinks(realRepo)
	if err != nil {
		t.Fatalf("resolve canonical repo: %v", err)
	}
	aliasRoot := t.TempDir()
	repoAlias := filepath.Join(aliasRoot, "repo-alias")
	if err := os.Symlink(realRepo, repoAlias); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	cachePath := filepath.Join(".cache", "lopper")
	pinnedPath, err := resolvePathWithinExistingTree(filepath.Join(canonicalRepo, cachePath))
	if err != nil {
		t.Fatalf("resolve pinned path: %v", err)
	}
	cacheOptions, err := ResolveTrustedCacheOptions(repoAlias, &CacheOptions{
		Enabled: true,
		Path:    filepath.Join(canonicalRepo, cachePath),
	})
	if err != nil {
		t.Fatalf("resolve trusted cache options for alias repo: %v", err)
	}
	if cacheOptions.trustedPinnedPath() != pinnedPath {
		t.Fatalf("expected canonical pinned path to be preserved, got %q", cacheOptions.trustedPinnedPath())
	}
}

func TestResolveTrustedCacheOptionsEstablishesOpaqueCanonicalPin(t *testing.T) {
	repo := t.TempDir()
	pinnedAlias := filepath.Join(repo, ".cache", "lopper")

	cacheOptions, err := ResolveTrustedCacheOptions(repo, &CacheOptions{
		Enabled: true,
		Path:    pinnedAlias,
	})
	if err != nil {
		t.Fatalf("resolve trusted cache options: %v", err)
	}
	expectedPinnedPath, err := resolvePathWithinExistingTree(pinnedAlias)
	if err != nil {
		t.Fatalf("resolve expected canonical pinned path: %v", err)
	}
	if cacheOptions.trustedPin == nil {
		t.Fatal("expected an opaque trusted cache pin")
	}
	if cacheOptions.trustedPinnedPath() != expectedPinnedPath {
		t.Fatalf("expected canonical pin %q, got %q", expectedPinnedPath, cacheOptions.trustedPinnedPath())
	}
}

func TestResolveTrustedCacheOptionsRejectsOutsidePathBeforeMutation(t *testing.T) {
	repo := t.TempDir()
	outsideRoot := t.TempDir()
	outside := filepath.Join(outsideRoot, "missing", "cache")

	cacheOptions, err := ResolveTrustedCacheOptions(repo, &CacheOptions{
		Enabled: true,
		Path:    outside,
	})
	if !AuthenticatedExternalCacheOptions(cacheOptions, err) {
		t.Fatalf("expected authenticated external options, options=%#v err=%v", cacheOptions, err)
	}
	if err == nil || !strings.Contains(err.Error(), "cachePath must stay within repoPath") {
		t.Fatalf("expected outside path rejection, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(outsideRoot, "missing")); !os.IsNotExist(statErr) {
		t.Fatalf("expected no filesystem mutation for rejected pinned path, stat err=%v", statErr)
	}
}

func assertCacheLayoutAbsent(t *testing.T, root string) {
	t.Helper()
	for _, dir := range []string{cacheKeysDirName, cacheObjectsDirName} {
		if _, err := os.Stat(filepath.Join(root, dir)); !os.IsNotExist(err) {
			t.Fatalf("expected %s to remain absent under %s, stat err=%v", dir, root, err)
		}
	}
}

func TestCachePathEscapesRepoReturnsFalseForMissingPath(t *testing.T) {
	repo := t.TempDir()
	if cachePathEscapesRepo(filepath.Join(repo, cacheDirName), repo) {
		t.Fatalf("expected missing in-repo cache path to remain allowed")
	}
}

func TestCachePathEscapesRepoFallsBackToCleanMissingRepoPath(t *testing.T) {
	cachePath := t.TempDir()
	if !cachePathEscapesRepo(cachePath, filepath.Join(t.TempDir(), "missing-repo")) {
		t.Fatalf("expected cache path outside missing repo root to be rejected")
	}
}

func TestPathWithinTrustedRootAcceptsCanonicalRepoAlias(t *testing.T) {
	realRepo := t.TempDir()
	canonicalRepo, err := filepath.EvalSymlinks(realRepo)
	if err != nil {
		t.Fatalf("resolve canonical repo: %v", err)
	}
	aliasRoot := t.TempDir()
	repoAlias := filepath.Join(aliasRoot, "repo-alias")
	if err := os.Symlink(realRepo, repoAlias); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	if !pathWithinTrustedRoot(repoAlias, filepath.Join(canonicalRepo, cacheDirName)) {
		t.Fatalf("expected canonical repo alias path to stay within trusted root")
	}
}

func TestValidateNoSymlinkEscapeAllowsMissingCanonicalDescendant(t *testing.T) {
	realRepo := t.TempDir()
	canonicalRepo, err := filepath.EvalSymlinks(realRepo)
	if err != nil {
		t.Fatalf("resolve canonical repo: %v", err)
	}
	aliasRoot := t.TempDir()
	repoAlias := filepath.Join(aliasRoot, "repo-alias")
	if err := os.Symlink(realRepo, repoAlias); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	err = validateNoSymlinkEscape(repoAlias, filepath.Join(canonicalRepo, "missing", cacheDirName), "cachePath")
	if err != nil {
		t.Fatalf("expected missing canonical descendant to remain allowed, got %v", err)
	}
}

func TestValidateNoSymlinkEscapeRejectsBrokenSymlinkDescendant(t *testing.T) {
	repo := t.TempDir()
	if err := os.Symlink(filepath.Join(repo, "missing-target"), filepath.Join(repo, "cache-link")); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	err := validateNoSymlinkEscape(repo, filepath.Join(repo, "cache-link", cacheDirName), "cachePath")
	if err == nil || !strings.Contains(err.Error(), "cachePath must stay within repoPath") {
		t.Fatalf("expected broken symlink descendant rejection, got %v", err)
	}
}

func TestOpenConfinedWriteRootUnderReturnsCanonicalRelativeSuffix(t *testing.T) {
	repo := t.TempDir()
	rootPath := filepath.Join(repo, "nested", "cache")
	targetPath := filepath.Join(rootPath, cacheKeysDirName)

	root, rel, err := openConfinedWriteRootUnder(rootPath, targetPath)
	if err != nil {
		t.Fatalf("open confined write root: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Errorf("close confined write root: %v", closeErr)
		}
	})

	if rel != filepath.Join("nested", "cache", cacheKeysDirName) {
		t.Fatalf("expected canonical relative suffix, got %q", rel)
	}
}

func TestOpenConfinedWriteRootUnderRejectsInvalidTargetPath(t *testing.T) {
	if _, _, err := openConfinedWriteRootUnder(t.TempDir(), string([]byte{0})); err == nil {
		t.Fatalf("expected invalid target path to be rejected")
	}
}

func TestOpenConfinedWriteRootUnderRejectsInvalidRootPath(t *testing.T) {
	if _, _, err := openConfinedWriteRootUnder(string([]byte{0}), filepath.Join(t.TempDir(), cacheKeysDirName)); err == nil {
		t.Fatalf("expected invalid root path to be rejected")
	}
}

func TestEnsureConfinedDirectoryCreatesChildWithinRoot(t *testing.T) {
	root := t.TempDir()
	if err := ensureConfinedDirectory(root, cacheKeysDirName, 0o750); err != nil {
		t.Fatalf("ensure confined directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, cacheKeysDirName)); err != nil {
		t.Fatalf("expected confined directory to be created: %v", err)
	}
}

func TestEnsureConfinedDirectoryRejectsSymlinkTarget(t *testing.T) {
	root := t.TempDir()
	if err := os.Symlink(t.TempDir(), filepath.Join(root, cacheKeysDirName)); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	if err := ensureConfinedDirectory(root, cacheKeysDirName, 0o750); err == nil {
		t.Fatalf("expected symlinked confined directory target to be rejected")
	}
}

func TestEnsureConfinedDirectoryRejectsInvalidRoot(t *testing.T) {
	if err := ensureConfinedDirectory(string([]byte{0}), cacheKeysDirName, 0o750); err == nil {
		t.Fatalf("expected invalid root path to be rejected")
	}
}

func TestEnsurePinnedCacheLayoutPinsExistingRoot(t *testing.T) {
	root := mustEvalSymlinks(t, t.TempDir())
	pinnedPath, info, err := ensurePinnedCacheLayout(root)
	if err != nil {
		t.Fatalf("ensure pinned cache layout: %v", err)
	}
	if pinnedPath != root {
		t.Fatalf("expected existing cache root to remain pinned, got %q want %q", pinnedPath, root)
	}
	if info == nil || !info.IsDir() {
		t.Fatalf("expected pinned cache root info for directory, got %#v", info)
	}
	for _, dir := range []string{cacheKeysDirName, cacheObjectsDirName} {
		if _, err := os.Stat(filepath.Join(root, dir)); err != nil {
			t.Fatalf("expected %s dir under pinned root: %v", dir, err)
		}
	}
}

func TestEnsurePinnedCacheLayoutCreatesMissingRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "cache-root")
	pinnedPath, info, err := ensurePinnedCacheLayout(root)
	if err != nil {
		t.Fatalf("ensure pinned cache layout: %v", err)
	}
	expectedRoot := mustEvalSymlinks(t, root)
	if pinnedPath != expectedRoot {
		t.Fatalf("expected missing cache root to be created at %q, got %q", expectedRoot, pinnedPath)
	}
	if info == nil || !info.IsDir() {
		t.Fatalf("expected created cache root info for directory, got %#v", info)
	}
	for _, dir := range []string{cacheKeysDirName, cacheObjectsDirName} {
		if _, err := os.Stat(filepath.Join(root, dir)); err != nil {
			t.Fatalf("expected %s dir under created root: %v", dir, err)
		}
	}
}

func TestEnsurePinnedCacheLayoutPropagatesObjectsHookError(t *testing.T) {
	expectedErr := errors.New("objects init blocked")
	root := filepath.Join(t.TempDir(), "cache-root")
	withCacheInitBeforeObjectsEnsureHook(t, func() error { return expectedErr })

	pinnedPath, info, err := ensurePinnedCacheLayout(root)
	if pinnedPath != "" || info != nil {
		t.Fatalf("expected no pinned cache root on hook error, got path=%q info=%#v", pinnedPath, info)
	}
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected objects hook error, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, cacheKeysDirName)); statErr != nil {
		t.Fatalf("expected keys dir to be created before hook failure: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(root, cacheObjectsDirName)); !os.IsNotExist(statErr) {
		t.Fatalf("expected objects dir to remain absent after hook failure, got err=%v", statErr)
	}
}

func TestEnsurePinnedCacheLayoutRejectsInvalidRoot(t *testing.T) {
	pinnedPath, info, err := ensurePinnedCacheLayout(string([]byte{0}))
	if pinnedPath != "" || info != nil {
		t.Fatalf("expected invalid root path to return no pinned cache root, got path=%q info=%#v", pinnedPath, info)
	}
	if err == nil {
		t.Fatalf("expected invalid root path to be rejected")
	}
}

func TestResolvePathWithinExistingTreeRejectsInvalidPath(t *testing.T) {
	if _, err := resolvePathWithinExistingTree(string([]byte{0})); err == nil {
		t.Fatalf("expected invalid path to be rejected")
	}
}

func TestResolveTrustedAbsolutePathFollowsInRepoSymlinkWithMissingSuffix(t *testing.T) {
	repo := t.TempDir()
	actual := filepath.Join(repo, "actual")
	if err := os.MkdirAll(actual, 0o755); err != nil {
		t.Fatalf("mkdir actual target: %v", err)
	}
	linkPath := filepath.Join(repo, "linked")
	if err := os.Symlink(filepath.Base(actual), linkPath); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	repoPaths, err := resolveTrustedRepoPaths(repo)
	if err != nil {
		t.Fatalf("resolve trusted repo paths: %v", err)
	}

	resolved, err := resolveTrustedAbsolutePath(filepath.Join(repo, "linked", "missing", "cache"), repoPaths, 0)
	if err != nil {
		t.Fatalf("resolve trusted absolute path through in-repo symlink: %v", err)
	}
	if !resolved.enteredRepository {
		t.Fatalf("expected symlinked path to stay inside repository, got %#v", resolved)
	}
	if !resolved.missingPath {
		t.Fatalf("expected unresolved suffix to be reported missing, got %#v", resolved)
	}
	if resolved.symlinkExpansions < 1 {
		t.Fatalf("expected at least one symlink expansion, got %#v", resolved)
	}
	expectedPath, err := resolvePathWithinExistingTree(filepath.Join(actual, "missing", "cache"))
	if err != nil {
		t.Fatalf("resolve expected path within existing tree: %v", err)
	}
	if resolved.path != expectedPath {
		t.Fatalf("resolved path = %q, want %q", resolved.path, expectedPath)
	}
}

func TestPathWithinTrustedRootRejectsMissingRepoAndInvalidCandidate(t *testing.T) {
	if pathWithinTrustedRoot(filepath.Join(t.TempDir(), "missing-repo"), t.TempDir()) {
		t.Fatalf("expected missing repo root to be rejected")
	}
	if pathWithinTrustedRoot(t.TempDir(), string([]byte{0})) {
		t.Fatalf("expected invalid candidate path to be rejected")
	}
}

func TestValidateNoSymlinkEscapeAllowsRepoRootAndRejectsInvalidCandidate(t *testing.T) {
	repo := t.TempDir()
	if err := validateNoSymlinkEscape(repo, repo, "cachePath"); err != nil {
		t.Fatalf("expected repo root to be allowed, got %v", err)
	}
	if err := validateNoSymlinkEscape(repo, string([]byte{0}), "cachePath"); err == nil || !strings.Contains(err.Error(), "cachePath must stay within repoPath") {
		t.Fatalf("expected invalid candidate path rejection, got %v", err)
	}
}

func TestPathWithinDirRejectsInvalidChildPath(t *testing.T) {
	if pathWithinDir(t.TempDir(), string([]byte{0})) {
		t.Fatalf("expected invalid child path to be rejected")
	}
}

func TestPinnedTrustedCachePathPreventsSymlinkRetargetRaceBeforeFirstWrite(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory replacement semantics are covered on Unix")
	}

	repo := t.TempDir()
	allowedTarget := filepath.Join(repo, "allowed-target")
	redirectedTarget := t.TempDir()
	if err := os.MkdirAll(allowedTarget, 0o755); err != nil {
		t.Fatalf("mkdir allowed target: %v", err)
	}
	linkPath := filepath.Join(repo, "allowed-link")
	if err := os.Symlink(filepath.Base(allowedTarget), linkPath); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	cacheOptions, err := ResolveTrustedCacheOptions(repo, &CacheOptions{
		Enabled: true,
		Path:    filepath.Join("allowed-link", "cache"),
	})
	if err != nil {
		t.Fatalf("resolve trusted cache options: %v", err)
	}
	expectedPinnedPath, err := resolvePathWithinExistingTree(filepath.Join(allowedTarget, "cache"))
	if err != nil {
		t.Fatalf("resolve expected pinned cache root: %v", err)
	}
	if cacheOptions.trustedPinnedPath() != expectedPinnedPath {
		t.Fatalf("expected pinned cache root under original target, got %q", cacheOptions.trustedPinnedPath())
	}

	if err := os.Remove(linkPath); err != nil {
		t.Fatalf("remove original symlink: %v", err)
	}
	if err := os.Symlink(redirectedTarget, linkPath); err != nil {
		t.Fatalf("retarget allowed symlink: %v", err)
	}

	cache := newAnalysisCache(Request{Cache: cacheOptions}, repo)
	if !cache.cacheable {
		t.Fatalf("expected pinned cache to remain usable, warnings=%#v", cache.takeWarnings())
	}
	if _, err := os.Stat(filepath.Join(allowedTarget, "cache", cacheKeysDirName)); err != nil {
		t.Fatalf("expected pinned keys dir to be created in original target: %v", err)
	}
	if _, err := os.Stat(filepath.Join(allowedTarget, "cache", cacheObjectsDirName)); err != nil {
		t.Fatalf("expected pinned objects dir to be created in original target: %v", err)
	}
	if _, err := os.Stat(filepath.Join(redirectedTarget, "cache")); !os.IsNotExist(err) {
		t.Fatalf("expected retargeted outside cache root to remain absent, got err=%v", err)
	}
}

func TestPinnedTrustedCachePathRejectsInsertedSymlinkForMissingSuffixBeforeFirstWrite(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory replacement semantics are covered on Unix")
	}

	repo := t.TempDir()
	cacheParent := filepath.Join(repo, "cache-parent")
	redirectedTarget := t.TempDir()
	if err := os.MkdirAll(cacheParent, 0o755); err != nil {
		t.Fatalf("mkdir cache parent: %v", err)
	}

	cacheOptions, err := ResolveTrustedCacheOptions(repo, &CacheOptions{
		Enabled: true,
		Path:    filepath.Join("cache-parent", "missing", "cache"),
	})
	if err != nil {
		t.Fatalf("resolve trusted cache options: %v", err)
	}
	expectedPinnedPath, err := resolvePathWithinExistingTree(filepath.Join(cacheParent, "missing", "cache"))
	if err != nil {
		t.Fatalf("resolve expected pinned cache root: %v", err)
	}
	if cacheOptions.trustedPinnedPath() != expectedPinnedPath {
		t.Fatalf("expected pinned cache root under original repo ancestor, got %q", cacheOptions.trustedPinnedPath())
	}

	if err := os.Symlink(redirectedTarget, filepath.Join(cacheParent, "missing")); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	cache := newAnalysisCache(Request{Cache: cacheOptions}, repo)
	if cache.cacheable {
		t.Fatalf("expected inserted missing-suffix symlink to fail cache init, warnings=%#v", cache.takeWarnings())
	}
	if _, err := os.Stat(filepath.Join(redirectedTarget, "cache")); !os.IsNotExist(err) {
		t.Fatalf("expected redirected outside cache root to remain absent, got err=%v", err)
	}
	if _, err := os.Lstat(filepath.Join(cacheParent, "missing")); err != nil {
		t.Fatalf("expected inserted symlink to remain in place for failure inspection: %v", err)
	}
}

func TestEnsurePinnedCacheLayoutRejectsCacheRootSwapBetweenInitOperations(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory replacement semantics are covered on Unix")
	}

	repo := t.TempDir()
	cachePath := filepath.Join(repo, "cache-root")
	renamedCachePath := filepath.Join(repo, "cache-root-renamed")
	redirectedCachePath := t.TempDir()

	withCacheInitBeforeObjectsEnsureHook(t, func() error {
		if err := os.Rename(cachePath, renamedCachePath); err != nil {
			return err
		}
		return os.Symlink(redirectedCachePath, cachePath)
	})

	if _, _, err := ensurePinnedCacheLayout(cachePath); err == nil {
		t.Fatalf("expected cache root replacement during init to fail closed")
	}
	if _, err := os.Stat(filepath.Join(renamedCachePath, cacheKeysDirName)); err != nil {
		t.Fatalf("expected keys dir created before replacement to remain in original root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(renamedCachePath, cacheObjectsDirName)); !os.IsNotExist(err) {
		t.Fatalf("expected objects dir creation to stop after replacement, got err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(redirectedCachePath, cacheKeysDirName)); !os.IsNotExist(err) {
		t.Fatalf("expected redirected keys dir to remain absent, got err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(redirectedCachePath, cacheObjectsDirName)); !os.IsNotExist(err) {
		t.Fatalf("expected redirected objects dir to remain absent, got err=%v", err)
	}
}

func TestAnalysisCacheRejectsCacheRootSwapBeforeStoreWrite(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory replacement semantics are covered on Unix")
	}

	repo := t.TempDir()
	cachePath := filepath.Join(repo, "cache-root")
	cache := newAnalysisCache(Request{Cache: &CacheOptions{Enabled: true, Path: cachePath}}, repo)
	if !cache.cacheable {
		t.Fatalf("expected cache to be usable before root swap, warnings=%#v", cache.takeWarnings())
	}

	redirectedCachePath := t.TempDir()
	originalCachePath := filepath.Join(repo, "cache-root-original")
	withCacheStoreBeforeRootOpenHook(t, func() error {
		if err := os.Rename(cachePath, originalCachePath); err != nil {
			return err
		}
		return os.Symlink(redirectedCachePath, cachePath)
	})

	entry := cacheEntryDescriptor{KeyDigest: "swapped-root", InputDigest: "input"}
	storeErr := cache.store(entry, report.Report{RepoPath: repo})
	if storeErr == nil || !containsAny(storeErr.Error(), "cache root changed while pinned", "open canonical root") {
		t.Fatalf("expected swapped cache root rejection, got %v", storeErr)
	}
	if _, err := os.Stat(filepath.Join(redirectedCachePath, "objects")); !os.IsNotExist(err) {
		t.Fatalf("expected redirected cache root to remain untouched, got err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(originalCachePath, "objects")); err != nil {
		t.Fatalf("expected original cache root contents to remain present, got %v", err)
	}
}

func TestAnalysisCacheLookupRejectsCacheRootSwapBeforeReads(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory replacement semantics are covered on Unix")
	}

	for _, tc := range []cacheLookupRootSwapCase{
		{name: "before pointer read", swapOnRead: 1, outsideRepo: "outside-pointer", outsideObjID: "outside-pointer"},
		{name: "before object read", swapOnRead: 2, outsideRepo: "outside-object", outsideObjID: "outside-object"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runCacheLookupRootSwapCase(t, tc)
		})
	}
}

func withCacheInitBeforeObjectsEnsureHook(t *testing.T, hook func() error) {
	t.Helper()
	previous := cacheInitBeforeObjectsEnsureFn
	cacheInitBeforeObjectsEnsureFn = hook
	t.Cleanup(func() {
		cacheInitBeforeObjectsEnsureFn = previous
	})
}

func canonicalAliasCacheEscapeFixture(t *testing.T) (requestedRepo, canonicalCache, outsideCache string) {
	t.Helper()

	canonicalParent := t.TempDir()
	canonicalRepo := filepath.Join(canonicalParent, "repo")
	if err := os.MkdirAll(canonicalRepo, 0o755); err != nil {
		t.Fatalf("mkdir canonical repo: %v", err)
	}
	canonicalRepo, err := filepath.EvalSymlinks(canonicalRepo)
	if err != nil {
		t.Fatalf("resolve canonical repo: %v", err)
	}
	requestedParent := filepath.Join(t.TempDir(), "requested-parent")
	if err := os.Symlink(canonicalParent, requestedParent); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(canonicalRepo, "tmp")); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	return filepath.Join(requestedParent, "repo"),
		filepath.Join(canonicalRepo, "tmp", "cache"),
		filepath.Join(outside, "cache")
}

type alternateRepoAliasEscapeFixture struct {
	name          string
	requestedRepo string
	cachePath     string
	outsideCache  string
}

func alternateAbsoluteRepoAliasEscapeFixtures(t *testing.T) []alternateRepoAliasEscapeFixture {
	t.Helper()
	fixtures := []alternateRepoAliasEscapeFixture{arbitraryAbsoluteRepoAliasEscapeFixture(t)}
	if systemAlias, ok := systemAbsoluteRepoAliasEscapeFixture(t); ok {
		fixtures = append(fixtures, systemAlias)
	}
	return fixtures
}

func arbitraryAbsoluteRepoAliasEscapeFixture(t *testing.T) alternateRepoAliasEscapeFixture {
	t.Helper()
	repoParent := t.TempDir()
	repo := filepath.Join(repoParent, "repo")
	if err := os.Mkdir(repo, 0o750); err != nil {
		t.Fatalf("mkdir arbitrary-alias repo: %v", err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(repo, "tmp")); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	aliasParent := filepath.Join(t.TempDir(), "alternate-parent")
	if err := os.Symlink(repoParent, aliasParent); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	return alternateRepoAliasEscapeFixture{
		name:          "arbitrary alias",
		requestedRepo: repo,
		cachePath:     filepath.Join(aliasParent, "repo", "tmp", "cache"),
		outsideCache:  filepath.Join(outside, "cache"),
	}
}

func systemAbsoluteRepoAliasEscapeFixture(t *testing.T) (alternateRepoAliasEscapeFixture, bool) {
	t.Helper()
	requestedRepo := t.TempDir()
	canonicalRepo, err := filepath.EvalSymlinks(requestedRepo)
	if err != nil {
		t.Fatalf("resolve system-alias repo: %v", err)
	}
	if filepath.Clean(requestedRepo) == filepath.Clean(canonicalRepo) {
		return alternateRepoAliasEscapeFixture{}, false
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(requestedRepo, "tmp")); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	return alternateRepoAliasEscapeFixture{
		name:          "system absolute alias",
		requestedRepo: canonicalRepo,
		cachePath:     filepath.Join(requestedRepo, "tmp", "cache"),
		outsideCache:  filepath.Join(outside, "cache"),
	}, true
}

func runCacheLookupRootSwapCase(t *testing.T, tc cacheLookupRootSwapCase) {
	t.Helper()

	repo := t.TempDir()
	cachePath := filepath.Join(repo, "cache-root")
	cache := newAnalysisCache(Request{Cache: &CacheOptions{Enabled: true, Path: cachePath}}, repo)
	if !cache.cacheable {
		t.Fatalf("expected cache to be usable before root swap, warnings=%#v", cache.takeWarnings())
	}

	entry := cacheEntryDescriptor{KeyLabel: "k", KeyDigest: "key", InputDigest: "input"}
	seedLookupCachePaths(t, cachePath, entry, "inside-object", report.Report{RepoPath: "inside"})

	redirectedCachePath := t.TempDir()
	seedLookupCachePaths(t, redirectedCachePath, entry, tc.outsideObjID, report.Report{RepoPath: tc.outsideRepo})

	installLookupSwapHook(t, cachePath, filepath.Join(repo, "cache-root-original"), redirectedCachePath, tc.swapOnRead)
	got, hit, err := cache.lookup(entry)
	assertLookupRootSwapResult(t, got, hit, err)
}

func seedLookupCachePaths(t *testing.T, cachePath string, entry cacheEntryDescriptor, objectID string, rep report.Report) {
	t.Helper()
	mustWriteCachedObject(t, cachePath, objectID, rep)
	mustWritePointer(t, filepath.Join(cachePath, cacheKeysDirName, entry.KeyDigest+".json"), cachePointer{
		InputDigest:  entry.InputDigest,
		ObjectDigest: objectID,
	})
}

func installLookupSwapHook(t *testing.T, cachePath, originalCachePath, redirectedCachePath string, swapOnRead int) {
	t.Helper()
	readCount := 0
	withCacheLookupBeforeReadHook(t, func() error {
		readCount++
		if readCount != swapOnRead {
			return nil
		}
		if err := os.Rename(cachePath, originalCachePath); err != nil {
			return err
		}
		return os.Symlink(redirectedCachePath, cachePath)
	})
}

func assertLookupRootSwapResult(t *testing.T, got report.Report, hit bool, err error) {
	t.Helper()
	if hit || got.RepoPath != "" {
		t.Fatalf("expected swapped lookup to return no cached report, got report=%#v hit=%v", got, hit)
	}
	if err != nil && !strings.Contains(err.Error(), "cache root changed while pinned") {
		t.Fatalf("expected lookup to fail closed without outside read, got err=%v", err)
	}
}

func assertSymlinkedDefaultCachePathRejected(t *testing.T, repo, outside, description string) {
	t.Helper()

	symlinkPath := filepath.Join(repo, cacheDirName)
	if err := os.Symlink(outside, symlinkPath); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	cache := newAnalysisCache(Request{}, repo)
	if cache.cacheable {
		t.Fatalf("expected %s default cache path to be rejected", description)
	}
	warnings := cache.takeWarnings()
	if len(warnings) == 0 || !strings.Contains(warnings[0], "cache path escapes repository root") {
		t.Fatalf("expected %s cache path warning, got %#v", description, warnings)
	}
	if _, err := os.Stat(filepath.Join(outside, cacheKeysDirName)); !os.IsNotExist(err) {
		t.Fatalf("expected no keys dir to be created outside repo, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, cacheObjectsDirName)); !os.IsNotExist(err) {
		t.Fatalf("expected no objects dir to be created outside repo, stat err=%v", err)
	}
}

func withCacheStoreBeforeRootOpenHook(t *testing.T, hook func() error) {
	t.Helper()
	previous := cacheStoreBeforeRootOpenFn
	cacheStoreBeforeRootOpenFn = hook
	t.Cleanup(func() {
		cacheStoreBeforeRootOpenFn = previous
	})
}

func withCacheLookupBeforeReadHook(t *testing.T, hook func() error) {
	t.Helper()
	previous := cacheLookupBeforeReadFn
	cacheLookupBeforeReadFn = hook
	t.Cleanup(func() {
		cacheLookupBeforeReadFn = previous
	})
}

func containsAny(got string, parts ...string) bool {
	for _, part := range parts {
		if strings.Contains(got, part) {
			return true
		}
	}
	return false
}

func TestLockOrConfigFileRecognizesGradleVersionCatalogs(t *testing.T) {
	if !lockOrConfigFile("libs.versions.toml") {
		t.Fatalf("expected Gradle version catalogs to participate in cache invalidation")
	}
	if lockOrConfigFile("README.md") {
		t.Fatalf("did not expect README.md to be treated as a cache-relevant config file")
	}
}

func TestHashFileOrMissingAndWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()
	missingPath := filepath.Join(dir, cacheMissingFileName)
	digest, err := hashFileOrMissing(missingPath)
	if err != nil {
		t.Fatalf("hash missing file: %v", err)
	}
	if digest != "missing" {
		t.Fatalf("expected missing digest marker, got %q", digest)
	}

	targetPath := filepath.Join(dir, "nested", "file.txt")
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o750); err != nil {
		t.Fatalf("mkdir target parent: %v", err)
	}
	if err := os.WriteFile(targetPath, []byte("before"), 0o644); err != nil {
		t.Fatalf("seed target file: %v", err)
	}
	if err := os.Chmod(targetPath, 0o644); err != nil {
		t.Fatalf("chmod target file: %v", err)
	}
	if err := writeFileAtomic(dir, targetPath, []byte("hello")); err != nil {
		t.Fatalf("write file atomic: %v", err)
	}
	digest, err = hashFileOrMissing(targetPath)
	if err != nil {
		t.Fatalf("hash existing file: %v", err)
	}
	if digest == "" || digest == "missing" {
		t.Fatalf("expected real digest for existing file, got %q", digest)
	}
	info, err := os.Stat(targetPath)
	if err != nil {
		t.Fatalf("stat target file: %v", err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("expected existing file mode 0644 to be preserved, got %#o", info.Mode().Perm())
	}
}

func TestWriteFileAtomicRewritesReadOnlyExistingCacheFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("read-only replacement semantics are covered by safeio platform tests")
	}

	dir := t.TempDir()
	targetPath := filepath.Join(dir, "keys", "pointer.json")
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o750); err != nil {
		t.Fatalf("mkdir target parent: %v", err)
	}
	if err := os.WriteFile(targetPath, []byte("before"), 0o444); err != nil {
		t.Fatalf("seed target file: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(targetPath, 0o600); err != nil && !os.IsNotExist(err) {
			t.Errorf("restore target file permissions: %v", err)
		}
	})

	probe, probeErr := os.OpenFile(targetPath, os.O_WRONLY, 0)
	if probeErr == nil {
		if err := probe.Close(); err != nil {
			t.Fatalf("close writability probe: %v", err)
		}
		t.Skip("effective privileges bypass read-only file permissions")
	}
	if !os.IsPermission(probeErr) {
		t.Skipf("read-only file semantics are not testable: %v", probeErr)
	}

	if err := writeFileAtomic(dir, targetPath, []byte("after")); err != nil {
		t.Fatalf("rewrite readonly cache file: %v", err)
	}
	if got, err := os.ReadFile(targetPath); err != nil {
		t.Fatalf("read rewritten cache file: %v", err)
	} else if string(got) != "after" {
		t.Fatalf("unexpected rewritten content: %q", string(got))
	}
	if info, err := os.Stat(targetPath); err != nil {
		t.Fatalf("stat rewritten cache file: %v", err)
	} else if info.Mode().Perm() != 0o444 {
		t.Fatalf("expected rewritten read-only mode 0444 to be preserved, got %#o", info.Mode().Perm())
	}
}

func TestWriteFileAtomicRejectsSymlinkedParentEscape(t *testing.T) {
	rootDir := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(rootDir, "keys")); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	targetPath := filepath.Join(rootDir, "keys", "pointer.json")
	err := writeFileAtomic(rootDir, targetPath, []byte("after"))
	if err == nil || !strings.Contains(err.Error(), "output parent contains symlink") {
		t.Fatalf("expected symlinked parent rejection, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(outside, "pointer.json")); !os.IsNotExist(statErr) {
		t.Fatalf("expected outside cache file to remain absent, got err=%v", statErr)
	}
}

func TestWriteFileAtomicRejectsRootSwapBeforeParentCreation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory replacement semantics are covered on Unix")
	}

	rootDir := t.TempDir()
	originalParent := filepath.Join(rootDir, "keys")
	if err := os.MkdirAll(originalParent, 0o755); err != nil {
		t.Fatalf("mkdir original parent: %v", err)
	}
	outside := t.TempDir()
	outsideTarget := filepath.Join(outside, "pointer.json")
	if err := os.Remove(originalParent); err != nil {
		t.Fatalf("remove original parent: %v", err)
	}
	if err := os.Symlink(outside, originalParent); err != nil {
		t.Fatalf("retarget parent symlink: %v", err)
	}

	targetPath := filepath.Join(rootDir, "keys", "pointer.json")
	err := writeFileAtomic(rootDir, targetPath, []byte("after"))
	if err == nil || !strings.Contains(err.Error(), "output parent contains symlink") {
		t.Fatalf("expected swapped parent symlink rejection, got %v", err)
	}
	if _, statErr := os.Stat(outsideTarget); !os.IsNotExist(statErr) {
		t.Fatalf("expected outside cache file to remain absent, got err=%v", statErr)
	}
}

func TestWriteFileDigestAndMissingMarker(t *testing.T) {
	dir := t.TempDir()
	targetPath := filepath.Join(dir, "tracked.txt")
	if err := os.WriteFile(targetPath, []byte("hello"), 0o600); err != nil {
		t.Fatalf("write tracked file: %v", err)
	}

	var existing bytes.Buffer
	if err := writeFileDigest(&existing, targetPath); err != nil {
		t.Fatalf("write existing digest: %v", err)
	}
	if len(strings.TrimSpace(existing.String())) != 64 {
		t.Fatalf("expected SHA-256 hex digest, got %q", existing.String())
	}

	var missing bytes.Buffer
	if err := writeFileDigestOrMissing(&missing, filepath.Join(dir, cacheMissingFileName)); err != nil {
		t.Fatalf("write missing digest marker: %v", err)
	}
	if missing.String() != "missing" {
		t.Fatalf("expected missing marker, got %q", missing.String())
	}
}

func TestHashFileOrMissingReturnsErrorForUnreadablePath(t *testing.T) {
	dir := t.TempDir()
	if _, err := hashFileOrMissing(dir); err == nil {
		t.Fatalf("expected hashFileOrMissing to fail for directory path")
	}
}

func TestAnalysisCacheLookupBranches(t *testing.T) {
	cacheDir := t.TempDir()
	mustMkdirCacheLayout(t, cacheDir)

	cache := &analysisCache{options: resolvedCacheOptions{Enabled: true, Path: cacheDir}, cacheable: true}
	entry := cacheEntryDescriptor{KeyLabel: "js-ts:/repo", KeyDigest: "key", InputDigest: "input-current"}
	pointerPath := filepath.Join(cacheDir, cacheKeysDirName, entry.KeyDigest+".json")

	cases := []analysisCacheLookupCase{
		{
			name: "pointer-corrupt",
			setup: func(t *testing.T) {
				mustWriteFile(t, pointerPath, []byte("{bad-json"))
			},
			wantReason: "pointer-corrupt",
		},
		{
			name: "input-changed",
			setup: func(t *testing.T) {
				mustWritePointer(t, pointerPath, cachePointer{InputDigest: "input-old", ObjectDigest: "obj"})
			},
			wantReason: "input-changed",
		},
		{
			name: "object-missing",
			setup: func(t *testing.T) {
				mustWritePointer(t, pointerPath, cachePointer{InputDigest: entry.InputDigest, ObjectDigest: "missing-object"})
			},
			wantReason: "object-missing",
		},
		{
			name: "object-corrupt",
			setup: func(t *testing.T) {
				mustWritePointer(t, pointerPath, cachePointer{InputDigest: entry.InputDigest, ObjectDigest: "obj-corrupt"})
				mustWriteFile(t, filepath.Join(cacheDir, cacheObjectsDirName, "obj-corrupt.json"), []byte("{not-json"))
			},
			wantReason: "object-corrupt",
		},
		{
			name: "hit",
			setup: func(t *testing.T) {
				mustWriteCachedObject(t, cacheDir, "obj-hit", report.Report{RepoPath: "repo"})
				mustWritePointer(t, pointerPath, cachePointer{InputDigest: entry.InputDigest, ObjectDigest: "obj-hit"})
			},
			wantHit:      true,
			wantRepoPath: "repo",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cache.metadata.Invalidations = nil
			tc.setup(t)
			got, hit, err := cache.lookup(entry)
			assertLookupCaseOutcome(t, cache.metadata.Invalidations, tc, got, hit, err)
		})
	}
}

func assertLookupCaseOutcome(t *testing.T, invalidations []report.CacheInvalidation, tc analysisCacheLookupCase, got report.Report, hit bool, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("lookup error: %v", err)
	}
	if hit != tc.wantHit {
		t.Fatalf("unexpected hit state: got %v want %v", hit, tc.wantHit)
	}
	if tc.wantRepoPath != "" && got.RepoPath != tc.wantRepoPath {
		t.Fatalf("unexpected cached report: %#v", got)
	}
	if tc.wantReason == "" {
		return
	}
	if len(invalidations) == 0 || invalidations[len(invalidations)-1].Reason != tc.wantReason {
		t.Fatalf("expected invalidation reason %q, got %#v", tc.wantReason, invalidations)
	}
}

func TestAnalysisCacheStoreAndFileCollectionBranches(t *testing.T) {
	repo := t.TempDir()
	cacheDir := filepath.Join(repo, cacheDirName)
	mustMkdirCacheLayout(t, cacheDir)

	entry := cacheEntryDescriptor{KeyDigest: "readonly-key", InputDigest: "readonly-input"}
	readOnlyCache := &analysisCache{options: resolvedCacheOptions{Enabled: true, Path: cacheDir, ReadOnly: true}, cacheable: true}
	if err := readOnlyCache.store(entry, report.Report{RepoPath: "repo"}); err != nil {
		t.Fatalf("readonly store should no-op, got %v", err)
	}
	if readOnlyCache.metadata.Writes != 0 {
		t.Fatalf("expected no writes in readonly mode, got %d", readOnlyCache.metadata.Writes)
	}

	writableCache := &analysisCache{options: resolvedCacheOptions{Enabled: true, Path: cacheDir}, cacheable: true}
	if err := writableCache.store(entry, report.Report{RepoPath: "repo"}); err != nil {
		t.Fatalf("writable store: %v", err)
	}
	if writableCache.metadata.Writes != 1 {
		t.Fatalf("expected write count 1, got %d", writableCache.metadata.Writes)
	}

	ignoredDir := filepath.Join(repo, ".git")
	if err := os.MkdirAll(ignoredDir, 0o750); err != nil {
		t.Fatalf("mkdir ignored dir: %v", err)
	}
	mustWriteFile(t, filepath.Join(ignoredDir, "config"), []byte("x"))
	mustWriteFile(t, filepath.Join(repo, cacheTestGoModName), []byte(cacheTestGoModContent))

	records, err := writableCache.collectRelevantFiles(repo)
	if err != nil {
		t.Fatalf("collect relevant files: %v", err)
	}
	if len(records) == 0 {
		t.Fatalf("expected at least one relevant file record")
	}
}

func TestAnalysisCacheStoreReplacesExistingFilesPreservingMode(t *testing.T) {
	cacheDir := t.TempDir()
	mustMkdirCacheLayout(t, cacheDir)

	entry := cacheEntryDescriptor{KeyDigest: "key", InputDigest: "input"}
	rep := report.Report{RepoPath: "repo"}
	payload := cachedPayload{Report: rep}
	serializedPayload, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal cached payload: %v", err)
	}
	objectDigest := sha256Hex(serializedPayload)
	objectPath := filepath.Join(cacheDir, cacheObjectsDirName, objectDigest+".json")
	pointerPath := filepath.Join(cacheDir, cacheKeysDirName, entry.KeyDigest+".json")

	mustWriteFile(t, objectPath, []byte("old-object"))
	mustWriteFile(t, pointerPath, []byte("old-pointer"))
	if err := os.Chmod(objectPath, 0o644); err != nil {
		t.Fatalf("chmod object path: %v", err)
	}
	if err := os.Chmod(pointerPath, 0o640); err != nil {
		t.Fatalf("chmod pointer path: %v", err)
	}

	cache := &analysisCache{options: resolvedCacheOptions{Enabled: true, Path: cacheDir}, cacheable: true}
	if err := cache.store(entry, rep); err != nil {
		t.Fatalf("store cache entry: %v", err)
	}

	var pointer cachePointer
	pointerData, err := os.ReadFile(pointerPath)
	if err != nil {
		t.Fatalf("read pointer file: %v", err)
	}
	if err := json.Unmarshal(pointerData, &pointer); err != nil {
		t.Fatalf("unmarshal pointer file: %v", err)
	}
	if pointer.ObjectDigest != objectDigest {
		t.Fatalf("unexpected object digest: got %q want %q", pointer.ObjectDigest, objectDigest)
	}
	objectInfo, err := os.Stat(objectPath)
	if err != nil {
		t.Fatalf("stat object path: %v", err)
	}
	if objectInfo.Mode().Perm() != 0o644 {
		t.Fatalf("expected object mode 0644 to be preserved, got %#o", objectInfo.Mode().Perm())
	}
	pointerInfo, err := os.Stat(pointerPath)
	if err != nil {
		t.Fatalf("stat pointer path: %v", err)
	}
	if pointerInfo.Mode().Perm() != 0o640 {
		t.Fatalf("expected pointer mode 0640 to be preserved, got %#o", pointerInfo.Mode().Perm())
	}
}

func TestAnalysisCacheHelperErrorBranches(t *testing.T) {
	t.Run("prepare entry and hash json error", testAnalysisCachePrepareEntryAndHashJSONError)
	t.Run("write atomic and hash file errors", testAnalysisCacheWriteAtomicAndHashFileErrors)
	t.Run("prepare and load cache warnings", testAnalysisCachePrepareAndLoadWarnings)
	t.Run("store cached report warning branch", testAnalysisCacheStoreCachedReportWarningBranch)
	t.Run("new cache disabled", testAnalysisCacheNewCacheDisabled)
}

func testAnalysisCachePrepareEntryAndHashJSONError(t *testing.T) {
	repo := t.TempDir()
	root := mustCreateRootWithGoMod(t, repo, "pkg")
	configPath := filepath.Join(repo, ".lopper.yml")
	mustWriteFile(t, configPath, []byte("thresholds: {}\n"))

	cache := &analysisCache{options: resolvedCacheOptions{Enabled: true, Path: filepath.Join(repo, cacheDirName)}, cacheable: true}
	entry, err := cache.prepareEntry(Request{Dependency: "lodash", TopN: 1, RuntimeProfile: "node-import", ConfigPath: configPath, LowConfidenceWarningPercent: intPtr(30), MinUsagePercentForRecommendations: intPtr(40), RemovalCandidateWeights: &report.RemovalCandidateWeights{Usage: 0.5, Impact: 0.3, Confidence: 0.2}}, "js-ts", root)
	if err != nil {
		t.Fatalf("prepare entry: %v", err)
	}
	if entry.KeyDigest == "" || entry.InputDigest == "" {
		t.Fatalf("expected non-empty cache entry digests: %#v", entry)
	}
	if _, err := hashJSON(map[string]any{"bad": make(chan int)}); err == nil {
		t.Fatalf("expected hashJSON to fail for unsupported value")
	}
}

func testAnalysisCacheWriteAtomicAndHashFileErrors(t *testing.T) {
	dir := t.TempDir()
	targetDir := filepath.Join(dir, "target")
	if err := os.MkdirAll(targetDir, 0o750); err != nil {
		t.Fatalf("mkdir target: %v", err)
	}
	if writeFileAtomic(dir, targetDir, []byte("x")) == nil {
		t.Fatalf("expected writeFileAtomic to fail when target is directory")
	}
	if _, err := hashFile(targetDir); err == nil {
		t.Fatalf("expected hashFile to fail for directory")
	}
}

func testAnalysisCachePrepareAndLoadWarnings(t *testing.T) {
	repo := t.TempDir()
	cacheDir := filepath.Join(repo, cacheDirName)
	mustMkdirCacheLayout(t, cacheDir)
	cache := &analysisCache{options: resolvedCacheOptions{Enabled: true, Path: cacheDir}, cacheable: true}

	_, _, hit := prepareAndLoadCachedReport(Request{RepoPath: repo, Dependency: "dep"}, cache, "js-ts", filepath.Join(repo, "missing-root"))
	if hit {
		t.Fatalf("did not expect cache hit when prepare entry fails")
	}
	if len(cache.takeWarnings()) == 0 {
		t.Fatalf("expected warning when prepare entry fails")
	}

	root := mustCreateRootWithGoMod(t, repo, "root")
	entry, err := cache.prepareEntry(Request{RepoPath: repo, Dependency: "dep"}, "js-ts", root)
	if err != nil {
		t.Fatalf("prepare entry: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(cacheDir, cacheKeysDirName, entry.KeyDigest+".json"), 0o750); err != nil {
		t.Fatalf("mkdir pointer as dir: %v", err)
	}
	_, _, hit = prepareAndLoadCachedReport(Request{RepoPath: repo, Dependency: "dep"}, cache, "js-ts", root)
	if hit {
		t.Fatalf("did not expect cache hit on lookup error")
	}
	if warnings := strings.Join(cache.takeWarnings(), "\n"); !strings.Contains(warnings, "lookup failed") {
		t.Fatalf("expected lookup warning, got %q", warnings)
	}
}

func testAnalysisCacheStoreCachedReportWarningBranch(t *testing.T) {
	repo := t.TempDir()
	cache := &analysisCache{options: resolvedCacheOptions{Enabled: true, Path: filepath.Join(repo, "cache-as-file")}, cacheable: true}
	mustWriteFile(t, cache.options.Path, []byte("x"))
	storeCachedReport(cache, "js-ts", repo, cacheEntryDescriptor{KeyDigest: "k", InputDigest: "i"}, report.Report{})
	if len(cache.takeWarnings()) == 0 {
		t.Fatalf("expected cache store warning on invalid path")
	}
	storeCachedReport(cache, "js-ts", repo, cacheEntryDescriptor{}, report.Report{})
	if len(cache.takeWarnings()) != 0 {
		t.Fatalf("expected no warning for empty key digest")
	}
}

func testAnalysisCacheNewCacheDisabled(t *testing.T) {
	repo := t.TempDir()
	cache := newAnalysisCache(Request{Cache: &CacheOptions{Enabled: false}}, repo)
	if cache.cacheable {
		t.Fatalf("expected disabled cache to be non-cacheable")
	}
	if cache.metadata.Enabled {
		t.Fatalf("expected metadata to mark cache disabled")
	}
}

func TestCollectRelevantFileWalkError(t *testing.T) {
	files := make([]cacheRelevantFile, 0)
	root := t.TempDir()
	if collectRelevantFile(root, filepath.Join(root, "missing"), nil, errors.New("walk failure"), &files) == nil {
		t.Fatalf("expected collectRelevantFile to return walk error")
	}
}

func TestCacheServiceBranchWithNoRootSeen(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, cacheTestJSIndexFileName), "console.log('x')\n")
	adapter := &countingAdapter{id: "cachelang"}
	reg := language.NewRegistry()
	if err := reg.Register(adapter); err != nil {
		t.Fatalf("register adapter: %v", err)
	}
	svc := &Service{Registry: reg}
	if _, err := svc.Analyse(context.Background(), Request{
		RepoPath: repo,
		Language: "cachelang",
		TopN:     1,
		Cache:    &CacheOptions{Enabled: true, Path: filepath.Join(repo, "cache")},
	}); err != nil {
		t.Fatalf("analyse with cache branch: %v", err)
	}
}

func mustMkdirCacheLayout(t *testing.T, cacheDir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(cacheDir, cacheKeysDirName), 0o750); err != nil {
		t.Fatalf("mkdir keys: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(cacheDir, cacheObjectsDirName), 0o750); err != nil {
		t.Fatalf("mkdir objects: %v", err)
	}
}

func mustWriteFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func mustWritePointer(t *testing.T, pointerPath string, pointer cachePointer) {
	t.Helper()
	payload, err := json.Marshal(pointer)
	if err != nil {
		t.Fatalf("marshal pointer: %v", err)
	}
	mustWriteFile(t, pointerPath, payload)
}

func mustWriteCachedObject(t *testing.T, cacheDir string, objectDigest string, data report.Report) {
	t.Helper()
	payload, err := json.Marshal(cachedPayload{Report: data})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	mustWriteFile(t, filepath.Join(cacheDir, cacheObjectsDirName, objectDigest+".json"), payload)
}

func mustCreateRootWithGoMod(t *testing.T, repo, dirName string) string {
	t.Helper()
	root := filepath.Join(repo, dirName)
	if err := os.MkdirAll(root, 0o750); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}
	mustWriteFile(t, filepath.Join(root, cacheTestGoModName), []byte(cacheTestGoModContent))
	return root
}

func intPtr(value int) *int { return &value }
