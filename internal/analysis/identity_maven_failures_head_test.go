package analysis

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/report/model"
)

func TestMavenAcceptedCacheCountsTowardLaterRootLimit(t *testing.T) {
	result := mavenCacheTestReport(t, "pom.xml")
	cache, entry := cacheWithCapturedMavenReport(t, result)
	restored, hit, err := cache.lookup(entry)
	if err != nil || !hit {
		t.Fatalf("cache=%v %v", hit, err)
	}
	accumulator := newMavenEvidenceAccumulator(restored.RepoPath)
	if err := accumulator.accept(restored); err != nil {
		t.Fatal(err)
	}
	next := mavenCacheTestReport(t, "other/pom.xml")
	next.RepoPath = restored.RepoPath
	accumulator.size = model.MavenEvidenceByteLimit - next.MavenManifests[0].Size()
	if err := accumulator.accept(next); !errors.Is(err, model.ErrMavenEvidenceLimit) {
		t.Fatalf("accepted cache + live root exceeded aggregate without error: %v", err)
	}
	if len(accumulator.entries) != 1 {
		t.Fatal("overflowing evidence appended")
	}
}
func TestMavenCancellationDisposesPipelineOwnership(t *testing.T) {
	result := mavenCacheTestReport(t, "pom.xml")
	pipeline := analysisPipeline{repoPath: result.RepoPath, analysisRepoPath: result.RepoPath, reports: []report.Report{result}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := pipeline.finalReportWithContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
	if len(pipeline.reports[0].MavenManifests) != 0 || pipeline.reports[0].MavenManifestCatalog {
		t.Fatal("owned pipeline artifact retained")
	}
	if len(result.MavenManifests) != 1 {
		t.Fatal("caller ownership cleared")
	}
}
func TestMavenJoinedLimitCloseErrorFailsAllLanguages(t *testing.T) {
	repo := t.TempDir()
	operation := fs.ErrPermission
	adapter := &testServiceAdapter{id: "jvm", err: errors.Join(model.ErrMavenEvidenceLimit, operation)}
	candidate := language.Candidate{Adapter: adapter, Detection: language.Detection{Matched: true, Confidence: 100, Roots: []string{repo}}}
	_, _, _, err := (&Service{}).runCandidates(context.Background(), Request{Language: "all", ScopeMode: ScopeModePackage}, repo, []language.Candidate{candidate}, nil)
	if !errors.Is(err, operation) || !errors.Is(err, model.ErrMavenEvidenceLimit) {
		t.Fatalf("joined error identity lost: %v", err)
	}
}
func TestMavenCachedRootIsBoundBeforeNestedRebase(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "nested")
	result := mavenCacheTestReport(t, "pom.xml")
	cache, entry := cacheWithCapturedMavenReport(t, result)
	entry.RootPath = nested
	restored, hit, err := cache.lookup(entry)
	if err != nil || !hit {
		t.Fatalf("cache=%v %v", hit, err)
	}
	documents, _, err := mergedMavenEvidence(root, []report.Report{restored})
	if err != nil || documents[0].Path() != "nested/pom.xml" {
		t.Fatalf("bound rebase=%v %v", documents, err)
	}
	if result.MavenManifests[0].Path() != "pom.xml" {
		t.Fatal("cached root rebase mutated producer")
	}
}
