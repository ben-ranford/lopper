package analysis

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	_ "unsafe"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	runtimetrace "github.com/ben-ranford/lopper/internal/runtime"
	"github.com/ben-ranford/lopper/internal/safeio"
)

//go:linkname safeioWriteFileParentReadyFn github.com/ben-ranford/lopper/internal/safeio.writeFileParentReadyFn
var safeioWriteFileParentReadyFn func() error

//go:linkname safeioWriteFilePublishReadyFn github.com/ben-ranford/lopper/internal/safeio.writeFilePublishReadyFn
var safeioWriteFilePublishReadyFn func() error

//go:linkname safeioWriteFileRenameReadyFn github.com/ben-ranford/lopper/internal/safeio.writeFileRenameReadyFn
var safeioWriteFileRenameReadyFn func() error

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

func TestNewAnalysisCacheRejectsRootReplacementAfterValidation(t *testing.T) {
	repo := t.TempDir()
	cachePath := filepath.Join(repo, cacheDirName)
	outside := t.TempDir()
	if err := os.Symlink(outside, cachePath); err != nil {
		t.Skipf("replace cache root with symlink: %v", err)
	}

	cache := newAnalysisCache(Request{Cache: &CacheOptions{Enabled: true, Path: cachePath}}, repo)
	if cache.cacheable {
		t.Fatal("expected replaced cache root to fail closed")
	}
	assertAnalysisCachePathAbsent(t, filepath.Join(outside, cacheKeysDirName))
	assertAnalysisCachePathAbsent(t, filepath.Join(outside, cacheObjectsDirName))
}

func TestNewAnalysisCacheRejectsSymlinkedCacheChild(t *testing.T) {
	repo := t.TempDir()
	cachePath := filepath.Join(repo, cacheDirName)
	outside := t.TempDir()
	if err := os.Mkdir(cachePath, 0o750); err != nil {
		t.Fatalf("create cache root: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(cachePath, cacheKeysDirName)); err != nil {
		t.Skipf("create cache child symlink: %v", err)
	}

	cache := newAnalysisCache(Request{Cache: &CacheOptions{Enabled: true, Path: cachePath}}, repo)
	if cache.cacheable {
		t.Fatal("expected symlinked cache child to fail closed")
	}
	assertAnalysisCachePathAbsent(t, filepath.Join(outside, "key.json"))
}

func TestNewAnalysisCacheRejectsSymlinkedAncestorAndCleansTraversal(t *testing.T) {
	t.Run("symlinked ancestor", func(t *testing.T) {
		repo := t.TempDir()
		outside := t.TempDir()
		ancestor := filepath.Join(repo, "ancestor")
		if err := os.Symlink(outside, ancestor); err != nil {
			t.Skipf("symlink not supported: %v", err)
		}
		cache := newAnalysisCache(Request{Cache: &CacheOptions{Enabled: true, Path: filepath.Join(ancestor, "cache")}}, repo)
		if cache.cacheable {
			t.Fatal("expected symlinked cache ancestry to fail closed")
		}
		assertAnalysisCachePathAbsent(t, filepath.Join(outside, "cache"))
	})

	t.Run("traversal is cleaned before initialization", func(t *testing.T) {
		repo := t.TempDir()
		cachePath := filepath.Join(repo, "nested", "..", cacheDirName)
		mustMkdirCacheLayout(t, filepath.Join(repo, cacheDirName))
		cache := newAnalysisCache(Request{Cache: &CacheOptions{Enabled: true, Path: cachePath}}, repo)
		if !cache.cacheable {
			t.Fatalf("expected cleaned cache path to remain usable, warnings=%#v", cache.takeWarnings())
		}
		if _, err := os.Stat(filepath.Join(repo, cacheDirName, cacheKeysDirName)); err != nil {
			t.Fatalf("expected keys under cleaned cache root: %v", err)
		}
	})
}

func TestAnalysisCacheStoreRejectsRootReplacementBeforeMutation(t *testing.T) {
	repo := t.TempDir()
	cachePath := filepath.Join(repo, cacheDirName)
	mustMkdirCacheLayout(t, cachePath)
	outside := t.TempDir()
	cache := newAnalysisCache(Request{Cache: &CacheOptions{Enabled: true, Path: cachePath}}, repo)
	if !cache.cacheable {
		t.Fatalf("expected cacheable setup, warnings=%#v", cache.takeWarnings())
	}
	if err := os.Rename(cachePath, filepath.Join(repo, "cache-holding")); err != nil {
		t.Fatalf("move cache root: %v", err)
	}
	if err := os.Symlink(outside, cachePath); err != nil {
		t.Skipf("replace cache root with symlink: %v", err)
	}

	err := cache.store(cacheEntryDescriptor{KeyDigest: "key", InputDigest: "input"}, report.Report{RepoPath: repo})
	if err == nil {
		t.Fatal("expected root replacement before cache mutation to fail")
	}
	assertAnalysisCachePathAbsent(t, filepath.Join(outside, cacheObjectsDirName))
	assertAnalysisCachePathAbsent(t, filepath.Join(outside, cacheKeysDirName))
}

func TestAnalysisCacheOpenWriteRootInitializesMissingIdentity(t *testing.T) {
	repo := t.TempDir()
	cachePath := filepath.Join(repo, cacheDirName)
	mustMkdirCacheLayout(t, cachePath)
	cache := &analysisCache{options: resolvedCacheOptions{Enabled: true, Path: cachePath}, cacheable: true}

	root, err := cache.openWriteRoot()
	if err != nil {
		t.Fatalf("open write root: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Errorf("close write root: %v", closeErr)
		}
	})
	if cache.rootIdentity == nil {
		t.Fatal("expected openWriteRoot to initialize cache root identity")
	}
}

