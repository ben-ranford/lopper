package analysis

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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
	assert       func(*testing.T, report.Report)
}

func TestAnalysisCacheWarningLifecycleAndSnapshot(t *testing.T) {
	useTestAnalysisCacheUserCacheDir(t)
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
	useTestAnalysisCacheUserCacheDir(t)
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
	useTestAnalysisCacheUserCacheDir(t)
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
	useTestAnalysisCacheUserCacheDir(t)
	repo := t.TempDir()
	outside := t.TempDir()
	assertSymlinkedDefaultCachePathRejected(t, repo, outside, "symlinked")
}

func TestNewAnalysisCacheRejectsBrokenSymlinkedDefaultPathOutsideRepo(t *testing.T) {
	useTestAnalysisCacheUserCacheDir(t)
	repo := t.TempDir()
	outside := filepath.Join(t.TempDir(), "missing-target")
	assertSymlinkedDefaultCachePathRejected(t, repo, outside, "broken symlinked")
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
	useTestAnalysisCacheUserCacheDir(t)
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
			name: "pointer-oversized",
			setup: func(t *testing.T) {
				mustWriteFile(t, pointerPath, bytes.Repeat([]byte("x"), analysisCachePointerMaxBytes+1))
			},
			wantReason: "pointer-oversized",
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
				sig, err := cache.signPointer(entry, "missing-object")
				if err != nil {
					t.Fatalf("sign missing-object pointer: %v", err)
				}
				mustWritePointer(t, pointerPath, cachePointer{InputDigest: entry.InputDigest, ObjectDigest: "missing-object", Signature: sig})
			},
			wantReason: "object-missing",
		},
		{
			name: "object-corrupt",
			setup: func(t *testing.T) {
				objectDigest := mustWriteCachedObjectBytes(t, cacheDir, []byte("{not-json"))
				sig, err := cache.signPointer(entry, objectDigest)
				if err != nil {
					t.Fatalf("sign corrupt pointer: %v", err)
				}
				mustWritePointer(t, pointerPath, cachePointer{InputDigest: entry.InputDigest, ObjectDigest: objectDigest, Signature: sig})
			},
			wantReason: "object-corrupt",
		},
		{
			name: "object-oversized",
			setup: func(t *testing.T) {
				objectDigest := mustWriteCachedObjectBytes(t, cacheDir, bytes.Repeat([]byte("x"), analysisCacheObjectMaxBytes+1))
				sig, err := cache.signPointer(entry, objectDigest)
				if err != nil {
					t.Fatalf("sign oversized object pointer: %v", err)
				}
				mustWritePointer(t, pointerPath, cachePointer{InputDigest: entry.InputDigest, ObjectDigest: objectDigest, Signature: sig})
			},
			wantReason: "object-oversized",
		},
		{
			name: "object-tampered",
			setup: func(t *testing.T) {
				objectDigest := mustWriteCachedObject(t, cacheDir, report.Report{RepoPath: "repo"})
				sig, err := cache.signPointer(entry, objectDigest)
				if err != nil {
					t.Fatalf("sign tampered pointer: %v", err)
				}
				mustWritePointer(t, pointerPath, cachePointer{InputDigest: entry.InputDigest, ObjectDigest: objectDigest, Signature: sig})
				mustWriteFile(t, filepath.Join(cacheDir, cacheObjectsDirName, objectDigest+".json"), []byte(`{"report":{"repoPath":"tampered"}}`))
			},
			wantReason: "object-tampered",
		},
		{
			name: "hit",
			setup: func(t *testing.T) {
				objectDigest := mustWriteCachedObject(t, cacheDir, report.Report{RepoPath: "repo"})
				sig, err := cache.signPointer(entry, objectDigest)
				if err != nil {
					t.Fatalf("sign hit pointer: %v", err)
				}
				mustWritePointer(t, pointerPath, cachePointer{InputDigest: entry.InputDigest, ObjectDigest: objectDigest, Signature: sig})
			},
			wantHit:      true,
			wantRepoPath: "repo",
		},
		{
			name: "hit-at-object-size-boundary",
			setup: func(t *testing.T) {
				cachedReport := maxSizedCachedReport(t, analysisCacheObjectMaxBytes)
				objectDigest := mustWriteCachedObject(t, cacheDir, cachedReport)
				sig, err := cache.signPointer(entry, objectDigest)
				if err != nil {
					t.Fatalf("sign boundary object pointer: %v", err)
				}
				mustWritePointer(t, pointerPath, cachePointer{InputDigest: entry.InputDigest, ObjectDigest: objectDigest, Signature: sig})
			},
			wantHit: true,
			assert: func(t *testing.T, got report.Report) {
				payload, err := json.Marshal(cachedPayload{Report: got})
				if err != nil {
					t.Fatalf("marshal returned payload: %v", err)
				}
				if len(payload) > analysisCacheObjectMaxBytes {
					t.Fatalf("expected boundary payload within cap, got %d > %d", len(payload), analysisCacheObjectMaxBytes)
				}
				if len(payload) < analysisCacheObjectMaxBytes-256 {
					t.Fatalf("expected boundary payload near cap, got %d", len(payload))
				}
			},
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
	if tc.assert != nil {
		tc.assert(t, got)
	}
	if tc.wantReason == "" {
		return
	}
	if len(invalidations) == 0 || invalidations[len(invalidations)-1].Reason != tc.wantReason {
		t.Fatalf("expected invalidation reason %q, got %#v", tc.wantReason, invalidations)
	}
}

