package analysis

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/ben-ranford/lopper/internal/featureflags"
	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/safeio"
	"github.com/ben-ranford/lopper/internal/testutil"
)

const (
	cacheTestJSIndexFileName     = "index.js"
	cacheTestPackageJSONFileName = "package.json"
	cacheTestDirectoryName       = "analysis-cache"
)

type countingAdapter struct {
	id    string
	calls int
}

type analysisCacheAuthAttackCase struct {
	name  string
	setup func(*testing.T, string, string, string)
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
	return report.Report{
		Dependencies: []report.DependencyReport{dependency},
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
	useTestAnalysisCacheUserCacheDir(t)
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

func TestAnalysisCacheDefaultRepoLocalPathWritesAndHits(t *testing.T) {
	useTestAnalysisCacheUserCacheDir(t)
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, cacheTestJSIndexFileName), "import { map } from \"lodash\"\nmap([1], (x) => x)\n")
	testutil.MustWriteFile(t, filepath.Join(repo, cacheTestPackageJSONFileName), "{\n  \"name\": \"demo\"\n}\n")

	svc, adapter := newCacheTestService(t)
	defaultCachePath := filepath.Join(repo, ".lopper-cache")

	first, err := svc.Analyse(context.Background(), Request{RepoPath: repo, Language: "cachelang", TopN: 1})
	if err != nil {
		t.Fatalf("first default repo-local analyse: %v", err)
	}
	if adapter.calls != 1 || first.Cache == nil || !first.Cache.Enabled || first.Cache.Path != defaultCachePath || first.Cache.Hits != 0 || first.Cache.Misses != 1 || first.Cache.Writes != 1 {
		t.Fatalf("expected default repo-local cache warmup metadata, calls=%d cache=%#v", adapter.calls, first.Cache)
	}
	if len(first.Warnings) != 0 {
		t.Fatalf("expected default repo-local cache warmup without warnings, got %#v", first.Warnings)
	}

	second, err := svc.Analyse(context.Background(), Request{RepoPath: repo, Language: "cachelang", TopN: 1})
	if err != nil {
		t.Fatalf("second default repo-local analyse: %v", err)
	}
	if adapter.calls != 1 {
		t.Fatalf("expected default repo-local cache hit, adapter calls=%d", adapter.calls)
	}
	if second.Cache == nil || !second.Cache.Enabled || second.Cache.Path != defaultCachePath || second.Cache.Hits != 1 || second.Cache.Misses != 0 || second.Cache.Writes != 0 {
		t.Fatalf("expected default repo-local cache hit metadata, got %#v", second.Cache)
	}
	if len(second.Warnings) != 0 {
		t.Fatalf("expected default repo-local cache hit without warnings, got %#v", second.Warnings)
	}
}

func TestAnalysisCacheDefaultStoreRejectsDescendantSymlinkEscapes(t *testing.T) {
	for _, childDir := range []string{cacheKeysDirName, cacheObjectsDirName} {
		t.Run(childDir, func(t *testing.T) {
			assertAnalysisCacheDefaultStoreRejectsDescendantSymlinkEscape(t, childDir)
		})
	}
}

func TestAnalysisCacheDefaultRepoLocalForgedEntryMissesAndDoesNotBypassAdapter(t *testing.T) {
	useTestAnalysisCacheUserCacheDir(t)
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, cacheTestJSIndexFileName), "import { map } from \"lodash\"\nmap([1], (x) => x)\n")
	testutil.MustWriteFile(t, filepath.Join(repo, cacheTestPackageJSONFileName), "{\n  \"name\": \"demo\"\n}\n")

	svc, adapter := newCacheTestService(t)
	defaultCachePath := filepath.Join(repo, ".lopper-cache")
	trustedCache := &analysisCache{
		options:   resolvedCacheOptions{Enabled: true, Path: defaultCachePath},
		cacheable: true,
	}
	entry, err := trustedCache.prepareEntry(Request{RepoPath: repo, Language: "cachelang", TopN: 1}, adapter.ID(), repo)
	if err != nil {
		t.Fatalf("prepare forged cache entry: %v", err)
	}
	mustMkdirCacheLayout(t, defaultCachePath)
	mustWriteCachedObject(t, defaultCachePath, report.Report{
		Dependencies: []report.DependencyReport{{Name: "forged-dep", UsedExportsCount: 99, TotalExportsCount: 100, UsedPercent: 99}},
	})
	mustWritePointer(t, filepath.Join(defaultCachePath, cacheKeysDirName, entry.KeyDigest+".json"), cachePointer{
		InputDigest:  entry.InputDigest,
		ObjectDigest: "forged-object",
	})

	got, err := svc.Analyse(context.Background(), Request{RepoPath: repo, Language: "cachelang", TopN: 1})
	if err != nil {
		t.Fatalf("analyse with forged default repo-local cache: %v", err)
	}
	if adapter.calls != 1 {
		t.Fatalf("expected default repo-local cache to be ignored and adapter to run once, got %d calls", adapter.calls)
	}
	if len(got.Dependencies) != 1 || got.Dependencies[0].Name != "dep" {
		t.Fatalf("expected live adapter result, got %#v", got.Dependencies)
	}
	if got.Cache == nil || !got.Cache.Enabled || got.Cache.Path != defaultCachePath || got.Cache.Hits != 0 || got.Cache.Misses != 1 || got.Cache.Writes != 1 {
		t.Fatalf("expected default cache metadata to report miss and rewrite, got %#v", got.Cache)
	}
	if len(got.Warnings) != 0 {
		t.Fatalf("expected forged default repo-local entry to fail closed without cache warnings, got %#v", got.Warnings)
	}
	if len(got.Cache.Invalidations) == 0 || got.Cache.Invalidations[0].Reason != "pointer-untrusted" {
		t.Fatalf("expected pointer-untrusted invalidation, got %#v", got.Cache.Invalidations)
	}
}

func TestAnalysisCacheRejectsSymlinkedAuthPathsAndForgedHits(t *testing.T) {
	for _, tc := range []analysisCacheAuthAttackCase{
		{name: "user-cache-root", setup: setupUserCacheRootAttack},
		{name: "auth-parent", setup: setupAuthParentAttack},
		{name: "auth-store", setup: setupAuthStoreAttack},
		{name: "key-file", setup: setupKeyFileAttack},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertAnalysisCacheRejectsSymlinkedAuthPathAndForgedHit(t, tc)
		})
	}
}

