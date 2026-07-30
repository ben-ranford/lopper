package analysis

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/featureflags"
	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/testutil"
)

const (
	cacheTestJSIndexFileName     = "index.js"
	cacheTestPackageJSONFileName = "package.json"
	cacheTestDirectoryName       = "analysis-cache"
)

type countingAdapter struct {
	id              string
	calls           int
	usageIncomplete bool
	hiddenImport    *report.ImportUse
}

func (a *countingAdapter) ID() string { return a.id }
func (a *countingAdapter) Aliases() []string {
	return nil
}
func (a *countingAdapter) Detect(context.Context, string) (bool, error) {
	return true, nil
}
func (a *countingAdapter) Analyse(_ context.Context, req language.Request) (report.Report, error) {
	a.calls++
	dependency := report.DependencyReport{
		Name:              "dep",
		UsedExportsCount:  1,
		TotalExportsCount: 2,
		UsedPercent:       50,
	}
	if req.SuggestOnly {
		dependency.Codemod = &report.CodemodReport{
			Mode: "suggest-only",
			Suggestions: []report.CodemodSuggestion{
				{
					File:        "index.js",
					Line:        1,
					ImportName:  "dep",
					FromModule:  "dep",
					ToModule:    "dep-lite",
					Original:    "import dep from \"dep\"",
					Replacement: "import dep from \"dep-lite\"",
					Patch:       "@@ -1 +1 @@\n-import dep from \"dep\"\n+import dep from \"dep-lite\"\n",
				},
			},
		}
	}
	if a.usageIncomplete && !markUsageIncompleteForTest(&dependency) {
		return report.Report{}, errors.New("usage-incomplete marker unavailable")
	}
	if a.hiddenImport != nil {
		dependency.SuppressedUnusedImports = []report.ImportUse{*a.hiddenImport}
	}
	return report.Report{
		Dependencies: []report.DependencyReport{dependency},
	}, nil
}

func markUsageIncompleteForTest(dependency *report.DependencyReport) bool {
	field := reflect.ValueOf(dependency).Elem().FieldByName("UsageIncomplete")
	if !field.IsValid() || !field.CanSet() {
		return false
	}
	field.SetBool(true)
	return true
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
	cacheDir := filepath.Join(repo, cacheTestDirectoryName)
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

func TestAnalysisCachePreservesUsageIncomplete(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, cacheTestJSIndexFileName), "import dep from \"dep\"\n")

	svc, adapter := newCacheTestService(t)
	adapter.usageIncomplete = true
	adapter.hiddenImport = &report.ImportUse{
		Name:   "filter",
		Module: "dep",
		Locations: []report.Location{{
			File: "src/hidden.js",
			Line: 8,
		}},
	}
	req := newCacheRequest(repo, filepath.Join(repo, cacheTestDirectoryName), false)

	first, err := svc.Analyse(context.Background(), req)
	if err != nil {
		t.Fatalf("first analyse: %v", err)
	}
	if len(first.Dependencies) != 1 || first.Dependencies[0].RemovalCandidate != nil {
		t.Fatalf("expected incomplete first analysis to suppress removal scoring, got %#v", first.Dependencies)
	}
	if !reflect.DeepEqual(first.Dependencies[0].SuppressedUnusedImports, []report.ImportUse{*adapter.hiddenImport}) {
		t.Fatalf("expected first analysis to preserve hidden imports, got %#v", first.Dependencies[0].SuppressedUnusedImports)
	}

	second, err := svc.Analyse(context.Background(), req)
	if err != nil {
		t.Fatalf("second analyse: %v", err)
	}
	if adapter.calls != 1 {
		t.Fatalf("expected second analysis to use cache, adapter calls=%d", adapter.calls)
	}
	if second.Cache == nil || second.Cache.Hits != 1 {
		t.Fatalf("expected cache hit metadata, got %#v", second.Cache)
	}
	if len(second.Dependencies) != 1 || second.Dependencies[0].RemovalCandidate != nil {
		t.Fatalf("expected cached incomplete analysis to suppress removal scoring, got %#v", second.Dependencies)
	}
	if !reflect.DeepEqual(second.Dependencies[0].SuppressedUnusedImports, []report.ImportUse{*adapter.hiddenImport}) {
		t.Fatalf("expected cached analysis to preserve hidden imports, got %#v", second.Dependencies[0].SuppressedUnusedImports)
	}
	serialized, err := json.Marshal(second)
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	if strings.Contains(string(serialized), "usageIncomplete") || strings.Contains(string(serialized), "SuppressedUnusedImports") || strings.Contains(string(serialized), "suppressedUnusedImports") || strings.Contains(string(serialized), "src/hidden.js") {
		t.Fatalf("did not expect internal incomplete-usage state in report JSON: %s", serialized)
	}
}