func TestAnalysisCacheStoreAndFileCollectionBranches(t *testing.T) {
	useTestAnalysisCacheUserCacheDir(t)
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
	useTestAnalysisCacheUserCacheDir(t)
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
	if _, err := cache.prepareEntry(Request{RepoPath: repo, Dependency: "dep"}, "js-ts", root); err != nil {
		t.Fatalf("prepare entry: %v", err)
	}
	blockingPath := filepath.Join(repo, "cache-as-file")
	mustWriteFile(t, blockingPath, []byte("x"))
	cache.options.Path = blockingPath
	cache.storageRoot = blockingPath
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
	useTestAnalysisCacheUserCacheDir(t)
	files := make([]cacheRelevantFile, 0)
	root := t.TempDir()
	if collectRelevantFile(root, filepath.Join(root, "missing"), nil, errors.New("walk failure"), &files) == nil {
		t.Fatalf("expected collectRelevantFile to return walk error")
	}
}

func TestCacheServiceBranchWithNoRootSeen(t *testing.T) {
	useTestAnalysisCacheUserCacheDir(t)
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

func TestResolveCacheStorageRootAndCanonicalHelpers(t *testing.T) {
	repo := t.TempDir()
	canonicalRepo, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatalf("resolve canonical repo: %v", err)
	}

	t.Run("explicit readonly missing path returns absolute path", func(t *testing.T) {
		missingPath := filepath.Join(t.TempDir(), "missing-cache")
		options := resolvedCacheOptions{
			Path:         missingPath,
			ReadOnly:     true,
			ExplicitPath: true,
		}
		resolved, err := resolveCacheStorageRoot(options, repo, canonicalRepo)
		if err != nil {
			t.Fatalf("resolve explicit readonly path: %v", err)
		}
		if resolved != missingPath {
			t.Fatalf("expected missing explicit readonly path to remain unresolved, got %s", resolved)
		}
	})

	t.Run("explicit symlink path resolves to canonical target", func(t *testing.T) {
		target := filepath.Join(t.TempDir(), "cache-target")
		if err := os.MkdirAll(target, 0o750); err != nil {
			t.Fatalf("mkdir target: %v", err)
		}
		canonicalTarget, err := filepath.EvalSymlinks(target)
		if err != nil {
			t.Fatalf("resolve canonical target: %v", err)
		}
		alias := filepath.Join(t.TempDir(), "cache-alias")
		if err := os.Symlink(target, alias); err != nil {
			t.Skipf("symlink not supported: %v", err)
		}
		options := resolvedCacheOptions{
			Path:         alias,
			ExplicitPath: true,
		}
		resolved, err := resolveCacheStorageRoot(options, repo, canonicalRepo)
		if err != nil {
			t.Fatalf("resolve explicit symlink path: %v", err)
		}
		if resolved != canonicalTarget {
			t.Fatalf("expected canonical target %s, got %s", canonicalTarget, resolved)
		}
	})

	t.Run("canonical storage root falls back to options path", func(t *testing.T) {
		target := filepath.Join(t.TempDir(), "cache-target")
		if err := os.MkdirAll(target, 0o750); err != nil {
			t.Fatalf("mkdir target: %v", err)
		}
		canonicalTarget, err := filepath.EvalSymlinks(target)
		if err != nil {
			t.Fatalf("resolve canonical target: %v", err)
		}
		cache := &analysisCache{options: resolvedCacheOptions{Path: target}}
		resolved, err := cache.canonicalStorageRoot()
		if err != nil {
			t.Fatalf("canonicalStorageRoot: %v", err)
		}
		if resolved != canonicalTarget {
			t.Fatalf("expected canonical storage root %s, got %s", canonicalTarget, resolved)
		}
	})

	t.Run("pathAtOrBelow handles empty and outside roots", func(t *testing.T) {
		if pathAtOrBelow("/tmp/path", "") {
			t.Fatal("expected empty root not to match")
		}
		if pathAtOrBelow(filepath.Join(repo, "..", "outside"), canonicalRepo) {
			t.Fatal("expected outside path not to be treated as under repo")
		}
	})

	t.Run("pathAtOrBelow evaluates windows paths by ancestry after volume checks", func(t *testing.T) {
		testCases := []struct {
			name string
			path string
			root string
			want bool
		}{
			{
				name: "different volumes false",
				path: `D:\repo\cache`,
				root: `C:\Users\test\AppData\Local`,
				want: false,
			},
			{
				name: "same drive outside root false",
				path: `C:\Users\test\AppData\Roaming\lopper`,
				root: `C:\Users\test\AppData\Local`,
				want: false,
			},
			{
				name: "same drive inside root true",
				path: `C:\Users\test\AppData\Local\lopper`,
				root: `C:\Users\test\AppData\Local`,
				want: true,
			},
			{
				name: "same drive exact root true",
				path: `C:\Users\test\AppData\Local`,
				root: `C:\Users\test\AppData\Local`,
				want: true,
			},
			{
				name: "case-insensitive drive and path true",
				path: `C:\Users\Test\AppData\LOCAL\Lopper`,
				root: `c:\users\test\appdata\local`,
				want: true,
			},
			{
				name: "traversal-like relative output rejected",
				path: `C:\repo\root\..\outside\auth`,
				root: `C:\repo\root`,
				want: false,
			},
		}
		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				if got := pathAtOrBelow(tc.path, tc.root); got != tc.want {
					t.Fatalf("pathAtOrBelow(%q, %q) = %v, want %v", tc.path, tc.root, got, tc.want)
				}
			})
		}
	})
}

