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
	"github.com/ben-ranford/lopper/internal/testutil"
)

const (
	cacheTestJSIndexFileName     = "index.js"
	cacheTestPackageJSONFileName = "package.json"
)

type countingAdapter struct {
	id    string
	calls int
}

func (a *countingAdapter) ID() string { return a.id }
func (a *countingAdapter) Aliases() []string {
	return nil
}
func (a *countingAdapter) Detect(context.Context, string) (bool, error) {
	return true, nil
}
func (a *countingAdapter) Analyse(context.Context, language.Request) (report.Report, error) {
	a.calls++
	return report.Report{
		Dependencies: []report.DependencyReport{{
			Name:              "dep",
			UsedExportsCount:  1,
			TotalExportsCount: 2,
			UsedPercent:       50,
		}},
	}, nil
}

func newCacheTestService(t *testing.T) (*Service, *countingAdapter) {
	t.Helper()
	adapter := &countingAdapter{id: "cachelang"}
	reg := language.NewRegistry()
	if err := reg.Register(adapter); err != nil {
		t.Fatalf("register adapter: %v", err)
	}
	return &Service{Registry: reg}, adapter
}

func newCacheRequest(repo, cacheDir string, readOnly bool) Request {
	return Request{
		RepoPath: repo,
		Language: "cachelang",
		TopN:     1,
		Cache: &CacheOptions{
			Enabled:  true,
			Path:     cacheDir,
			ReadOnly: readOnly,
		},
	}
}

func TestAnalysisCacheHitAndInvalidation(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, cacheTestJSIndexFileName), "import { map } from \"lodash\"\nmap([1], (x) => x)\n")
	testutil.MustWriteFile(t, filepath.Join(repo, cacheTestPackageJSONFileName), "{\n  \"name\": \"demo\"\n}\n")

	svc, adapter := newCacheTestService(t)
	cacheDir := filepath.Join(repo, "analysis-cache")
	req := newCacheRequest(repo, cacheDir, false)

	first, err := svc.Analyse(context.Background(), req)
	if err != nil {
		t.Fatalf("first analyse: %v", err)
	}
	if adapter.calls != 1 {
		t.Fatalf("expected first run to call adapter once, got %d", adapter.calls)
	}
	if first.Cache == nil || first.Cache.Misses != 1 || first.Cache.Writes != 1 || first.Cache.Hits != 0 {
		t.Fatalf("unexpected first cache metadata: %#v", first.Cache)
	}

	second, err := svc.Analyse(context.Background(), req)
	if err != nil {
		t.Fatalf("second analyse: %v", err)
	}
	if adapter.calls != 1 {
		t.Fatalf("expected second run to be cache hit, adapter calls=%d", adapter.calls)
	}
	if second.Cache == nil || second.Cache.Hits != 1 || second.Cache.Misses != 0 {
		t.Fatalf("unexpected second cache metadata: %#v", second.Cache)
	}

	testutil.MustWriteFile(t, filepath.Join(repo, cacheTestJSIndexFileName), "import { filter } from \"lodash\"\nfilter([1], (x) => x)\n")
	third, err := svc.Analyse(context.Background(), req)
	if err != nil {
		t.Fatalf("third analyse: %v", err)
	}
	if adapter.calls != 2 {
		t.Fatalf("expected cache invalidation after source change, adapter calls=%d", adapter.calls)
	}
	if third.Cache == nil || third.Cache.Misses != 1 {
		t.Fatalf("expected miss after source change, got %#v", third.Cache)
	}
	if len(third.Cache.Invalidations) == 0 || !strings.Contains(third.Cache.Invalidations[0].Reason, "input-changed") {
		t.Fatalf("expected input-changed invalidation, got %#v", third.Cache.Invalidations)
	}
}

func TestAnalysisCacheReadOnlySkipsWrites(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, cacheTestJSIndexFileName), "console.log('hello')\n")

	svc, adapter := newCacheTestService(t)
	req := newCacheRequest(repo, filepath.Join(repo, "analysis-cache"), true)

	first, err := svc.Analyse(context.Background(), req)
	if err != nil {
		t.Fatalf("first readonly analyse: %v", err)
	}
	if first.Cache == nil || !first.Cache.ReadOnly || first.Cache.Writes != 0 {
		t.Fatalf("expected readonly cache metadata with no writes, got %#v", first.Cache)
	}

	second, err := svc.Analyse(context.Background(), req)
	if err != nil {
		t.Fatalf("second readonly analyse: %v", err)
	}
	if adapter.calls != 2 {
		t.Fatalf("expected readonly mode to avoid persisting misses, adapter calls=%d", adapter.calls)
	}
	if second.Cache == nil || second.Cache.Hits != 0 || second.Cache.Misses == 0 {
		t.Fatalf("expected readonly run miss metadata, got %#v", second.Cache)
	}
}

