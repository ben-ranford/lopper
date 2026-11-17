package analysis

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestGradleIdentityColdWarmAndChangedInput(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "build.gradle"), "implementation 'org.example:demo'\n")
	testutil.MustWriteFile(t, filepath.Join(repo, "Main.java"), "import org.example.Widget;\nclass Main { Widget value; }\n")
	lock := filepath.Join(repo, "gradle.lockfile")
	testutil.MustWriteFile(t, lock, "org.example:demo:1=compile\n")
	service := mavenTestService(t)
	request := newCacheRequest(t, repo, t.TempDir(), false)
	request.Language = "jvm"
	request.Features = mustResolveDependencyIdentityPreviewFeatureSet(t)
	cold, err := service.Analyse(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	assertGradleCachedIdentity(t, cold, "1", 0, 1)
	warm, err := service.Analyse(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	assertGradleCachedIdentity(t, warm, "1", 1, 0)
	if !reflect.DeepEqual(cold.Dependencies, warm.Dependencies) {
		t.Fatal("cold/warm dependency payload differs")
	}
	testutil.MustWriteFile(t, lock, "org.example:demo:2=compile\n")
	changed, err := service.Analyse(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	assertGradleCachedIdentity(t, changed, "2", 0, 1)
}

func assertGradleCachedIdentity(t *testing.T, result report.Report, version string, hits, writes int) {
	t.Helper()
	if result.Cache == nil || result.Cache.Hits != hits || result.Cache.Writes != writes {
		t.Fatalf("cache accounting: %+v", result.Cache)
	}
	if len(result.Dependencies) != 1 || result.Dependencies[0].Identity == nil {
		t.Fatalf("identity missing: %+v", result.Dependencies)
	}
	if result.Dependencies[0].Identity.Version != version {
		t.Fatalf("identity version: %+v", result.Dependencies[0].Identity)
	}
}

func TestCurrentGradleCacheHitStillRequiresCompleteIdentitySnapshot(t *testing.T) {
	pipeline := newStrictPendingPipeline(t, "jvm")
	pipeline.request.Language = "all"
	if err := pipeline.execute(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := pipeline.finalReportWithContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	before := mavenPublishedFiles(t, pipeline.cache.options.Path)
	if err := pipeline.service.Registry.Register(&testServiceAdapter{id: "sibling", detect: language.Detection{Matched: true, Confidence: 90}, analyse: report.Report{Dependencies: []report.DependencyReport{{Name: "sibling"}}}}); err != nil {
		t.Fatal(err)
	}
	warm, err := pipeline.service.newAnalysisPipeline(t.Context(), pipeline.request)
	if err != nil {
		t.Fatal(err)
	}
	defer warm.cleanup()
	warm.identityPolicy.limit = 1
	if err := warm.execute(t.Context()); err != nil {
		t.Fatal(err)
	}
	if warm.cache.metadata.Hits != 1 || len(warm.cache.pending) != 1 {
		t.Fatalf("fixture did not hit current cache: %+v", warm.cache.metadata)
	}
	testutil.MustWriteFile(t, filepath.Join(warm.repoPath, "zzz.txt"), "")
	result, err := warm.finalReportWithContext(t.Context())
	if err == nil || !reflect.DeepEqual(result, report.Report{}) {
		t.Fatalf("cache hit bypassed strict snapshot: %+v %v", result, err)
	}
	if !reflect.DeepEqual(before, mavenPublishedFiles(t, warm.cache.options.Path)) {
		t.Fatal("failed cache-hit enrichment modified published bytes")
	}
}