func TestAnalysisCacheStableRootUsesAnalysisRepoMapping(t *testing.T) {
	stableRepo := filepath.Join(t.TempDir(), "stable")
	analysisRepo := filepath.Join(t.TempDir(), "analysis")
	cache := newAnalysisCache(Request{Cache: &CacheOptions{Enabled: false}}, stableRepo, analysisRepo)

	got := cache.stableCacheRoot(filepath.Join(analysisRepo, "nested", cacheDirName))
	want := filepath.Join(stableRepo, "nested", cacheDirName)
	if got != want {
		t.Fatalf("stable cache root = %q, want %q", got, want)
	}
	outside := filepath.Join(t.TempDir(), cacheDirName)
	if got := cache.stableCacheRoot(outside); got != outside {
		t.Fatalf("outside cache root = %q, want original %q", got, outside)
	}
	var nilCache *analysisCache
	if got := nilCache.stableCacheRoot(outside); got != outside {
		t.Fatalf("nil cache stable root = %q, want original %q", got, outside)
	}
}

func TestAnalysisCacheStoreRejectsRootReplacementBetweenWrites(t *testing.T) {
	assertAnalysisCacheStoreRejectsRootReplacementAfterWriteParentReady(t, 1, "between cache writes")
}

func TestAnalysisCacheStoreRejectsRootReplacementDuringPointerPublish(t *testing.T) {
	assertAnalysisCacheStoreRejectsRootReplacementAfterWriteParentReady(t, 2, "during pointer publish")
}

func TestAnalysisCacheStoreRejectsRootReplacementAtPointerCommit(t *testing.T) {
	repo, cache, cachePath, _, movedRoot := newReplaceableCacheForStoreTest(t)
	replacementRoot := filepath.Join(repo, "cache-replacement")
	mustMkdirCacheLayout(t, replacementRoot)

	originalHook := safeioWriteFilePublishReadyFn
	t.Cleanup(func() { safeioWriteFilePublishReadyFn = originalHook })
	publishCalls := 0
	safeioWriteFilePublishReadyFn = func() error {
		publishCalls++
		if publishCalls != 2 {
			return nil
		}
		if err := os.Rename(cachePath, movedRoot); err != nil {
			return err
		}
		return os.Rename(replacementRoot, cachePath)
	}

	err := cache.store(cacheEntryDescriptor{KeyDigest: "key", InputDigest: "input"}, report.Report{RepoPath: repo})
	if err == nil {
		t.Fatal("expected root replacement at pointer commit to fail")
	}
	if !isDirectoryIdentityOrMissingError(err) {
		t.Fatalf("expected directory identity or missing-parent error, got %v", err)
	}
	if publishCalls != 2 {
		t.Fatalf("expected cache root swap at pointer commit, got %d publish calls", publishCalls)
	}
	assertAnalysisCachePathAbsent(t, filepath.Join(movedRoot, cacheKeysDirName, "key.json"))
	assertAnalysisCachePathAbsent(t, filepath.Join(cachePath, cacheKeysDirName, "key.json"))
}

