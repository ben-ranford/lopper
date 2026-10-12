package analysis

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	jvmlang "github.com/ben-ranford/lopper/internal/lang/jvm"
	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestGradleDiscoveryFailurePublishesNoSiblingCache(t *testing.T) {
	repo, cacheDir := t.TempDir(), t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "build.gradle"), "implementation 'org.example:demo:1'\n")
	testutil.MustWriteFile(t, filepath.Join(repo, "settings.gradle"), `dependencyResolutionManagement { versionCatalogs { create("libs") { from(files("missing.toml")) } } }`)
	registry := language.NewRegistry()
	for _, adapter := range []*testServiceAdapter{
		{id: "sibling", detect: language.Detection{Matched: true, Confidence: 100}, analyse: report.Report{Dependencies: []report.DependencyReport{{Name: "sibling"}}}},
		{id: "jvm", detect: language.Detection{Matched: true, Confidence: 90}, analyseFn: jvmlang.NewAdapter().Analyse},
	} {
		if err := registry.Register(adapter); err != nil {
			t.Fatal(err)
		}
	}
	req := newCacheRequest(t, repo, cacheDir, false)
	req.Language = "all"
	result, err := (&Service{Registry: registry}).Analyse(context.Background(), req)
	if err == nil || !reflect.DeepEqual(result, report.Report{}) {
		t.Errorf("incomplete Gradle discovery accepted: error=%v result=%+v", err, result)
	}
	assertGradleCacheUnpublished(t, cacheDir)
}

func TestGradleMalformedBuildFilePublishesNoCache(t *testing.T) {
	repo, cacheDir := t.TempDir(), t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "build.gradle"), "implementation 'org.example:early:1'\nimplementation(\n")
	registry := language.NewRegistry()
	if err := registry.Register(jvmlang.NewAdapter()); err != nil {
		t.Fatal(err)
	}
	req := newCacheRequest(t, repo, cacheDir, false)
	req.Language = "jvm"
	result, err := (&Service{Registry: registry}).Analyse(context.Background(), req)
	if err == nil || !reflect.DeepEqual(result, report.Report{}) {
		t.Fatalf("malformed Gradle discovery accepted or returned partial result: error=%v result=%+v", err, result)
	}
	assertGradleCacheUnpublished(t, cacheDir)
}

func assertGradleCacheUnpublished(t *testing.T, root string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			t.Errorf("failed invocation published cache file: %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestGradleIdentityFailurePublishesNoCache(t *testing.T) {
	repo, cacheDir := t.TempDir(), t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "build.gradle"), "implementation 'org.example:demo:1'\n")
	testutil.MustWriteFile(t, filepath.Join(repo, "Main.java"), "import org.example.Widget;\nclass Main { Widget value; }\n")
	lock, err := os.Create(filepath.Join(repo, "gradle.lockfile"))
	if err != nil {
		t.Fatal(err)
	}
	truncateErr := lock.Truncate((32 << 20) + 1)
	closeErr := lock.Close()
	if err := errors.Join(truncateErr, closeErr); err != nil {
		t.Fatal(err)
	}
	registry := language.NewRegistry()
	if err := registry.Register(jvmlang.NewAdapter()); err != nil {
		t.Fatal(err)
	}
	req := newCacheRequest(t, repo, cacheDir, false)
	req.Language = "jvm"
	req.Features = mustResolveDependencyIdentityPreviewFeatureSet(t)
	result, err := (&Service{Registry: registry}).Analyse(context.Background(), req)
	if err == nil || !reflect.DeepEqual(result, report.Report{}) {
		t.Errorf("incomplete identity accepted: error=%v result=%+v", err, result)
	}
	assertGradleCacheUnpublished(t, cacheDir)
}

func TestGradleIdentityPreviewPreservesKnownSymlinkWarnings(t *testing.T) {
	for _, name := range []string{"build.gradle", "build.gradle.kts", "gradle.lockfile", "settings.gradle", "settings.gradle.kts", "libs.versions.toml", "gradle/libs.versions.toml"} {
		t.Run(name, func(t *testing.T) {
			assertGradleIdentityKnownSymlinkService(t, name)
		})
	}
}

func assertGradleIdentityKnownSymlinkService(t *testing.T, name string) {
	t.Helper()
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "build.gradle"), "implementation 'org.example:demo:1'\n")
	testutil.MustWriteFile(t, filepath.Join(repo, "Main.java"), "import org.example.Widget;\nclass Main { Widget value; }\n")
	link := filepath.Join(repo, "decoy", name)
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "absent"), link); err != nil {
		t.Fatal(err)
	}
	service := mavenTestService(t)
	req := newCacheRequest(t, repo, t.TempDir(), false)
	req.Language = "jvm"
	off, err := service.Analyse(t.Context(), req)
	if err != nil || len(off.Dependencies) != 1 {
		t.Fatalf("preview-off changed below-limit result: %+v %v", off, err)
	}
	legacy := off
	annotateDependencyIdentitiesWithContext(t.Context(), repo, &legacy)
	if legacy.Dependencies[0].Identity == nil || legacy.Dependencies[0].Identity.Version != "1" {
		t.Fatalf("legacy identity control: %+v", legacy.Dependencies)
	}
	req.Features = mustResolveDependencyIdentityPreviewFeatureSet(t)
	for attempt := 0; attempt < 2; attempt++ {
		on, err := service.Analyse(t.Context(), req)
		assertGradleKnownSymlinkResult(t, legacy, on, err)
		if attempt == 1 && on.Cache.Hits != 1 {
			t.Fatalf("expected actual warm JVM cache hit: %+v", on.Cache)
		}
	}
}

func assertGradleKnownSymlinkResult(t *testing.T, want, got report.Report, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("preview converted known symlink exclusion to fatal error: %v", err)
	}
	if !reflect.DeepEqual(got.Dependencies, want.Dependencies) || !reflect.DeepEqual(got.Warnings, want.Warnings) {
		t.Fatalf("preview changed legacy dependencies or ordered warnings: got=%+v/%q want=%+v/%q", got.Dependencies, got.Warnings, want.Dependencies, want.Warnings)
	}
}