func TestAnalysisCacheExplicitRepoLocalPathRemainsTrusted(t *testing.T) {
	useTestAnalysisCacheUserCacheDir(t)
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, cacheTestJSIndexFileName), "import { map } from \"lodash\"\nmap([1], (x) => x)\n")
	testutil.MustWriteFile(t, filepath.Join(repo, cacheTestPackageJSONFileName), "{\n  \"name\": \"demo\"\n}\n")

	svc, adapter := newCacheTestService(t)
	req := newCacheRequest(repo, filepath.Join(repo, ".lopper-cache"), false)

	first, err := svc.Analyse(context.Background(), req)
	if err != nil {
		t.Fatalf("first explicit repo-local analyse: %v", err)
	}
	if adapter.calls != 1 || first.Cache == nil || first.Cache.Misses != 1 || first.Cache.Writes != 1 {
		t.Fatalf("expected explicit repo-local cache warmup, calls=%d cache=%#v", adapter.calls, first.Cache)
	}

	second, err := svc.Analyse(context.Background(), req)
	if err != nil {
		t.Fatalf("second explicit repo-local analyse: %v", err)
	}
	if adapter.calls != 1 {
		t.Fatalf("expected explicit repo-local cache hit, adapter calls=%d", adapter.calls)
	}
	if second.Cache == nil || second.Cache.Hits != 1 || second.Cache.Misses != 0 {
		t.Fatalf("expected explicit repo-local cache hit metadata, got %#v", second.Cache)
	}
}

func TestAnalysisCacheCanonicalStorageAliasesShareTrustedHits(t *testing.T) {
	userCacheDir := setTestAnalysisCacheUserCacheDir(t)
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, cacheTestJSIndexFileName), "console.log('hello')\n")

	realCachePath := filepath.Join(t.TempDir(), "real-cache")
	if err := os.Mkdir(realCachePath, 0o750); err != nil {
		t.Fatalf("mkdir real cache: %v", err)
	}
	aliasCachePath := filepath.Join(t.TempDir(), "cache-alias")
	if err := os.Symlink(realCachePath, aliasCachePath); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	svc, adapter := newCacheTestService(t)

	first, err := svc.Analyse(context.Background(), newCacheRequest(repo, realCachePath, false))
	if err != nil {
		t.Fatalf("analyse through real cache path: %v", err)
	}
	if adapter.calls != 1 || first.Cache == nil || first.Cache.Misses != 1 || first.Cache.Writes != 1 {
		t.Fatalf("expected real-path cache warmup, calls=%d cache=%#v", adapter.calls, first.Cache)
	}
	realKeyPath := testAnalysisCacheAuthKeyPath(t, userCacheDir, realCachePath)
	aliasKeyPath := testAnalysisCacheAuthKeyPath(t, userCacheDir, aliasCachePath)
	if realKeyPath != aliasKeyPath {
		t.Fatalf("expected canonical aliases to share auth identity, real=%s alias=%s", realKeyPath, aliasKeyPath)
	}
	keyBefore, err := os.ReadFile(realKeyPath)
	if err != nil {
		t.Fatalf("read canonical cache auth key: %v", err)
	}

	second, err := svc.Analyse(context.Background(), newCacheRequest(repo, aliasCachePath, false))
	if err != nil {
		t.Fatalf("analyse through cache alias: %v", err)
	}
	if adapter.calls != 1 {
		t.Fatalf("expected alias path to trust existing cache entry, adapter calls=%d", adapter.calls)
	}
	if second.Cache == nil || second.Cache.Hits != 1 || second.Cache.Misses != 0 || second.Cache.Writes != 0 {
		t.Fatalf("expected trusted alias-path hit, got %#v", second.Cache)
	}
	keyAfter, err := os.ReadFile(aliasKeyPath)
	if err != nil {
		t.Fatalf("re-read canonical cache auth key: %v", err)
	}
	if string(keyAfter) != string(keyBefore) {
		t.Fatalf("expected canonical cache aliases to retain one stable auth key")
	}
}

func TestAnalysisCacheExplicitLegacyUnsignedPointerMissesOnceThenRepopulates(t *testing.T) {
	userCacheDir := setTestAnalysisCacheUserCacheDir(t)
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, cacheTestJSIndexFileName), "import { map } from \"lodash\"\nmap([1], (x) => x)\n")
	testutil.MustWriteFile(t, filepath.Join(repo, cacheTestPackageJSONFileName), "{\n  \"name\": \"demo\"\n}\n")

	svc, adapter := newCacheTestService(t)
	cachePath := filepath.Join(t.TempDir(), "explicit-cache")
	req := newCacheRequest(repo, cachePath, false)
	trustedCache := newAnalysisCache(req, repo)
	if !trustedCache.cacheable {
		t.Fatalf("expected explicit cache setup to be cacheable")
	}
	entry, err := trustedCache.prepareEntry(req, adapter.ID(), repo)
	if err != nil {
		t.Fatalf("prepare explicit legacy cache entry: %v", err)
	}
	mustMkdirCacheLayout(t, cachePath)
	objectDigest := mustWriteCachedObject(t, cachePath, report.Report{
		Dependencies: []report.DependencyReport{{Name: "legacy-explicit-dep", UsedExportsCount: 5, TotalExportsCount: 5, UsedPercent: 100}},
	})
	mustWritePointer(t, filepath.Join(cachePath, cacheKeysDirName, entry.KeyDigest+".json"), cachePointer{
		InputDigest:  entry.InputDigest,
		ObjectDigest: objectDigest,
	})

	first, err := svc.Analyse(context.Background(), req)
	if err != nil {
		t.Fatalf("first explicit legacy analyse: %v", err)
	}
	if adapter.calls != 1 {
		t.Fatalf("expected explicit legacy unsigned pointer to miss and call adapter once, got %d", adapter.calls)
	}
	if first.Cache == nil || first.Cache.Hits != 0 || first.Cache.Misses != 1 || first.Cache.Writes != 1 {
		t.Fatalf("expected explicit legacy miss and rewrite metadata, got %#v", first.Cache)
	}
	if len(first.Cache.Invalidations) == 0 || first.Cache.Invalidations[0].Reason != "pointer-untrusted" {
		t.Fatalf("expected explicit legacy unsigned pointer invalidation, got %#v", first.Cache.Invalidations)
	}
	keyPath := testAnalysisCacheAuthKeyPath(t, userCacheDir, cachePath)
	keyBefore, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("read explicit cache auth key: %v", err)
	}

	second, err := svc.Analyse(context.Background(), req)
	if err != nil {
		t.Fatalf("second explicit legacy analyse: %v", err)
	}
	if adapter.calls != 1 {
		t.Fatalf("expected explicit cache to hit after rewrite, adapter calls=%d", adapter.calls)
	}
	if second.Cache == nil || second.Cache.Hits != 1 || second.Cache.Misses != 0 || second.Cache.Writes != 0 {
		t.Fatalf("expected rewritten explicit cache hit metadata, got %#v", second.Cache)
	}
	keyAfter, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("re-read explicit cache auth key: %v", err)
	}
	if string(keyBefore) != string(keyAfter) {
		t.Fatalf("expected explicit cache auth key to remain stable across rewrite")
	}
}

func TestAnalysisCacheConcurrentAuthKeyInitializationUsesPersistedWinner(t *testing.T) {
	setTestAnalysisCacheUserCacheDir(t)
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, cacheTestJSIndexFileName), "console.log('hello')\n")

	const (
		iterations = 12
		workers    = 24
	)
	for iteration := 0; iteration < iterations; iteration++ {
		assertAnalysisCacheConcurrentAuthKeyInitializationIteration(t, repo, workers, iteration)
	}
}

