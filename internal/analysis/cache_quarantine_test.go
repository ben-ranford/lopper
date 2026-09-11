package analysis

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/safeio"
)

func TestRemoveCreatedAnalysisCacheRootsFallsBackToLiveParentWhenRollbackParentMissing(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	_, childInfo := createAnalysisCacheChild(t, repo, cacheKeysDirName)

	opened := []openedAnalysisCacheRoot{{
		parent:         root,
		rollbackParent: nil,
		name:           cacheKeysDirName,
		info:           childInfo,
		created:        true,
	}}

	if err := removeCreatedAnalysisCacheRoots(opened, true); err != nil {
		t.Fatalf("remove created cache roots with missing rollback parent: %v", err)
	}
	assertAnalysisCachePathAbsent(t, filepath.Join(repo, cacheKeysDirName))
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

func TestCachePathAndRelevantFileBoundaryBranches(t *testing.T) {
	var nilCache *analysisCache
	cachePath := filepath.Join(t.TempDir(), cacheDirName)
	if got := nilCache.stableCacheRoot(cachePath); got != cachePath {
		t.Fatalf("expected nil cache to preserve cache root, got %q", got)
	}

	repo := t.TempDir()
	outside := t.TempDir()
	symlink := filepath.Join(repo, "linked-cache")
	if err := os.Symlink(outside, symlink); err != nil {
		t.Fatalf("create cache symlink: %v", err)
	}
	if !cachePathEscapesRepo(symlink, repo) {
		t.Fatal("expected symlinked cache path to be rejected")
	}
	missingRootPath := filepath.Join(repo, "missing", cacheDirName)
	missingRootInfo, err := prepareWritableAnalysisCacheRoot(missingRootPath)
	if err != nil {
		t.Fatalf("expected missing cache root to be created capability-bound: %v", err)
	}
	if missingRootInfo == nil {
		t.Fatal("expected identity for newly created missing cache root")
	}
	if info, statErr := os.Stat(missingRootPath); statErr != nil || !info.IsDir() {
		t.Fatalf("expected missing cache root to exist on disk, stat err=%v", statErr)
	}
	cache := &analysisCache{options: resolvedCacheOptions{Path: repo}}
	writeRoot, err := cache.openWriteRoot()
	if err != nil {
		t.Fatalf("open cache write root: %v", err)
	}
	if err := writeRoot.Close(); err != nil {
		t.Fatalf("close cache write root: %v", err)
	}

	exclusions := cacheExcludedPathSet(cacheAnalysisExclusions{files: []string{"", filepath.Join(repo, "trace.ndjson")}})
	if len(exclusions) != 1 {
		t.Fatalf("expected only non-empty cache exclusion, got %#v", exclusions)
	}
	if !shouldSkipCacheDir(cacheDirName) || isCacheRelevantFile("README.txt") {
		t.Fatal("expected cache directory and unsupported file handling")
	}
	if _, err := collectPHPShortOpenTagTraversalEntries(filepath.Join(repo, "missing-root"), cacheAnalysisExclusions{}); err == nil {
		t.Fatal("expected missing short-open-tag traversal root to fail")
	}
	if err := os.MkdirAll(filepath.Join(repo, "objects", "broken.json"), 0o750); err != nil {
		t.Fatalf("create malformed cache object path: %v", err)
	}
	if _, reason, err := readCachedPayload(repo, "broken"); err != nil || reason != "object-read-error" {
		t.Fatalf("expected malformed cache object read to invalidate, reason=%q err=%v", reason, err)
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

func TestNewAnalysisCacheCreatesMissingRootWithinPinnedAncestor(t *testing.T) {
	repo := t.TempDir()
	cachePath := filepath.Join(repo, "missing", cacheDirName)

	cache := newAnalysisCache(Request{Cache: &CacheOptions{Enabled: true, Path: cachePath}}, repo)
	if !cache.cacheable {
		t.Fatalf("expected missing cache root to be created, warnings=%#v", cache.takeWarnings())
	}
	for _, path := range []string{
		cachePath,
		filepath.Join(cachePath, cacheKeysDirName),
		filepath.Join(cachePath, cacheObjectsDirName),
	} {
		if info, err := os.Stat(path); err != nil || !info.IsDir() {
			t.Fatalf("expected cache initialization to create directory %s, info=%#v err=%v", path, info, err)
		}
	}
}

func TestNewAnalysisCacheReadOnlyMissingRootDoesNotCreate(t *testing.T) {
	repo := t.TempDir()
	cachePath := filepath.Join(repo, "missing", cacheDirName)

	cache := newAnalysisCache(Request{Cache: &CacheOptions{Enabled: true, Path: cachePath, ReadOnly: true}}, repo)
	if cache.cacheable {
		t.Fatal("expected missing read-only cache root to be unavailable")
	}
	warnings := cache.takeWarnings()
	if len(warnings) == 0 {
		t.Fatal("expected missing read-only cache root warning")
	}
	for _, path := range []string{
		filepath.Join(repo, "missing"),
		cachePath,
		filepath.Join(cachePath, cacheKeysDirName),
		filepath.Join(cachePath, cacheObjectsDirName),
	} {
		assertAnalysisCachePathAbsent(t, path)
	}
}

func TestNewAnalysisCacheReadOnlyExistingRootUsesPinnedIdentity(t *testing.T) {
	repo := t.TempDir()
	cachePath := filepath.Join(repo, cacheDirName)
	mustMkdirCacheLayout(t, cachePath)

	cache := newAnalysisCache(Request{Cache: &CacheOptions{Enabled: true, Path: cachePath, ReadOnly: true}}, repo)
	if !cache.cacheable {
		t.Fatalf("expected existing read-only cache root to be usable, warnings=%#v", cache.takeWarnings())
	}
	if cache.rootIdentity == nil {
		t.Fatal("expected read-only cache root identity to be pinned")
	}
}

func TestPrepareAnalysisCacheRootReturnsOpenAncestorErrors(t *testing.T) {
	openErr := errors.New("open ancestor failed")
	hookOpenAnalysisCacheAncestor(t, func(func(string) (safeio.Root, string, []string, error), string) (safeio.Root, string, []string, error) {
		return nil, "", nil, openErr
	})

	if _, err := prepareReadableAnalysisCacheRoot(filepath.Join(t.TempDir(), cacheDirName)); !errors.Is(err, openErr) {
		t.Fatalf("readable root error = %v, want %v", err, openErr)
	}
	if _, err := prepareWritableAnalysisCacheRoot(filepath.Join(t.TempDir(), cacheDirName)); !errors.Is(err, openErr) {
		t.Fatalf("writable root error = %v, want %v", err, openErr)
	}
}

func TestPrepareAnalysisCacheRootReadOnlyPreservesValidationFailure(t *testing.T) {
	repo := t.TempDir()
	cachePath := filepath.Join(repo, cacheDirName)
	mustMkdirCacheLayout(t, cachePath)
	validationErr := errors.New("read-only validation failed")

	hookValidateAnalysisCacheRoot(t, func(validate func(string, fs.FileInfo) error, path string, expected fs.FileInfo) error {
		if path == cachePath {
			return validationErr
		}
		return validate(path, expected)
	})

	if _, err := prepareAnalysisCacheRoot(resolvedCacheOptions{Enabled: true, Path: cachePath, ReadOnly: true}); !errors.Is(err, validationErr) {
		t.Fatalf("prepare read-only cache root error = %v, want %v", err, validationErr)
	}
}

func TestVerifyPinnedAnalysisCacheDirectoryPropagatesLstatError(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	lstatErr := errors.New("root lstat failed")

	if _, err := verifyPinnedAnalysisCacheDirectory(&lstatErrorAnalysisCacheRoot{Root: root, name: ".", err: lstatErr}, repo); !errors.Is(err, lstatErr) {
		t.Fatalf("verify pinned directory error = %v, want %v", err, lstatErr)
	}
}

func TestAnalysisCacheStableCacheRootNilReceiver(t *testing.T) {
	rootPath := filepath.Join(t.TempDir(), "repo", cacheDirName)
	var cache *analysisCache
	if got := cache.stableCacheRoot(rootPath); got != rootPath {
		t.Fatalf("nil cache stable root = %q, want %q", got, rootPath)
	}
}

func TestNewAnalysisCacheRejectsAncestorSwapBeforeMissingRootCreate(t *testing.T) {
	fixture := newAnalysisCacheSwapFixture(t)
	swapped := false
	createdMissingPart := false
	observedAncestorPath := ""
	var observedMissingParts []string
	hookOpenAnalysisCacheAncestor(t, func(open func(string) (safeio.Root, string, []string, error), name string) (safeio.Root, string, []string, error) {
		root, currentPath, missingParts, err := open(name)
		if err == nil && name == fixture.cachePath && !swapped {
			observedAncestorPath = currentPath
			observedMissingParts = append([]string(nil), missingParts...)
			swapped = true
			fixture.swapRepo(t, "during cache root init")
		}
		return root, currentPath, missingParts, err
	})
	hookMkdirAnalysisCacheDir(t, func(root safeio.Root, name string, perm os.FileMode, mkdir func(safeio.Root, string, os.FileMode) (fs.FileInfo, error)) (fs.FileInfo, error) {
		createdMissingPart = true
		return mkdir(root, name, perm)
	})

	cache := newAnalysisCache(Request{Cache: &CacheOptions{Enabled: true, Path: fixture.cachePath}}, fixture.repo)
	if cache.cacheable {
		t.Fatal("expected retargeted cache ancestor to be unavailable")
	}
	if !swapped {
		t.Fatal("expected test hook to swap repo before missing cache root creation")
	}
	if filepath.Base(observedAncestorPath) != filepath.Base(fixture.repo) {
		t.Fatalf("expected deepest opened ancestor to be repo, got %q", observedAncestorPath)
	}
	if !slices.Equal(observedMissingParts, []string{"missing", cacheDirName}) {
		t.Fatalf("expected missing cache components after ancestor open, got %#v", observedMissingParts)
	}
	if createdMissingPart {
		t.Fatal("expected cache root initialization to fail before creating missing path components")
	}
	if warnings := cache.takeWarnings(); len(warnings) == 0 {
		t.Fatal("expected cache initialization warning")
	}
	fixture.assertMissingPartsAbsent(t)
}

func TestNewAnalysisCacheRollsBackAllCreatedMissingPartsWhenLaterAncestorSwapFails(t *testing.T) {
	fixture := newAnalysisCacheSwapFixture(t)
	swapped := false
	hookMkdirAnalysisCacheDir(t, func(root safeio.Root, name string, perm os.FileMode, mkdir func(safeio.Root, string, os.FileMode) (fs.FileInfo, error)) (fs.FileInfo, error) {
		info, err := mkdir(root, name, perm)
		if err == nil && name == cacheDirName && !swapped {
			swapped = true
			fixture.swapRepo(t, "after nested cache root create")
		}
		return info, err
	})

	cache := newAnalysisCache(Request{Cache: &CacheOptions{Enabled: true, Path: fixture.cachePath}}, fixture.repo)
	if cache.cacheable {
		t.Fatal("expected retargeted cache ancestor to be unavailable")
	}
	if !swapped {
		t.Fatal("expected test hook to swap repo after nested cache root creation")
	}
	if warnings := cache.takeWarnings(); len(warnings) == 0 {
		t.Fatal("expected cache initialization warning")
	}
	fixture.assertMissingPartsAbsent(t)
}

func TestNewAnalysisCacheRollsBackMissingRootWhenAncestorSwapsAfterCreate(t *testing.T) {
	fixture := newAnalysisCacheSwapFixture(t)
	swapped := false
	hookMkdirAnalysisCacheDir(t, func(root safeio.Root, name string, perm os.FileMode, mkdir func(safeio.Root, string, os.FileMode) (fs.FileInfo, error)) (fs.FileInfo, error) {
		info, err := mkdir(root, name, perm)
		if err == nil && name == "missing" && !swapped {
			swapped = true
			fixture.swapRepo(t, "after missing cache root create")
		}
		return info, err
	})

	cache := newAnalysisCache(Request{Cache: &CacheOptions{Enabled: true, Path: fixture.cachePath}}, fixture.repo)
	if cache.cacheable {
		t.Fatal("expected retargeted cache ancestor to be unavailable")
	}
	if !swapped {
		t.Fatal("expected test hook to swap repo after missing cache root creation")
	}
	if warnings := cache.takeWarnings(); len(warnings) == 0 || !strings.Contains(warnings[0], "directory identity changed") {
		t.Fatalf("expected directory identity warning, got %#v", warnings)
	}
	fixture.assertMissingPartsAbsent(t)
}

func TestNewAnalysisCacheRollsBackAllCreatedPartsWhenCleanupCloseFails(t *testing.T) {
	fixture := newAnalysisCacheSwapFixture(t)
	closeErr := errors.New("close ancestor failed")
	var ancestor safeio.Root
	ancestorCloseFailed := false
	hookOpenAnalysisCacheAncestor(t, func(open func(string) (safeio.Root, string, []string, error), name string) (safeio.Root, string, []string, error) {
		root, currentPath, missingParts, err := open(name)
		if err == nil && name == fixture.cachePath {
			ancestor = root
		}
		return root, currentPath, missingParts, err
	})
	hookCloseAnalysisCacheRoot(t, func(closeRoot func(safeio.Root) error, root safeio.Root) error {
		if root == ancestor {
			ancestorCloseFailed = true
			return closeErr
		}
		return closeRoot(root)
	})
	t.Cleanup(func() {
		if ancestorCloseFailed {
			if err := ancestor.Close(); err != nil {
				t.Fatalf("close test ancestor: %v", err)
			}
		}
	})

	cache := newAnalysisCache(Request{Cache: &CacheOptions{Enabled: true, Path: fixture.cachePath}}, fixture.repo)
	if cache.cacheable {
		t.Fatal("expected close failure to make the cache unavailable")
	}
	if !ancestorCloseFailed {
		t.Fatal("expected cache initialization to close the opened ancestor")
	}
	if warnings := cache.takeWarnings(); len(warnings) == 0 || !strings.Contains(warnings[0], closeErr.Error()) {
		t.Fatalf("expected close failure warning, got %#v", warnings)
	}
	fixture.assertMissingPartsAbsent(t)
}

func TestNewAnalysisCacheRollsBackCreatedPartsWhenFinalPathValidationFails(t *testing.T) {
	fixture := newAnalysisCacheSwapFixture(t)
	validationErr := errors.New("final cache path validation failed")
	validated := false
	hookValidateAnalysisCacheRoot(t, func(validate func(string, fs.FileInfo) error, path string, expected fs.FileInfo) error {
		if path == fixture.cachePath && !validated {
			validated = true
			fixture.swapRepo(t, "before final cache root validation")
			return validationErr
		}
		return validate(path, expected)
	})

	cache := newAnalysisCache(Request{Cache: &CacheOptions{Enabled: true, Path: fixture.cachePath}}, fixture.repo)
	if cache.cacheable {
		t.Fatal("expected final validation failure to make the cache unavailable")
	}
	if !validated {
		t.Fatal("expected writable cache initialization to validate the final cache path")
	}
	if warnings := cache.takeWarnings(); len(warnings) == 0 || !strings.Contains(warnings[0], validationErr.Error()) {
		t.Fatalf("expected final validation warning, got %#v", warnings)
	}
	fixture.assertMissingPartsAbsent(t)
}

func TestNewAnalysisCacheRejectsExistingRootSwapAfterOpen(t *testing.T) {
	parent := t.TempDir()
	repo := filepath.Join(parent, "repo")
	cachePath := filepath.Join(repo, cacheDirName)
	mustMkdirCacheLayout(t, cachePath)
	renamedCachePath := filepath.Join(parent, "cache-renamed")
	swapped := false
	hookOpenAnalysisCacheAncestor(t, func(open func(string) (safeio.Root, string, []string, error), name string) (safeio.Root, string, []string, error) {
		root, currentPath, missingParts, err := open(name)
		if err == nil && name == cachePath && !swapped {
			swapped = true
			if err := os.Rename(cachePath, renamedCachePath); err != nil {
				t.Fatalf("rename cache root after open: %v", err)
			}
			if err := os.Mkdir(cachePath, 0o750); err != nil {
				t.Fatalf("replace cache root after open: %v", err)
			}
		}
		return root, currentPath, missingParts, err
	})

	cache := newAnalysisCache(Request{Cache: &CacheOptions{Enabled: true, Path: cachePath}}, repo)
	if cache.cacheable {
		t.Fatal("expected swapped existing cache root to be unavailable")
	}
	if !swapped {
		t.Fatal("expected test hook to swap cache root after open")
	}
	if warnings := cache.takeWarnings(); len(warnings) == 0 || !strings.Contains(warnings[0], "directory identity changed") {
		t.Fatalf("expected directory identity warning, got %#v", warnings)
	}
}

func TestOpenOrCreatePinnedAnalysisCacheChildPreservesOpenRootError(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	openErr := errors.New("open child denied")

	_, err := openOrCreatePinnedAnalysisCacheChild(&openRootErrorAnalysisCacheRoot{Root: root, err: openErr}, repo, "keys")
	if !errors.Is(err, openErr) {
		t.Fatalf("expected OpenRoot error to be preserved, got %v", err)
	}
	assertAnalysisCachePathAbsent(t, filepath.Join(repo, "keys"))
}

func TestOpenOrCreatePinnedAnalysisCacheChildRollsBackWhenPostCreateLstatFails(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	lstatErr := errors.New("child lstat failed after create")

	_, err := openOrCreatePinnedAnalysisCacheChild(&postCreateLstatErrorAnalysisCacheRoot{
		Root: root,
		name: cacheKeysDirName,
		err:  lstatErr,
	}, repo, cacheKeysDirName)
	if !errors.Is(err, lstatErr) {
		t.Fatalf("expected post-create lstat error to be preserved, got %v", err)
	}
	assertAnalysisCachePathAbsent(t, filepath.Join(repo, cacheKeysDirName))
}

func TestOpenOrCreatePinnedAnalysisCacheChildRollsBackWhenSecondPostCreateLstatFails(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	lstatErr := errors.New("child lstat failed after create helper returned")

	_, err := openOrCreatePinnedAnalysisCacheChild(&secondPostCreateLstatErrorAnalysisCacheRoot{
		Root: root,
		name: cacheKeysDirName,
		err:  lstatErr,
	}, repo, cacheKeysDirName)
	if !errors.Is(err, lstatErr) {
		t.Fatalf("expected second post-create lstat error to be preserved, got %v", err)
	}
	assertAnalysisCachePathAbsent(t, filepath.Join(repo, cacheKeysDirName))
}

func TestOpenOrCreatePinnedAnalysisCacheChildUsesPreCreateRollbackParentAfterRetarget(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	wrappedRoot := &retargetedPostCreateAnalysisCacheRoot{Root: root, name: cacheKeysDirName}

	_, err := openOrCreatePinnedAnalysisCacheChild(wrappedRoot, repo, cacheKeysDirName)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected retargeted post-create lookup error, got %v", err)
	}
	if !wrappedRoot.retargeted {
		t.Fatal("expected test root to retarget after creating cache child")
	}
	assertAnalysisCachePathAbsent(t, filepath.Join(repo, cacheKeysDirName))
}

func TestMkdirAnalysisCacheDirValidatesOpenedChildIdentity(t *testing.T) {
	t.Run("mkdir error is preserved", testMkdirAnalysisCacheDirPreservesMkdirError)
	t.Run("opened child lstat error is preserved", testMkdirAnalysisCacheDirPreservesOpenedLstatError)
	t.Run("opened child identity mismatch is rejected", testMkdirAnalysisCacheDirRejectsOpenedIdentityMismatch)
}

func testMkdirAnalysisCacheDirPreservesMkdirError(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	mkdirErr := errors.New("mkdir denied")

	if _, err := mkdirAnalysisCacheDir(&mkdirErrorAnalysisCacheRoot{Root: root, name: cacheKeysDirName, err: mkdirErr}, cacheKeysDirName, 0o750); !errors.Is(err, mkdirErr) {
		t.Fatalf("mkdir cache dir error = %v, want %v", err, mkdirErr)
	}
	assertAnalysisCachePathAbsent(t, filepath.Join(repo, cacheKeysDirName))
}

func testMkdirAnalysisCacheDirPreservesOpenedLstatError(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	childPath := filepath.Join(repo, cacheKeysDirName)
	childRoot, err := safeio.OpenRoot(repo)
	if err != nil {
		t.Fatalf("open alternate child root: %v", err)
	}
	lstatErr := errors.New("opened child lstat failed")

	_, err = mkdirAnalysisCacheDir(
		&openNamedRootAnalysisCacheRoot{
			Root: root,
			name: cacheKeysDirName,
			root: &lstatErrorAnalysisCacheRoot{Root: childRoot, name: ".", err: lstatErr},
		},
		cacheKeysDirName,
		0o750,
	)
	if !errors.Is(err, lstatErr) {
		t.Fatalf("mkdir cache dir opened child lstat error = %v, want %v", err, lstatErr)
	}
	assertAnalysisCacheDirExists(t, childPath)
}

func testMkdirAnalysisCacheDirRejectsOpenedIdentityMismatch(t *testing.T) {
	repo := t.TempDir()
	alternate := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	alternateRoot, err := safeio.OpenRoot(alternate)
	if err != nil {
		t.Fatalf("open alternate child root: %v", err)
	}

	_, err = mkdirAnalysisCacheDir(
		&openNamedRootAnalysisCacheRoot{Root: root, name: cacheKeysDirName, root: alternateRoot},
		cacheKeysDirName,
		0o750,
	)
	if err == nil || !strings.Contains(err.Error(), "directory changed after creation") {
		t.Fatalf("expected opened child identity mismatch, got %v", err)
	}
	assertAnalysisCacheDirExists(t, filepath.Join(repo, cacheKeysDirName))
}

func TestOpenOrCreatePinnedAnalysisCacheChildDoesNotRemovePostMkdirReplacement(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	childPath := filepath.Join(repo, cacheKeysDirName)
	renamedChildPath := filepath.Join(repo, "created-child")
	hookMkdirAnalysisCacheDir(t, func(root safeio.Root, name string, perm os.FileMode, mkdir func(safeio.Root, string, os.FileMode) (fs.FileInfo, error)) (fs.FileInfo, error) {
		info, err := mkdir(root, name, perm)
		if err != nil {
			return info, err
		}
		if err := os.Rename(childPath, renamedChildPath); err != nil {
			t.Fatalf("rename created child: %v", err)
		}
		if err := os.Mkdir(childPath, 0o750); err != nil {
			t.Fatalf("replace created child: %v", err)
		}
		return info, nil
	})

	_, err := openOrCreatePinnedAnalysisCacheChild(root, repo, cacheKeysDirName)
	if err == nil || !strings.Contains(err.Error(), "directory changed after creation") {
		t.Fatalf("expected replacement to fail closed, got %v", err)
	}
	for _, path := range []string{childPath, renamedChildPath} {
		if info, err := os.Stat(path); err != nil || !info.IsDir() {
			t.Fatalf("expected %q to remain intact, info=%#v err=%v", path, info, err)
		}
	}
}

func TestOpenOrCreatePinnedAnalysisCacheChildReturnsLstatAfterConcurrentCreateError(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	hookMkdirAnalysisCacheDir(t, func(safeio.Root, string, os.FileMode, func(safeio.Root, string, os.FileMode) (fs.FileInfo, error)) (fs.FileInfo, error) {
		return nil, fs.ErrExist
	})

	_, err := openOrCreatePinnedAnalysisCacheChild(root, repo, cacheKeysDirName)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("child lstat after fs.ErrExist = %v, want not-exist", err)
	}
}

func TestOpenOrCreatePinnedAnalysisCacheChildHandlesConcurrentCreate(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	hookMkdirAnalysisCacheDir(t, func(root safeio.Root, name string, perm os.FileMode, mkdir func(safeio.Root, string, os.FileMode) (fs.FileInfo, error)) (fs.FileInfo, error) {
		info, err := mkdir(root, name, perm)
		if err != nil {
			return nil, err
		}
		return info, fs.ErrExist
	})

	child, err := openOrCreatePinnedAnalysisCacheChild(root, repo, cacheKeysDirName)
	if err != nil {
		t.Fatalf("open concurrently created child: %v", err)
	}
	if child.created {
		t.Fatal("expected fs.ErrExist path to preserve non-created rollback state")
	}
	if err := child.root.Close(); err != nil {
		t.Fatalf("close child: %v", err)
	}
}

func TestOpenOrCreatePinnedAnalysisCacheChildFailureEdges(t *testing.T) {
	t.Run("existing child open error leaves directory intact", testOpenOrCreatePinnedAnalysisCacheChildExistingOpenError)
	t.Run("opened existing child lstat error is preserved", testOpenOrCreatePinnedAnalysisCacheChildExistingLstatError)
	t.Run("opened existing child identity mismatch fails closed", testOpenOrCreatePinnedAnalysisCacheChildExistingIdentityMismatch)
	t.Run("rollback parent open failure removes created child", testOpenOrCreatePinnedAnalysisCacheChildRollbackParentOpenFailure)
	t.Run("mkdir failure rolls back only the attempted child", testOpenOrCreatePinnedAnalysisCacheChildMkdirFailure)
}

func testOpenOrCreatePinnedAnalysisCacheChildExistingOpenError(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	childPath, _ := createAnalysisCacheChild(t, repo, cacheKeysDirName)
	openErr := errors.New("open existing child failed")

	_, err := openOrCreatePinnedAnalysisCacheChild(
		&openNamedRootErrorAnalysisCacheRoot{Root: root, name: cacheKeysDirName, err: openErr},
		repo,
		cacheKeysDirName,
	)
	if !errors.Is(err, openErr) {
		t.Fatalf("existing child open error = %v, want %v", err, openErr)
	}
	assertAnalysisCacheDirExists(t, childPath)
}

func testOpenOrCreatePinnedAnalysisCacheChildExistingLstatError(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	childPath, _ := createAnalysisCacheChild(t, repo, cacheKeysDirName)
	childRoot, err := safeio.OpenRoot(childPath)
	if err != nil {
		t.Fatalf("open child root: %v", err)
	}
	lstatErr := errors.New("opened child dot lstat failed")

	_, err = openOrCreatePinnedAnalysisCacheChild(
		&openNamedRootAnalysisCacheRoot{
			Root: root,
			name: cacheKeysDirName,
			root: &lstatErrorAnalysisCacheRoot{Root: childRoot, name: ".", err: lstatErr},
		},
		repo,
		cacheKeysDirName,
	)
	if !errors.Is(err, lstatErr) {
		t.Fatalf("opened existing child lstat error = %v, want %v", err, lstatErr)
	}
}

func testOpenOrCreatePinnedAnalysisCacheChildExistingIdentityMismatch(t *testing.T) {
	repo := t.TempDir()
	alternate := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	childPath, _ := createAnalysisCacheChild(t, repo, cacheKeysDirName)
	alternateRoot, err := safeio.OpenRoot(alternate)
	if err != nil {
		t.Fatalf("open alternate root: %v", err)
	}

	_, err = openOrCreatePinnedAnalysisCacheChild(
		&openNamedRootAnalysisCacheRoot{Root: root, name: cacheKeysDirName, root: alternateRoot},
		repo,
		cacheKeysDirName,
	)
	if err == nil || !strings.Contains(err.Error(), "directory changed while opening") {
		t.Fatalf("expected opened child identity mismatch, got %v", err)
	}
	assertAnalysisCacheDirExists(t, childPath)
}

func testOpenOrCreatePinnedAnalysisCacheChildRollbackParentOpenFailure(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	openErr := errors.New("rollback parent open failed")

	_, err := openOrCreatePinnedAnalysisCacheChild(
		&openNamedRootErrorAnalysisCacheRoot{Root: root, name: ".", err: openErr},
		repo,
		cacheKeysDirName,
	)
	if !errors.Is(err, openErr) {
		t.Fatalf("rollback parent open error = %v, want %v", err, openErr)
	}
	assertAnalysisCachePathAbsent(t, filepath.Join(repo, cacheKeysDirName))
}

func testOpenOrCreatePinnedAnalysisCacheChildMkdirFailure(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	mkdirErr := errors.New("mkdir cache child failed")
	hookMkdirAnalysisCacheDir(t, func(safeio.Root, string, os.FileMode, func(safeio.Root, string, os.FileMode) (fs.FileInfo, error)) (fs.FileInfo, error) {
		return nil, mkdirErr
	})

	_, err := openOrCreatePinnedAnalysisCacheChild(root, repo, cacheKeysDirName)
	if !errors.Is(err, mkdirErr) {
		t.Fatalf("mkdir failure error = %v, want %v", err, mkdirErr)
	}
	assertAnalysisCachePathAbsent(t, filepath.Join(repo, cacheKeysDirName))
}

func TestOpenedAnalysisCacheChildInfoBranches(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		repo := t.TempDir()
		root := openAnalysisCacheTestRoot(t, repo)
		_, wantInfo := createAnalysisCacheChild(t, repo, cacheKeysDirName)

		got, err := openedAnalysisCacheChildInfo(root, cacheKeysDirName)
		if err != nil {
			t.Fatalf("opened child info: %v", err)
		}
		if !os.SameFile(got, wantInfo) {
			t.Fatal("expected opened child info to preserve the created directory identity")
		}
	})

	t.Run("open error", func(t *testing.T) {
		repo := t.TempDir()
		root := openAnalysisCacheTestRoot(t, repo)
		openErr := errors.New("open child denied")

		if _, err := openedAnalysisCacheChildInfo(&openNamedRootErrorAnalysisCacheRoot{Root: root, name: cacheKeysDirName, err: openErr}, cacheKeysDirName); !errors.Is(err, openErr) {
			t.Fatalf("opened child info open error = %v, want %v", err, openErr)
		}
	})

	t.Run("lstat error joins close", func(t *testing.T) {
		repo := t.TempDir()
		root := openAnalysisCacheTestRoot(t, repo)
		childPath, _ := createAnalysisCacheChild(t, repo, cacheKeysDirName)
		childRoot, err := safeio.OpenRoot(childPath)
		if err != nil {
			t.Fatalf("open child root: %v", err)
		}
		lstatErr := errors.New("child dot lstat failed")
		closeErr := errors.New("close child failed")

		_, err = openedAnalysisCacheChildInfo(&openNamedRootAnalysisCacheRoot{
			Root: root,
			name: cacheKeysDirName,
			root: &closeErrorAnalysisCacheRoot{
				Root: &lstatErrorAnalysisCacheRoot{Root: childRoot, name: ".", err: lstatErr},
				err:  closeErr,
			},
		}, cacheKeysDirName)
		if !errors.Is(err, lstatErr) {
			t.Fatalf("expected lstat error to be preserved, got %v", err)
		}
		if !errors.Is(err, closeErr) {
			t.Fatalf("expected close error to be preserved, got %v", err)
		}
	})
}

