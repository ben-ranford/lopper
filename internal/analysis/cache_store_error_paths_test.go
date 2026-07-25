package analysis

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/safeio"
)

type cacheFailAfterWriter struct {
	failOn int
	writes int
}

type cacheStoreFailureCase struct {
	name       string
	blockedDir string
	keyDigest  string
}

type cacheAtomicWriteRootStub struct {
	writeErr error
	closeErr error
}

func (r *cacheAtomicWriteRootStub) WriteFileReplacingParents(string, []byte, os.FileMode, os.FileMode) error {
	return r.writeErr
}

func (r *cacheAtomicWriteRootStub) Close() error {
	return r.closeErr
}

func (w *cacheFailAfterWriter) Write(p []byte) (int, error) {
	w.writes++
	if w.writes == w.failOn {
		return 0, errors.New("write failed")
	}
	return len(p), nil
}

func TestAnalysisCacheAdditionalBranchCoverage(t *testing.T) {
	repo := t.TempDir()
	root := mustCreateRootWithGoMod(t, repo, "pkg")
	cache := &analysisCache{options: resolvedCacheOptions{Enabled: true, Path: filepath.Join(repo, cacheDirName)}, cacheable: true}
	req := Request{
		Dependency: "dep",
		RemovalCandidateWeights: &report.RemovalCandidateWeights{
			Usage: math.NaN(),
		},
	}
	if _, err := cache.prepareEntry(req, "js-ts", root); err == nil {
		t.Fatalf("expected prepareEntry to fail when key payload cannot be marshaled")
	}

	configDir := filepath.Join(repo, "config-dir")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if _, err := cache.computeInputDigest(root, configDir); err == nil {
		t.Fatalf("expected computeInputDigest to fail for unreadable config path")
	}

	mustMkdirCacheLayout(t, cache.options.Path)
	entry := cacheEntryDescriptor{KeyDigest: "nan", InputDigest: "input"}
	if cache.store(entry, report.Report{
		Dependencies: []report.DependencyReport{{
			Name: "dep",
			RemovalCandidate: &report.RemovalCandidate{
				Score: math.NaN(),
			},
		}},
	}) == nil {
		t.Fatalf("expected cache store to fail for NaN report payload")
	}
}

func TestAnalysisCacheAdditionalAtomicWriteErrors(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	if writeFileAtomic(blocker, filepath.Join(blocker, "child.json"), []byte("x")) == nil {
		t.Fatalf("expected atomic write to fail when parent path is a file")
	}
	if entries, err := os.ReadDir(dir); err != nil {
		t.Fatalf("read temp cleanup dir: %v", err)
	} else if len(entries) != 1 || entries[0].Name() != "blocker" {
		t.Fatalf("expected atomic write failure to clean temp files, got entries=%v", entries)
	}

	if runtime.GOOS == "windows" {
		t.Skip("permission-based temp-file creation failures are not portable on windows")
	}

	readOnlyDir := filepath.Join(dir, "readonly")
	if err := os.Mkdir(readOnlyDir, 0o500); err != nil {
		t.Fatalf("mkdir readonly dir: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(readOnlyDir, 0o700); err != nil {
			t.Fatalf("restore readonly dir perms: %v", err)
		}
	})
	if writeFileAtomic(readOnlyDir, filepath.Join(readOnlyDir, "child.json"), []byte("x")) == nil {
		t.Fatalf("expected atomic write to fail when temp file cannot be created")
	}

	t.Run("writeFileAtomicWithRoot returns close error", func(t *testing.T) {
		closeErr := errors.New("close root failure")
		err := writeFileAtomicWithRoot(&cacheAtomicWriteRootStub{closeErr: closeErr}, "child.json", []byte("x"))
		if !errors.Is(err, closeErr) {
			t.Fatalf("expected close error, got %v", err)
		}
	})

	t.Run("writeFileAtomicWithRoot joins write and close errors", func(t *testing.T) {
		writeErr := errors.New("write failure")
		closeErr := errors.New("close root failure")
		err := writeFileAtomicWithRoot(&cacheAtomicWriteRootStub{writeErr: writeErr, closeErr: closeErr}, "child.json", []byte("x"))
		if !errors.Is(err, writeErr) || !errors.Is(err, closeErr) {
			t.Fatalf("expected joined write and close errors, got %v", err)
		}
	})
}