func assertAnalysisCacheDefaultStoreRejectsDescendantSymlinkEscape(t *testing.T, childDir string) {
	t.Helper()

	useTestAnalysisCacheUserCacheDir(t)
	repo := t.TempDir()
	cache := newAnalysisCache(Request{}, repo)
	if !cache.cacheable {
		t.Fatalf("expected initialized default cache, warnings=%#v", cache.takeWarnings())
	}

	childPath := filepath.Join(repo, cacheDirName, childDir)
	if err := os.Remove(childPath); err != nil {
		t.Fatalf("remove initialized %s directory: %v", childDir, err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, childPath); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	err := cache.store(cacheEntryDescriptor{KeyDigest: "symlink-" + childDir, InputDigest: "input"}, report.Report{RepoPath: "trusted"})
	if err == nil || !strings.Contains(err.Error(), "output parent contains symlink") {
		t.Fatalf("expected confined %s write to reject symlink, got %v", childDir, err)
	}
	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatalf("read outside directory: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected no external writes through %s symlink, got %#v", childDir, entries)
	}
	if cache.metadata.Writes != 0 {
		t.Fatalf("expected rejected store not to count a write, metadata=%#v", cache.metadata)
	}
}

func assertAnalysisCacheRejectsSymlinkedAuthPathAndForgedHit(t *testing.T, tc analysisCacheAuthAttackCase) {
	t.Helper()

	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, cacheTestJSIndexFileName), "console.log('hello')\n")
	cachePath := filepath.Join(repo, cacheDirName)
	mustMkdirCacheLayout(t, cachePath)
	canonicalCachePath, err := filepath.EvalSymlinks(cachePath)
	if err != nil {
		t.Fatalf("resolve canonical cache path: %v", err)
	}

	controlledRoot := filepath.Join(repo, "repo-controlled-auth")
	if err := os.MkdirAll(controlledRoot, 0o750); err != nil {
		t.Fatalf("mkdir repo-controlled auth root: %v", err)
	}
	storageInfo, err := os.Stat(canonicalCachePath)
	if err != nil {
		t.Fatalf("stat canonical cache path: %v", err)
	}
	keyName, err := analysisCacheAuthKeyName(canonicalCachePath, storageInfo)
	if err != nil {
		t.Fatalf("derive cache auth-key identity: %v", err)
	}
	attackerKeyHex := strings.Repeat("42", analysisCacheAuthKeyLength)
	tc.setup(t, controlledRoot, keyName, attackerKeyHex)
	controlledKeyPath := analysisCacheControlledKeyPath(tc.name, controlledRoot, keyName)
	attackerKey, err := decodeAuthKey(attackerKeyHex)
	if err != nil {
		t.Fatalf("decode attacker key: %v", err)
	}

	svc, adapter := newCacheTestService(t)
	req := Request{RepoPath: repo, Language: "cachelang", TopN: 1}
	entryCache := &analysisCache{options: resolvedCacheOptions{Enabled: true, Path: cachePath}, cacheable: true, storageRoot: canonicalCachePath}
	entry, err := entryCache.prepareEntry(req, adapter.ID(), repo)
	if err != nil {
		t.Fatalf("prepare forged cache entry: %v", err)
	}
	objectDigest := mustWriteCachedObject(t, cachePath, report.Report{
		Dependencies: []report.DependencyReport{{Name: "forged-dep", UsedExportsCount: 9, TotalExportsCount: 9, UsedPercent: 100}},
	})
	signature, err := pointerSignature(attackerKey, entry, objectDigest)
	if err != nil {
		t.Fatalf("sign forged pointer: %v", err)
	}
	mustWritePointer(t, filepath.Join(cachePath, cacheKeysDirName, entry.KeyDigest+".json"), cachePointer{
		InputDigest:  entry.InputDigest,
		ObjectDigest: objectDigest,
		Signature:    signature,
	})
	assertAnalysisCacheForgedHitRejected(t, svc, adapter, req, controlledRoot, controlledKeyPath)
}

func assertAnalysisCacheForgedHitRejected(t *testing.T, svc *Service, adapter *countingAdapter, req Request, controlledRoot, controlledKeyPath string) {
	t.Helper()

	keyBefore, err := os.ReadFile(controlledKeyPath)
	if err != nil {
		t.Fatalf("read controlled key before analysis: %v", err)
	}
	entriesBefore := countTestTreeEntries(t, controlledRoot)

	got, err := svc.Analyse(context.Background(), req)
	if err != nil {
		t.Fatalf("analyse with symlinked auth path: %v", err)
	}
	if adapter.calls != 1 {
		t.Fatalf("expected forged cache to be rejected, adapter calls=%d", adapter.calls)
	}
	if len(got.Dependencies) != 1 || got.Dependencies[0].Name != "dep" {
		t.Fatalf("expected live adapter result, got %#v", got.Dependencies)
	}
	if got.Cache == nil || got.Cache.Hits != 0 || got.Cache.Writes != 0 {
		t.Fatalf("expected unavailable auth store to prevent cache hits and writes, got %#v", got.Cache)
	}
	if !strings.Contains(strings.Join(got.Warnings, "\n"), "analysis cache unavailable") {
		t.Fatalf("expected auth-store rejection warning, got %#v", got.Warnings)
	}
	keyAfter, err := os.ReadFile(controlledKeyPath)
	if err != nil {
		t.Fatalf("read controlled key after analysis: %v", err)
	}
	if string(keyAfter) != string(keyBefore) {
		t.Fatalf("expected repo-controlled key not to be mutated")
	}
	if entriesAfter := countTestTreeEntries(t, controlledRoot); entriesAfter != entriesBefore {
		t.Fatalf("expected no files to be placed in repo-controlled auth storage, before=%d after=%d", entriesBefore, entriesAfter)
	}
}

func setupUserCacheRootAttack(t *testing.T, controlledRoot, keyName, attackerKeyHex string) {
	t.Helper()
	mustWriteFile(t, filepath.Join(controlledRoot, "lopper", analysisCacheAuthDirName, keyName), []byte(attackerKeyHex))
	configuredUserCacheDir := filepath.Join(t.TempDir(), "user-cache-link")
	if err := os.Symlink(controlledRoot, configuredUserCacheDir); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	setTestAnalysisCacheUserCachePath(t, configuredUserCacheDir)
}

func setupAuthParentAttack(t *testing.T, controlledRoot, keyName, attackerKeyHex string) {
	t.Helper()
	mustWriteFile(t, filepath.Join(controlledRoot, analysisCacheAuthDirName, keyName), []byte(attackerKeyHex))
	userCacheDir := t.TempDir()
	if err := os.Symlink(controlledRoot, filepath.Join(userCacheDir, "lopper")); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	setTestAnalysisCacheUserCachePath(t, userCacheDir)
}

func setupAuthStoreAttack(t *testing.T, controlledRoot, keyName, attackerKeyHex string) {
	t.Helper()
	mustWriteFile(t, filepath.Join(controlledRoot, keyName), []byte(attackerKeyHex))
	userCacheDir := t.TempDir()
	lopperDir := filepath.Join(userCacheDir, "lopper")
	if err := os.Mkdir(lopperDir, 0o750); err != nil {
		t.Fatalf("mkdir auth parent: %v", err)
	}
	if err := os.Symlink(controlledRoot, filepath.Join(lopperDir, analysisCacheAuthDirName)); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	setTestAnalysisCacheUserCachePath(t, userCacheDir)
}