func TestValidateOpenedAnalysisCacheChildRejectsRetargetedPath(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	childPath, childInfo := createAnalysisCacheChild(t, repo, cacheKeysDirName)
	childRoot, err := safeio.OpenRoot(childPath)
	if err != nil {
		t.Fatalf("open child root: %v", err)
	}
	retargetedPath := filepath.Join(repo, "missing-child")

	rollback := analysisCacheChildRollback{
		root:    root,
		name:    cacheKeysDirName,
		child:   childRoot,
		info:    childInfo,
		created: false,
	}
	err = validateOpenedAnalysisCacheChild(root, repo, retargetedPath, rollback)
	if err == nil || !strings.Contains(err.Error(), "missing-child") {
		t.Fatalf("expected retargeted child path validation failure, got %v", err)
	}
}

func TestAnalysisCacheChildHelperBranches(t *testing.T) {
	t.Run("rollback parent existing child", testRollbackParentExistingChild)
	t.Run("rollback parent missing child", testRollbackParentMissingChild)
	t.Run("rollback parent lstat error", testRollbackParentLstatError)
	t.Run("load existing child info", testLoadExistingChildInfo)
	t.Run("load child lstat error", testLoadChildLstatError)
	t.Run("validate opened child success", testValidateOpenedChildSuccess)
	t.Run("validate opened child parent identity failure", testValidateOpenedChildParentIdentityFailure)
}