func TestCanonicalUserCacheDirAdditionalBranches(t *testing.T) {
	t.Run("rejects symlink cache dir", func(t *testing.T) {
		target := t.TempDir()
		link := filepath.Join(t.TempDir(), "cache-link")
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("symlink not supported: %v", err)
		}
		if _, err := canonicalUserCacheDir(link, false); err == nil || !strings.Contains(err.Error(), "is a symlink") {
			t.Fatalf("expected symlink rejection, got %v", err)
		}
	})

	t.Run("rejects non-directory cache path", func(t *testing.T) {
		filePath := filepath.Join(t.TempDir(), "cache-file")
		if err := os.WriteFile(filePath, []byte("x"), 0o600); err != nil {
			t.Fatalf("write cache file: %v", err)
		}
		if _, err := canonicalUserCacheDir(filePath, false); err == nil || !strings.Contains(err.Error(), "not a directory") {
			t.Fatalf("expected non-directory rejection, got %v", err)
		}
	})

	t.Run("readonly missing path fails closed", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "missing-cache")
		if _, err := canonicalUserCacheDir(missing, true); err == nil || !strings.Contains(err.Error(), "missing") {
			t.Fatalf("expected readonly missing-dir rejection, got %v", err)
		}
	})

	t.Run("rejects non-directory ancestor when creating", func(t *testing.T) {
		parent := t.TempDir()
		blocker := filepath.Join(parent, "blocked")
		if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
			t.Fatalf("write blocker: %v", err)
		}
		missing := filepath.Join(blocker, "child")
		if _, err := canonicalUserCacheDir(missing, false); err == nil || (!strings.Contains(err.Error(), "ancestor is not a directory") && !strings.Contains(err.Error(), "not a directory")) {
			t.Fatalf("expected ancestor rejection, got %v", err)
		}
	})
}

func TestResolveAuthKeyAndPointerHelpersAdditionalBranches(t *testing.T) {
	if _, err := (*analysisCache)(nil).resolveAuthKey(); err == nil || !strings.Contains(err.Error(), "cache is nil") {
		t.Fatalf("expected nil-cache resolveAuthKey failure, got %v", err)
	}

	originalUserCacheDirFn := analysisCacheUserCacheDirFn
	analysisCacheUserCacheDirFn = func() (string, error) { return "", errors.New("user cache failure") }
	t.Cleanup(func() {
		analysisCacheUserCacheDirFn = originalUserCacheDirFn
	})
	cache := &analysisCache{}
	entry := cacheEntryDescriptor{KeyDigest: "key", InputDigest: "input"}
	if _, err := cache.signPointer(entry, "object"); err == nil || !strings.Contains(err.Error(), "user cache failure") {
		t.Fatalf("expected signPointer to fail without an auth key, got %v", err)
	}

	key := bytes.Repeat([]byte{0x11}, analysisCacheAuthKeyLength)
	analysisCacheUserCacheDirFn = originalUserCacheDirFn
	cache.authKey = append([]byte(nil), key...)
	signature, err := pointerSignature(key, entry, "object")
	if err != nil {
		t.Fatalf("pointerSignature: %v", err)
	}
	trusted, err := cache.pointerTrusted(entry, cachePointer{
		InputDigest:  entry.InputDigest,
		ObjectDigest: "object",
		Signature:    signature,
	})
	if err != nil {
		t.Fatalf("pointerTrusted: %v", err)
	}
	if !trusted {
		t.Fatal("expected matching pointer signature to be trusted")
	}
}

func TestAuthKeyHelpersAdditionalBranches(t *testing.T) {
	userCacheDir := setTestAnalysisCacheUserCacheDir(t)
	cachePath := filepath.Join(t.TempDir(), "cache")
	authDir := filepath.Dir(testAnalysisCacheAuthKeyPath(userCacheDir, cachePath))
	if err := os.MkdirAll(authDir, 0o750); err != nil {
		t.Fatalf("mkdir auth dir: %v", err)
	}
	authRoot, keyName := openTestAnalysisCacheAuthRoot(t, userCacheDir, cachePath)

	if _, err := invalidAuthKeyGeneration(authRoot, keyName); !errors.Is(err, errAnalysisCacheAuthKeyChanged) {
		t.Fatalf("expected missing key generation to report changed key, got %v", err)
	}

	validKeyHex := strings.Repeat("aa", analysisCacheAuthKeyLength)
	if err := os.WriteFile(filepath.Join(authDir, keyName), []byte(validKeyHex), 0o600); err != nil {
		t.Fatalf("write valid key: %v", err)
	}
	if _, err := invalidAuthKeyGeneration(authRoot, keyName); !errors.Is(err, errAnalysisCacheAuthKeyChanged) {
		t.Fatalf("expected valid key generation to report changed key, got %v", err)
	}

	if err := removeAuthFileIfPresent(authRoot, "missing.key"); err != nil {
		t.Fatalf("expected missing auth file removal to be ignored, got %v", err)
	}
	if _, err := decodeAuthKey("zz"); err == nil {
		t.Fatal("expected invalid hex auth key to fail decoding")
	}
	if _, err := decodeAuthKey("aa"); err == nil || !strings.Contains(err.Error(), "unexpected key length") {
		t.Fatalf("expected invalid auth key length, got %v", err)
	}
}