func TestAnalysisCacheAdditionalWriteBranches(t *testing.T) {
	dir := t.TempDir()
	targetPath := filepath.Join(dir, "tracked.txt")
	if err := os.WriteFile(targetPath, []byte("hello"), 0o600); err != nil {
		t.Fatalf("write tracked file: %v", err)
	}

	t.Run("writeInputDigestRecord propagates writer failures", func(t *testing.T) {
		cases := []struct {
			name   string
			failOn int
		}{
			{name: "sort key", failOn: 1},
			{name: "separator", failOn: 2},
			{name: "digest", failOn: 3},
			{name: "newline", failOn: 4},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				if err := writeInputDigestRecord(&cacheFailAfterWriter{failOn: tc.failOn}, cacheDigestInput{sortKey: "tracked", path: targetPath}); err == nil {
					t.Fatalf("expected writeInputDigestRecord to fail on write %d", tc.failOn)
				}
			})
		}
	})

	t.Run("buildRelevantFile rejects invalid root", func(t *testing.T) {
		if _, err := buildRelevantFile("\x00", targetPath); err == nil {
			t.Fatalf("expected buildRelevantFile to fail for invalid root path")
		}
	})

	t.Run("writeFileDigest bubbles file errors", func(t *testing.T) {
		if err := writeFileDigest(&cacheFailAfterWriter{}, filepath.Join(dir, cacheMissingFileName)); err == nil {
			t.Fatalf("expected writeFileDigest to fail for missing file")
		}
	})
}

func TestAnalysisCacheAdditionalStoreBranches(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission-based cache write failures are not portable on windows")
	}

	for _, tc := range []cacheStoreFailureCase{
		{name: "object write failure", blockedDir: cacheObjectsDirName, keyDigest: "object-write"},
		{name: "pointer write failure", blockedDir: cacheKeysDirName, keyDigest: "pointer-write"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testAnalysisCacheStoreWriteFailure(t, tc)
		})
	}
}

func TestAnalysisCachePinnedWriteRootValidationBranches(t *testing.T) {
	t.Run("validateOpenedWriteRoot rejects changed pinned root", func(t *testing.T) {
		expectedRoot := mustEvalSymlinks(t, t.TempDir())
		openedRoot := mustEvalSymlinks(t, t.TempDir())

		info, err := os.Lstat(expectedRoot)
		if err != nil {
			t.Fatalf("lstat expected root: %v", err)
		}
		root, err := safeio.OpenCanonicalWriteRoot(openedRoot)
		if err != nil {
			t.Fatalf("open canonical write root: %v", err)
		}
		t.Cleanup(func() {
			if closeErr := root.Close(); closeErr != nil {
				t.Errorf("close canonical write root: %v", closeErr)
			}
		})

		cache := &analysisCache{writeRootInfo: info}
		err = cache.validateOpenedWriteRoot(root)
		if err == nil || !strings.Contains(err.Error(), "cache root changed while pinned") {
			t.Fatalf("expected changed pinned root error, got %v", err)
		}
	})

	t.Run("validateOpenedWriteRoot propagates pinned root lstat errors", func(t *testing.T) {
		rootPath := mustEvalSymlinks(t, t.TempDir())
		info, err := os.Lstat(rootPath)
		if err != nil {
			t.Fatalf("lstat root path: %v", err)
		}
		root, err := safeio.OpenCanonicalWriteRoot(rootPath)
		if err != nil {
			t.Fatalf("open canonical write root: %v", err)
		}
		if err := root.Close(); err != nil {
			t.Fatalf("close canonical write root: %v", err)
		}

		cache := &analysisCache{writeRootInfo: info}
		if err := cache.validateOpenedWriteRoot(root); err == nil {
			t.Fatal("expected closed pinned root lstat error")
		}
	})
}