func testRollbackParentExistingChild(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	createAnalysisCacheChild(t, repo, cacheKeysDirName)

	parent, err := rollbackParentForMissingAnalysisCacheChild(root, cacheKeysDirName)
	if err != nil {
		t.Fatalf("rollback parent for existing child: %v", err)
	}
	if parent != nil {
		t.Fatal("expected existing child to avoid opening a rollback parent")
	}
}

func testRollbackParentMissingChild(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)

	parent, err := rollbackParentForMissingAnalysisCacheChild(root, cacheKeysDirName)
	if err != nil {
		t.Fatalf("rollback parent for missing child: %v", err)
	}
	if parent == nil {
		t.Fatal("expected missing child to open a rollback parent")
	}
	if err := parent.Close(); err != nil {
		t.Fatalf("close rollback parent: %v", err)
	}
}

func testRollbackParentLstatError(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	lstatErr := errors.New("child lstat denied")

	parent, err := rollbackParentForMissingAnalysisCacheChild(&lstatErrorAnalysisCacheRoot{Root: root, name: cacheKeysDirName, err: lstatErr}, cacheKeysDirName)
	if !errors.Is(err, lstatErr) {
		t.Fatalf("rollback parent lstat error = %v, want %v", err, lstatErr)
	}
	if parent != nil {
		t.Fatal("expected lstat error to avoid opening a rollback parent")
	}
}