func TestAuthStoreCreationAndRotationHelpers(t *testing.T) {
	repo := t.TempDir()
	repoRoot, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	userCacheDir := filepath.Join(t.TempDir(), "user-cache")
	setTestAnalysisCacheUserCachePath(t, userCacheDir)

	t.Run("canonical user cache creation syncs parent and succeeds", func(t *testing.T) {
		missing := filepath.Join(userCacheDir, "nested")
		originalSync := analysisCacheAuthSyncDirFn
		syncCalls := 0
		analysisCacheAuthSyncDirFn = func(root *safeio.WriteRoot) error {
			syncCalls++
			return root.Sync()
		}
		t.Cleanup(func() {
			analysisCacheAuthSyncDirFn = originalSync
		})

		resolved, err := canonicalUserCacheDir(missing, false)
		if err != nil {
			t.Fatalf("canonicalUserCacheDir(create): %v", err)
		}
		if syncCalls == 0 {
			t.Fatal("expected user cache creation to sync its parent")
		}
		if _, err := os.Stat(resolved); err != nil {
			t.Fatalf("expected created canonical user cache dir, got %v", err)
		}
	})

	t.Run("open auth store creates directory and returns keyed root", func(t *testing.T) {
		cachePath := filepath.Join(t.TempDir(), "cache")
		cache := &analysisCache{
			options:     resolvedCacheOptions{Enabled: true, Path: cachePath, ExplicitPath: true},
			repoRoot:    repoRoot,
			storageRoot: cachePath,
		}
		authRoot, keyName, err := cache.openAuthStore()
		if err != nil {
			t.Fatalf("openAuthStore: %v", err)
		}
		defer func() {
			if err := authRoot.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
				t.Fatalf("close auth root: %v", err)
			}
		}()
		if !strings.HasSuffix(keyName, ".key") {
			t.Fatalf("expected hashed auth key name, got %s", keyName)
		}
		if _, err := os.Stat(filepath.Join(userCacheDir, "lopper", analysisCacheAuthDirName)); err != nil {
			t.Fatalf("expected auth dir creation, got %v", err)
		}
	})

	t.Run("create or rotate publishes missing key", func(t *testing.T) {
		cachePath := filepath.Join(t.TempDir(), "cache")
		authDir := filepath.Dir(testAnalysisCacheAuthKeyPath(userCacheDir, cachePath))
		if err := os.MkdirAll(authDir, 0o750); err != nil {
			t.Fatalf("mkdir auth dir: %v", err)
		}
		authRoot, keyName := openTestAnalysisCacheAuthRoot(t, userCacheDir, cachePath)
		cache := &analysisCache{}
		key, err := cache.createOrRotateAuthKey(authRoot, keyName, false)
		if err != nil {
			t.Fatalf("createOrRotateAuthKey: %v", err)
		}
		if len(key) != analysisCacheAuthKeyLength {
			t.Fatalf("expected complete auth key, got %d bytes", len(key))
		}
		persistedKey, err := readAnalysisCacheAuthKey(authRoot, keyName, true)
		if err != nil {
			t.Fatalf("read persisted auth key: %v", err)
		}
		if !bytes.Equal(key, persistedKey) {
			t.Fatalf("expected published key to match persisted key")
		}
	})

	t.Run("rotate invalid key installs valid replacement", func(t *testing.T) {
		cachePath := filepath.Join(t.TempDir(), "rotate-cache")
		authDir := filepath.Dir(testAnalysisCacheAuthKeyPath(userCacheDir, cachePath))
		if err := os.MkdirAll(authDir, 0o750); err != nil {
			t.Fatalf("mkdir auth dir: %v", err)
		}
		authRoot, keyName := openTestAnalysisCacheAuthRoot(t, userCacheDir, cachePath)
		keyPath := filepath.Join(authDir, keyName)
		if err := os.WriteFile(keyPath, []byte("corrupt-key"), 0o600); err != nil {
			t.Fatalf("write invalid key: %v", err)
		}
		if err := rotateInvalidAuthKey(authRoot, keyName); err != nil {
			t.Fatalf("rotateInvalidAuthKey: %v", err)
		}
		persistedKey, err := readAnalysisCacheAuthKey(authRoot, keyName, true)
		if err != nil {
			t.Fatalf("read rotated auth key: %v", err)
		}
		if len(persistedKey) != analysisCacheAuthKeyLength {
			t.Fatalf("expected rotated key length %d, got %d", analysisCacheAuthKeyLength, len(persistedKey))
		}
	})

	t.Run("resolve auth key in readonly mode treats missing store as cold cache", func(t *testing.T) {
		cache := &analysisCache{
			options:     resolvedCacheOptions{ReadOnly: true},
			repoRoot:    repoRoot,
			storageRoot: filepath.Join(t.TempDir(), "cache"),
		}
		key, err := cache.resolveAuthKey()
		if err != nil {
			t.Fatalf("resolveAuthKey(readonly missing): %v", err)
		}
		if len(key) != 0 {
			t.Fatalf("expected readonly missing auth store to return nil key, got %x", key)
		}
	})
}

