package analysis

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
)

const (
	cacheDirName          = ".lopper-cache"
	cacheKeysDirName      = "keys"
	cacheObjectsDirName   = "objects"
	cacheTestGoModName    = "go.mod"
	cacheTestGoModContent = "module demo\n"
)

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

func TestHashFileOrMissingAndWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()
	missingPath := filepath.Join(dir, "missing.txt")
	digest, err := hashFileOrMissing(missingPath)
	if err != nil {
		t.Fatalf("hash missing file: %v", err)
	}
	if digest != "missing" {
		t.Fatalf("expected missing digest marker, got %q", digest)
	}

	targetPath := filepath.Join(dir, "nested", "file.txt")
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

	cases := []struct {
		name         string
		setup        func(*testing.T)
		wantReason   string
		wantHit      bool
		wantRepoPath string
	}{
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
			if err != nil {
				t.Fatalf("lookup error: %v", err)
			}
			if hit != tc.wantHit {
				t.Fatalf("unexpected hit state: got %v want %v", hit, tc.wantHit)
			}
			if tc.wantRepoPath != "" && got.RepoPath != tc.wantRepoPath {
				t.Fatalf("unexpected cached report: %#v", got)
			}
			if tc.wantReason != "" {
				if len(cache.metadata.Invalidations) == 0 || cache.metadata.Invalidations[len(cache.metadata.Invalidations)-1].Reason != tc.wantReason {
					t.Fatalf("expected invalidation reason %q, got %#v", tc.wantReason, cache.metadata.Invalidations)
				}
			}
		})
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
	if err := writeFileAtomic(targetDir, []byte("x")); err == nil {
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

func TestCollectFileRecordWalkError(t *testing.T) {
	records := make([]string, 0)
	root := t.TempDir()
	if err := collectFileRecord(root, filepath.Join(root, "missing"), nil, errors.New("walk failure"), &records); err == nil {
		t.Fatalf("expected collectFileRecord to return walk error")
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

func TestCacheFileCleanupHelpers(t *testing.T) {
	if err := closeIfPresent(nil); err != nil {
		t.Fatalf("closeIfPresent(nil): %v", err)
	}

	f, err := os.CreateTemp(t.TempDir(), "close-if-present-*")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	if err := closeIfPresent(f); err != nil {
		t.Fatalf("closeIfPresent(open): %v", err)
	}
	if err := closeIfPresent(f); err != nil {
		t.Fatalf("closeIfPresent(closed): %v", err)
	}

	path := filepath.Join(t.TempDir(), "cleanup.txt")
	cleanupFile, err := os.Create(path)
	if err != nil {
		t.Fatalf("create cleanup file: %v", err)
	}
	if err := cleanupTempFile(cleanupFile, path); err != nil {
		t.Fatalf("cleanupTempFile: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected cleanupTempFile to remove file, stat err=%v", err)
	}
}

func TestRemoveIfPresentErrorBranch(t *testing.T) {
	dir := t.TempDir()
	nonEmpty := filepath.Join(dir, "non-empty")
	if err := os.MkdirAll(nonEmpty, 0o750); err != nil {
		t.Fatalf("mkdir non-empty: %v", err)
	}
	mustWriteFile(t, filepath.Join(nonEmpty, "child.txt"), []byte("x"))
	if removeIfPresent(nonEmpty) == nil {
		t.Fatalf("expected removeIfPresent to fail for non-empty directory")
	}
	if err := removeIfPresent(filepath.Join(dir, "missing.txt")); err != nil {
		t.Fatalf("removeIfPresent missing should be nil: %v", err)
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