func testLoadExistingChildInfo(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	childPath, wantInfo := createAnalysisCacheChild(t, repo, cacheKeysDirName)

	gotInfo, created, err := loadOrCreateAnalysisCacheChildInfo(root, nil, childPath, cacheKeysDirName)
	if err != nil {
		t.Fatalf("load existing child info: %v", err)
	}
	if created {
		t.Fatal("expected existing child to report created=false")
	}
	if !os.SameFile(gotInfo, wantInfo) {
		t.Fatal("expected loaded child info to match the existing child")
	}
}

func testLoadChildLstatError(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	lstatErr := errors.New("load child lstat denied")

	_, created, err := loadOrCreateAnalysisCacheChildInfo(&lstatErrorAnalysisCacheRoot{Root: root, name: cacheKeysDirName, err: lstatErr}, nil, filepath.Join(repo, cacheKeysDirName), cacheKeysDirName)
	if !errors.Is(err, lstatErr) {
		t.Fatalf("load child lstat error = %v, want %v", err, lstatErr)
	}
	if created {
		t.Fatal("expected lstat error to report created=false")
	}
}

func testValidateOpenedChildSuccess(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	childPath, childInfo := createAnalysisCacheChild(t, repo, cacheKeysDirName)
	childRoot, err := safeio.OpenRoot(childPath)
	if err != nil {
		t.Fatalf("open child root: %v", err)
	}
	defer func() {
		if err := childRoot.Close(); err != nil {
			t.Fatalf("close child root: %v", err)
		}
	}()

	rollback := analysisCacheChildRollback{root: root, name: cacheKeysDirName, child: childRoot, info: childInfo, created: false}
	if err := validateOpenedAnalysisCacheChild(root, repo, childPath, rollback); err != nil {
		t.Fatalf("validate opened child success: %v", err)
	}
}

