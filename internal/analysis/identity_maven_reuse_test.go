package analysis

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	jvmlang "github.com/ben-ranford/lopper/internal/lang/jvm"
	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestMavenAdapterEvidenceSurvivesManifestReplacement(t *testing.T) {
	for _, cached := range []bool{false, true} {
		name := "live"
		if cached {
			name = "cached"
		}
		t.Run(name, func(t *testing.T) {
			assertMavenAdapterEvidenceSurvivesRemoval(t, cached)
		})
	}
}

func assertMavenAdapterEvidenceSurvivesRemoval(t *testing.T, cached bool) {
	t.Helper()
	repo := t.TempDir()
	pom := filepath.Join(repo, "pom.xml")
	testutil.MustWriteFile(t, pom, `<project><dependencies><dependency><groupId>org.example</groupId><artifactId>widgets</artifactId><version>1.2.3</version></dependency></dependencies></project>`)
	testutil.MustWriteFile(t, filepath.Join(repo, "Main.java"), "import org.example.widgets.Widget;\nclass Main { Widget widget; }\n")
	result, err := jvmlang.NewAdapter().Analyse(context.Background(), language.Request{RepoPath: repo, Dependency: "widgets"})
	if err != nil {
		t.Fatal(err)
	}
	if cached {
		result = roundTripMavenReuseReport(t, result)
	}
	if err := os.Remove(pom); err != nil {
		t.Fatal(err)
	}
	result = mergeReports(repo, []report.Report{result})
	annotateDependencyIdentities(repo, &result)
	dep := findIdentityDependency(t, result, "jvm", "widgets")
	if dep.Identity.Version != "1.2.3" || dep.Identity.Source != "pom.xml" || dep.Identity.Confidence != "high" {
		t.Fatalf("adapter POM evidence lost after source removal: %#v", dep.Identity)
	}
}

func roundTripMavenReuseReport(t *testing.T, result report.Report) report.Report {
	t.Helper()
	cache, entry := cacheWithCapturedMavenReport(t, result)
	cached, hit, err := cache.lookup(entry)
	if err != nil || !hit {
		t.Fatalf("cache lookup: hit=%v err=%v", hit, err)
	}
	return cached
}

func cacheWithCapturedMavenReport(t *testing.T, result report.Report) (*analysisCache, cacheEntryDescriptor) {
	t.Helper()
	req := Request{RepoPath: result.RepoPath, Language: "jvm", Cache: &CacheOptions{Enabled: true, Path: filepath.Join(t.TempDir(), "cache")}}
	cache := newAnalysisCache(req, result.RepoPath)
	if !cache.cacheable {
		t.Fatal("expected cacheable Maven test setup")
	}
	entry, err := cache.prepareEntry(req, "jvm", result.RepoPath)
	if err != nil || entry.KeyDigest == "" || entry.InputDigest == "" {
		t.Fatalf("prepare Maven cache entry: %v", err)
	}
	if err := cache.store(entry, result); err != nil {
		t.Fatalf("store Maven cache report: %v", err)
	}
	return cache, entry
}