func TestAnalysisCacheIgnoresLegacySchemaEntries(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, cacheTestJSIndexFileName), "import dep from \"dep\"\n")

	svc, adapter := newCacheTestService(t)
	adapter.usageIncomplete = true
	cacheDir := filepath.Join(repo, cacheTestDirectoryName)
	req := newCacheRequest(repo, cacheDir, false)

	cache := newAnalysisCache(req, repo)
	if !cache.cacheable {
		t.Fatalf("expected cacheable test setup, warnings=%#v", cache.takeWarnings())
	}
	entry, err := cache.prepareEntry(req, adapter.ID(), repo)
	if err != nil {
		t.Fatalf("prepare current cache entry: %v", err)
	}
	legacyEntry, err := cache.prepareEntryWithSchemaVersion(req, adapter.ID(), repo, "v1")
	if err != nil {
		t.Fatalf("prepare legacy cache entry: %v", err)
	}
	if legacyEntry.KeyDigest == entry.KeyDigest {
		t.Fatalf("expected schema version to change cache key digest")
	}

	legacyReport := report.Report{
		RepoPath: repo,
		Dependencies: []report.DependencyReport{{
			Name:              "dep",
			UsedExportsCount:  1,
			TotalExportsCount: 2,
			UsedPercent:       50,
			RemovalCandidate:  &report.RemovalCandidate{Score: 99},
		}},
	}
	legacyPayload, err := json.Marshal(cachedPayload{Report: legacyReport})
	if err != nil {
		t.Fatalf("marshal legacy payload: %v", err)
	}
	legacyObjectDigest := sha256Hex(legacyPayload)
	mustWriteFile(t, filepath.Join(cacheDir, "objects", legacyObjectDigest+".json"), legacyPayload)
	writePointerJSON(t, filepath.Join(cacheDir, "keys", legacyEntry.KeyDigest+".json"), legacyEntry.InputDigest, legacyObjectDigest)

	got, err := svc.Analyse(context.Background(), req)
	if err != nil {
		t.Fatalf("analyse with legacy cache entry: %v", err)
	}
	if adapter.calls != 1 {
		t.Fatalf("expected legacy schema cache entry to miss and rerun analysis, calls=%d", adapter.calls)
	}
	if got.Cache == nil || got.Cache.Hits != 0 || got.Cache.Misses != 1 || got.Cache.Writes != 1 {
		t.Fatalf("unexpected cache metadata for legacy schema miss: %#v", got.Cache)
	}
	if len(got.Dependencies) != 1 || got.Dependencies[0].RemovalCandidate != nil {
		t.Fatalf("expected rerun analysis to suppress removal scoring, got %#v", got.Dependencies)
	}
}