func testValidateOpenedChildParentIdentityFailure(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	childPath, childInfo := createAnalysisCacheChild(t, repo, cacheKeysDirName)
	childRoot, err := safeio.OpenRoot(childPath)
	if err != nil {
		t.Fatalf("open child root: %v", err)
	}

	rollback := analysisCacheChildRollback{root: root, name: cacheKeysDirName, child: childRoot, info: childInfo, created: false}
	err = validateOpenedAnalysisCacheChild(root, filepath.Join(repo, "missing-parent"), childPath, rollback)
	if err == nil || !strings.Contains(err.Error(), "missing-parent") {
		t.Fatalf("expected parent identity validation failure, got %v", err)
	}
}

func TestRollbackCreatedAnalysisCacheChildNoopsForUncreatedChild(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	childPath, childInfo := createAnalysisCacheChild(t, repo, cacheKeysDirName)

	if err := rollbackCreatedAnalysisCacheChild(root, cacheKeysDirName, nil, childInfo, false); err != nil {
		t.Fatalf("rollback uncreated child: %v", err)
	}
	if info, err := os.Stat(childPath); err != nil || !info.IsDir() {
		t.Fatalf("expected uncreated child to remain, info=%#v err=%v", info, err)
	}
}

func TestRollbackCreatedAnalysisCacheChildSkipsMissingOrReplacedChild(t *testing.T) {
	t.Run("missing", testRollbackCreatedAnalysisCacheChildSkipsMissingChild)
	t.Run("replaced", testRollbackCreatedAnalysisCacheChildSkipsReplacedChild)
}