func TestAnalysisCacheStoreRejectsKeysRetargetAtPointerCommit(t *testing.T) {
	repo, cache, cachePath, _, _ := newReplaceableCacheForStoreTest(t)
	keysPath := filepath.Join(cachePath, cacheKeysDirName)
	movedKeys := filepath.Join(repo, "moved-keys")

	originalHook := safeioWriteFilePublishReadyFn
	t.Cleanup(func() { safeioWriteFilePublishReadyFn = originalHook })
	publishCalls := 0
	safeioWriteFilePublishReadyFn = func() error {
		publishCalls++
		if publishCalls != 2 {
			return nil
		}
		if err := os.Rename(keysPath, movedKeys); err != nil {
			return err
		}
		return os.Mkdir(keysPath, 0o750)
	}

	err := cache.store(cacheEntryDescriptor{KeyDigest: "key", InputDigest: "input"}, report.Report{RepoPath: repo})
	if err == nil {
		t.Fatal("expected keys retarget at pointer commit to fail")
	}
	if !isDirectoryIdentityOrMissingError(err) {
		t.Fatalf("expected directory identity or missing-parent error, got %v", err)
	}
	if publishCalls != 2 {
		t.Fatalf("expected keys retarget at pointer commit, got %d publish calls", publishCalls)
	}
	assertAnalysisCachePathAbsent(t, filepath.Join(movedKeys, "key.json"))
	assertAnalysisCachePathAbsent(t, filepath.Join(keysPath, "key.json"))
}

func TestAnalysisCacheStoreRejectsKeysReplacementAfterParentPinned(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory replacement semantics are covered on Unix")
	}

	repo, cache, cachePath, _, _ := newReplaceableCacheForStoreTest(t)
	keysPath := filepath.Join(cachePath, cacheKeysDirName)
	movedKeys := filepath.Join(repo, "moved-keys")

	originalHook := safeioWriteFileParentReadyFn
	t.Cleanup(func() { safeioWriteFileParentReadyFn = originalHook })
	parentReadyCalls := 0
	safeioWriteFileParentReadyFn = func() error {
		parentReadyCalls++
		if parentReadyCalls != 2 {
			return nil
		}
		if err := os.Rename(keysPath, movedKeys); err != nil {
			return err
		}
		return os.Mkdir(keysPath, 0o750)
	}

	err := cache.store(cacheEntryDescriptor{KeyDigest: "key", InputDigest: "input"}, report.Report{RepoPath: repo})
	if err == nil {
		t.Fatal("expected keys replacement after parent pin to fail")
	}
	if !isDirectoryIdentityOrMissingError(err) {
		t.Fatalf("expected directory identity or missing-parent error, got %v", err)
	}
	if parentReadyCalls != 2 {
		t.Fatalf("expected keys replacement during pointer publish, got %d parent-ready calls", parentReadyCalls)
	}
	assertAnalysisCachePathAbsent(t, filepath.Join(movedKeys, "key.json"))
	assertAnalysisCachePathAbsent(t, filepath.Join(keysPath, "key.json"))
}

func TestAnalysisCacheStoreRejectsRootReplacementBetweenFinalCheckAndPointerRename(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory replacement semantics are covered on Unix")
	}

	repo, cache, cachePath, _, movedRoot := newReplaceableCacheForStoreTest(t)
	keysPath := filepath.Join(cachePath, cacheKeysDirName)
	replacementRoot := filepath.Join(repo, "cache-replacement")
	if err := os.Mkdir(replacementRoot, 0o750); err != nil {
		t.Fatalf("mkdir replacement root: %v", err)
	}

	originalHook := safeioWriteFileRenameReadyFn
	t.Cleanup(func() { safeioWriteFileRenameReadyFn = originalHook })
	renameReadyCalls := 0
	safeioWriteFileRenameReadyFn = func() error {
		renameReadyCalls++
		if err := os.Rename(cachePath, movedRoot); err != nil {
			return err
		}
		if err := os.Rename(replacementRoot, cachePath); err != nil {
			return err
		}
		return os.Rename(filepath.Join(movedRoot, cacheKeysDirName), keysPath)
	}

	err := cache.store(cacheEntryDescriptor{KeyDigest: "key", InputDigest: "input"}, report.Report{RepoPath: repo})
	if err == nil {
		t.Fatal("expected root replacement after final parent check to fail")
	}
	if !strings.Contains(err.Error(), "directory identity changed") {
		t.Fatalf("expected directory identity error, got %v", err)
	}
	if renameReadyCalls != 1 {
		t.Fatalf("expected one pointer rename-ready call, got %d", renameReadyCalls)
	}
	assertAnalysisCachePathAbsent(t, filepath.Join(keysPath, "key.json"))
	assertAnalysisCachePathAbsent(t, filepath.Join(movedRoot, cacheKeysDirName, "key.json"))
}

