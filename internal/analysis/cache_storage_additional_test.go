package analysis

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/safeio"
)

func TestResolveCacheStorageRootReturnsAbsErrorForExplicitPath(t *testing.T) {
	prevAbs := analysisCacheAbsFn
	analysisCacheAbsFn = func(string) (string, error) {
		return "", errors.New("abs failed")
	}
	t.Cleanup(func() {
		analysisCacheAbsFn = prevAbs
	})

	repo := t.TempDir()
	canonicalRepo, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatalf("EvalSymlinks(repo): %v", err)
	}

	_, err = resolveCacheStorageRoot(resolvedCacheOptions{Path: filepath.Join(repo, "cache"), ExplicitPath: true, ReadOnly: true}, repo, canonicalRepo)
	if err == nil || !strings.Contains(err.Error(), "resolve cache root") || !strings.Contains(err.Error(), "abs failed") {
		t.Fatalf("expected explicit-path abs error, got %v", err)
	}
}

func TestResolveCacheStorageRootReturnsRelativeErrorForInvalidRepoPath(t *testing.T) {
	_, err := resolveCacheStorageRoot(resolvedCacheOptions{Path: "/tmp/cache"}, "\x00", "/tmp/repo")
	if err == nil || !strings.Contains(err.Error(), "resolve cache path relative to repository") {
		t.Fatalf("expected repo-relative resolution error, got %v", err)
	}
}

func TestInitializeStorageReturnsRepositoryResolutionError(t *testing.T) {
	prevEvalSymlinks := analysisCacheEvalSymlinksFn
	analysisCacheEvalSymlinksFn = func(path string) (string, error) {
		if strings.Contains(path, "repo") {
			return "", errors.New("repo resolve failed")
		}
		return prevEvalSymlinks(path)
	}
	t.Cleanup(func() {
		analysisCacheEvalSymlinksFn = prevEvalSymlinks
	})

	cache := &analysisCache{
		options: resolvedCacheOptions{
			Enabled:      true,
			Path:         filepath.Join(t.TempDir(), "cache"),
			ExplicitPath: true,
		},
	}
	err := cache.initializeStorage(filepath.Join(t.TempDir(), "repo"))
	if err == nil || !strings.Contains(err.Error(), "resolve repository root") || !strings.Contains(err.Error(), "repo resolve failed") {
		t.Fatalf("expected repository resolution error, got %v", err)
	}
}

func TestOpenPinnedStorageRootReturnsRootWithoutBaselineInfo(t *testing.T) {
	cacheDir := t.TempDir()
	cache := &analysisCache{storageRoot: cacheDir}

	root, err := cache.openPinnedStorageRoot()
	if err != nil {
		t.Fatalf("openPinnedStorageRoot(without baseline): %v", err)
	}
	if err := root.Close(); err != nil {
		t.Fatalf("close pinned root: %v", err)
	}
}

func TestOpenPinnedStorageRootReturnsRootForMatchingBaseline(t *testing.T) {
	cacheDir := t.TempDir()
	rootInfo, err := os.Stat(cacheDir)
	if err != nil {
		t.Fatalf("stat cache dir: %v", err)
	}
	cache := &analysisCache{
		storageRoot:     cacheDir,
		storageRootInfo: rootInfo,
	}

	root, err := cache.openPinnedStorageRoot()
	if err != nil {
		t.Fatalf("openPinnedStorageRoot(with baseline): %v", err)
	}
	if err := root.Close(); err != nil {
		t.Fatalf("close pinned root: %v", err)
	}
}

func TestStoreSkipsPayloadLargerThanLookupLimit(t *testing.T) {
	cacheDir := t.TempDir()
	rootInfo, err := os.Stat(cacheDir)
	if err != nil {
		t.Fatalf("stat cache dir: %v", err)
	}
	cache := &analysisCache{
		options:         resolvedCacheOptions{Enabled: true, Path: cacheDir, ExplicitPath: true},
		cacheable:       true,
		authKey:         []byte("0123456789abcdef0123456789abcdef"),
		storageRoot:     cacheDir,
		storageRootInfo: rootInfo,
	}
	reportData := report.Report{RepoPath: strings.Repeat("r", analysisCacheObjectMaxBytes)}
	serializedPayload, err := json.Marshal(cachedPayload{Report: reportData})
	if err != nil {
		t.Fatalf("marshal oversized payload: %v", err)
	}
	if len(serializedPayload) <= analysisCacheObjectMaxBytes {
		t.Fatalf("expected regression payload larger than %d bytes, got %d", analysisCacheObjectMaxBytes, len(serializedPayload))
	}

	originalWrite := analysisCacheWriteFileFn
	writeCalls := 0
	analysisCacheWriteFileFn = func(*safeio.WriteRoot, string, []byte, os.FileMode, os.FileMode) error {
		writeCalls++
		return nil
	}
	t.Cleanup(func() {
		analysisCacheWriteFileFn = originalWrite
	})

	err = cache.store(cacheEntryDescriptor{KeyDigest: "key", InputDigest: "input"}, reportData)
	if err != nil {
		t.Fatalf("store oversized payload: %v", err)
	}
	if writeCalls != 0 {
		t.Fatalf("expected oversized payload to be skipped before object or pointer writes, got %d writes", writeCalls)
	}
	if cache.metadata.Writes != 0 {
		t.Fatalf("expected skipped payload not to increment write metadata, got %d", cache.metadata.Writes)
	}
}