func TestAnalysisCacheSeparatesSuggestOnlyEntries(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, cacheTestJSIndexFileName), "import dep from \"dep\"\n")

	svc, adapter := newCacheTestService(t)
	cacheDir := filepath.Join(repo, cacheTestDirectoryName)

	baseReq := newCacheRequest(repo, cacheDir, false)
	baseReq.Dependency = "dep"

	first, err := svc.Analyse(context.Background(), baseReq)
	if err != nil {
		t.Fatalf("first analyse: %v", err)
	}
	if adapter.calls != 1 {
		t.Fatalf("expected first run to call adapter once, got %d", adapter.calls)
	}
	if first.Dependencies[0].Codemod != nil {
		t.Fatalf("expected non-suggest run to skip codemod, got %#v", first.Dependencies[0].Codemod)
	}

	suggestReq := baseReq
	suggestReq.SuggestOnly = true

	second, err := svc.Analyse(context.Background(), suggestReq)
	if err != nil {
		t.Fatalf("suggest-only analyse: %v", err)
	}
	if adapter.calls != 2 {
		t.Fatalf("expected suggest-only mode to use a distinct cache key, adapter calls=%d", adapter.calls)
	}
	if second.Cache == nil || second.Cache.Hits != 0 || second.Cache.Misses != 1 {
		t.Fatalf("expected suggest-only run cache miss on first invocation, got %#v", second.Cache)
	}
	if second.Dependencies[0].Codemod == nil || second.Dependencies[0].Codemod.Mode != "suggest-only" {
		t.Fatalf("expected suggest-only codemod output, got %#v", second.Dependencies[0].Codemod)
	}

	third, err := svc.Analyse(context.Background(), suggestReq)
	if err != nil {
		t.Fatalf("repeat suggest-only analyse: %v", err)
	}
	if adapter.calls != 2 {
		t.Fatalf("expected repeat suggest-only run to hit cache, adapter calls=%d", adapter.calls)
	}
	if third.Cache == nil || third.Cache.Hits != 1 || third.Cache.Misses != 0 {
		t.Fatalf("expected suggest-only cache hit, got %#v", third.Cache)
	}
	if third.Dependencies[0].Codemod == nil || third.Dependencies[0].Codemod.Mode != "suggest-only" {
		t.Fatalf("expected cached suggest-only codemod output, got %#v", third.Dependencies[0].Codemod)
	}
}

func TestAnalysisCacheReadOnlySkipsWrites(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, cacheTestJSIndexFileName), "console.log('hello')\n")

	svc, adapter := newCacheTestService(t)
	req := newCacheRequest(repo, filepath.Join(repo, cacheTestDirectoryName), true)

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

func TestDefaultRepoLocalCacheForgedEntryMisses(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, cacheTestJSIndexFileName), "console.log('hello')\n")
	testutil.MustWriteFile(t, filepath.Join(repo, cacheTestPackageJSONFileName), "{\n  \"name\": \"demo\"\n}\n")

	svc, adapter := newCacheTestService(t)
	cachePath := filepath.Join(repo, ".lopper-cache")
	req := Request{
		RepoPath: repo,
		Language: "cachelang",
		TopN:     1,
		Cache: &CacheOptions{
			Enabled: true,
		},
	}

	cache := newAnalysisCache(req, repo)
	if !cache.cacheable {
		t.Fatalf("expected resolved default repo-local cache test setup to be cacheable, warnings=%#v", cache.takeWarnings())
	}

	entry, err := cache.prepareEntry(req, adapter.ID(), repo)
	if err != nil {
		t.Fatalf("prepare forged cache entry: %v", err)
	}
	objectDigest := "forged-object"
	mustWriteFile(t, filepath.Join(cachePath, "objects", objectDigest+".json"), []byte(`{"report":{"repoPath":"forged-repo"}}`))
	writePointerJSON(t, filepath.Join(cachePath, "keys", entry.KeyDigest+".json"), entry.InputDigest, objectDigest)

	got, err := svc.Analyse(context.Background(), req)
	if err != nil {
		t.Fatalf("analyse with forged default cache entry: %v", err)
	}
	if adapter.calls != 1 {
		t.Fatalf("expected forged default cache entry to miss and run adapter, calls=%d", adapter.calls)
	}
	if got.Cache == nil || got.Cache.Hits != 0 || got.Cache.Misses != 1 || got.Cache.Writes != 1 {
		t.Fatalf("unexpected cache metadata for forged default cache entry: %#v", got.Cache)
	}
	if len(got.Cache.Invalidations) == 0 || got.Cache.Invalidations[0].Reason != "default-local-untrusted" {
		t.Fatalf("expected default-local-untrusted invalidation, got %#v", got.Cache.Invalidations)
	}
}

