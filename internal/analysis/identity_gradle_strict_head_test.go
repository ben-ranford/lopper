package analysis

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/report/model"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestStrictIdentitySnapshotCountsAllEntries(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "build")
	for _, name := range []string{"build.gradle", "unrelated.txt", "nested/plain.txt", "build/skipped.txt"} {
		testutil.MustWriteFile(t, filepath.Join(repo, name), "")
	}
	if err := os.Symlink("absent", filepath.Join(repo, "link")); err != nil {
		t.Fatal(err)
	}
	snapshot, err := discoverStrictIdentitySnapshot(context.Background(), repo, 4)
	if err != nil || len(snapshot.gradleBuildFiles) != 1 {
		t.Fatalf("exact boundary/root exception: %+v %v", snapshot, err)
	}
	snapshot, err = discoverStrictIdentitySnapshot(context.Background(), repo, 3)
	assertStrictIdentityLimit(t, snapshot, err, 3)
	for _, limit := range []int{0, -1} {
		if _, err := discoverStrictIdentitySnapshot(context.Background(), repo, limit); err == nil {
			t.Fatalf("accepted invalid limit %d", limit)
		}
	}
}

func assertStrictIdentityLimit(t *testing.T, snapshot identityManifestSnapshot, err error, limit int) {
	t.Helper()
	var failure *shared.GradleDiscoveryError
	if !errors.As(err, &failure) || !errors.Is(err, shared.ErrGradleDiscoveryLimit) {
		t.Fatalf("untyped limit: %v", err)
	}
	if failure.Limit != int64(limit) || failure.ObservedAtLeast != int64(limit+1) || !reflect.DeepEqual(snapshot, identityManifestSnapshot{}) {
		t.Fatalf("partial snapshot or incorrect bound: %+v %+v", snapshot, failure)
	}
}

func TestStrictIdentitySnapshotPreservesFailures(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent")
	snapshot, err := discoverStrictIdentitySnapshot(context.Background(), missing, 1)
	if !errors.Is(err, os.ErrNotExist) || !reflect.DeepEqual(snapshot, identityManifestSnapshot{}) {
		t.Fatalf("missing walk root: %+v %v", snapshot, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	snapshot, err = discoverStrictIdentitySnapshot(ctx, t.TempDir(), 1)
	if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(snapshot, identityManifestSnapshot{}) {
		t.Fatalf("cancelled snapshot: %+v %v", snapshot, err)
	}
}

func TestStrictIdentityFailureLeavesAnnotationsUntouched(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "build.gradle"), "implementation 'org.example:demo:1'\n")
	testutil.MustWriteFile(t, filepath.Join(repo, "settings.gradle"), `dependencyResolutionManagement { versionCatalogs { create("libs") { from(files("missing.toml")) } } }`)
	result := report.Report{Dependencies: []report.DependencyReport{{Name: "demo", Language: "jvm"}, {Name: "other", Language: "js-ts"}}}
	err := annotateDependencyIdentitiesChecked(context.Background(), repo, &result, identityDiscoveryPolicy{limit: 100})
	var failure *shared.GradleDiscoveryError
	if !errors.As(err, &failure) || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected missing catalog failure: %v", err)
	}
	for _, dep := range result.Dependencies {
		if dep.Identity != nil {
			t.Fatalf("partial identity escaped: %+v", dep)
		}
	}
}

func TestStrictGradleIdentityPreservesBuildAndLockCorrelation(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "build.gradle"), "implementation 'org.example:first'\n")
	testutil.MustWriteFile(t, filepath.Join(repo, "build.gradle.kts"), "implementation(\"org.example:second\")\n")
	testutil.MustWriteFile(t, filepath.Join(repo, "gradle.lockfile"), "org.example:first:1=compile\norg.example:second:2=compile\norg.example:unrelated:3=compile\n")
	result := report.Report{Dependencies: []report.DependencyReport{{Name: "first", Language: "jvm"}, {Name: "second", Language: "jvm"}, {Name: "unrelated", Language: "jvm"}}}
	if err := annotateDependencyIdentitiesChecked(context.Background(), repo, &result, identityDiscoveryPolicy{limit: 100}); err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"1", "2", ""} {
		if result.Dependencies[i].Identity.Version != want {
			t.Fatalf("identity %d: %+v", i, result.Dependencies[i].Identity)
		}
	}
}

func TestCompleteMavenIdentitySnapshotRecoversBeyondAdapterCandidateCap(t *testing.T) {
	repo := t.TempDir()
	writeMavenServiceFixture(t, repo, "")
	documents := make([]report.MavenManifest, 0, 2048)
	for i := 0; i < 2048; i++ {
		name := "pom.xml"
		if i > 0 {
			name = filepath.Join(fmt.Sprintf("m%04d", i), "pom.xml")
			testutil.MustWriteFile(t, filepath.Join(repo, name), "<project/>")
		}
		raw, err := os.ReadFile(filepath.Join(repo, name))
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := shared.DecodePOM(raw)
		if err != nil {
			t.Fatal(err)
		}
		view := parsed.IdentityPolicy()
		document, err := model.NewMavenManifest(filepath.ToSlash(name), view.Properties, view.Dependencies, view.ManagedDependencies, "", "")
		if err != nil {
			t.Fatal(err)
		}
		documents = append(documents, document)
	}
	testutil.MustWriteFile(t, filepath.Join(repo, "zzz", "pom.xml"), `<project><dependencies><dependency><groupId>org.example</groupId><artifactId>widgets</artifactId><version>7.9.1</version></dependency></dependencies></project>`)
	snapshot, err := discoverStrictIdentitySnapshot(t.Context(), repo, maxIdentityDiscoveryFiles)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.pomFiles) != 2049 {
		t.Fatalf("incomplete snapshot: %d", len(snapshot.pomFiles))
	}
	reads, decodes := observeMavenIdentityIO(t)
	index := identityIndex{}
	warnings := newIdentityWarningCollector(repo)
	collectMavenCatalogEvidence(t.Context(), repo, index, snapshot.pomFiles, documents, warnings)
	dep := report.DependencyReport{Name: "widgets", Language: "jvm"}
	identity := buildDependencyIdentityWithContext(t.Context(), dep, identityEvidenceForDependencyWithContext(t.Context(), index, dep))
	if *reads != 1 || *decodes != 1 || identity.Version != "7.9.1" || !reflect.DeepEqual(identity.Evidence, []string{"pom.xml", "zzz/pom.xml"}) || len(warnings.list()) != 0 {
		t.Fatalf("complete Maven recovery: reads/decodes=%d/%d identity=%+v warnings=%v", *reads, *decodes, identity, warnings.list())
	}
}

func TestStrictIdentityCancellationAtCompletedSnapshot(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "build.gradle"), "")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	visited := 0
	snapshot, err := discoverStrictIdentitySnapshotWithPolicy(ctx, repo, identityDiscoveryPolicy{limit: 1, visit: func(string) error { visited++; cancel(); return nil }})
	if visited != 1 || !errors.Is(err, context.Canceled) || !reflect.DeepEqual(snapshot, identityManifestSnapshot{}) {
		t.Fatalf("completed cancelled snapshot escaped: visited=%d snapshot=%+v err=%v", visited, snapshot, err)
	}
}