func TestOpenPinnedStorageRootReturnsCanonicalStorageError(t *testing.T) {
	cache := &analysisCache{storageRoot: filepath.Join(t.TempDir(), "missing-parent", "missing-cache")}
	if _, err := cache.openPinnedStorageRoot(); err == nil {
		t.Fatal("expected canonical storage error for missing pinned root")
	}
}

func TestInitializeStorageReturnsReadonlyStatErrorWhenParentIsInaccessible(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission-based stat failures are not reliable as root")
	}

	repo := t.TempDir()
	parent := filepath.Join(t.TempDir(), "blocked")
	if err := os.MkdirAll(parent, 0o700); err != nil {
		t.Fatalf("mkdir blocked parent: %v", err)
	}
	if err := os.Chmod(parent, 0o000); err != nil {
		t.Fatalf("chmod blocked parent: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(parent, 0o700); err != nil {
			t.Fatalf("restore blocked parent perms: %v", err)
		}
	})

	cache := &analysisCache{
		options: resolvedCacheOptions{
			Enabled:      true,
			Path:         filepath.Join(parent, "cache"),
			ExplicitPath: true,
			ReadOnly:     true,
		},
	}
	if err := cache.initializeStorage(repo); err == nil {
		t.Fatal("expected readonly storage stat failure")
	}
}

func TestInitializeStorageReturnsReadonlyStatErrorFromStatSeam(t *testing.T) {
	prevStat := analysisCacheStatFn
	analysisCacheStatFn = func(string) (os.FileInfo, error) {
		return nil, errors.New("stat failed")
	}
	t.Cleanup(func() {
		analysisCacheStatFn = prevStat
	})

	repo := t.TempDir()
	cache := &analysisCache{
		options: resolvedCacheOptions{
			Enabled:      true,
			Path:         filepath.Join(t.TempDir(), "cache"),
			ExplicitPath: true,
			ReadOnly:     true,
		},
	}

	if err := cache.initializeStorage(repo); err == nil || !strings.Contains(err.Error(), "stat failed") {
		t.Fatalf("expected readonly stat error, got %v", err)
	}
}

func TestInitializeStorageReturnsPinnedRootLstatError(t *testing.T) {
	prevLstat := analysisCacheRootLstatFn
	analysisCacheRootLstatFn = func(*safeio.WriteRoot, string) (os.FileInfo, error) {
		return nil, errors.New("root lstat failed")
	}
	t.Cleanup(func() {
		analysisCacheRootLstatFn = prevLstat
	})

	repo := t.TempDir()
	cacheDir := filepath.Join(t.TempDir(), "cache")
	if err := os.MkdirAll(cacheDir, 0o750); err != nil {
		t.Fatalf("mkdir cache dir: %v", err)
	}
	cache := &analysisCache{
		options: resolvedCacheOptions{
			Enabled:      true,
			Path:         cacheDir,
			ExplicitPath: true,
		},
	}

	if err := cache.initializeStorage(repo); err == nil || !strings.Contains(err.Error(), "root lstat failed") {
		t.Fatalf("expected pinned-root lstat failure, got %v", err)
	}
}

func TestOpenPinnedStorageRootReturnsPinnedRootLstatError(t *testing.T) {
	prevLstat := analysisCacheRootLstatFn
	analysisCacheRootLstatFn = func(*safeio.WriteRoot, string) (os.FileInfo, error) {
		return nil, errors.New("pinned root lstat failed")
	}
	t.Cleanup(func() {
		analysisCacheRootLstatFn = prevLstat
	})

	cacheDir := t.TempDir()
	rootInfo, err := os.Stat(cacheDir)
	if err != nil {
		t.Fatalf("stat cache dir: %v", err)
	}
	cache := &analysisCache{
		storageRoot:     cacheDir,
		storageRootInfo: rootInfo,
	}

	root, err := cache.openPinnedStorageRoot()
	if err == nil || !strings.Contains(err.Error(), "pinned root lstat failed") {
		if root != nil {
			if closeErr := root.Close(); closeErr != nil {
				t.Fatalf("close pinned root after lstat failure: %v", closeErr)
			}
		}
		t.Fatalf("expected pinned-root lstat failure, got root=%v err=%v", root, err)
	}
}