func TestDefaultRepoLocalCacheColdMissHasNoUntrustedInvalidation(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, cacheTestJSIndexFileName), "console.log('hello')\n")
	testutil.MustWriteFile(t, filepath.Join(repo, cacheTestPackageJSONFileName), "{\n  \"name\": \"demo\"\n}\n")

	svc, adapter := newCacheTestService(t)
	got, err := svc.Analyse(context.Background(), Request{
		RepoPath: repo,
		Language: "cachelang",
		TopN:     1,
		Cache: &CacheOptions{
			Enabled: true,
		},
	})
	if err != nil {
		t.Fatalf("analyse with empty default cache: %v", err)
	}
	if adapter.calls != 1 {
		t.Fatalf("expected empty default cache to run adapter, calls=%d", adapter.calls)
	}
	if got.Cache == nil || got.Cache.Hits != 0 || got.Cache.Misses != 1 || got.Cache.Writes != 1 {
		t.Fatalf("unexpected cold-miss cache metadata: %#v", got.Cache)
	}
	if len(got.Cache.Invalidations) != 0 {
		t.Fatalf("expected empty default cache to have no invalidations, got %#v", got.Cache.Invalidations)
	}
}

func TestAnalysisCachePrepareEntryIncludesLicensePolicyInputs(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, cacheTestJSIndexFileName), "console.log('hello')\n")
	req := Request{
		RepoPath: repo,
		Cache: &CacheOptions{
			Enabled: true,
			Path:    filepath.Join(repo, cacheTestDirectoryName),
		},
	}
	cache := newAnalysisCache(req, repo)

	baseReq := Request{
		RepoPath:                  repo,
		TopN:                      1,
		LicenseDenyList:           []string{"GPL-3.0-ONLY"},
		IncludeRegistryProvenance: true,
	}
	entryA, err := cache.prepareEntry(baseReq, "cachelang", repo)
	if err != nil {
		t.Fatalf("prepare entry A: %v", err)
	}
	changedReq := baseReq
	changedReq.LicenseDenyList = []string{"MIT"}
	entryB, err := cache.prepareEntry(changedReq, "cachelang", repo)
	if err != nil {
		t.Fatalf("prepare entry B: %v", err)
	}
	if entryA.KeyDigest == entryB.KeyDigest {
		t.Fatalf("expected different cache keys when license deny list changes")
	}
}

func TestAnalysisCachePrepareEntryIncludesFeatureFlags(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, cacheTestJSIndexFileName), "console.log('hello')\n")
	req := Request{
		RepoPath: repo,
		Cache: &CacheOptions{
			Enabled: true,
			Path:    filepath.Join(repo, cacheTestDirectoryName),
		},
	}
	cache := newAnalysisCache(req, repo)

	disabledSet := mustResolveFeatureSet(t, false)
	enabledSet := mustResolveFeatureSet(t, true)

	entryDisabled, err := cache.prepareEntry(Request{RepoPath: repo, TopN: 1, Features: disabledSet}, "cachelang", repo)
	if err != nil {
		t.Fatalf("prepare disabled feature entry: %v", err)
	}
	entryEnabled, err := cache.prepareEntry(Request{RepoPath: repo, TopN: 1, Features: enabledSet}, "cachelang", repo)
	if err != nil {
		t.Fatalf("prepare enabled feature entry: %v", err)
	}
	if entryDisabled.KeyDigest == entryEnabled.KeyDigest {
		t.Fatalf("expected different cache keys when feature flag state changes")
	}
}

func TestAnalysisCachePrepareEntryIncludesRuntimeCaptureRequestScope(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, cacheTestJSIndexFileName), "console.log('hello')\n")
	req := Request{
		RepoPath: repo,
		Cache: &CacheOptions{
			Enabled: true,
			Path:    filepath.Join(repo, cacheTestDirectoryName),
		},
	}
	cache := newAnalysisCache(req, repo)

	baseReq := Request{
		RepoPath:           repo,
		Language:           "all",
		RuntimeTestCommand: "make test",
	}
	baseEntry, err := cache.prepareEntry(baseReq, "python", repo)
	if err != nil {
		t.Fatalf("prepare base entry: %v", err)
	}

	pythonReq := baseReq
	pythonReq.Language = "python"
	pythonEntry, err := cache.prepareEntry(pythonReq, "python", repo)
	if err != nil {
		t.Fatalf("prepare python entry: %v", err)
	}
	if baseEntry.KeyDigest == pythonEntry.KeyDigest {
		t.Fatalf("expected different cache keys when requested language changes")
	}

	commandReq := baseReq
	commandReq.RuntimeTestCommand = "pytest"
	commandEntry, err := cache.prepareEntry(commandReq, "python", repo)
	if err != nil {
		t.Fatalf("prepare command entry: %v", err)
	}
	if baseEntry.KeyDigest == commandEntry.KeyDigest {
		t.Fatalf("expected different cache keys when runtime test command changes")
	}

	traceReq := baseReq
	traceReq.RuntimeTracePath = filepath.Join(repo, ".artifacts", "python.ndjson")
	traceReq.RuntimeTracePathExplicit = true
	traceEntry, err := cache.prepareEntry(traceReq, "python", repo)
	if err != nil {
		t.Fatalf("prepare trace path entry: %v", err)
	}
	if baseEntry.KeyDigest == traceEntry.KeyDigest {
		t.Fatalf("expected different cache keys when runtime trace path changes")
	}
}