func setupKeyFileAttack(t *testing.T, controlledRoot, keyName, attackerKeyHex string) {
	t.Helper()
	controlledKeyPath := filepath.Join(controlledRoot, "attacker.key")
	mustWriteFile(t, controlledKeyPath, []byte(attackerKeyHex))
	userCacheDir := t.TempDir()
	authDir := filepath.Join(userCacheDir, "lopper", analysisCacheAuthDirName)
	if err := os.MkdirAll(authDir, 0o750); err != nil {
		t.Fatalf("mkdir auth dir: %v", err)
	}
	if err := os.Symlink(controlledKeyPath, filepath.Join(authDir, keyName)); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	setTestAnalysisCacheUserCachePath(t, userCacheDir)
}

func analysisCacheControlledKeyPath(attack, controlledRoot, keyName string) string {
	switch attack {
	case "user-cache-root":
		return filepath.Join(controlledRoot, "lopper", analysisCacheAuthDirName, keyName)
	case "auth-parent":
		return filepath.Join(controlledRoot, analysisCacheAuthDirName, keyName)
	case "auth-store":
		return filepath.Join(controlledRoot, keyName)
	case "key-file":
		return filepath.Join(controlledRoot, "attacker.key")
	default:
		return ""
	}
}

func assertAnalysisCacheConcurrentAuthKeyInitializationIteration(t *testing.T, repo string, workers, iteration int) {
	t.Helper()

	cachePath := filepath.Join(repo, "race-cache-"+strconv.Itoa(iteration))
	req := newCacheRequest(repo, cachePath, false)
	caches := runConcurrentAnalysisCacheInitialization(t, req, repo, workers)
	assertAnalysisCacheConcurrentAuthKeyWinner(t, caches)
	assertAnalysisCacheConcurrentAuthKeyLookup(t, caches, req, repo)
}

func runConcurrentAnalysisCacheInitialization(t *testing.T, req Request, repo string, workers int) []*analysisCache {
	t.Helper()

	caches := make([]*analysisCache, workers)
	start := make(chan struct{})
	errCh := make(chan error, workers)
	var wg sync.WaitGroup
	initializeCache := func(index int) {
		defer wg.Done()
		<-start
		cache := newAnalysisCache(req, repo)
		if !cache.cacheable {
			errCh <- os.ErrInvalid
			return
		}
		caches[index] = cache
	}
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go initializeCache(i)
	}
	close(start)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("expected concurrent cache init to remain cacheable, got %v", err)
		}
	}
	return caches
}

func assertAnalysisCacheConcurrentAuthKeyWinner(t *testing.T, caches []*analysisCache) {
	t.Helper()

	keys := make(map[string]struct{}, len(caches))
	for _, cache := range caches {
		if cache == nil || len(cache.authKey) != analysisCacheAuthKeyLength {
			t.Fatalf("expected initialized auth key for all caches, cache=%#v", cache)
		}
		keys[string(cache.authKey)] = struct{}{}
	}
	if len(keys) != 1 {
		t.Fatalf("expected single persisted auth key winner, got %d distinct keys", len(keys))
	}
}

func assertAnalysisCacheConcurrentAuthKeyLookup(t *testing.T, caches []*analysisCache, req Request, repo string) {
	t.Helper()

	entry, err := caches[0].prepareEntry(req, "cachelang", repo)
	if err != nil {
		t.Fatalf("prepare concurrent cache entry: %v", err)
	}
	if err := caches[0].store(entry, report.Report{RepoPath: "repo"}); err != nil {
		t.Fatalf("store concurrent cache entry: %v", err)
	}
	for i, cache := range caches {
		got, hit, err := cache.lookup(entry)
		if err != nil {
			t.Fatalf("lookup from concurrent cache %d: %v", i, err)
		}
		if !hit || got.RepoPath != "repo" {
			t.Fatalf("expected concurrent cache %d to trust persisted winner pointer, hit=%v report=%#v", i, hit, got)
		}
	}
}

func TestAnalysisCacheConcurrentFirstRunIgnoresLegacyStaleLock(t *testing.T) {
	userCacheDir := setTestAnalysisCacheUserCacheDir(t)
	repo := t.TempDir()
	cachePath := filepath.Join(repo, "stale-lock-cache")
	req := newCacheRequest(repo, cachePath, false)
	keyPath := testAnalysisCacheAuthKeyPath(t, userCacheDir, cachePath)
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o750); err != nil {
		t.Fatalf("mkdir auth key dir: %v", err)
	}
	if err := os.Mkdir(keyPath+analysisCacheAuthLegacyLockSuffix, 0o700); err != nil {
		t.Fatalf("create legacy stale init lock: %v", err)
	}

	const workers = 32
	caches := make([]*analysisCache, workers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	initializeCache := func(index int) {
		defer wg.Done()
		<-start
		caches[index] = newAnalysisCache(req, repo)
	}
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go initializeCache(i)
	}
	close(start)
	wg.Wait()

	keys := make(map[string]struct{}, workers)
	for i, cache := range caches {
		if cache == nil || !cache.cacheable {
			t.Fatalf("expected cache %d to recover despite stale lock, cache=%#v", i, cache)
		}
		if len(cache.authKey) != analysisCacheAuthKeyLength {
			t.Fatalf("expected cache %d to load a complete auth key, got %d bytes", i, len(cache.authKey))
		}
		keys[string(cache.authKey)] = struct{}{}
	}
	if len(keys) != 1 {
		t.Fatalf("expected one persisted key winner despite stale lock, got %d keys", len(keys))
	}
	persistedKey, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("read recovered auth key: %v", err)
	}
	decodedKey, err := decodeAuthKey(strings.TrimSpace(string(persistedKey)))
	if err != nil {
		t.Fatalf("decode recovered auth key: %v", err)
	}
	if _, ok := keys[string(decodedKey)]; !ok {
		t.Fatalf("expected every cache to use persisted winner, persisted=%x", decodedKey)
	}
}