func TestVerifyPinnedAnalysisCacheDirectoryRejectsRetargetedPath(t *testing.T) {
	cachePath, root := retargetPinnedAnalysisCacheRoot(t)

	if _, err := verifyPinnedAnalysisCacheDirectory(root, cachePath); err == nil || !strings.Contains(err.Error(), "directory identity changed") {
		t.Fatalf("expected retargeted cache root to be rejected, got %v", err)
	}
}

func TestOpenOrCreatePinnedAnalysisCacheChildRejectsRetargetedParentPath(t *testing.T) {
	cachePath, root := retargetPinnedAnalysisCacheRoot(t)

	if _, err := openOrCreatePinnedAnalysisCacheChild(root, cachePath, cacheKeysDirName); err == nil || !strings.Contains(err.Error(), "directory identity changed") {
		t.Fatalf("expected retargeted parent path to be rejected, got %v", err)
	}
}

func retargetPinnedAnalysisCacheRoot(t *testing.T) (string, safeio.Root) {
	t.Helper()

	cachePath := filepath.Join(t.TempDir(), cacheDirName)
	if err := os.Mkdir(cachePath, 0o750); err != nil {
		t.Fatalf("mkdir cache root: %v", err)
	}
	root, err := safeio.OpenRootNoFollow(cachePath)
	if err != nil {
		t.Fatalf("open cache root: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Errorf("close cache root: %v", closeErr)
		}
	})
	movedRoot := filepath.Join(filepath.Dir(cachePath), "cache-moved")
	if err := os.Rename(cachePath, movedRoot); err != nil {
		t.Fatalf("move cache root: %v", err)
	}
	if err := os.Mkdir(cachePath, 0o750); err != nil {
		t.Fatalf("replace cache root: %v", err)
	}

	return cachePath, root
}

func assertAnalysisCacheStoreRejectsRootReplacementAfterWriteParentReady(t *testing.T, replaceOnCall int, stage string) {
	t.Helper()
	repo, cache, cachePath, outside, movedRoot := newReplaceableCacheForStoreTest(t)
	withAnalysisCacheStoreReplacementAfterWriteParentReady(t, cachePath, outside, movedRoot, replaceOnCall)

	err := cache.store(cacheEntryDescriptor{KeyDigest: "key", InputDigest: "input"}, report.Report{RepoPath: repo})
	if err == nil {
		t.Fatalf("expected root replacement %s to fail", stage)
	}
	if !isDirectoryIdentityOrMissingError(err) {
		t.Fatalf("expected directory identity or missing-parent error, got %v", err)
	}
	assertAnalysisCachePathAbsent(t, filepath.Join(outside, cacheKeysDirName, "key.json"))
	assertAnalysisCachePathAbsent(t, filepath.Join(movedRoot, cacheKeysDirName, "key.json"))
}

func newReplaceableCacheForStoreTest(t *testing.T) (string, *analysisCache, string, string, string) {
	t.Helper()
	repo := t.TempDir()
	cachePath := filepath.Join(repo, cacheDirName)
	mustMkdirCacheLayout(t, cachePath)
	cache := newAnalysisCache(Request{Cache: &CacheOptions{Enabled: true, Path: cachePath}}, repo)
	if !cache.cacheable {
		t.Fatalf("expected cacheable setup, warnings=%#v", cache.takeWarnings())
	}
	return repo, cache, cachePath, t.TempDir(), filepath.Join(repo, "cache-holding")
}