func TestAuthHelperAdditionalDirectBranches(t *testing.T) {
	userCacheDir := setTestAnalysisCacheUserCacheDir(t)
	repo := t.TempDir()
	repoRoot, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}

	t.Run("open auth store rejects repo-controlled location", func(t *testing.T) {
		setTestAnalysisCacheUserCachePath(t, repoRoot)
		cache := &analysisCache{
			options:     resolvedCacheOptions{Enabled: true, Path: filepath.Join(t.TempDir(), "cache"), ExplicitPath: true},
			repoRoot:    repoRoot,
			storageRoot: filepath.Join(t.TempDir(), "cache"),
		}
		if _, _, err := cache.openAuthStore(); err == nil || !strings.Contains(err.Error(), "repository-controlled storage") {
			t.Fatalf("expected repo-controlled auth store rejection, got %v", err)
		}
	})

	t.Run("read auth key flags invalid payload", func(t *testing.T) {
		cachePath := filepath.Join(t.TempDir(), "invalid-read-cache")
		authDir := filepath.Dir(testAnalysisCacheAuthKeyPath(userCacheDir, cachePath))
		if err := os.MkdirAll(authDir, 0o750); err != nil {
			t.Fatalf("mkdir auth dir: %v", err)
		}
		authRoot, keyName := openTestAnalysisCacheAuthRoot(t, userCacheDir, cachePath)
		if err := os.WriteFile(filepath.Join(authDir, keyName), []byte("invalid"), 0o600); err != nil {
			t.Fatalf("write invalid key: %v", err)
		}
		if _, err := readAnalysisCacheAuthKey(authRoot, keyName, false); !errors.Is(err, errAnalysisCacheAuthKeyInvalid) {
			t.Fatalf("expected invalid auth key error, got %v", err)
		}
	})

	t.Run("publish missing auth key leaves existing winner intact", func(t *testing.T) {
		cachePath := filepath.Join(t.TempDir(), "publish-existing-cache")
		authDir := filepath.Dir(testAnalysisCacheAuthKeyPath(userCacheDir, cachePath))
		if err := os.MkdirAll(authDir, 0o750); err != nil {
			t.Fatalf("mkdir auth dir: %v", err)
		}
		authRoot, keyName := openTestAnalysisCacheAuthRoot(t, userCacheDir, cachePath)
		original := strings.Repeat("ab", analysisCacheAuthKeyLength)
		if err := os.WriteFile(filepath.Join(authDir, keyName), []byte(original), 0o600); err != nil {
			t.Fatalf("write winner key: %v", err)
		}
		if err := publishMissingAuthKey(authRoot, keyName); err != nil {
			t.Fatalf("publishMissingAuthKey(existing): %v", err)
		}
		persistedKey, err := os.ReadFile(filepath.Join(authDir, keyName))
		if err != nil {
			t.Fatalf("read existing winner: %v", err)
		}
		if strings.TrimSpace(string(persistedKey)) != original {
			t.Fatalf("expected existing winner to remain unchanged, got %q", string(persistedKey))
		}
	})

	t.Run("create or rotate replaces invalid key when requested", func(t *testing.T) {
		cachePath := filepath.Join(t.TempDir(), "replace-invalid-cache")
		authDir := filepath.Dir(testAnalysisCacheAuthKeyPath(userCacheDir, cachePath))
		if err := os.MkdirAll(authDir, 0o750); err != nil {
			t.Fatalf("mkdir auth dir: %v", err)
		}
		authRoot, keyName := openTestAnalysisCacheAuthRoot(t, userCacheDir, cachePath)
		if err := os.WriteFile(filepath.Join(authDir, keyName), []byte("invalid"), 0o600); err != nil {
			t.Fatalf("write invalid key: %v", err)
		}
		cache := &analysisCache{}
		key, err := cache.createOrRotateAuthKey(authRoot, keyName, true)
		if err != nil {
			t.Fatalf("createOrRotateAuthKey(replace invalid): %v", err)
		}
		if len(key) != analysisCacheAuthKeyLength {
			t.Fatalf("expected replaced key length %d, got %d", analysisCacheAuthKeyLength, len(key))
		}
	})

	t.Run("resolve auth key warns and returns cold cache for readonly invalid key", func(t *testing.T) {
		cachePath := filepath.Join(t.TempDir(), "readonly-invalid-cache")
		authDir := filepath.Dir(testAnalysisCacheAuthKeyPath(userCacheDir, cachePath))
		if err := os.MkdirAll(authDir, 0o750); err != nil {
			t.Fatalf("mkdir auth dir: %v", err)
		}
		keyPath := testAnalysisCacheAuthKeyPath(userCacheDir, cachePath)
		if err := os.WriteFile(keyPath, []byte("invalid"), 0o600); err != nil {
			t.Fatalf("write invalid key: %v", err)
		}
		cache := &analysisCache{
			options:     resolvedCacheOptions{ReadOnly: true},
			repoRoot:    repoRoot,
			storageRoot: cachePath,
		}
		key, err := cache.resolveAuthKey()
		if err != nil {
			t.Fatalf("resolveAuthKey(readonly invalid): %v", err)
		}
		if len(key) != 0 {
			t.Fatalf("expected readonly invalid key to produce cold cache, got %x", key)
		}
		if len(cache.authKey) != 0 {
			t.Fatalf("expected readonly invalid key path not to persist auth material, got %x", cache.authKey)
		}
	})

	t.Run("pointer trusted rejects missing signature", func(t *testing.T) {
		cache := &analysisCache{authKey: bytes.Repeat([]byte{0x44}, analysisCacheAuthKeyLength)}
		trusted, err := cache.pointerTrusted(cacheEntryDescriptor{KeyDigest: "key", InputDigest: "input"}, cachePointer{})
		if err != nil {
			t.Fatalf("pointerTrusted(missing signature): %v", err)
		}
		if trusted {
			t.Fatal("expected pointer without signature to be untrusted")
		}
	})
}