func TestAnalysisCacheConcurrentCorruptKeyRecoveryUsesPublishedCandidate(t *testing.T) {
	userCacheDir := setTestAnalysisCacheUserCacheDir(t)
	repo := t.TempDir()
	cachePath := filepath.Join(repo, "corrupt-key-cache")
	req := newCacheRequest(repo, cachePath, false)
	seed := newAnalysisCache(req, repo)
	if !seed.cacheable {
		t.Fatalf("expected seed cache to be cacheable, warnings=%#v", seed.takeWarnings())
	}

	keyPath := testAnalysisCacheAuthKeyPath(t, userCacheDir, cachePath)
	if err := os.WriteFile(keyPath, []byte("corrupt-key"), 0o600); err != nil {
		t.Fatalf("corrupt auth key: %v", err)
	}
	authRoot, keyName := openTestAnalysisCacheAuthRoot(t, userCacheDir, cachePath)
	generation, err := invalidAuthKeyGeneration(authRoot, keyName)
	if err != nil {
		t.Fatalf("resolve corrupt key generation: %v", err)
	}
	rotationPath := keyName + analysisCacheAuthRotateTag + generation
	if err := publishMissingAuthKey(authRoot, rotationPath); err != nil {
		t.Fatalf("publish interrupted rotation candidate: %v", err)
	}
	expectedKey, err := readAnalysisCacheAuthKey(authRoot, rotationPath, true)
	if err != nil {
		t.Fatalf("read interrupted rotation candidate: %v", err)
	}

	const workers = 24
	caches := make([]*analysisCache, workers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	recoverCache := func(index int) {
		defer wg.Done()
		<-start
		caches[index] = newAnalysisCache(req, repo)
	}
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go recoverCache(i)
	}
	close(start)
	wg.Wait()

	for i, cache := range caches {
		if cache == nil || !cache.cacheable {
			t.Fatalf("expected cache %d to recover corrupt key, cache=%#v", i, cache)
		}
		if string(cache.authKey) != string(expectedKey) {
			t.Fatalf("expected cache %d to use published rotation candidate", i)
		}
	}
	persistedKey, err := readAnalysisCacheAuthKey(authRoot, keyName, true)
	if err != nil {
		t.Fatalf("read recovered auth key: %v", err)
	}
	if string(persistedKey) != string(expectedKey) {
		t.Fatalf("expected recovered key file to contain published rotation candidate")
	}
}

func TestAnalysisCacheAuthPublishSyncFailureFailsClosed(t *testing.T) {
	userCacheDir := setTestAnalysisCacheUserCacheDir(t)
	repo := t.TempDir()
	cachePath := filepath.Join(repo, "sync-failure-cache")
	req := newCacheRequest(repo, cachePath, false)
	authDir := filepath.Dir(testAnalysisCacheAuthKeyPath(t, userCacheDir, cachePath))
	if err := os.MkdirAll(authDir, 0o750); err != nil {
		t.Fatalf("mkdir auth store dir: %v", err)
	}
	syncErr := errors.New("injected auth directory sync failure")
	originalSync := analysisCacheAuthSyncDirFn
	analysisCacheAuthSyncDirFn = func(*safeio.WriteRoot) error { return syncErr }
	t.Cleanup(func() {
		analysisCacheAuthSyncDirFn = originalSync
	})

	cache := newAnalysisCache(req, repo)
	if cache.cacheable {
		t.Fatalf("expected publish sync failure to disable cache")
	}
	if len(cache.authKey) != 0 {
		t.Fatalf("expected failed durability check not to retain an in-memory key")
	}
	warnings := strings.Join(cache.takeWarnings(), "\n")
	if !strings.Contains(warnings, "sync cache auth key directory after publish") ||
		!strings.Contains(warnings, syncErr.Error()) {
		t.Fatalf("expected publish sync error to take priority, warnings=%q", warnings)
	}
	persistedKey := readTestAnalysisCacheAuthKey(t, userCacheDir, cachePath)
	if len(persistedKey) != analysisCacheAuthKeyLength {
		t.Fatalf("expected atomic winner to be complete despite sync failure")
	}

	analysisCacheAuthSyncDirFn = originalSync
	retry := newAnalysisCache(req, repo)
	if !retry.cacheable || string(retry.authKey) != string(persistedKey) {
		t.Fatalf("expected retry to re-read persisted winner, cache=%#v", retry)
	}
}

func TestAnalysisCacheAuthRotationDoesNotRunRedundantDirectorySync(t *testing.T) {
	userCacheDir := setTestAnalysisCacheUserCacheDir(t)
	repo := t.TempDir()
	cachePath := filepath.Join(repo, "rotation-writer-owned-sync-cache")
	req := newCacheRequest(repo, cachePath, false)
	seed := newAnalysisCache(req, repo)
	if !seed.cacheable {
		t.Fatalf("expected seed cache to be cacheable, warnings=%#v", seed.takeWarnings())
	}

	keyPath := testAnalysisCacheAuthKeyPath(t, userCacheDir, cachePath)
	if err := os.WriteFile(keyPath, []byte("corrupt-key"), 0o600); err != nil {
		t.Fatalf("corrupt auth key: %v", err)
	}
	authRoot, keyName := openTestAnalysisCacheAuthRoot(t, userCacheDir, cachePath)
	generation, err := invalidAuthKeyGeneration(authRoot, keyName)
	if err != nil {
		t.Fatalf("resolve invalid key generation: %v", err)
	}
	rotationName := keyName + analysisCacheAuthRotateTag + generation
	if err := publishMissingAuthKey(authRoot, rotationName); err != nil {
		t.Fatalf("publish rotation candidate: %v", err)
	}
	expectedKey, err := readAnalysisCacheAuthKey(authRoot, rotationName, true)
	if err != nil {
		t.Fatalf("read rotation candidate: %v", err)
	}

	syncCalls := 0
	originalSync := analysisCacheAuthSyncDirFn
	analysisCacheAuthSyncDirFn = func(*safeio.WriteRoot) error {
		syncCalls++
		return errors.New("unexpected redundant rotation directory sync")
	}
	t.Cleanup(func() {
		analysisCacheAuthSyncDirFn = originalSync
	})
	err = rotateInvalidAuthKey(authRoot, keyName)
	if err != nil {
		t.Fatalf("expected writer-owned commit sync to avoid a false rotation failure, got %v", err)
	}
	if syncCalls != 0 {
		t.Fatalf("expected no redundant analysis-layer directory sync, got %d calls", syncCalls)
	}
	persistedKey, err := readAnalysisCacheAuthKey(authRoot, keyName, true)
	if err != nil {
		t.Fatalf("read renamed auth key: %v", err)
	}
	if string(persistedKey) != string(expectedKey) {
		t.Fatalf("expected rotation writer to install the complete immutable candidate")
	}
}

func TestAnalysisCacheAuthStoreCreationSyncFailureFailsClosed(t *testing.T) {
	userCacheDir := setTestAnalysisCacheUserCacheDir(t)
	repo := t.TempDir()
	cachePath := filepath.Join(repo, "auth-store-sync-failure-cache")
	req := newCacheRequest(repo, cachePath, false)
	syncErr := errors.New("auth-store parent sync failure")
	originalSync := analysisCacheAuthMkdirAllDurableFn
	analysisCacheAuthMkdirAllDurableFn = func(root *safeio.WriteRoot, path string, perm os.FileMode) error {
		if err := root.MkdirAll(path, perm); err != nil {
			return err
		}
		return syncErr
	}
	t.Cleanup(func() {
		analysisCacheAuthMkdirAllDurableFn = originalSync
	})

	cache := newAnalysisCache(req, repo)
	if cache.cacheable {
		t.Fatalf("expected auth-store parent sync failure to disable cache")
	}
	if len(cache.authKey) != 0 {
		t.Fatalf("expected auth-store sync failure not to retain an in-memory key")
	}
	warnings := strings.Join(cache.takeWarnings(), "\n")
	if !strings.Contains(warnings, "sync cache auth store parent after creation") || !strings.Contains(warnings, syncErr.Error()) {
		t.Fatalf("expected auth-store parent sync failure warning, got %q", warnings)
	}
	if _, err := os.Stat(filepath.Join(userCacheDir, "lopper", analysisCacheAuthDirName)); err != nil {
		t.Fatalf("expected auth-store directory to have been created before sync failure, got %v", err)
	}
}