func withAnalysisCacheStoreReplacementAfterWriteParentReady(t *testing.T, cachePath, outside, movedRoot string, replaceOnCall int) {
	t.Helper()
	originalHook := safeioWriteFileParentReadyFn
	t.Cleanup(func() { safeioWriteFileParentReadyFn = originalHook })
	callCount := 0
	safeioWriteFileParentReadyFn = func() error {
		callCount++
		if callCount != replaceOnCall {
			return nil
		}
		if err := os.Rename(cachePath, movedRoot); err != nil {
			return err
		}
		return os.Symlink(outside, cachePath)
	}
}

func isDirectoryIdentityOrMissingError(err error) bool {
	return strings.Contains(err.Error(), "directory identity changed") || errors.Is(err, os.ErrNotExist)
}

func TestAnalysisCacheStorePreservesExistingObject(t *testing.T) {
	repo := t.TempDir()
	cachePath := filepath.Join(repo, cacheDirName)
	mustMkdirCacheLayout(t, cachePath)
	cache := newAnalysisCache(Request{Cache: &CacheOptions{Enabled: true, Path: cachePath}}, repo)
	if !cache.cacheable {
		t.Fatalf("expected cacheable setup, warnings=%#v", cache.takeWarnings())
	}
	reportData := report.Report{RepoPath: repo}
	serializedPayload, err := json.Marshal(newCachedPayload(reportData))
	if err != nil {
		t.Fatalf("marshal cache payload: %v", err)
	}
	objectPath := filepath.Join(cachePath, cacheObjectsDirName, sha256Hex(serializedPayload)+".json")
	if err := os.WriteFile(objectPath, []byte("existing complete object"), 0o640); err != nil {
		t.Fatalf("seed cache object: %v", err)
	}

	if err := cache.store(cacheEntryDescriptor{KeyDigest: "key", InputDigest: "input"}, reportData); err != nil {
		t.Fatalf("store cache report: %v", err)
	}
	if got, err := os.ReadFile(objectPath); err != nil {
		t.Fatalf("read preserved cache object: %v", err)
	} else if string(got) != "existing complete object" {
		t.Fatalf("existing cache object = %q, want preserved content", got)
	}
}

func TestAnalysisCacheOpenWriteRootFailureBranches(t *testing.T) {
	t.Run("open canonical write root failure after validation", testAnalysisCacheOpenWriteRootCanonicalOpenFailure)
	t.Run("opened canonical root must match pinned identity", testAnalysisCacheOpenWriteRootIdentityMismatch)
	t.Run("second validation after opening write root fails closed", testAnalysisCacheOpenWriteRootSecondValidationFailure)
}

func testAnalysisCacheOpenWriteRootCanonicalOpenFailure(t *testing.T) {
	fixture := newAnalysisCacheWriteRootFixture(t)
	renamedPath := filepath.Join(fixture.repo, "cache-renamed")

	hookValidateAnalysisCacheRootCall(t, fixture.cachePath, func(call int) (bool, error) {
		if call != 1 {
			return false, nil
		}
		if err := os.Rename(fixture.cachePath, renamedPath); err != nil {
			t.Fatalf("rename cache root before canonical open: %v", err)
		}
		return true, nil
	})

	if _, err := fixture.cache.openWriteRoot(); err == nil || !strings.Contains(err.Error(), "open canonical root") {
		t.Fatalf("open write root error = %v, want canonical open failure", err)
	}
}

func testAnalysisCacheOpenWriteRootIdentityMismatch(t *testing.T) {
	fixture := newAnalysisCacheWriteRootFixture(t)
	renamedPath := filepath.Join(fixture.repo, "cache-renamed")

	hookValidateAnalysisCacheRootCall(t, fixture.cachePath, func(call int) (bool, error) {
		if call != 1 {
			return false, nil
		}
		if err := os.Rename(fixture.cachePath, renamedPath); err != nil {
			t.Fatalf("rename cache root before canonical open: %v", err)
		}
		mustMkdirCacheLayout(t, fixture.cachePath)
		return true, nil
	})

	if _, err := fixture.cache.openWriteRoot(); err == nil || !strings.Contains(err.Error(), "pinned root identity changed") {
		t.Fatalf("open write root identity error = %v, want identity mismatch", err)
	}
}

func testAnalysisCacheOpenWriteRootSecondValidationFailure(t *testing.T) {
	fixture := newAnalysisCacheWriteRootFixture(t)
	validationErr := errors.New("post-open validation failed")

	hookValidateAnalysisCacheRootCall(t, fixture.cachePath, func(call int) (bool, error) {
		if call == 2 {
			return true, validationErr
		}
		return false, nil
	})

	if _, err := fixture.cache.openWriteRoot(); !errors.Is(err, validationErr) {
		t.Fatalf("open write root error = %v, want %v", err, validationErr)
	}
}