func testRollbackCreatedAnalysisCacheChildSkipsMissingChild(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	childPath, childInfo := createAnalysisCacheChild(t, repo, cacheKeysDirName)
	child, err := safeio.OpenRoot(childPath)
	if err != nil {
		t.Fatalf("open child: %v", err)
	}
	if err := os.Remove(childPath); err != nil {
		t.Fatalf("remove child before rollback: %v", err)
	}

	if err := rollbackCreatedAnalysisCacheChild(root, cacheKeysDirName, child, childInfo, true); err != nil {
		t.Fatalf("rollback missing child: %v", err)
	}
	assertAnalysisCachePathAbsent(t, childPath)
}

func testRollbackCreatedAnalysisCacheChildSkipsReplacedChild(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	childPath, childInfo := createAnalysisCacheChild(t, repo, cacheKeysDirName)
	child, err := safeio.OpenRoot(childPath)
	if err != nil {
		t.Fatalf("open child: %v", err)
	}
	if err := os.Rename(childPath, filepath.Join(repo, "renamed-child")); err != nil {
		t.Fatalf("rename child before rollback: %v", err)
	}
	if err := os.Mkdir(childPath, 0o750); err != nil {
		t.Fatalf("replace child before rollback: %v", err)
	}

	if err := rollbackCreatedAnalysisCacheChild(root, cacheKeysDirName, child, childInfo, true); err != nil {
		t.Fatalf("rollback replaced child: %v", err)
	}
	assertAnalysisCacheDirExists(t, childPath)
}