func TestAnalysisCacheUserCacheParentSyncFailureFailsClosed(t *testing.T) {
	repo := t.TempDir()
	cachePath := filepath.Join(repo, "user-cache-parent-sync-failure-cache")
	req := newCacheRequest(repo, cachePath, false)
	userCacheDir := filepath.Join(t.TempDir(), "missing", "user-cache")
	setTestAnalysisCacheUserCachePath(t, userCacheDir)
	syncErr := errors.New("user-cache parent sync failure")
	originalSync := analysisCacheAuthMkdirAllDurableFn
	analysisCacheAuthMkdirAllDurableFn = func(root *safeio.WriteRoot, path string, perm os.FileMode) error {
		if err := root.MkdirAll(path, perm); err != nil {
			return err
		}
		return syncErr
	}
	t.Cleanup(func() {
		analysisCacheAuthMkdirAllDurableFn = originalSync
	})

	cache := newAnalysisCache(req, repo)
	if cache.cacheable {
		t.Fatalf("expected user-cache parent sync failure to disable cache")
	}
	warnings := strings.Join(cache.takeWarnings(), "\n")
	if !strings.Contains(warnings, "sync user cache parent after creation") || !strings.Contains(warnings, syncErr.Error()) {
		t.Fatalf("expected user-cache parent sync failure warning, got %q", warnings)
	}
	if _, err := os.Stat(userCacheDir); err != nil {
		t.Fatalf("expected user cache directory to be created before sync failure, got %v", err)
	}
}

func TestAnalysisCacheSeparatesSuggestOnlyEntries(t *testing.T) {
	useTestAnalysisCacheUserCacheDir(t)
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
	userCacheDir := setTestAnalysisCacheUserCacheDir(t)
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, cacheTestJSIndexFileName), "console.log('hello')\n")

	svc, adapter := newCacheTestService(t)
	cachePath := filepath.Join(repo, cacheTestDirectoryName)
	req := newCacheRequest(repo, cachePath, true)

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
	authStorePath := filepath.Join(userCacheDir, "lopper", analysisCacheAuthDirName)
	if _, err := os.Stat(authStorePath); !os.IsNotExist(err) {
		t.Fatalf("expected readonly mode not to create the auth store, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(cachePath, cacheKeysDirName)); !os.IsNotExist(err) {
		t.Fatalf("expected readonly mode not to create keys dir, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(cachePath, cacheObjectsDirName)); !os.IsNotExist(err) {
		t.Fatalf("expected readonly mode not to create objects dir, stat err=%v", err)
	}
}

func TestAnalysisCacheReadOnlyAllowsTrustedHits(t *testing.T) {
	useTestAnalysisCacheUserCacheDir(t)
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, cacheTestJSIndexFileName), "console.log('hello')\n")

	svc, adapter := newCacheTestService(t)
	cachePath := filepath.Join(repo, cacheTestDirectoryName)

	if _, err := svc.Analyse(context.Background(), newCacheRequest(repo, cachePath, false)); err != nil {
		t.Fatalf("seed writable analyse: %v", err)
	}
	if adapter.calls != 1 {
		t.Fatalf("expected writable seed run to call adapter once, got %d", adapter.calls)
	}

	got, err := svc.Analyse(context.Background(), newCacheRequest(repo, cachePath, true))
	if err != nil {
		t.Fatalf("readonly trusted analyse: %v", err)
	}
	if adapter.calls != 1 {
		t.Fatalf("expected readonly trusted cache hit without extra adapter calls, got %d", adapter.calls)
	}
	if got.Cache == nil || !got.Cache.ReadOnly || got.Cache.Hits != 1 || got.Cache.Writes != 0 {
		t.Fatalf("expected readonly trusted cache hit metadata, got %#v", got.Cache)
	}
}

func TestAnalysisCacheReadOnlyWithCorruptAuthKeyFailsClosedWithoutMutation(t *testing.T) {
	userCacheDir := setTestAnalysisCacheUserCacheDir(t)
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, cacheTestJSIndexFileName), "console.log('hello')\n")

	svc, adapter := newCacheTestService(t)
	cachePath := filepath.Join(repo, cacheTestDirectoryName)
	if _, err := svc.Analyse(context.Background(), newCacheRequest(repo, cachePath, false)); err != nil {
		t.Fatalf("seed writable analyse: %v", err)
	}
	if adapter.calls != 1 {
		t.Fatalf("expected writable seed run to call adapter once, got %d", adapter.calls)
	}

	keyPath := testAnalysisCacheAuthKeyPath(t, userCacheDir, cachePath)
	if err := os.WriteFile(keyPath, []byte("corrupt-key"), 0o600); err != nil {
		t.Fatalf("corrupt auth key: %v", err)
	}

	got, err := svc.Analyse(context.Background(), newCacheRequest(repo, cachePath, true))
	if err != nil {
		t.Fatalf("readonly analyse with corrupt auth key: %v", err)
	}
	if adapter.calls != 2 {
		t.Fatalf("expected readonly corrupt-key run to fail closed and call adapter, got %d calls", adapter.calls)
	}
	if got.Cache == nil || got.Cache.Hits != 0 || got.Cache.Misses != 1 || got.Cache.Writes != 0 {
		t.Fatalf("expected readonly corrupt-key miss metadata, got %#v", got.Cache)
	}
	if len(got.Cache.Invalidations) == 0 || got.Cache.Invalidations[0].Reason != "pointer-untrusted" {
		t.Fatalf("expected readonly corrupt-key pointer-untrusted invalidation, got %#v", got.Cache.Invalidations)
	}
	keyData, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("read corrupt auth key after readonly analyse: %v", err)
	}
	if string(keyData) != "corrupt-key" {
		t.Fatalf("expected readonly mode not to mutate corrupt auth key, got %q", string(keyData))
	}
}

func TestAnalysisCacheReadOnlyWithOversizedAuthKeyFailsClosedWithoutMutation(t *testing.T) {
	userCacheDir := setTestAnalysisCacheUserCacheDir(t)
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, cacheTestJSIndexFileName), "console.log('hello')\n")

	svc, adapter := newCacheTestService(t)
	cachePath := filepath.Join(repo, cacheTestDirectoryName)
	if _, err := svc.Analyse(context.Background(), newCacheRequest(repo, cachePath, false)); err != nil {
		t.Fatalf("seed writable analyse: %v", err)
	}
	if adapter.calls != 1 {
		t.Fatalf("expected writable seed run to call adapter once, got %d", adapter.calls)
	}

	keyPath := testAnalysisCacheAuthKeyPath(t, userCacheDir, cachePath)
	oversized := strings.Repeat("a", analysisCacheAuthKeyMaxBytes+1)
	if err := os.WriteFile(keyPath, []byte(oversized), 0o600); err != nil {
		t.Fatalf("write oversized auth key: %v", err)
	}

	got, err := svc.Analyse(context.Background(), newCacheRequest(repo, cachePath, true))
	if err != nil {
		t.Fatalf("readonly analyse with oversized auth key: %v", err)
	}
	if adapter.calls != 2 {
		t.Fatalf("expected readonly oversized-key run to fail closed and call adapter, got %d calls", adapter.calls)
	}
	if got.Cache == nil || got.Cache.Hits != 0 || got.Cache.Misses != 1 || got.Cache.Writes != 0 {
		t.Fatalf("expected readonly oversized-key miss metadata, got %#v", got.Cache)
	}
	if len(got.Cache.Invalidations) == 0 || got.Cache.Invalidations[0].Reason != "pointer-untrusted" {
		t.Fatalf("expected readonly oversized-key pointer-untrusted invalidation, got %#v", got.Cache.Invalidations)
	}
	keyData, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("read oversized auth key after readonly analyse: %v", err)
	}
	if string(keyData) != oversized {
		t.Fatalf("expected readonly mode not to mutate oversized auth key, got %q", string(keyData))
	}
}

