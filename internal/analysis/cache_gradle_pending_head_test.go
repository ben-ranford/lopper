package analysis

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/safeio"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func newStrictPendingPipeline(t *testing.T, id string) *analysisPipeline {
	t.Helper()
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "unrelated.txt"), "")
	registry := language.NewRegistry()
	if err := registry.Register(&testServiceAdapter{id: id, detect: language.Detection{Matched: true, Confidence: 100}, analyse: report.Report{MavenManifestCatalog: id == "jvm"}}); err != nil {
		t.Fatal(err)
	}
	req := newCacheRequest(t, repo, t.TempDir(), false)
	req.Language = id
	req.Features = mustResolveDependencyIdentityPreviewFeatureSet(t)
	pipeline, err := (&Service{Registry: registry}).newAnalysisPipeline(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pipeline.cleanup)
	return pipeline
}

func TestStrictSelectedEmptyResultRejectsIdentityTruncation(t *testing.T) {
	for _, id := range []string{"jvm", kotlinAndroidLanguageName} {
		t.Run(id, func(t *testing.T) {
			pipeline := newStrictPendingPipeline(t, id)
			if pipeline.identityPolicy.limit != maxIdentityDiscoveryFiles {
				t.Fatal("selected candidate did not activate strict identity")
			}
			pipeline.identityPolicy.limit = 1
			if err := pipeline.execute(t.Context()); err != nil {
				t.Fatal(err)
			}
			testutil.MustWriteFile(t, filepath.Join(pipeline.repoPath, "zzz.txt"), "")
			result, err := pipeline.finalReportWithContext(t.Context())
			var failure *shared.GradleDiscoveryError
			if !errors.As(err, &failure) || !errors.Is(err, shared.ErrGradleDiscoveryLimit) || !reflect.DeepEqual(result, report.Report{}) {
				t.Fatalf("empty selected result bypassed strict snapshot: %+v %v", result, err)
			}
			assertGradleCacheUnpublished(t, pipeline.cache.options.Path)
			if !reflect.DeepEqual(pipeline.cache.pending, []pendingCacheReport(nil)) {
				t.Fatal("pending payload retained after failure")
			}
		})
	}
}

func TestUnselectedEmptyResultPreservesLegacyIdentity(t *testing.T) {
	pipeline := newStrictPendingPipeline(t, "unrelated")
	if pipeline.identityPolicy.limit != 0 {
		t.Fatal("unrelated candidate activated strict identity")
	}
	if err := pipeline.execute(t.Context()); err != nil {
		t.Fatal(err)
	}
	result, err := pipeline.finalReportWithContext(t.Context())
	if err != nil || result.Cache.Writes != 1 {
		t.Fatalf("legacy result: %+v %v", result.Cache, err)
	}
}