func TestAnalysisCacheWarnTakeWarningsAndSnapshot(t *testing.T) {
	cache := &analysisCache{
		metadata: report.CacheMetadata{
			Enabled: true,
			Invalidations: []report.CacheInvalidation{
				{Key: "k", Reason: "r"},
			},
		},
	}

	cache.warn("  ")
	cache.warn("warn-1")
	cache.warn("warn-2")
	gotWarnings := cache.takeWarnings()
	if len(gotWarnings) != 2 || gotWarnings[0] != "warn-1" || gotWarnings[1] != "warn-2" {
		t.Fatalf("unexpected warnings: %#v", gotWarnings)
	}
	if again := cache.takeWarnings(); len(again) != 0 {
		t.Fatalf("expected nil warnings after drain, got %#v", again)
	}

	snapshot := cache.metadataSnapshot()
	if snapshot == nil || len(snapshot.Invalidations) != 1 {
		t.Fatalf("expected snapshot with invalidations, got %#v", snapshot)
	}
	snapshot.Invalidations[0].Reason = "mutated"
	if cache.metadata.Invalidations[0].Reason != "r" {
		t.Fatalf("expected snapshot to be detached copy")
	}

	var nilCache *analysisCache
	if nilCache.metadataSnapshot() != nil {
		t.Fatalf("expected nil cache snapshot")
	}
}

func TestNewAnalysisCacheUnavailablePathWarns(t *testing.T) {
	repo := t.TempDir()
	blocker := filepath.Join(repo, "file-blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocker file: %v", err)
	}
	cacheReq := Request{
		Cache: &CacheOptions{
			Enabled: true,
			Path:    blocker,
		},
	}
	cache := newAnalysisCache(cacheReq, repo)
	if cache.cacheable {
		t.Fatalf("expected non-cacheable when cache path cannot be prepared")
	}
	warnings := cache.takeWarnings()
	if len(warnings) == 0 || !strings.Contains(warnings[0], "analysis cache unavailable") {
		t.Fatalf("expected cache unavailable warning, got %#v", warnings)
	}
}