func TestAnalysisCacheWritableCorruptAuthKeyRotatesAndRepopulates(t *testing.T) {
	userCacheDir := setTestAnalysisCacheUserCacheDir(t)
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, cacheTestJSIndexFileName), "console.log('hello')\n")

	svc, adapter := newCacheTestService(t)
	cachePath := filepath.Join(repo, cacheTestDirectoryName)
	req := newCacheRequest(repo, cachePath, false)
	if _, err := svc.Analyse(context.Background(), req); err != nil {
		t.Fatalf("seed writable analyse: %v", err)
	}

	keyPath := testAnalysisCacheAuthKeyPath(t, userCacheDir, cachePath)
	if err := os.WriteFile(keyPath, []byte("corrupt-key"), 0o600); err != nil {
		t.Fatalf("corrupt auth key: %v", err)
	}

	second, err := svc.Analyse(context.Background(), req)
	if err != nil {
		t.Fatalf("analyse after auth key corruption: %v", err)
	}
	if adapter.calls != 2 {
		t.Fatalf("expected corrupt key to invalidate pointer and call adapter, got %d calls", adapter.calls)
	}
	if second.Cache == nil || second.Cache.Hits != 0 || second.Cache.Misses != 1 || second.Cache.Writes != 1 {
		t.Fatalf("expected corrupt-key miss and repopulation metadata, got %#v", second.Cache)
	}
	if len(second.Cache.Invalidations) == 0 || second.Cache.Invalidations[0].Reason != "pointer-untrusted" {
		t.Fatalf("expected corrupt-key pointer-untrusted invalidation, got %#v", second.Cache.Invalidations)
	}
	persistedKey := readTestAnalysisCacheAuthKey(t, userCacheDir, cachePath)
	if len(persistedKey) != analysisCacheAuthKeyLength {
		t.Fatalf("expected complete rotated auth key, got %d bytes", len(persistedKey))
	}

	third, err := svc.Analyse(context.Background(), req)
	if err != nil {
		t.Fatalf("analyse after corrupt-key repopulation: %v", err)
	}
	if adapter.calls != 2 {
		t.Fatalf("expected cache hit after corrupt-key repopulation, calls=%d", adapter.calls)
	}
	if third.Cache == nil || third.Cache.Hits != 1 || third.Cache.Misses != 0 {
		t.Fatalf("expected hit after corrupt-key repopulation, got %#v", third.Cache)
	}
}

func TestAnalysisCacheWritableOversizedAuthKeyRotatesAndRepopulates(t *testing.T) {
	userCacheDir := setTestAnalysisCacheUserCacheDir(t)
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, cacheTestJSIndexFileName), "console.log('hello')\n")

	svc, adapter := newCacheTestService(t)
	cachePath := filepath.Join(repo, cacheTestDirectoryName)
	req := newCacheRequest(repo, cachePath, false)
	if _, err := svc.Analyse(context.Background(), req); err != nil {
		t.Fatalf("seed writable analyse: %v", err)
	}

	keyPath := testAnalysisCacheAuthKeyPath(t, userCacheDir, cachePath)
	if err := os.WriteFile(keyPath, []byte(strings.Repeat("a", analysisCacheAuthKeyMaxBytes+1)), 0o600); err != nil {
		t.Fatalf("write oversized auth key: %v", err)
	}

	second, err := svc.Analyse(context.Background(), req)
	if err != nil {
		t.Fatalf("analyse after oversized auth key: %v", err)
	}
	if adapter.calls != 2 {
		t.Fatalf("expected oversized key to invalidate pointer and call adapter, got %d calls", adapter.calls)
	}
	if second.Cache == nil || second.Cache.Hits != 0 || second.Cache.Misses != 1 || second.Cache.Writes != 1 {
		t.Fatalf("expected oversized-key miss and repopulation metadata, got %#v", second.Cache)
	}
	if len(second.Cache.Invalidations) == 0 || second.Cache.Invalidations[0].Reason != "pointer-untrusted" {
		t.Fatalf("expected oversized-key pointer-untrusted invalidation, got %#v", second.Cache.Invalidations)
	}
	if persistedKey := readTestAnalysisCacheAuthKey(t, userCacheDir, cachePath); len(persistedKey) != analysisCacheAuthKeyLength {
		t.Fatalf("expected complete rotated auth key, got %d bytes", len(persistedKey))
	}
}

func TestAnalysisCacheReadOnlyRejectsPermissiveAuthKeyModesWithoutMutation(t *testing.T) {
	userCacheDir := setTestAnalysisCacheUserCacheDir(t)
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, cacheTestJSIndexFileName), "console.log('hello')\n")

	svc, adapter := newCacheTestService(t)
	cachePath := filepath.Join(repo, cacheTestDirectoryName)
	if _, err := svc.Analyse(context.Background(), newCacheRequest(repo, cachePath, false)); err != nil {
		t.Fatalf("seed writable analyse: %v", err)
	}
	keyPath := testAnalysisCacheAuthKeyPath(t, userCacheDir, cachePath)
	if err := os.Chmod(keyPath, 0o644); err != nil {
		t.Fatalf("chmod auth key: %v", err)
	}

	got, err := svc.Analyse(context.Background(), newCacheRequest(repo, cachePath, true))
	if err != nil {
		t.Fatalf("readonly analyse with permissive auth key: %v", err)
	}
	if adapter.calls != 2 {
		t.Fatalf("expected readonly permissive-key run to fail closed and call adapter, got %d calls", adapter.calls)
	}
	if got.Cache == nil || got.Cache.Hits != 0 || got.Cache.Misses != 1 || got.Cache.Writes != 0 {
		t.Fatalf("expected readonly permissive-key miss metadata, got %#v", got.Cache)
	}
	if len(got.Cache.Invalidations) == 0 || got.Cache.Invalidations[0].Reason != "pointer-untrusted" {
		t.Fatalf("expected readonly permissive-key pointer-untrusted invalidation, got %#v", got.Cache.Invalidations)
	}
	if info, err := os.Stat(keyPath); err != nil {
		t.Fatalf("stat auth key after readonly analyse: %v", err)
	} else if info.Mode().Perm() != 0o644 {
		t.Fatalf("expected readonly mode not to repair auth key perms, got %o", info.Mode().Perm())
	}
}