func TestAnalysisCacheReadCacheFileValidationBranches(t *testing.T) {
	cachePath := mustEvalSymlinks(t, t.TempDir())
	root, err := safeio.OpenCanonicalWriteRoot(cachePath)
	if err != nil {
		t.Fatalf("open canonical write root: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Errorf("close canonical write root: %v", closeErr)
		}
	})

	t.Run("readCacheFile returns lookup hook error", func(t *testing.T) {
		expectedErr := errors.New("lookup hook failure")
		withCacheLookupBeforeReadHook(t, func() error { return expectedErr })

		cache := &analysisCache{}
		_, err := cache.readCacheFile(root, "keys/missing.json")
		if !errors.Is(err, expectedErr) {
			t.Fatalf("expected lookup hook error, got %v", err)
		}
	})

	t.Run("readCacheFile returns validateWriteRoot error before read", func(t *testing.T) {
		pinnedRoot := t.TempDir()
		pinnedInfo, err := os.Lstat(pinnedRoot)
		if err != nil {
			t.Fatalf("lstat pinned root: %v", err)
		}

		cache := &analysisCache{
			options:       resolvedCacheOptions{Path: filepath.Join(cachePath, "cache")},
			writeRootPath: filepath.Join(cachePath, "missing-root"),
			writeRootInfo: pinnedInfo,
		}
		_, err = cache.readCacheFile(root, "keys/missing.json")
		if err == nil || !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("expected validateWriteRoot lstat error, got %v", err)
		}
	})

	t.Run("readCacheFile returns validateOpenedWriteRoot error before read", func(t *testing.T) {
		pinnedRoot := mustEvalSymlinks(t, t.TempDir())
		openedRootPath := mustEvalSymlinks(t, t.TempDir())
		pinnedInfo, err := os.Lstat(pinnedRoot)
		if err != nil {
			t.Fatalf("lstat pinned root: %v", err)
		}

		openedRoot, err := safeio.OpenCanonicalWriteRoot(openedRootPath)
		if err != nil {
			t.Fatalf("open canonical opened root: %v", err)
		}
		t.Cleanup(func() {
			if closeErr := openedRoot.Close(); closeErr != nil {
				t.Errorf("close canonical opened root: %v", closeErr)
			}
		})

		cache := &analysisCache{
			options:       resolvedCacheOptions{Path: pinnedRoot},
			writeRootPath: pinnedRoot,
			writeRootInfo: pinnedInfo,
		}
		_, err = cache.readCacheFile(openedRoot, "keys/missing.json")
		if err == nil || !strings.Contains(err.Error(), "cache root changed while pinned") {
			t.Fatalf("expected changed pinned root error before read, got %v", err)
		}
	})
}

func TestAnalysisCacheOpenStoreWriteRootPropagatesHookError(t *testing.T) {
	cachePath := t.TempDir()
	cache := &analysisCache{
		options:       resolvedCacheOptions{Enabled: true, Path: cachePath},
		cacheable:     true,
		writeRootPath: cachePath,
	}

	expectedErr := errors.New("store hook failure")
	withCacheStoreBeforeRootOpenHook(t, func() error { return expectedErr })

	root, err := cache.openStoreWriteRoot()
	if root != nil {
		t.Fatalf("expected no write root on hook failure, got %v", root)
	}
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected hook failure, got %v", err)
	}
}

func TestAnalysisCacheOpenStoreWriteRootReturnsValidateWriteRootError(t *testing.T) {
	pinnedRoot := t.TempDir()
	pinnedInfo, err := os.Lstat(pinnedRoot)
	if err != nil {
		t.Fatalf("lstat pinned root: %v", err)
	}

	cache := &analysisCache{
		options:       resolvedCacheOptions{Enabled: true, Path: filepath.Join(pinnedRoot, "cache")},
		writeRootPath: filepath.Join(pinnedRoot, "missing-root"),
		writeRootInfo: pinnedInfo,
	}

	root, err := cache.openStoreWriteRoot()
	if root != nil {
		t.Fatalf("expected no write root on validation failure, got %v", root)
	}
	if err == nil || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected validateWriteRoot error, got %v", err)
	}
}

