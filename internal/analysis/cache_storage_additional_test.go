package analysis

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
