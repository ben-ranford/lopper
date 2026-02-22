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

func TestAnalysisCacheHitAndInvalidation(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, cacheTestJSIndexFileName), "import { map } from \"lodash\"\nmap([1], (x) => x)\n")
	writeFile(t, filepath.Join(repo, cacheTestPackageJSONFileName), "{\n  \"name\": \"demo\"\n}\n")

	adapter := &countingAdapter{id: "cachelang"}
	reg := language.NewRegistry()
	if err := reg.Register(adapter); err != nil {
		t.Fatalf("register adapter: %v", err)
	}
	svc := &Service{Registry: reg}
	cacheDir := filepath.Join(repo, "analysis-cache")

	req := Request{
		RepoPath: repo,
		Language: "cachelang",
		TopN:     1,
		Cache: &CacheOptions{
			Enabled: true,
			Path:    cacheDir,
		},
	}

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

	writeFile(t, filepath.Join(repo, cacheTestJSIndexFileName), "import { filter } from \"lodash\"\nfilter([1], (x) => x)\n")
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
	writeFile(t, filepath.Join(repo, cacheTestJSIndexFileName), "console.log('hello')\n")

	adapter := &countingAdapter{id: "cachelang"}
	reg := language.NewRegistry()
	if err := reg.Register(adapter); err != nil {
		t.Fatalf("register adapter: %v", err)
	}
	svc := &Service{Registry: reg}

	req := Request{
		RepoPath: repo,
		Language: "cachelang",
		TopN:     1,
		Cache: &CacheOptions{
			Enabled:  true,
			Path:     filepath.Join(repo, "analysis-cache"),
			ReadOnly: true,
		},
	}

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
	if again := cache.takeWarnings(); again != nil {
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
	cache := newAnalysisCache(Request{
		Cache: &CacheOptions{
			Enabled: true,
			Path:    blocker,
		},
	}, repo)
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
	if err := writeFileAtomic(dirTarget, []byte("x")); err == nil {
		t.Fatalf("expected error when target path is an existing directory")
	}
}

func TestAnalysisCacheLookupInvalidationBranches(t *testing.T) {
	repo := t.TempDir()
	cachePath := filepath.Join(repo, "cache")
	cache := newAnalysisCache(Request{
		Cache: &CacheOptions{Enabled: true, Path: cachePath},
	}, repo)
	if !cache.cacheable {
		t.Fatalf("expected cacheable test setup")
	}

	entry := cacheEntryDescriptor{KeyLabel: "k", KeyDigest: "digest", InputDigest: "input-a"}
	keyPath := filepath.Join(cachePath, "keys", entry.KeyDigest+".json")

	if err := os.WriteFile(keyPath, []byte("{"), 0o600); err != nil {
		t.Fatalf("write corrupt pointer: %v", err)
	}
	if _, hit, err := cache.lookup(entry); err != nil || hit {
		t.Fatalf("expected miss for corrupt pointer, hit=%v err=%v", hit, err)
	}
	if len(cache.metadata.Invalidations) == 0 || cache.metadata.Invalidations[len(cache.metadata.Invalidations)-1].Reason != "pointer-corrupt" {
		t.Fatalf("expected pointer-corrupt invalidation, got %#v", cache.metadata.Invalidations)
	}

	pointerBytes, err := json.Marshal(cachePointer{InputDigest: "input-b", ObjectDigest: "obj"})
	if err != nil {
		t.Fatalf("marshal pointer: %v", err)
	}
	if err := os.WriteFile(keyPath, pointerBytes, 0o600); err != nil {
		t.Fatalf("write mismatch pointer: %v", err)
	}
	if _, hit, err := cache.lookup(entry); err != nil || hit {
		t.Fatalf("expected miss for input mismatch, hit=%v err=%v", hit, err)
	}
	if cache.metadata.Invalidations[len(cache.metadata.Invalidations)-1].Reason != "input-changed" {
		t.Fatalf("expected input-changed invalidation, got %#v", cache.metadata.Invalidations)
	}

	pointerBytes, err = json.Marshal(cachePointer{InputDigest: entry.InputDigest, ObjectDigest: "missing-object"})
	if err != nil {
		t.Fatalf("marshal missing pointer: %v", err)
	}
	if err := os.WriteFile(keyPath, pointerBytes, 0o600); err != nil {
		t.Fatalf("write missing pointer: %v", err)
	}
	if _, hit, err := cache.lookup(entry); err != nil || hit {
		t.Fatalf("expected miss for missing object, hit=%v err=%v", hit, err)
	}
	if cache.metadata.Invalidations[len(cache.metadata.Invalidations)-1].Reason != "object-missing" {
		t.Fatalf("expected object-missing invalidation, got %#v", cache.metadata.Invalidations)
	}

	objectPath := filepath.Join(cachePath, "objects", "obj-corrupt.json")
	if err := os.WriteFile(objectPath, []byte("{"), 0o600); err != nil {
		t.Fatalf("write corrupt object: %v", err)
	}
	pointerBytes, err = json.Marshal(cachePointer{InputDigest: entry.InputDigest, ObjectDigest: "obj-corrupt"})
	if err != nil {
		t.Fatalf("marshal corrupt object pointer: %v", err)
	}
	if err := os.WriteFile(keyPath, pointerBytes, 0o600); err != nil {
		t.Fatalf("write corrupt object pointer: %v", err)
	}
	if _, hit, err := cache.lookup(entry); err != nil || hit {
		t.Fatalf("expected miss for corrupt object, hit=%v err=%v", hit, err)
	}
	if cache.metadata.Invalidations[len(cache.metadata.Invalidations)-1].Reason != "object-corrupt" {
		t.Fatalf("expected object-corrupt invalidation, got %#v", cache.metadata.Invalidations)
	}
}