func TestAnalysisCacheWritableRepairsPermissiveAuthKeyModesAndPreservesHits(t *testing.T) {
	userCacheDir := setTestAnalysisCacheUserCacheDir(t)
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, cacheTestJSIndexFileName), "console.log('hello')\n")

	svc, adapter := newCacheTestService(t)
	cachePath := filepath.Join(repo, cacheTestDirectoryName)
	req := newCacheRequest(repo, cachePath, false)
	if _, err := svc.Analyse(context.Background(), req); err != nil {
		t.Fatalf("seed writable analyse: %v", err)
	}
	keyPath := testAnalysisCacheAuthKeyPath(t, userCacheDir, cachePath)
	if err := os.Chmod(keyPath, 0o646); err != nil {
		t.Fatalf("chmod auth key: %v", err)
	}

	got, err := svc.Analyse(context.Background(), req)
	if err != nil {
		t.Fatalf("writable analyse with permissive auth key: %v", err)
	}
	if adapter.calls != 1 {
		t.Fatalf("expected writable permissive-key run to preserve trusted hit, got %d calls", adapter.calls)
	}
	if got.Cache == nil || got.Cache.Hits != 1 || got.Cache.Misses != 0 || got.Cache.Writes != 0 {
		t.Fatalf("expected writable permissive-key hit metadata, got %#v", got.Cache)
	}
	if info, err := os.Stat(keyPath); err != nil {
		t.Fatalf("stat auth key after writable analyse: %v", err)
	} else if info.Mode().Perm() != analysisCacheAuthKeyPerm {
		t.Fatalf("expected writable mode to repair auth key perms to %o, got %o", analysisCacheAuthKeyPerm, info.Mode().Perm())
	}
}

func TestAnalysisCacheWritableKeyRotationInvalidatesPointersAndRepopulates(t *testing.T) {
	userCacheDir := setTestAnalysisCacheUserCacheDir(t)
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, cacheTestJSIndexFileName), "console.log('hello')\n")

	svc, adapter := newCacheTestService(t)
	cachePath := filepath.Join(repo, cacheTestDirectoryName)
	req := newCacheRequest(repo, cachePath, false)
	if _, err := svc.Analyse(context.Background(), req); err != nil {
		t.Fatalf("seed writable analyse: %v", err)
	}
	if adapter.calls != 1 {
		t.Fatalf("expected writable seed run to call adapter once, got %d", adapter.calls)
	}

	keyPath := testAnalysisCacheAuthKeyPath(t, userCacheDir, cachePath)
	rotatedKey := strings.Repeat("ab", analysisCacheAuthKeyLength)
	if err := os.WriteFile(keyPath, []byte(rotatedKey), 0o600); err != nil {
		t.Fatalf("rotate auth key: %v", err)
	}

	second, err := svc.Analyse(context.Background(), req)
	if err != nil {
		t.Fatalf("analyse after key rotation: %v", err)
	}
	if adapter.calls != 2 {
		t.Fatalf("expected rotated key to invalidate pointer and call adapter again, got %d calls", adapter.calls)
	}
	if second.Cache == nil || second.Cache.Hits != 0 || second.Cache.Misses != 1 || second.Cache.Writes != 1 {
		t.Fatalf("expected rotated key miss and repopulation metadata, got %#v", second.Cache)
	}
	if len(second.Cache.Invalidations) == 0 || second.Cache.Invalidations[0].Reason != "pointer-untrusted" {
		t.Fatalf("expected rotated key pointer-untrusted invalidation, got %#v", second.Cache.Invalidations)
	}

	third, err := svc.Analyse(context.Background(), req)
	if err != nil {
		t.Fatalf("analyse after key-rotation repopulation: %v", err)
	}
	if adapter.calls != 2 {
		t.Fatalf("expected cache hit after key-rotation repopulation, adapter calls=%d", adapter.calls)
	}
	if third.Cache == nil || third.Cache.Hits != 1 || third.Cache.Misses != 0 {
		t.Fatalf("expected hit after key-rotation repopulation, got %#v", third.Cache)
	}
}

func TestAnalysisCachePrepareEntryIncludesLicensePolicyInputs(t *testing.T) {
	useTestAnalysisCacheUserCacheDir(t)
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
	useTestAnalysisCacheUserCacheDir(t)
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
	useTestAnalysisCacheUserCacheDir(t)
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
	useTestAnalysisCacheUserCacheDir(t)
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
	useTestAnalysisCacheUserCacheDir(t)
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
	useTestAnalysisCacheUserCacheDir(t)
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

func countTestTreeEntries(t *testing.T, root string) int {
	t.Helper()
	count := 0
	if err := filepath.WalkDir(root, func(path string, _ os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path != root {
			count++
		}
		return nil
	}); err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return count
}

func writePointerJSON(t *testing.T, keyPath string, pointer cachePointer) {
	t.Helper()
	pointerBytes, err := json.Marshal(pointer)
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
	useTestAnalysisCacheUserCacheDir(t)
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

	writePointerJSON(t, keyPath, cachePointer{InputDigest: "input-b", ObjectDigest: "obj"})
	assertLookupMissWithReason(t, cache, entry, "input-changed")

	missingSignature, err := cache.signPointer(entry, "missing-object")
	if err != nil {
		t.Fatalf("sign missing object pointer: %v", err)
	}
	writePointerJSON(t, keyPath, cachePointer{InputDigest: entry.InputDigest, ObjectDigest: "missing-object", Signature: missingSignature})
	assertLookupMissWithReason(t, cache, entry, "object-missing")

	corruptPayload := []byte("{")
	corruptDigest := sha256Hex(corruptPayload)
	objectPath := filepath.Join(cachePath, "objects", corruptDigest+".json")
	if err := os.WriteFile(objectPath, corruptPayload, 0o600); err != nil {
		t.Fatalf("write corrupt object: %v", err)
	}
	corruptSignature, err := cache.signPointer(entry, corruptDigest)
	if err != nil {
		t.Fatalf("sign corrupt object pointer: %v", err)
	}
	writePointerJSON(t, keyPath, cachePointer{InputDigest: entry.InputDigest, ObjectDigest: corruptDigest, Signature: corruptSignature})
	assertLookupMissWithReason(t, cache, entry, "object-corrupt")
}

func TestResolveCacheOptionsDefaultsAndOverrides(t *testing.T) {
	defaults := resolveCacheOptions(nil, "/repo")
	if !defaults.Enabled || defaults.Path != filepath.Join("/repo", ".lopper-cache") || defaults.ReadOnly || defaults.ExplicitPath {
		t.Fatalf("unexpected default cache options: %#v", defaults)
	}

	requested := &CacheOptions{
		Enabled:  false,
		Path:     "  /tmp/cache  ",
		ReadOnly: true,
	}
	overrides := resolveCacheOptions(requested, "/repo")
	if overrides.Enabled || overrides.Path != "/tmp/cache" || !overrides.ReadOnly || !overrides.ExplicitPath {
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