func TestCacheAndAuthInitializationDirectCoverage(t *testing.T) {
	userCacheDir := setTestAnalysisCacheUserCacheDir(t)
	repo := t.TempDir()
	repoRoot, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}

	t.Run("resolve auth key creates and reuses writable key", func(t *testing.T) {
		cachePath := filepath.Join(t.TempDir(), "cache")
		cache := &analysisCache{
			options:     resolvedCacheOptions{Enabled: true, Path: cachePath, ExplicitPath: true},
			repoRoot:    repoRoot,
			storageRoot: cachePath,
		}
		key, err := cache.resolveAuthKey()
		if err != nil {
			t.Fatalf("resolveAuthKey(create): %v", err)
		}
		if len(key) != analysisCacheAuthKeyLength {
			t.Fatalf("expected created auth key length %d, got %d", analysisCacheAuthKeyLength, len(key))
		}
		reused, err := cache.resolveAuthKey()
		if err != nil {
			t.Fatalf("resolveAuthKey(reuse): %v", err)
		}
		if !bytes.Equal(key, reused) {
			t.Fatalf("expected cached auth key reuse, got %x then %x", key, reused)
		}
	})

	t.Run("resolve auth key rotates invalid writable key", func(t *testing.T) {
		cachePath := filepath.Join(t.TempDir(), "rotate-invalid")
		keyPath := testAnalysisCacheAuthKeyPath(userCacheDir, cachePath)
		if err := os.MkdirAll(filepath.Dir(keyPath), 0o750); err != nil {
			t.Fatalf("mkdir auth dir: %v", err)
		}
		if err := os.WriteFile(keyPath, []byte("invalid"), 0o600); err != nil {
			t.Fatalf("write invalid key: %v", err)
		}
		cache := &analysisCache{
			options:     resolvedCacheOptions{Enabled: true, Path: cachePath, ExplicitPath: true},
			repoRoot:    repoRoot,
			storageRoot: cachePath,
		}
		key, err := cache.resolveAuthKey()
		if err != nil {
			t.Fatalf("resolveAuthKey(rotate invalid): %v", err)
		}
		if len(key) != analysisCacheAuthKeyLength {
			t.Fatalf("expected rotated auth key length %d, got %d", analysisCacheAuthKeyLength, len(key))
		}
	})

	t.Run("create or rotate returns invalid-key error when replacement disabled", func(t *testing.T) {
		cachePath := filepath.Join(t.TempDir(), "invalid-no-replace")
		authDir := filepath.Dir(testAnalysisCacheAuthKeyPath(userCacheDir, cachePath))
		if err := os.MkdirAll(authDir, 0o750); err != nil {
			t.Fatalf("mkdir auth dir: %v", err)
		}
		authRoot, keyName := openTestAnalysisCacheAuthRoot(t, userCacheDir, cachePath)
		if err := os.WriteFile(filepath.Join(authDir, keyName), []byte("invalid"), 0o600); err != nil {
			t.Fatalf("write invalid key: %v", err)
		}
		if _, err := (&analysisCache{}).createOrRotateAuthKey(authRoot, keyName, false); !errors.Is(err, errAnalysisCacheAuthKeyInvalid) {
			t.Fatalf("expected invalid key error without replacement, got %v", err)
		}
	})

	t.Run("write auth key candidate persists and cleanup removes it", func(t *testing.T) {
		cachePath := filepath.Join(t.TempDir(), "candidate-cache")
		authDir := filepath.Dir(testAnalysisCacheAuthKeyPath(userCacheDir, cachePath))
		if err := os.MkdirAll(authDir, 0o750); err != nil {
			t.Fatalf("mkdir auth dir: %v", err)
		}
		authRoot, _ := openTestAnalysisCacheAuthRoot(t, userCacheDir, cachePath)
		candidatePath, err := writeAuthKeyCandidate(authRoot, []byte(strings.Repeat("ab", analysisCacheAuthKeyLength)))
		if err != nil {
			t.Fatalf("writeAuthKeyCandidate: %v", err)
		}
		if _, err := os.Stat(filepath.Join(authDir, candidatePath)); err != nil {
			t.Fatalf("expected candidate to exist, got %v", err)
		}
		if err := removeAuthFileIfPresent(authRoot, candidatePath); err != nil {
			t.Fatalf("removeAuthFileIfPresent(existing): %v", err)
		}
		if _, err := os.Stat(filepath.Join(authDir, candidatePath)); !os.IsNotExist(err) {
			t.Fatalf("expected candidate removal, got %v", err)
		}
	})

	t.Run("resolve cache storage root covers explicit create and relative escape", func(t *testing.T) {
		explicitPath := filepath.Join(t.TempDir(), "explicit-cache")
		options := resolvedCacheOptions{
			Path:         explicitPath,
			ExplicitPath: true,
		}
		resolved, err := resolveCacheStorageRoot(options, repo, repoRoot)
		if err != nil {
			t.Fatalf("resolve explicit cache storage root: %v", err)
		}
		if _, err := os.Stat(resolved); err != nil {
			t.Fatalf("expected explicit cache root creation, got %v", err)
		}
		escapeOptions := resolvedCacheOptions{Path: filepath.Join(repo, "..", "escape")}
		if _, err := resolveCacheStorageRoot(escapeOptions, repo, repoRoot); err == nil || !strings.Contains(err.Error(), "escapes repository root") {
			t.Fatalf("expected relative escape rejection, got %v", err)
		}
	})

	t.Run("initialize storage covers writable and readonly paths", func(t *testing.T) {
		writablePath := filepath.Join(t.TempDir(), "writable-cache")
		cache := &analysisCache{options: resolvedCacheOptions{Enabled: true, Path: writablePath, ExplicitPath: true}}
		if err := cache.initializeStorage(repo); err != nil {
			t.Fatalf("initializeStorage(writable): %v", err)
		}
		for _, dir := range []string{cacheKeysDirName, cacheObjectsDirName} {
			if _, err := os.Stat(filepath.Join(writablePath, dir)); err != nil {
				t.Fatalf("expected %s dir creation, got %v", dir, err)
			}
		}

		readonlyPath := filepath.Join(t.TempDir(), "readonly-cache")
		readonlyCache := &analysisCache{options: resolvedCacheOptions{Enabled: true, Path: readonlyPath, ReadOnly: true, ExplicitPath: true}}
		if err := readonlyCache.initializeStorage(repo); err != nil {
			t.Fatalf("initializeStorage(readonly): %v", err)
		}
		if _, err := os.Stat(readonlyPath); !os.IsNotExist(err) {
			t.Fatalf("expected readonly cache init not to create storage, got %v", err)
		}
	})

	t.Run("cache path escapes repo detects symlink and in-repo directories", func(t *testing.T) {
		cachePath := filepath.Join(repo, "cache")
		if err := os.MkdirAll(cachePath, 0o750); err != nil {
			t.Fatalf("mkdir cache path: %v", err)
		}
		if cachePathEscapesRepo(cachePath, repo) {
			t.Fatal("expected in-repo directory not to escape")
		}
		symlinkPath := filepath.Join(repo, "cache-link")
		if err := os.Symlink(t.TempDir(), symlinkPath); err != nil {
			t.Skipf("symlink not supported: %v", err)
		}
		if !cachePathEscapesRepo(symlinkPath, repo) {
			t.Fatal("expected symlinked cache path to be rejected")
		}
	})
}