func TestRollbackCreatedAnalysisCacheChildSkipsReplacementSwappedAfterVerification(t *testing.T) {
	repo := t.TempDir()
	childPath, childInfo := createAnalysisCacheChild(t, repo, cacheKeysDirName)
	renamedChildPath := filepath.Join(repo, "renamed-child")

	root := openAnalysisCacheTestRoot(t, repo)
	if err := rollbackCreatedAnalysisCacheChild(
		&lstatSwapAnalysisCacheRoot{
			t:                t,
			Root:             root,
			name:             cacheKeysDirName,
			childPath:        childPath,
			renamedChildPath: renamedChildPath,
		},
		cacheKeysDirName,
		nil,
		childInfo,
		true,
	); err != nil {
		t.Fatalf("rollback swapped child after verification: %v", err)
	}
	for _, path := range []string{childPath, renamedChildPath} {
		if info, err := os.Stat(path); err != nil || !info.IsDir() {
			t.Fatalf("expected %q to remain after rollback race, info=%#v err=%v", path, info, err)
		}
	}
}

func TestRollbackCreatedAnalysisCacheChildPreservesCurrentLstatFailure(t *testing.T) {
	repo := t.TempDir()
	root := openAnalysisCacheTestRoot(t, repo)
	childPath, childInfo := createAnalysisCacheChild(t, repo, cacheKeysDirName)
	lstatErr := errors.New("rollback current lstat failed")

	err := rollbackCreatedAnalysisCacheChild(&lstatErrorAnalysisCacheRoot{Root: root, name: cacheKeysDirName, err: lstatErr}, cacheKeysDirName, nil, childInfo, true)
	if !errors.Is(err, lstatErr) {
		t.Fatalf("expected current lstat error to be preserved, got %v", err)
	}
	assertAnalysisCacheDirExists(t, childPath)
}