func TestAnalysisCachePrepareEntryMemoizesInputDigestForSameRootAndConfig(t *testing.T) {
	repo := t.TempDir()
	root := filepath.Join(repo, "root")
	testutil.MustWriteFile(t, filepath.Join(root, cacheTestJSIndexFileName), "console.log('hello')\n")
	configPath := filepath.Join(repo, ".lopper.yml")
	testutil.MustWriteFile(t, configPath, "threshold: 1\n")

	req := Request{
		RepoPath:   repo,
		ConfigPath: configPath,
		TopN:       1,
		Cache:      &CacheOptions{Enabled: true, Path: filepath.Join(repo, cacheTestDirectoryName)},
	}
	cache := newAnalysisCache(req, repo)

	firstEntry, err := cache.prepareEntry(req, "adapter-a", root)
	if err != nil {
		t.Fatalf("prepare first entry: %v", err)
	}
	if firstEntry.InputDigest == "" {
		t.Fatalf("expected first entry to include input digest")
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatalf("remove root: %v", err)
	}

	secondEntry, err := cache.prepareEntry(req, "adapter-b", root)
	if err != nil {
		t.Fatalf("expected memoized digest reuse for same root+config, got error: %v", err)
	}
	if firstEntry.InputDigest != secondEntry.InputDigest {
		t.Fatalf("expected reused input digest for same root+config, first=%q second=%q", firstEntry.InputDigest, secondEntry.InputDigest)
	}
	if firstEntry.KeyDigest == secondEntry.KeyDigest {
		t.Fatalf("expected adapter-specific cache keys to remain distinct")
	}
}

func TestAnalysisCachePrepareEntryDoesNotReuseInputDigestForDifferentConfigPath(t *testing.T) {
	repo := t.TempDir()
	root := filepath.Join(repo, "root")
	testutil.MustWriteFile(t, filepath.Join(root, cacheTestJSIndexFileName), "console.log('hello')\n")
	configPathA := filepath.Join(repo, ".lopper-a.yml")
	configPathB := filepath.Join(repo, ".lopper-b.yml")
	testutil.MustWriteFile(t, configPathA, "threshold: 1\n")
	testutil.MustWriteFile(t, configPathB, "threshold: 2\n")

	baseReq := Request{
		RepoPath: repo,
		TopN:     1,
		Cache:    &CacheOptions{Enabled: true, Path: filepath.Join(repo, cacheTestDirectoryName)},
	}
	cache := newAnalysisCache(baseReq, repo)

	reqA := baseReq
	reqA.ConfigPath = configPathA
	if _, err := cache.prepareEntry(reqA, "adapter-a", root); err != nil {
		t.Fatalf("prepare first entry: %v", err)
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatalf("remove root: %v", err)
	}

	reqB := baseReq
	reqB.ConfigPath = configPathB
	if _, err := cache.prepareEntry(reqB, "adapter-b", root); err == nil {
		t.Fatalf("expected digest recomputation for different config path to fail after root removal")
	}
}

func TestAnalysisCachePrepareEntryDoesNotReuseInputDigestForDifferentRoot(t *testing.T) {
	repo := t.TempDir()
	rootA := filepath.Join(repo, "root-a")
	rootB := filepath.Join(repo, "root-b")
	testutil.MustWriteFile(t, filepath.Join(rootA, cacheTestJSIndexFileName), "console.log('hello')\n")
	configPath := filepath.Join(repo, ".lopper.yml")
	testutil.MustWriteFile(t, configPath, "threshold: 1\n")

	req := Request{
		RepoPath:   repo,
		ConfigPath: configPath,
		TopN:       1,
		Cache:      &CacheOptions{Enabled: true, Path: filepath.Join(repo, cacheTestDirectoryName)},
	}
	cache := newAnalysisCache(req, repo)

	if _, err := cache.prepareEntry(req, "adapter-a", rootA); err != nil {
		t.Fatalf("prepare first entry: %v", err)
	}
	if _, err := cache.prepareEntry(req, "adapter-b", rootB); err == nil {
		t.Fatalf("expected digest recomputation for different root to fail when root is missing")
	}
}