func TestRemainingCacheHelperBranches(t *testing.T) {
	t.Run("initialize storage fails for missing repository root", func(t *testing.T) {
		cache := &analysisCache{options: resolvedCacheOptions{Enabled: true, Path: filepath.Join(t.TempDir(), "cache"), ExplicitPath: true}}
		if err := cache.initializeStorage(filepath.Join(t.TempDir(), "missing-repo")); err == nil {
			t.Fatal("expected initializeStorage to fail for missing repository root")
		}
	})

	t.Run("initialize storage readonly returns stat error for blocked explicit path", func(t *testing.T) {
		repo := t.TempDir()
		blocker := filepath.Join(t.TempDir(), "blocked")
		if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
			t.Fatalf("write blocker: %v", err)
		}
		cache := &analysisCache{
			options: resolvedCacheOptions{
				Enabled:      true,
				Path:         filepath.Join(blocker, "cache"),
				ReadOnly:     true,
				ExplicitPath: true,
			},
		}
		if err := cache.initializeStorage(repo); err == nil {
			t.Fatal("expected readonly initializeStorage to fail when explicit path parent is not a directory")
		}
	})

	t.Run("canonical storage root returns missing-path error", func(t *testing.T) {
		cache := &analysisCache{options: resolvedCacheOptions{Path: filepath.Join(t.TempDir(), "missing-cache")}}
		if _, err := cache.canonicalStorageRoot(); err == nil {
			t.Fatal("expected canonicalStorageRoot to fail for missing path")
		}
	})

	t.Run("canonical storage root returns raw readonly missing storage root", func(t *testing.T) {
		prevEvalSymlinks := analysisCacheEvalSymlinksFn
		analysisCacheEvalSymlinksFn = func(path string) (string, error) {
			return "", os.ErrNotExist
		}
		t.Cleanup(func() {
			analysisCacheEvalSymlinksFn = prevEvalSymlinks
		})

		storageRoot := filepath.Join(t.TempDir(), "missing-parent", "missing-cache")
		cache := &analysisCache{
			options:     resolvedCacheOptions{ReadOnly: true},
			storageRoot: storageRoot,
		}
		resolved, err := cache.canonicalStorageRoot()
		if err != nil {
			t.Fatalf("canonicalStorageRoot(readonly missing storage root): %v", err)
		}
		if resolved != storageRoot {
			t.Fatalf("expected readonly missing storage root %q, got %q", storageRoot, resolved)
		}
	})

	t.Run("resolve cache storage root returns explicit eval error after creation", func(t *testing.T) {
		repo := t.TempDir()
		prevAbs := analysisCacheAbsFn
		prevMkdirAll := analysisCacheMkdirAllFn
		prevEvalSymlinks := analysisCacheEvalSymlinksFn
		analysisCacheAbsFn = func(path string) (string, error) {
			return filepath.Join(t.TempDir(), "explicit-cache"), nil
		}
		analysisCacheMkdirAllFn = func(string, os.FileMode) error {
			return nil
		}
		analysisCacheEvalSymlinksFn = func(path string) (string, error) {
			if path == repo {
				return repo, nil
			}
			return "", errors.New("eval cache root failed")
		}
		t.Cleanup(func() {
			analysisCacheAbsFn = prevAbs
			analysisCacheMkdirAllFn = prevMkdirAll
			analysisCacheEvalSymlinksFn = prevEvalSymlinks
		})

		options := resolvedCacheOptions{
			Path:         filepath.Join(t.TempDir(), "cache"),
			ExplicitPath: true,
		}
		if _, err := resolveCacheStorageRoot(options, repo, repo); err == nil || !strings.Contains(err.Error(), "eval cache root failed") {
			t.Fatalf("expected explicit cache-root eval failure, got %v", err)
		}
	})

	t.Run("read analysis cache auth key reports missing key", func(t *testing.T) {
		userCacheDir := setTestAnalysisCacheUserCacheDir(t)
		cachePath := filepath.Join(t.TempDir(), "cache")
		authDir := filepath.Dir(testAnalysisCacheAuthKeyPath(userCacheDir, cachePath))
		if err := os.MkdirAll(authDir, 0o750); err != nil {
			t.Fatalf("mkdir auth dir: %v", err)
		}
		authRoot, keyName := openTestAnalysisCacheAuthRoot(t, userCacheDir, cachePath)
		if _, err := readAnalysisCacheAuthKey(authRoot, keyName, false); !errors.Is(err, errAnalysisCacheAuthKeyMissing) {
			t.Fatalf("expected missing auth key error, got %v", err)
		}
	})

	t.Run("sign pointer succeeds and mismatched pointer stays untrusted", func(t *testing.T) {
		cache := &analysisCache{authKey: bytes.Repeat([]byte{0x55}, analysisCacheAuthKeyLength)}
		entry := cacheEntryDescriptor{KeyDigest: "key", InputDigest: "input"}
		signature, err := cache.signPointer(entry, "object")
		if err != nil {
			t.Fatalf("signPointer(success): %v", err)
		}
		if signature == "" {
			t.Fatal("expected non-empty pointer signature")
		}
		trusted, err := cache.pointerTrusted(entry, cachePointer{
			InputDigest:  entry.InputDigest,
			ObjectDigest: "other-object",
			Signature:    signature,
		})
		if err != nil {
			t.Fatalf("pointerTrusted(mismatch): %v", err)
		}
		if trusted {
			t.Fatal("expected mismatched pointer digest to be untrusted")
		}
	})
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