func TestAnalysisCacheOpenPinnedWriteRootBranches(t *testing.T) {
	t.Run("openPinnedWriteRoot returns resolve existing tree error", func(t *testing.T) {
		cache := &analysisCache{
			options: resolvedCacheOptions{
				Enabled: true,
				Path:    string([]byte{0}),
			},
		}

		root, err := cache.openPinnedWriteRoot()
		if root != nil {
			t.Fatalf("expected no write root for invalid path, got %v", root)
		}
		if err == nil {
			t.Fatal("expected resolvePathWithinExistingTree error")
		}
	})

	t.Run("openPinnedWriteRoot returns validation error after open", func(t *testing.T) {
		rootPath := mustEvalSymlinks(t, t.TempDir())
		expectedRoot := mustEvalSymlinks(t, t.TempDir())
		expectedInfo, err := os.Lstat(expectedRoot)
		if err != nil {
			t.Fatalf("lstat expected root: %v", err)
		}

		cache := &analysisCache{
			options:       resolvedCacheOptions{Enabled: true, Path: rootPath},
			writeRootPath: rootPath,
			writeRootInfo: expectedInfo,
		}

		root, err := cache.openPinnedWriteRoot()
		if root != nil {
			t.Fatalf("expected no write root on validation failure, got %v", root)
		}
		if err == nil || !strings.Contains(err.Error(), "cache root changed while pinned") {
			t.Fatalf("expected changed pinned root error, got %v", err)
		}
	})
}

func TestAnalysisCacheLookupReturnsOpenPinnedWriteRootError(t *testing.T) {
	cache := &analysisCache{
		options: resolvedCacheOptions{
			Enabled: true,
			Path:    string([]byte{0}),
		},
		cacheable: true,
	}

	got, hit, err := cache.lookup(cacheEntryDescriptor{KeyDigest: "key"})
	if hit || got.RepoPath != "" || len(got.Dependencies) != 0 {
		t.Fatalf("expected lookup miss on pinned root error, got report=%#v hit=%v", got, hit)
	}
	if err == nil {
		t.Fatal("expected pinned root open error")
	}
}

func TestAnalysisCacheStoreReturnsOpenStoreWriteRootError(t *testing.T) {
	pinnedRoot := t.TempDir()
	pinnedInfo, err := os.Lstat(pinnedRoot)
	if err != nil {
		t.Fatalf("lstat pinned root: %v", err)
	}

	cache := &analysisCache{
		options:       resolvedCacheOptions{Enabled: true, Path: filepath.Join(pinnedRoot, "cache")},
		cacheable:     true,
		writeRootPath: filepath.Join(pinnedRoot, "missing-root"),
		writeRootInfo: pinnedInfo,
	}

	err = cache.store(cacheEntryDescriptor{KeyDigest: "key", InputDigest: "input"}, report.Report{RepoPath: "repo"})
	if err == nil || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected openStoreWriteRoot error, got %v", err)
	}
}

func testAnalysisCacheStoreWriteFailure(t *testing.T, tc cacheStoreFailureCase) {
	t.Helper()

	cachePath := filepath.Join(t.TempDir(), cacheDirName)
	objectsDir := filepath.Join(cachePath, cacheObjectsDirName)
	keysDir := filepath.Join(cachePath, cacheKeysDirName)
	for _, dir := range []string{objectsDir, keysDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	blockedPath := filepath.Join(cachePath, tc.blockedDir)
	if err := os.Chmod(blockedPath, 0o500); err != nil {
		t.Fatalf("chmod %s: %v", blockedPath, err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(blockedPath, 0o700); err != nil {
			t.Fatalf("restore %s perms: %v", blockedPath, err)
		}
	})

	if err := storeTestAnalysisCache(cachePath, tc.keyDigest); err == nil {
		t.Fatalf("expected cache store to fail when %s is not writable", tc.blockedDir)
	}
}

func storeTestAnalysisCache(cachePath, keyDigest string) error {
	entry := cacheEntryDescriptor{KeyDigest: keyDigest, InputDigest: "input"}
	rep := report.Report{RepoPath: "repo"}
	return newTestAnalysisCache(cachePath).store(entry, rep)
}

func newTestAnalysisCache(cachePath string) *analysisCache {
	return &analysisCache{
		options:   resolvedCacheOptions{Enabled: true, Path: cachePath},
		cacheable: true,
	}
}

func mustEvalSymlinks(t *testing.T, path string) string {
	t.Helper()

	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("eval symlinks for %s: %v", path, err)
	}
	return resolved
}