func TestRollbackCreatedAnalysisCacheChildAtPathErrorBranches(t *testing.T) {
	t.Run("missing child returns nil", func(t *testing.T) {
		repo := t.TempDir()
		root := openAnalysisCacheTestRoot(t, repo)
		childPath := filepath.Join(repo, cacheKeysDirName)

		if err := rollbackCreatedAnalysisCacheChildAtPath(root, childPath, cacheKeysDirName, nil, true); err != nil {
			t.Fatalf("rollback missing child at path: %v", err)
		}
	})

	t.Run("remove failure is preserved", func(t *testing.T) {
		repo := t.TempDir()
		root := openAnalysisCacheTestRoot(t, repo)
		childPath, childInfo := createAnalysisCacheChild(t, repo, cacheKeysDirName)
		removeErr := errors.New("remove child failed")

		err := rollbackCreatedAnalysisCacheChildAtPath(
			&openReservationRemoveChildErrorAnalysisCacheRoot{
				Root:            root,
				reservationName: ".lopper-cache-rollback-keys-0",
				childName:       cacheKeysDirName,
				err:             removeErr,
			},
			childPath,
			cacheKeysDirName,
			childInfo,
			true,
		)
		if !errors.Is(err, removeErr) {
			t.Fatalf("expected remove error to be preserved, got %v", err)
		}
		assertAnalysisCachePathAbsent(t, childPath)
		quarantinePath := filepath.Join(repo, ".lopper-cache-rollback-keys-0", cacheKeysDirName)
		if info, statErr := os.Stat(quarantinePath); statErr != nil || !info.IsDir() {
			t.Fatalf("expected child to remain quarantined after failed rollback, info=%#v err=%v", info, statErr)
		}
	})
}
