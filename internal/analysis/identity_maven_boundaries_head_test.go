package analysis

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/report/model"
	"github.com/ben-ranford/lopper/internal/safeio"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestMavenCatalogPreservesIdentityEligibilityAndFallback(t *testing.T) {
	repo := t.TempDir()
	base := mavenCacheTestReport(t, "pom.xml")
	base.RepoPath = repo
	for _, path := range []string{"POM.XML", "target/pom.xml", "build/pom.xml", ".gradle/pom.xml"} {
		entry, err := base.MavenManifests[0].WithPath(path)
		if err != nil {
			t.Fatal(err)
		}
		base.MavenManifests = append(base.MavenManifests, entry)
	}
	testutil.MustWriteFile(t, filepath.Join(repo, ".mvn", "pom.xml"), `<project><dependencies><dependency><groupId>extra</groupId><artifactId>uncaptured</artifactId><version>2</version></dependency></dependencies></project>`)
	base.Dependencies = append(base.Dependencies, report.DependencyReport{Language: "jvm", Name: "uncaptured"})
	reads, decodes := observeMavenIdentityIO(t)
	annotateDependencyIdentities(repo, &base)
	if *reads != 1 || *decodes != 1 {
		t.Fatalf("uncaptured-only IO=%d/%d", *reads, *decodes)
	}
	assertMavenServiceIdentity(t, base, "1", "pom.xml")
	extra := findIdentityDependency(t, base, "jvm", "uncaptured")
	if extra.Identity.Version != "2" || extra.Identity.Source != ".mvn/pom.xml" {
		t.Fatalf("broader identity discovery changed: %#v", extra.Identity)
	}
}
func TestMavenCapturedFailurePreservesWarningsWithoutRead(t *testing.T) {
	for _, tc := range []struct{ stage, kind, detail string }{{"read", "permission", "permission denied"}, {"read", "missing", "not found"}, {"read", "large", safeio.ErrFileTooLarge.Error()}, {"read", "io", "I/O error"}, {"parse", "xml", "invalid XML"}} {
		t.Run(tc.kind, func(t *testing.T) {
			entry, err := model.NewMavenManifest("pom.xml", nil, nil, nil, tc.stage, tc.kind)
			if err != nil {
				t.Fatal(err)
			}
			result := report.Report{MavenManifests: []report.MavenManifest{entry}, MavenManifestCatalog: true, Dependencies: []report.DependencyReport{{Language: "jvm", Name: "widgets"}}}
			reads, decodes := observeMavenIdentityIO(t)
			annotateDependencyIdentities(t.TempDir(), &result)
			if *reads != 0 || *decodes != 0 || len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], tc.detail) {
				t.Fatalf("warnings=%v IO=%d/%d", result.Warnings, *reads, *decodes)
			}
		})
	}
}
func TestMavenMergedRootsDeduplicateAndRejectConflicts(t *testing.T) {
	root := t.TempDir()
	first := mavenCacheTestReport(t, "pom.xml")
	first.RepoPath = filepath.Join(root, "nested")
	entries, present, err := mergedMavenEvidence(root, []report.Report{first, first})
	if err != nil || !present || len(entries) != 1 || entries[0].Path() != "nested/pom.xml" {
		t.Fatalf("merge: %v %v %#v", err, present, entries)
	}
	if first.MavenManifests[0].Path() != "pom.xml" {
		t.Fatal("merge mutated producer")
	}
	other := first
	entry, err := model.NewMavenManifest("pom.xml", nil, nil, nil, "parse", "xml")
	if err != nil {
		t.Fatal(err)
	}
	other.MavenManifests = []report.MavenManifest{entry}
	if _, _, err := mergedMavenEvidence(root, []report.Report{first, other}); err == nil {
		t.Fatal("conflicting states merged")
	}
	other.RepoPath = filepath.Join(root, "..", "outside")
	if _, _, err := mergedMavenEvidence(root, []report.Report{other}); err == nil {
		t.Fatal("escaping root merged")
	}
}
func TestMavenRetentionErrorsRemainFatalInEveryMode(t *testing.T) {
	joined := errors.Join(model.ErrMavenEvidenceLimit, context.Canceled)
	for _, mode := range []string{"jvm", "auto", "all"} {
		t.Run(mode, func(t *testing.T) {
			repo := t.TempDir()
			adapter := &testServiceAdapter{id: "jvm", err: joined}
			candidate := language.Candidate{Adapter: adapter, Detection: language.Detection{Matched: true, Roots: []string{repo}}}
			result, warnings, _, err := (&Service{}).runCandidateOnRoots(context.Background(), Request{Language: mode}, repo, candidate, nil)
			if !errors.Is(err, model.ErrMavenEvidenceLimit) || !errors.Is(err, context.Canceled) || len(result) != 0 || len(warnings) != 0 {
				t.Fatalf("mode%s err=%v reports=%v warnings=%v", mode, err, result, warnings)
			}
		})
	}
}
func TestMavenFinalizationAlwaysDisposesArtifacts(t *testing.T) {
	for _, mode := range []string{"preview-off", "preview-on", "empty", "cancelled", "runtime-error", "runtime-malformed"} {
		t.Run(mode, func(t *testing.T) {
			value := mavenCacheTestReport(t, "pom.xml")
			req := Request{}
			ctx := context.Background()
			ctx, req, value = mavenFinalizationCase(ctx, t, mode, req, value)
			result, err := finalizeReportWithContext(ctx, req, value.RepoPath, value.RepoPath, nil, value)
			wantError := mode == "cancelled" || mode == "runtime-malformed"
			if (err != nil) != wantError {
				t.Fatalf("mode %s error=%v", mode, err)
			}
			if result.MavenManifestCatalog || len(result.MavenManifests) != 0 {
				t.Fatal("result retained artifacts")
			}
			if !value.MavenManifestCatalog || len(value.MavenManifests) != 1 {
				t.Fatal("finalizer mutated caller ownership")
			}
		})
	}
}

func mavenFinalizationCase(ctx context.Context, t *testing.T, mode string, req Request, value report.Report) (context.Context, Request, report.Report) {
	t.Helper()
	switch mode {
	case "preview-on":
		req.Features = mustResolveDependencyIdentityPreviewFeatureSet(t)
	case "empty":
		value.Dependencies = nil
	case "cancelled":
		cancelCtx, cancel := context.WithCancel(ctx)
		cancel()
		ctx = cancelCtx
	case "runtime-error":
		req.RuntimeTracePath = filepath.Join(t.TempDir(), "missing")
	case "runtime-malformed":
		req.RuntimeTracePath = filepath.Join(t.TempDir(), "trace.json")
		testutil.MustWriteFile(t, req.RuntimeTracePath, "{")
	}
	return ctx, req, value
}