func TestPendingCacheCancellationPublishesNothing(t *testing.T) {
	pipeline := newStrictPendingPipeline(t, "jvm")
	if err := pipeline.execute(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(pipeline.cache.pending) != 1 {
		t.Fatal("missing pending raw report")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	result, err := pipeline.finalReportWithContext(ctx)
	if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(result, report.Report{}) {
		t.Fatalf("cancelled report: %+v %v", result, err)
	}
	assertGradleCacheUnpublished(t, pipeline.cache.options.Path)
	if !reflect.DeepEqual(pipeline.cache.pending, []pendingCacheReport(nil)) {
		t.Fatal("cancelled pending report retained")
	}
}

func TestPendingPublicationUsesOriginalMavenCachePolicy(t *testing.T) {
	cache, entry, req, raw := newMavenPublicationFixture(t)
	cache.deferWrites = true
	cache.mavenPublicationLimit = 1
	reads := 0
	cache.observeInputRead = func(string, int64) { reads++ }
	storeCachedReport(cache, "jvm", entry.RootPath, entry, raw)
	pipeline := analysisPipeline{request: req, repoPath: req.RepoPath, analysisRepoPath: req.RepoPath, cache: cache, reports: []report.Report{detachPendingReport(raw)}}
	result, err := pipeline.finalReportWithContext(t.Context())
	if err != nil || len(result.Dependencies) != 1 || result.Cache.Writes != 0 {
		t.Fatalf("publication budget lost: %+v %v", result, err)
	}
	if !strings.Contains(strings.Join(result.Warnings, "\n"), "maven cache object exceeds admission limit") || reads != 0 || !reflect.DeepEqual(cache.pending, []pendingCacheReport(nil)) {
		t.Fatalf("wrong flush receiver or repeated digest reads: warnings=%v reads=%d pending=%d", result.Warnings, reads, len(cache.pending))
	}
	assertGradleCacheUnpublished(t, cache.options.Path)
}

func TestPendingReportDetachesPreparationAndFinalization(t *testing.T) {
	_, _, req, raw := newMavenPublicationFixture(t)
	raw.Dependencies[0].UsedImports = []report.ImportUse{{Name: "Used", Locations: []report.Location{{File: "Main.java"}}}}
	raw.Dependencies[0].UnusedImports = []report.ImportUse{{Name: "Unused", Locations: []report.Location{{File: "Main.java"}}}}
	raw.Dependencies[0].License = &report.DependencyLicense{SPDX: "MIT"}
	raw.Dependencies[0].RuntimeUsage = &report.RuntimeUsage{}
	raw.CoverageGaps = []report.CoverageGap{{Path: "Main.java"}}
	before := detachPendingReport(raw)
	current := detachPendingReport(raw)
	repo := filepath.Dir(raw.RepoPath)
	current, err := prepareCandidateReport(req, repo, raw.RepoPath, "jvm", current)
	if err != nil {
		t.Fatal(err)
	}
	req.LicenseDenyList = []string{"MIT"}
	if _, err := finalizeReportWithContext(t.Context(), req, repo, repo, nil, current); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(raw, before) {
		t.Fatal("pending raw cache report mutated by preparation/finalization")
	}
	if len(raw.MavenManifests) == 0 || !raw.UsageIncomplete || !raw.Dependencies[0].UsageIncomplete {
		t.Fatal("immutable Maven or incomplete coverage state lost")
	}
}

func TestStrictIdentityLateWalkFailureDiscardsPendingSiblings(t *testing.T) {
	for _, early := range []string{"a.txt", "build.gradle"} {
		t.Run(early, func(t *testing.T) {
			pipeline := newStrictPendingPipeline(t, "jvm")
			testutil.MustWriteFile(t, filepath.Join(pipeline.repoPath, early), "")
			late := filepath.Join(pipeline.repoPath, "zzz")
			testutil.MustWriteFile(t, filepath.Join(late, "pom.xml"), "<project/>")
			if err := pipeline.execute(t.Context()); err != nil {
				t.Fatal(err)
			}
			calls := 0
			pipeline.identityPolicy.visit = func(path string) error {
				if filepath.Base(path) == early {
					calls++
					return os.RemoveAll(late)
				}
				return nil
			}
			result, err := pipeline.finalReportWithContext(t.Context())
			var failure *shared.GradleDiscoveryError
			if calls != 1 || !errors.As(err, &failure) || !errors.Is(err, os.ErrNotExist) || !reflect.DeepEqual(result, report.Report{}) {
				t.Fatalf("walk failure after capture: calls=%d result=%+v err=%v", calls, result, err)
			}
			assertGradleCacheUnpublished(t, pipeline.cache.options.Path)
		})
	}
}

func TestPendingCacheChecksCancellationAfterFinalization(t *testing.T) {
	pipeline := newStrictPendingPipeline(t, "jvm")
	if err := pipeline.execute(t.Context()); err != nil {
		t.Fatal(err)
	}
	pipeline.request.Features = Request{}.Features
	pipeline.request.LicenseDenyList = []string{"MIT"}
	live := &report.DependencyLicense{SPDX: "MIT"}
	pipeline.reports[0].Dependencies = []report.DependencyReport{{Name: "demo", Language: "jvm", License: live}}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	checked := &identityAnnotationCancelContext{deadline: ctx.Deadline, done: ctx.Done, value: ctx.Value, err: func() error {
		if live.Denied {
			cancel()
		}
		return ctx.Err()
	}}
	result, err := pipeline.finalReportWithContext(checked)
	if !live.Denied || !errors.Is(err, context.Canceled) || !reflect.DeepEqual(result, report.Report{}) {
		t.Fatalf("prepublication cancellation: finalized=%v result=%+v error=%v", live.Denied, result, err)
	}
	assertGradleCacheUnpublished(t, pipeline.cache.options.Path)
}

func TestPendingPipelinePreservesRawNestedLocations(t *testing.T) {
	repo := t.TempDir()
	module := filepath.Join(repo, "app")
	testutil.MustWriteFile(t, filepath.Join(module, "Main.kt"), "class Main")
	raw := report.Report{RepoPath: module, UsageIncomplete: true, Dependencies: []report.DependencyReport{{Name: "demo", UsageIncomplete: true, UsedImports: []report.ImportUse{{Name: "Used", Locations: []report.Location{{File: "Main.kt"}}}}, UnusedImports: []report.ImportUse{{Name: "Unused", Locations: []report.Location{{File: "Main.kt"}}}}, SuppressedUnusedImports: []report.ImportUse{{Name: "Hidden", Locations: []report.Location{{File: "Main.kt"}}}}}}}
	expected := detachPendingReport(raw)
	registry := language.NewRegistry()
	if err := registry.Register(&testServiceAdapter{id: kotlinAndroidLanguageName, detect: language.Detection{Matched: true, Confidence: 100, Roots: []string{module}}, analyse: raw}); err != nil {
		t.Fatal(err)
	}
	req := newCacheRequest(t, repo, t.TempDir(), false)
	req.Language = kotlinAndroidLanguageName
	service := Service{Registry: registry}
	result, err := service.Analyse(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.Cache.Writes != 1 || !reflect.DeepEqual(raw, expected) {
		t.Fatalf("raw report mutated before publication: writes=%d raw=%+v", result.Cache.Writes, raw)
	}
	cache := newAnalysisCache(req, repo)
	entry, err := cache.prepareEntry(req, kotlinAndroidLanguageName, module)
	if err != nil {
		t.Fatal(err)
	}
	cached, hit, err := cache.lookup(entry)
	if err != nil || !hit {
		t.Fatalf("published payload unavailable: hit=%v err=%v", hit, err)
	}
	if !reflect.DeepEqual(cached.Dependencies, expected.Dependencies) || !cached.UsageIncomplete {
		t.Fatalf("normalized live fields escaped into raw cache: %+v", cached)
	}
	if result.Dependencies[0].UsedImports[0].Locations[0].File != "app/Main.kt" {
		t.Fatalf("live result was not normalized: %+v", result.Dependencies)
	}
}

func TestStrictIdentityRegularReplacementDiscardsPendingCache(t *testing.T) {
	for _, name := range []string{"build.gradle", "build.gradle.kts", "gradle.lockfile", "settings.gradle", "settings.gradle.kts", "gradle/libs.versions.toml"} {
		for _, warm := range []bool{false, true} {
			t.Run(name+"/"+map[bool]string{false: "cold", true: "warm"}[warm], func(t *testing.T) {
				assertStrictIdentityReplacementCache(t, name, warm)
			})
		}
	}
}

func assertStrictIdentityReplacementCache(t *testing.T, name string, warm bool) {
	t.Helper()
	repo, cacheDir := t.TempDir(), t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "build.gradle"), "implementation 'org.example:demo:1'\n")
	testutil.MustWriteFile(t, filepath.Join(repo, "Main.java"), "import org.example.Widget;\nclass Main { Widget value; }\n")
	target := filepath.Join(repo, "decoy", name)
	testutil.MustWriteFile(t, target, "")
	service := mavenTestService(t)
	req := newCacheRequest(t, repo, cacheDir, false)
	req.Language = "jvm"
	req.Features = mustResolveDependencyIdentityPreviewFeatureSet(t)
	if warm {
		if _, err := service.Analyse(t.Context(), req); err != nil {
			t.Fatal(err)
		}
	}
	before := mavenPublishedFiles(t, cacheDir)
	pipeline, err := service.newAnalysisPipeline(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	defer pipeline.cleanup()
	if err := pipeline.execute(t.Context()); err != nil {
		t.Fatal(err)
	}
	if warm && (pipeline.cache.metadata.Hits == 0 || pipeline.cache.metadata.Misses != 0) {
		t.Fatalf("expected actual warm hit: %+v", pipeline.cache.metadata)
	}
	if !warm && len(pipeline.cache.pending) == 0 {
		t.Fatal("cold actual JVM analysis did not retain deferred reports")
	}
	calls := 0
	pipeline.identityPolicy.visit = func(path string) error {
		if path != target {
			return nil
		}
		calls++
		if err := os.Remove(target); err != nil {
			return err
		}
		return os.Symlink("absent", target)
	}
	result, err := pipeline.finalReportWithContext(t.Context())
	assertGradleReplacementFailure(t, result, err)
	assertGradleReplacementUnpublished(t, pipeline.cache, before, calls)
}

func assertGradleReplacementFailure(t *testing.T, result report.Report, err error) {
	t.Helper()
	var failure *shared.GradleDiscoveryError
	if !errors.As(err, &failure) || !errors.Is(err, safeio.ErrTargetPathSymlink) || !reflect.DeepEqual(result, report.Report{}) {
		t.Fatalf("regular-observed replacement lost fatal cause or published partial result: %+v %v", result, err)
	}
}

func assertGradleReplacementUnpublished(t *testing.T, cache *analysisCache, before map[string]string, calls int) {
	t.Helper()
	if calls != 1 || cache.metadata.Writes != 0 || len(cache.pending) != 0 || !reflect.DeepEqual(before, mavenPublishedFiles(t, cache.options.Path)) {
		t.Fatalf("replacement published or retained pending cache: calls=%d metadata=%+v pending=%d", calls, cache.metadata, len(cache.pending))
	}
	cache.publishPending()
	if !reflect.DeepEqual(before, mavenPublishedFiles(t, cache.options.Path)) {
		t.Fatal("same receiver published discarded pending reports")
	}
}