func TestCachePathEscapesRepoHandlesResolvedAndMissingPaths(t *testing.T) {
	repo := t.TempDir()
	inside := filepath.Join(repo, cacheDirName)
	if err := os.Mkdir(inside, 0o750); err != nil {
		t.Fatalf("create inside cache path: %v", err)
	}
	if cachePathEscapesRepo(inside, repo) {
		t.Fatal("expected cache path inside repository to be accepted")
	}
	if !cachePathEscapesRepo(t.TempDir(), repo) {
		t.Fatal("expected cache path outside repository to be rejected")
	}
	if !cachePathEscapesRepo(inside, filepath.Join(repo, "missing-repository")) {
		t.Fatal("expected cache path to be rejected when its repository root is missing")
	}
}

func TestResolveCacheOptionsUsesResolvedPath(t *testing.T) {
	resolvedPath := filepath.Join(t.TempDir(), "resolved-cache")
	options := resolveCacheOptions(&CacheOptions{Enabled: true, Path: "requested-cache", ResolvedPath: resolvedPath}, "repository")
	if options.Path != resolvedPath {
		t.Fatalf("cache path = %q, want resolved path %q", options.Path, resolvedPath)
	}
}

func TestAnalysisCacheDigestSerializationErrors(t *testing.T) {
	dir := t.TempDir()
	existingPath := filepath.Join(dir, "cache-input.go")
	if err := os.WriteFile(existingPath, []byte("cache input"), 0o600); err != nil {
		t.Fatalf("write cache input: %v", err)
	}
	if _, err := hashJSON(make(chan struct{})); err == nil {
		t.Fatal("expected unsupported digest value to fail JSON serialization")
	}
	if err := writeFileDigest(&cacheFailAfterWriter{failOn: 1}, existingPath); err == nil {
		t.Fatal("expected digest write failure")
	}
	if err := writeFileDigestOrMissing(&cacheFailAfterWriter{failOn: 1}, filepath.Join(t.TempDir(), cacheMissingFileName)); err == nil {
		t.Fatal("expected missing digest marker write failure")
	}
	if _, err := hashFile(filepath.Join(dir, cacheMissingFileName)); err == nil {
		t.Fatal("expected missing file hash to fail")
	}
	if _, err := hashFileDigest(dir); err == nil {
		t.Fatal("expected directory digest to fail")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read cache input directory: %v", err)
	}
	files := make([]cacheRelevantFile, 0)
	if err := collectRelevantFile("\x00", existingPath, entries[0], nil, &files); err == nil {
		t.Fatal("expected invalid cache root to reject relevant file")
	}
}

func TestAnalysisCacheLookupRejectsMissingDefaultCacheRoot(t *testing.T) {
	cache := &analysisCache{
		options:         resolvedCacheOptions{Enabled: true, Path: filepath.Join(t.TempDir(), cacheDirName)},
		cacheable:       true,
		rejectReadHits:  true,
		metadata:        report.CacheMetadata{},
		inputDigestMemo: make(map[cacheInputDigestMemoKey]string),
	}

	if _, hit, err := cache.lookup(cacheEntryDescriptor{KeyDigest: "key", KeyLabel: "adapter:root"}); err == nil || hit {
		t.Fatalf("lookup with a missing no-follow cache root = hit=%v err=%v, want error", hit, err)
	}
}

func TestAnalysisCacheLookupRejectsUsageIncompleteIndexOutsideReport(t *testing.T) {
	cache, entry := cacheWithPayloadForLookupTest(t, cachedPayload{UsageIncompleteDependencies: []int{0}}, "invalid-usage-incomplete-index")

	assertLookupMissWithReason(t, cache, entry, cacheObjectCorruptReason)
}

func TestCachePointerExistsRejectsNonDirectoryKeysPath(t *testing.T) {
	cachePath := t.TempDir()
	if err := os.WriteFile(filepath.Join(cachePath, cacheKeysDirName), []byte("not-a-directory"), 0o600); err != nil {
		t.Fatalf("write keys path: %v", err)
	}

	if _, err := cachePointerExists(cachePath, "key"); err == nil {
		t.Fatal("expected non-directory keys path to fail cache pointer lookup")
	}
}

func assertAnalysisCachePathAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected %q to remain absent, stat err=%v", path, err)
	}
}

func assertAnalysisCacheDirExists(t *testing.T, path string) {
	t.Helper()
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		t.Fatalf("expected %q to be a directory, info=%#v err=%v", path, info, err)
	}
}

func assertAnalysisCacheSameFile(t *testing.T, path string, want fs.FileInfo) {
	t.Helper()
	got, err := os.Lstat(path)
	if err != nil || !os.SameFile(got, want) {
		t.Fatalf("expected %q to keep identity, got=%#v want=%#v err=%v", path, got, want, err)
	}
}

func assertAnalysisCacheQuarantineSuffix(t *testing.T, quarantineName, suffix string) {
	t.Helper()
	if !strings.HasSuffix(filepath.Dir(quarantineName), suffix) {
		t.Fatalf("expected quarantine suffix %s, got %q", suffix, quarantineName)
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

func TestLockOrConfigFileRecognizesGradleVersionCatalogs(t *testing.T) {
	if !lockOrConfigFile("libs.versions.toml") {
		t.Fatalf("expected Gradle version catalogs to participate in cache invalidation")
	}
	if lockOrConfigFile("README.md") {
		t.Fatalf("did not expect README.md to be treated as a cache-relevant config file")
	}
}

func TestAnalysisCacheCollectsPHPShortOpenTagConfigs(t *testing.T) {
	repo := t.TempDir()
	for _, filename := range []string{"php.ini", ".user.ini", ".htaccess"} {
		mustWriteFile(t, filepath.Join(repo, filename), []byte("short_open_tag = On\n"))
	}

	cache := &analysisCache{}
	records, err := cache.collectRelevantFiles(repo)
	if err != nil {
		t.Fatalf("collect relevant files: %v", err)
	}
	collected := make(map[string]struct{}, len(records))
	for _, record := range records {
		collected[record.relativePath] = struct{}{}
	}
	for _, filename := range []string{"php.ini", ".user.ini", ".htaccess"} {
		if _, ok := collected[filename]; !ok {
			t.Fatalf("expected %s to participate in cache invalidation, got %#v", filename, collected)
		}
	}
}

func TestAnalysisCachePHPShortOpenTagConfigChangesInvalidateInputDigest(t *testing.T) {
	for _, filename := range []string{"php.ini", ".user.ini", ".htaccess"} {
		t.Run(filename, func(t *testing.T) {
			repo := t.TempDir()
			configPath := filepath.Join(repo, filename)
			mustWriteFile(t, configPath, []byte("short_open_tag = Off\n"))

			cache := &analysisCache{}
			before, err := cache.computeInputDigest(repo, "")
			if err != nil {
				t.Fatalf("compute digest before config update: %v", err)
			}
			mustWriteFile(t, configPath, []byte("short_open_tag = On\n"))
			after, err := cache.computeInputDigest(repo, "")
			if err != nil {
				t.Fatalf("compute digest after config update: %v", err)
			}
			if before == after {
				t.Fatalf("expected %s update to invalidate the input digest", filename)
			}
		})
	}
}

func TestAnalysisCachePHPShortOpenTagTraversalCutoffInvalidatesInputDigest(t *testing.T) {
	repo := t.TempDir()
	mustWriteFile(t, filepath.Join(repo, "z-config", "php.ini"), []byte("short_open_tag = On\n"))

	cache := &analysisCache{}
	before, err := cache.computeInputDigest(repo, "")
	if err != nil {
		t.Fatalf("compute digest before traversal change: %v", err)
	}
	for i := 0; i < shared.PHPShortOpenTagConfigWalkEntryLimit; i++ {
		if err := os.Mkdir(filepath.Join(repo, fmt.Sprintf("a-%04d", i)), 0o750); err != nil {
			t.Fatalf("create traversal entry %d: %v", i, err)
		}
	}
	after, err := cache.computeInputDigest(repo, "")
	if err != nil {
		t.Fatalf("compute digest after traversal change: %v", err)
	}
	if before == after {
		t.Fatal("expected PHP short_open_tag traversal cutoff to invalidate the input digest")
	}
}

func TestAnalysisCacheExplicitRuntimeTraceExcludesOnlyTraceArtifacts(t *testing.T) {
	repo := t.TempDir()
	tracePath := filepath.Join("tests", "trace.ndjson")
	sourcePath := filepath.Join(repo, "tests", "source.php")
	mustWriteFile(t, sourcePath, []byte("<?php echo 'before';\n"))

	cache := &analysisCache{}
	exclusions := cache.cacheAnalysisExclusions(repo, Request{RuntimeTracePath: tracePath})
	before, err := cache.computeInputDigestWithExclusions(repo, "", exclusions)
	if err != nil {
		t.Fatalf("compute digest before trace artifacts: %v", err)
	}
	mustWriteFile(t, filepath.Join(repo, tracePath), []byte("{\"module\":\"example\"}\n"))
	mustWriteFile(t, runtimetrace.TraceStatePath(filepath.Join(repo, tracePath)), []byte("{\"schema\":\"v2\"}\n"))
	afterTrace, err := cache.computeInputDigestWithExclusions(repo, "", exclusions)
	if err != nil {
		t.Fatalf("compute digest after trace artifacts: %v", err)
	}
	if before != afterTrace {
		t.Fatal("expected generated runtime trace artifacts not to invalidate the static input digest")
	}
	mustWriteFile(t, sourcePath, []byte("<?php echo 'after';\n"))
	afterSource, err := cache.computeInputDigestWithExclusions(repo, "", exclusions)
	if err != nil {
		t.Fatalf("compute digest after source change: %v", err)
	}
	if afterTrace == afterSource {
		t.Fatal("expected source beside an explicit runtime trace to invalidate the input digest")
	}
}

func TestAnalysisCacheRuntimeTraceResolvesRelativeToRepoRootForNestedCandidateRoots(t *testing.T) {
	repo := t.TempDir()
	nestedRoot := filepath.Join(repo, "pkg")
	tracePath := filepath.Join("pkg", "tests", "trace.ndjson")
	sourcePath := filepath.Join(nestedRoot, "tests", "source.php")
	mustWriteFile(t, sourcePath, []byte("<?php echo 'before';\n"))

	cache := &analysisCache{}
	req := Request{RuntimeTracePath: tracePath}

	exclusions := cache.cacheAnalysisExclusions(nestedRoot, req, repo)
	before, err := cache.computeInputDigestWithExclusions(nestedRoot, "", exclusions)
	if err != nil {
		t.Fatalf("compute digest before trace artifacts: %v", err)
	}
	mustWriteFile(t, filepath.Join(repo, tracePath), []byte("{\"module\":\"example\"}\n"))
	after, err := cache.computeInputDigestWithExclusions(nestedRoot, "", exclusions)
	if err != nil {
		t.Fatalf("compute digest after trace artifacts: %v", err)
	}
	if before != after {
		t.Fatal("expected a relative runtime trace path to resolve against the repository root, excluding trace artifacts from a nested candidate root's digest")
	}
}

func TestAnalysisCacheRuntimeTraceExclusionRemapsIntoScopedWorkspace(t *testing.T) {
	trueRepo := t.TempDir()
	scopedRoot := t.TempDir()
	tracePath := filepath.Join("tests", "trace.ndjson")
	sourcePath := filepath.Join(scopedRoot, "tests", "source.php")
	mustWriteFile(t, sourcePath, []byte("<?php echo 'before';\n"))

	cache := &analysisCache{stableRepoPath: trueRepo, analysisRepoPath: scopedRoot}
	req := Request{RuntimeTracePath: tracePath}

	exclusions := cache.cacheAnalysisExclusions(scopedRoot, req, trueRepo)
	before, err := cache.computeInputDigestWithExclusions(scopedRoot, "", exclusions)
	if err != nil {
		t.Fatalf("compute digest before trace artifacts: %v", err)
	}
	mustWriteFile(t, filepath.Join(scopedRoot, tracePath), []byte("{\"module\":\"example\"}\n"))
	after, err := cache.computeInputDigestWithExclusions(scopedRoot, "", exclusions)
	if err != nil {
		t.Fatalf("compute digest after trace artifacts: %v", err)
	}
	if before != after {
		t.Fatal("expected a runtime trace exclusion resolved against the true repo root to be remapped into a scoped workspace copy's candidate root")
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
	if err := writeFileAtomic(targetPath, []byte("hello")); err != nil {
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
	if writeFileAtomic(targetDir, []byte("x")) == nil {
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