func TestHashFileOrMissingScenarios(t *testing.T) {
	repo := t.TempDir()
	path := filepath.Join(repo, "file.txt")
	if err := os.WriteFile(path, []byte("abc"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	digest, err := hashFileOrMissing(path)
	if err != nil || strings.TrimSpace(digest) == "" || digest == "missing" {
		t.Fatalf("expected digest for existing file, got digest=%q err=%v", digest, err)
	}

	missingDigest, err := hashFileOrMissing(filepath.Join(repo, "missing.txt"))
	if err != nil || missingDigest != "missing" {
		t.Fatalf("expected missing digest marker, got digest=%q err=%v", missingDigest, err)
	}

	_, err = hashFileOrMissing(repo)
	if err == nil {
		t.Fatalf("expected error when hashing directory path")
	}
}

func TestWriteFileAtomicSuccessAndFallbackError(t *testing.T) {
	repo := t.TempDir()
	target := filepath.Join(repo, "dir", "file.json")
	if err := writeFileAtomic(target, []byte(`{"x":1}`)); err != nil {
		t.Fatalf("write file atomic success: %v", err)
	}
	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read atomic write target: %v", err)
	}
	if string(content) != `{"x":1}` {
		t.Fatalf("unexpected atomic write content: %q", string(content))
	}

	dirTarget := filepath.Join(repo, "existing-dir")
	if err := os.MkdirAll(dirTarget, 0o755); err != nil {
		t.Fatalf("mkdir dirTarget: %v", err)
	}
	if writeFileAtomic(dirTarget, []byte("x")) == nil {
		t.Fatalf("expected error when target path is an existing directory")
	}
}

func writePointerJSON(t *testing.T, keyPath, inputDigest, objectDigest string) {
	t.Helper()
	pointerBytes, err := json.Marshal(cachePointer{InputDigest: inputDigest, ObjectDigest: objectDigest})
	if err != nil {
		t.Fatalf("marshal pointer: %v", err)
	}
	if err := os.WriteFile(keyPath, pointerBytes, 0o600); err != nil {
		t.Fatalf("write pointer: %v", err)
	}
}

func assertLookupMissWithReason(t *testing.T, cache *analysisCache, entry cacheEntryDescriptor, expectedReason string) {
	t.Helper()
	if _, hit, err := cache.lookup(entry); err != nil || hit {
		t.Fatalf("expected miss, hit=%v err=%v", hit, err)
	}
	if len(cache.metadata.Invalidations) == 0 || cache.metadata.Invalidations[len(cache.metadata.Invalidations)-1].Reason != expectedReason {
		t.Fatalf("expected %s invalidation, got %#v", expectedReason, cache.metadata.Invalidations)
	}
}

func TestAnalysisCacheLookupInvalidationBranches(t *testing.T) {
	repo := t.TempDir()
	cachePath := filepath.Join(repo, "cache")
	cacheReq := Request{
		Cache: &CacheOptions{Enabled: true, Path: cachePath},
	}
	cache := newAnalysisCache(cacheReq, repo)
	if !cache.cacheable {
		t.Fatalf("expected cacheable test setup")
	}

	entry := cacheEntryDescriptor{KeyLabel: "k", KeyDigest: "digest", InputDigest: "input-a"}
	keyPath := filepath.Join(cachePath, "keys", entry.KeyDigest+".json")

	if err := os.WriteFile(keyPath, []byte("{"), 0o600); err != nil {
		t.Fatalf("write corrupt pointer: %v", err)
	}
	assertLookupMissWithReason(t, cache, entry, "pointer-corrupt")

	writePointerJSON(t, keyPath, "input-b", "obj")
	assertLookupMissWithReason(t, cache, entry, "input-changed")

	writePointerJSON(t, keyPath, entry.InputDigest, "missing-object")
	assertLookupMissWithReason(t, cache, entry, "object-missing")

	objectPath := filepath.Join(cachePath, "objects", "obj-corrupt.json")
	if err := os.WriteFile(objectPath, []byte("{"), 0o600); err != nil {
		t.Fatalf("write corrupt object: %v", err)
	}
	writePointerJSON(t, keyPath, entry.InputDigest, "obj-corrupt")
	assertLookupMissWithReason(t, cache, entry, "object-corrupt")
}

func TestResolveCacheOptionsDefaultsAndOverrides(t *testing.T) {
	defaults := resolveCacheOptions(nil, "/repo")
	if !defaults.Enabled || defaults.Path != filepath.Join("/repo", ".lopper-cache") || defaults.ReadOnly {
		t.Fatalf("unexpected default cache options: %#v", defaults)
	}

	requested := &CacheOptions{
		Enabled:  false,
		Path:     "  /tmp/cache  ",
		ReadOnly: true,
	}
	overrides := resolveCacheOptions(requested, "/repo")
	if overrides.Enabled || overrides.Path != "/tmp/cache" || !overrides.ReadOnly {
		t.Fatalf("unexpected override cache options: %#v", overrides)
	}
}

func TestAnalysisCachePrepareEntryBypassBranches(t *testing.T) {
	entry, err := (*analysisCache)(nil).prepareEntry(Request{}, "adapter", "/repo")
	if err != nil || entry != (cacheEntryDescriptor{}) {
		t.Fatalf("expected nil-cache prepareEntry bypass, entry=%#v err=%v", entry, err)
	}

	cache := &analysisCache{
		options:   resolvedCacheOptions{Enabled: true},
		cacheable: false,
	}
	entry, err = cache.prepareEntry(Request{}, "adapter", "/repo")
	if err != nil || entry != (cacheEntryDescriptor{}) {
		t.Fatalf("expected non-cacheable prepareEntry bypass, entry=%#v err=%v", entry, err)
	}
}

func TestAnalysisCacheLookupBypassBranches(t *testing.T) {
	got, hit, err := (*analysisCache)(nil).lookup(cacheEntryDescriptor{})
	if err != nil || hit || len(got.Dependencies) != 0 || got.RepoPath != "" {
		t.Fatalf("expected nil-cache lookup bypass, got=%#v hit=%v err=%v", got, hit, err)
	}

	cache := &analysisCache{
		options:   resolvedCacheOptions{Enabled: false},
		cacheable: true,
	}
	got, hit, err = cache.lookup(cacheEntryDescriptor{})
	if err != nil || hit || len(got.Dependencies) != 0 || got.RepoPath != "" {
		t.Fatalf("expected disabled-cache lookup bypass, got=%#v hit=%v err=%v", got, hit, err)
	}
}

func TestCacheCleanupHelpers(t *testing.T) {
	if err := closeIfPresent(nil); err != nil {
		t.Fatalf("closeIfPresent nil: %v", err)
	}

	tmp := t.TempDir()
	filePath := filepath.Join(tmp, "file.tmp")
	file, err := os.Create(filePath)
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close temp file: %v", err)
	}
	if err := closeIfPresent(file); err != nil {
		t.Fatalf("closeIfPresent already-closed file: %v", err)
	}

	missing := filepath.Join(tmp, "missing.tmp")
	if err := removeIfPresent(missing); err != nil {
		t.Fatalf("removeIfPresent missing: %v", err)
	}
	if err := cleanupTempFile(nil, missing); err != nil {
		t.Fatalf("cleanupTempFile nil+missing: %v", err)
	}
}
