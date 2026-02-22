package analysis

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
)

func TestAnalysisCacheWarningLifecycleAndSnapshot(t *testing.T) {
	cache := &analysisCache{
		metadata: report.CacheMetadata{
			Invalidations: []report.CacheInvalidation{
				{Key: "k", Reason: "reason"},
			},
		},
		warnings: []string{},
	}

	cache.warn("")
	cache.warn("cache warning")
	warnings := cache.takeWarnings()
	if len(warnings) != 1 || warnings[0] != "cache warning" {
		t.Fatalf("unexpected warnings: %#v", warnings)
	}
	if again := cache.takeWarnings(); again != nil {
		t.Fatalf("expected warnings to be drained, got %#v", again)
	}

	snapshot := cache.metadataSnapshot()
	if snapshot == nil || len(snapshot.Invalidations) != 1 {
		t.Fatalf("unexpected snapshot: %#v", snapshot)
	}
	// Snapshot should be independent copy of invalidations.
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
	// Use a file path as cache directory to force MkdirAll failure.
	blockingPath := filepath.Join(repo, "not-a-dir")
	if err := os.WriteFile(blockingPath, []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocking file: %v", err)
	}

	cache := newAnalysisCache(Request{
		Cache: &CacheOptions{
			Enabled: true,
			Path:    blockingPath,
		},
	}, repo)

	if cache.cacheable {
		t.Fatalf("expected cache to be non-cacheable when path is invalid")
	}
	warnings := cache.takeWarnings()
	if len(warnings) == 0 {
		t.Fatalf("expected warning when cache directory init fails")
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

func TestAnalysisCacheLookupBranches(t *testing.T) {
	cacheDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cacheDir, "keys"), 0o750); err != nil {
		t.Fatalf("mkdir keys: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(cacheDir, "objects"), 0o750); err != nil {
		t.Fatalf("mkdir objects: %v", err)
	}
	cache := &analysisCache{
		options:   resolvedCacheOptions{Enabled: true, Path: cacheDir},
		cacheable: true,
	}
	entry := cacheEntryDescriptor{
		KeyLabel:    "js-ts:/repo",
		KeyDigest:   "key",
		InputDigest: "input-current",
	}
	pointerPath := filepath.Join(cacheDir, "keys", entry.KeyDigest+".json")

	t.Run("pointer-corrupt", func(t *testing.T) {
		if err := os.WriteFile(pointerPath, []byte("{bad-json"), 0o600); err != nil {
			t.Fatalf("write corrupt pointer: %v", err)
		}
		_, hit, err := cache.lookup(entry)
		if err != nil || hit {
			t.Fatalf("expected miss without error for corrupt pointer, hit=%v err=%v", hit, err)
		}
		if len(cache.metadata.Invalidations) == 0 || cache.metadata.Invalidations[len(cache.metadata.Invalidations)-1].Reason != "pointer-corrupt" {
			t.Fatalf("expected pointer-corrupt invalidation, got %#v", cache.metadata.Invalidations)
		}
	})

	t.Run("input-changed", func(t *testing.T) {
		pointer, err := json.Marshal(cachePointer{InputDigest: "input-old", ObjectDigest: "obj"})
		if err != nil {
			t.Fatalf("marshal pointer: %v", err)
		}
		if err := os.WriteFile(pointerPath, pointer, 0o600); err != nil {
			t.Fatalf("write pointer: %v", err)
		}
		_, hit, err := cache.lookup(entry)
		if err != nil || hit {
			t.Fatalf("expected miss without error for input change, hit=%v err=%v", hit, err)
		}
		if cache.metadata.Invalidations[len(cache.metadata.Invalidations)-1].Reason != "input-changed" {
			t.Fatalf("expected input-changed invalidation, got %#v", cache.metadata.Invalidations)
		}
	})

	t.Run("object-missing", func(t *testing.T) {
		pointer, err := json.Marshal(cachePointer{InputDigest: entry.InputDigest, ObjectDigest: "missing-object"})
		if err != nil {
			t.Fatalf("marshal pointer: %v", err)
		}
		if err := os.WriteFile(pointerPath, pointer, 0o600); err != nil {
			t.Fatalf("write pointer: %v", err)
		}
		_, hit, err := cache.lookup(entry)
		if err != nil || hit {
			t.Fatalf("expected miss without error for missing object, hit=%v err=%v", hit, err)
		}
		if cache.metadata.Invalidations[len(cache.metadata.Invalidations)-1].Reason != "object-missing" {
			t.Fatalf("expected object-missing invalidation, got %#v", cache.metadata.Invalidations)
		}
	})

	t.Run("object-corrupt", func(t *testing.T) {
		pointer, err := json.Marshal(cachePointer{InputDigest: entry.InputDigest, ObjectDigest: "obj-corrupt"})
		if err != nil {
			t.Fatalf("marshal pointer: %v", err)
		}
		if err := os.WriteFile(pointerPath, pointer, 0o600); err != nil {
			t.Fatalf("write pointer: %v", err)
		}
		objectPath := filepath.Join(cacheDir, "objects", "obj-corrupt.json")
		if err := os.WriteFile(objectPath, []byte("{not-json"), 0o600); err != nil {
			t.Fatalf("write corrupt object: %v", err)
		}
		_, hit, err := cache.lookup(entry)
		if err != nil || hit {
			t.Fatalf("expected miss without error for corrupt object, hit=%v err=%v", hit, err)
		}
		if cache.metadata.Invalidations[len(cache.metadata.Invalidations)-1].Reason != "object-corrupt" {
			t.Fatalf("expected object-corrupt invalidation, got %#v", cache.metadata.Invalidations)
		}
	})

	t.Run("hit", func(t *testing.T) {
		payload, err := json.Marshal(cachedPayload{Report: report.Report{RepoPath: "repo"}})
		if err != nil {
			t.Fatalf("marshal payload: %v", err)
		}
		objectDigest := "obj-hit"
		if err := os.WriteFile(filepath.Join(cacheDir, "objects", objectDigest+".json"), payload, 0o600); err != nil {
			t.Fatalf("write object: %v", err)
		}
		pointer, err := json.Marshal(cachePointer{InputDigest: entry.InputDigest, ObjectDigest: objectDigest})
		if err != nil {
			t.Fatalf("marshal pointer: %v", err)
		}
		if err := os.WriteFile(pointerPath, pointer, 0o600); err != nil {
			t.Fatalf("write pointer: %v", err)
		}
		got, hit, err := cache.lookup(entry)
		if err != nil || !hit {
			t.Fatalf("expected cache hit, hit=%v err=%v", hit, err)
		}
		if got.RepoPath != "repo" {
			t.Fatalf("unexpected cached report: %#v", got)
		}
	})
}

func TestAnalysisCacheStoreAndFileCollectionBranches(t *testing.T) {
	repo := t.TempDir()
	cacheDir := filepath.Join(repo, ".lopper-cache")
	if err := os.MkdirAll(filepath.Join(cacheDir, "keys"), 0o750); err != nil {
		t.Fatalf("mkdir keys: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(cacheDir, "objects"), 0o750); err != nil {
		t.Fatalf("mkdir objects: %v", err)
	}

	readOnlyCache := &analysisCache{options: resolvedCacheOptions{Enabled: true, Path: cacheDir, ReadOnly: true}, cacheable: true}
	entry := cacheEntryDescriptor{KeyDigest: "readonly-key", InputDigest: "readonly-input"}
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
	if err := os.WriteFile(filepath.Join(ignoredDir, "config"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write ignored file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module demo\n"), 0o600); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	records, err := writableCache.collectRelevantFiles(repo)
	if err != nil {
		t.Fatalf("collect relevant files: %v", err)
	}
	if len(records) == 0 {
		t.Fatalf("expected at least one relevant file record")
	}
}

func TestAnalysisCacheHelpersAndErrorBranches(t *testing.T) {
	t.Run("prepare entry and hash json error", func(t *testing.T) {
		repo := t.TempDir()
		root := filepath.Join(repo, "pkg")
		if err := os.MkdirAll(root, 0o750); err != nil {
			t.Fatalf("mkdir root: %v", err)
		}
		if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module demo\n"), 0o600); err != nil {
			t.Fatalf("write go.mod: %v", err)
		}
		configPath := filepath.Join(repo, ".lopper.yml")
		if err := os.WriteFile(configPath, []byte("thresholds: {}\n"), 0o600); err != nil {
			t.Fatalf("write config: %v", err)
		}

		cache := &analysisCache{options: resolvedCacheOptions{Enabled: true, Path: filepath.Join(repo, ".lopper-cache")}, cacheable: true}
		entry, err := cache.prepareEntry(Request{
			Dependency:                        "lodash",
			TopN:                              1,
			RuntimeProfile:                    "node-import",
			ConfigPath:                        configPath,
			LowConfidenceWarningPercent:       intPtr(30),
			MinUsagePercentForRecommendations: intPtr(40),
			RemovalCandidateWeights:           &report.RemovalCandidateWeights{Usage: 0.5, Impact: 0.3, Confidence: 0.2},
		}, "js-ts", root)
		if err != nil {
			t.Fatalf("prepare entry: %v", err)
		}
		if entry.KeyDigest == "" || entry.InputDigest == "" {
			t.Fatalf("expected non-empty cache entry digests: %#v", entry)
		}

		if _, err := hashJSON(map[string]interface{}{"bad": func() {}}); err == nil {
			t.Fatalf("expected hashJSON to fail for unsupported value")
		}
	})

	t.Run("write atomic and hash file errors", func(t *testing.T) {
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
	})

	t.Run("prepare and load cache warnings", func(t *testing.T) {
		repo := t.TempDir()
		cacheDir := filepath.Join(repo, ".lopper-cache")
		if err := os.MkdirAll(filepath.Join(cacheDir, "keys"), 0o750); err != nil {
			t.Fatalf("mkdir keys: %v", err)
		}
		if err := os.MkdirAll(filepath.Join(cacheDir, "objects"), 0o750); err != nil {
			t.Fatalf("mkdir objects: %v", err)
		}
		cache := &analysisCache{options: resolvedCacheOptions{Enabled: true, Path: cacheDir}, cacheable: true}

		_, _, hit := prepareAndLoadCachedReport(Request{RepoPath: repo, Dependency: "dep"}, cache, "js-ts", filepath.Join(repo, "missing-root"))
		if hit {
			t.Fatalf("did not expect cache hit when prepare entry fails")
		}
		if warnings := cache.takeWarnings(); len(warnings) == 0 {
			t.Fatalf("expected warning when prepare entry fails")
		}

		root := filepath.Join(repo, "root")
		if err := os.MkdirAll(root, 0o750); err != nil {
			t.Fatalf("mkdir root: %v", err)
		}
		if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module demo\n"), 0o600); err != nil {
			t.Fatalf("write go.mod: %v", err)
		}
		entry, err := cache.prepareEntry(Request{RepoPath: repo, Dependency: "dep"}, "js-ts", root)
		if err != nil {
			t.Fatalf("prepare entry: %v", err)
		}
		if err := os.MkdirAll(filepath.Join(cacheDir, "keys", entry.KeyDigest+".json"), 0o750); err != nil {
			t.Fatalf("mkdir pointer as dir: %v", err)
		}
		_, _, hit = prepareAndLoadCachedReport(Request{RepoPath: repo, Dependency: "dep"}, cache, "js-ts", root)
		if hit {
			t.Fatalf("did not expect cache hit on lookup error")
		}
		if warnings := strings.Join(cache.takeWarnings(), "\n"); !strings.Contains(warnings, "lookup failed") {
			t.Fatalf("expected lookup warning, got %q", warnings)
		}
	})

	t.Run("store cached report warning branch", func(t *testing.T) {
		repo := t.TempDir()
		cache := &analysisCache{
			options:   resolvedCacheOptions{Enabled: true, Path: filepath.Join(repo, "cache-as-file")},
			cacheable: true,
		}
		if err := os.WriteFile(cache.options.Path, []byte("x"), 0o600); err != nil {
			t.Fatalf("write blocking cache path: %v", err)
		}
		storeCachedReport(cache, "js-ts", repo, cacheEntryDescriptor{KeyDigest: "k", InputDigest: "i"}, report.Report{})
		if warnings := cache.takeWarnings(); len(warnings) == 0 {
			t.Fatalf("expected cache store warning on invalid path")
		}
		storeCachedReport(cache, "js-ts", repo, cacheEntryDescriptor{}, report.Report{})
		if warnings := cache.takeWarnings(); warnings != nil {
			t.Fatalf("expected no warning for empty key digest, got %#v", warnings)
		}
	})

	t.Run("new cache disabled", func(t *testing.T) {
		repo := t.TempDir()
		cache := newAnalysisCache(Request{Cache: &CacheOptions{Enabled: false}}, repo)
		if cache.cacheable {
			t.Fatalf("expected disabled cache to be non-cacheable")
		}
		if cache.metadata.Enabled {
			t.Fatalf("expected metadata to mark cache disabled")
		}
	})
}

func TestCollectFileRecordErrorBranch(t *testing.T) {
	records := make([]string, 0)
	root := t.TempDir()
	err := collectFileRecord(root, filepath.Join(root, "missing"), nil, os.ErrNotExist, &records)
	if err == nil {
		t.Fatalf("expected collectFileRecord to return walk error")
	}
}

func intPtr(value int) *int { return &value }

func TestCacheServiceBranchWithNoRootSeen(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, cacheTestJSIndexFileName), "console.log('x')\n")
	adapter := &countingAdapter{id: "cachelang"}
	reg := language.NewRegistry()
	if err := reg.Register(adapter); err != nil {
		t.Fatalf("register adapter: %v", err)
	}
	svc := &Service{Registry: reg}
	_, err := svc.Analyse(context.Background(), Request{
		RepoPath: repo,
		Language: "cachelang",
		TopN:     1,
		Cache:    &CacheOptions{Enabled: true, Path: filepath.Join(repo, "cache")},
	})
	if err != nil {
		t.Fatalf("analyse with cache branch: %v", err)
	}
}