func mustResolveFeatureSet(t *testing.T, enabled bool) featureflags.Set {
	t.Helper()
	options := featureflags.ResolveOptions{Channel: featureflags.ChannelDev}
	if !enabled {
		options.Disable = []string{"swift-carthage"}
	}
	resolved, err := featureflags.DefaultRegistry().Resolve(options)
	if err != nil {
		t.Fatalf("resolve feature set: %v", err)
	}
	return resolved
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

func TestWriteFileAtomicOverwritesExistingFilePreservingMode(t *testing.T) {
	repo := t.TempDir()
	target := filepath.Join(repo, "pointer.json")
	if err := os.WriteFile(target, []byte("before"), 0o644); err != nil {
		t.Fatalf("seed target file: %v", err)
	}

	if err := writeFileAtomic(target, []byte("after")); err != nil {
		t.Fatalf("overwrite existing cache file: %v", err)
	}

	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read overwritten target: %v", err)
	}
	if string(content) != "after" {
		t.Fatalf("unexpected overwritten content: %q", string(content))
	}

	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat overwritten target: %v", err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("expected cache writes to preserve mode 0644, got %#o", info.Mode().Perm())
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

func cacheWithPayloadForLookupTest(t *testing.T, payload cachedPayload, objectDigest string) (*analysisCache, cacheEntryDescriptor) {
	t.Helper()
	repo := t.TempDir()
	cachePath := filepath.Join(repo, "cache")
	cache := newAnalysisCache(Request{Cache: &CacheOptions{Enabled: true, Path: cachePath}}, repo)
	if !cache.cacheable {
		t.Fatalf("expected cacheable test setup")
	}
	entry := cacheEntryDescriptor{KeyLabel: "k", KeyDigest: "digest", InputDigest: "input-a"}
	serialized, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	mustWriteFile(t, filepath.Join(cachePath, "objects", objectDigest+".json"), serialized)
	writePointerJSON(t, filepath.Join(cachePath, "keys", entry.KeyDigest+".json"), entry.InputDigest, objectDigest)
	return cache, entry
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

func TestAnalysisCacheLookupRejectsSuppressedUnusedSidecarOutOfRangeIndex(t *testing.T) {
	for _, test := range []struct {
		name         string
		index        int
		objectDigest string
	}{
		{name: "negative", index: -1, objectDigest: "obj-sidecar-negative"},
		{name: "past end", index: 1, objectDigest: "obj-sidecar-past-end"},
	} {
		t.Run(test.name, func(t *testing.T) {
			payload := cachedPayload{
				Report: report.Report{Dependencies: []report.DependencyReport{{
					Name: "dep",
				}}},
				UsageIncompleteDependencies: []int{0},
				SuppressedUnusedImportsByDependency: map[int][]report.ImportUse{
					test.index: []report.ImportUse{{Name: "hidden", Module: "dep"}},
				},
			}
			cache, entry := cacheWithPayloadForLookupTest(t, payload, test.objectDigest)
			assertLookupMissWithReason(t, cache, entry, cacheObjectCorruptReason)
		})
	}
}

func TestAnalysisCacheLookupRejectsSuppressedUnusedSidecarOnCompleteDependency(t *testing.T) {
	payload := cachedPayload{
		Report: report.Report{Dependencies: []report.DependencyReport{{
			Name: "dep",
		}}},
		SuppressedUnusedImportsByDependency: map[int][]report.ImportUse{
			0: []report.ImportUse{{Name: "hidden", Module: "dep"}},
		},
	}
	objectDigest := "obj-sidecar-complete"
	cache, entry := cacheWithPayloadForLookupTest(t, payload, objectDigest)
	assertLookupMissWithReason(t, cache, entry, cacheObjectCorruptReason)
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