func mustWriteCachedObject(t *testing.T, cacheDir string, data report.Report) string {
	t.Helper()
	payload, err := json.Marshal(cachedPayload{Report: data})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return mustWriteCachedObjectBytes(t, cacheDir, payload)
}

func mustWriteCachedObjectBytes(t *testing.T, cacheDir string, payload []byte) string {
	t.Helper()
	objectDigest := sha256Hex(payload)
	mustWriteFile(t, filepath.Join(cacheDir, cacheObjectsDirName, objectDigest+".json"), payload)
	return objectDigest
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

func maxSizedCachedReport(t *testing.T, maxBytes int) report.Report {
	t.Helper()
	reportAtCap := report.Report{
		RepoPath: strings.Repeat("r", maxBytes),
	}
	payloadAtCap, err := json.Marshal(cachedPayload{Report: reportAtCap})
	if err != nil {
		t.Fatalf("marshal cap-sized payload: %v", err)
	}
	if len(payloadAtCap) <= maxBytes {
		return reportAtCap
	}

	overhead := len(payloadAtCap) - maxBytes
	if overhead <= 0 || overhead > maxBytes {
		t.Fatalf("unexpected payload overhead for cache boundary test: %d", overhead)
	}

	cachedReport := report.Report{
		RepoPath: strings.Repeat("r", maxBytes-overhead),
	}
	payload, err := json.Marshal(cachedPayload{Report: cachedReport})
	if err != nil {
		t.Fatalf("marshal boundary payload: %v", err)
	}
	if len(payload) > maxBytes {
		t.Fatalf("expected payload within cap, got %d > %d", len(payload), maxBytes)
	}
	return cachedReport
}

func useTestAnalysisCacheUserCacheDir(t *testing.T) {
	t.Helper()
	_ = setTestAnalysisCacheUserCacheDir(t)
}

func setTestAnalysisCacheUserCacheDir(t *testing.T) string {
	t.Helper()
	userCacheDir := t.TempDir()
	setTestAnalysisCacheUserCachePath(t, userCacheDir)
	return userCacheDir
}

func setTestAnalysisCacheUserCachePath(t *testing.T, userCacheDir string) {
	t.Helper()
	original := analysisCacheUserCacheDirFn
	analysisCacheUserCacheDirFn = func() (string, error) { return userCacheDir, nil }
	t.Cleanup(func() {
		analysisCacheUserCacheDirFn = original
	})
}

func testAnalysisCacheAuthKeyPath(userCacheDir, cachePath string) string {
	canonicalUserCacheDir, err := filepath.EvalSymlinks(userCacheDir)
	if err != nil {
		canonicalUserCacheDir = filepath.Clean(userCacheDir)
	}
	canonicalCachePath, err := filepath.EvalSymlinks(cachePath)
	if err != nil {
		canonicalParent, parentErr := filepath.EvalSymlinks(filepath.Dir(cachePath))
		if parentErr == nil {
			canonicalCachePath = filepath.Join(canonicalParent, filepath.Base(cachePath))
		} else {
			canonicalCachePath = filepath.Clean(cachePath)
		}
	}
	return filepath.Join(canonicalUserCacheDir, "lopper", analysisCacheAuthDirName, analysisCacheAuthKeyName(canonicalCachePath))
}

func openTestAnalysisCacheAuthRoot(t *testing.T, userCacheDir, cachePath string) (*safeio.WriteRoot, string) {
	t.Helper()
	keyPath := testAnalysisCacheAuthKeyPath(userCacheDir, cachePath)
	root, err := safeio.OpenCanonicalWriteRoot(filepath.Dir(keyPath))
	if err != nil {
		t.Fatalf("open test auth root: %v", err)
	}
	t.Cleanup(func() {
		if err := root.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			t.Errorf("close test auth root: %v", err)
		}
	})
	return root, filepath.Base(keyPath)
}

func readTestAnalysisCacheAuthKey(t *testing.T, userCacheDir, cachePath string) []byte {
	t.Helper()
	root, keyName := openTestAnalysisCacheAuthRoot(t, userCacheDir, cachePath)
	key, err := readAnalysisCacheAuthKey(root, keyName, true)
	if err != nil {
		t.Fatalf("read test auth key: %v", err)
	}
	return key
}