func TestLookupPropagatesStorageRootCloseFailure(t *testing.T) {
	prevCloseRoot := analysisCacheCloseRootFn
	closeErr := errors.New("close root failed")
	analysisCacheCloseRootFn = func(root *safeio.WriteRoot) error {
		return errors.Join(closeErr, root.Close())
	}
	t.Cleanup(func() {
		analysisCacheCloseRootFn = prevCloseRoot
	})

	cacheDir := t.TempDir()
	mustMkdirCacheLayout(t, cacheDir)
	entry := cacheEntryDescriptor{KeyLabel: "cache-key", KeyDigest: "key", InputDigest: "input"}
	payload := []byte(`{"report":{"repoPath":"repo"}}`)
	objectDigest := sha256Hex(payload)
	authKey := []byte("0123456789abcdef0123456789abcdef")
	signature, err := pointerSignature(authKey, entry, objectDigest)
	if err != nil {
		t.Fatalf("pointerSignature: %v", err)
	}
	mustWriteFile(t, filepath.Join(cacheDir, cacheKeysDirName, entry.KeyDigest+".json"), []byte(`{"inputDigest":"input","objectDigest":"`+objectDigest+`","signature":"`+signature+`"}`))
	mustWriteFile(t, filepath.Join(cacheDir, cacheObjectsDirName, objectDigest+".json"), payload)

	rootInfo, err := os.Stat(cacheDir)
	if err != nil {
		t.Fatalf("stat cache dir: %v", err)
	}
	cache := &analysisCache{
		options:         resolvedCacheOptions{Enabled: true, Path: cacheDir, ExplicitPath: true},
		cacheable:       true,
		authKey:         authKey,
		storageRoot:     cacheDir,
		storageRootInfo: rootInfo,
	}

	got, hit, err := cache.lookup(entry)
	if !errors.Is(err, closeErr) {
		t.Fatalf("expected close failure from lookup, got %v", err)
	}
	if hit {
		t.Fatal("expected close failure to fail closed without a cache hit")
	}
	if !reflect.DeepEqual(got, report.Report{}) {
		t.Fatalf("expected close failure to clear cached payload, got %#v", got)
	}
	if cache.metadata.Hits != 0 {
		t.Fatalf("expected close failure not to increment hit count, got %d", cache.metadata.Hits)
	}
}

func TestLookupJoinsPrimaryAndStorageRootCloseFailure(t *testing.T) {
	prevCloseRoot := analysisCacheCloseRootFn
	prevUserCacheDir := analysisCacheUserCacheDirFn
	closeErr := errors.New("close root failed")
	primaryErr := errors.New("user-cache lookup failed")
	analysisCacheCloseRootFn = func(root *safeio.WriteRoot) error {
		return errors.Join(closeErr, root.Close())
	}
	analysisCacheUserCacheDirFn = func() (string, error) {
		return "", primaryErr
	}
	t.Cleanup(func() {
		analysisCacheCloseRootFn = prevCloseRoot
		analysisCacheUserCacheDirFn = prevUserCacheDir
	})

	cacheDir := t.TempDir()
	mustMkdirCacheLayout(t, cacheDir)
	entry := cacheEntryDescriptor{KeyLabel: "cache-key", KeyDigest: "key", InputDigest: "input"}
	payload := []byte(`{"report":{"repoPath":"repo"}}`)
	objectDigest := sha256Hex(payload)
	signature, err := pointerSignature([]byte("0123456789abcdef0123456789abcdef"), entry, objectDigest)
	if err != nil {
		t.Fatalf("pointerSignature: %v", err)
	}
	mustWriteFile(t, filepath.Join(cacheDir, cacheKeysDirName, entry.KeyDigest+".json"), []byte(`{"inputDigest":"input","objectDigest":"`+objectDigest+`","signature":"`+signature+`"}`))
	mustWriteFile(t, filepath.Join(cacheDir, cacheObjectsDirName, objectDigest+".json"), payload)

	rootInfo, err := os.Stat(cacheDir)
	if err != nil {
		t.Fatalf("stat cache dir: %v", err)
	}
	cache := &analysisCache{
		options:         resolvedCacheOptions{Enabled: true, Path: cacheDir, ExplicitPath: true},
		cacheable:       true,
		storageRoot:     cacheDir,
		storageRootInfo: rootInfo,
	}

	got, hit, err := cache.lookup(entry)
	if !errors.Is(err, primaryErr) || !errors.Is(err, closeErr) {
		t.Fatalf("expected joined primary and close failures, got %v", err)
	}
	if hit {
		t.Fatal("expected joined failure to fail closed without a cache hit")
	}
	if !reflect.DeepEqual(got, report.Report{}) {
		t.Fatalf("expected joined failure to clear cached payload, got %#v", got)
	}
	if cache.metadata.Hits != 0 {
		t.Fatalf("expected joined failure not to increment hit count, got %d", cache.metadata.Hits)
	}
}
