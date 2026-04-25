package analysis

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestAnalysisPipelineFinalReportMergedPath(t *testing.T) {
	got := runMergedFinalReport(t)
	assertMergedFinalReportMetadata(t, got)
	assertMergedFinalReportWarnings(t, got)
	assertMergedFinalReportScope(t, got)
	assertMergedFinalReportSummary(t, got)
}

func runMergedFinalReport(t *testing.T) report.Report {
	t.Helper()

	cache := &analysisCache{
		metadata: report.CacheMetadata{
			Enabled: true,
			Hits:    1,
		},
	}
	cache.warn("cache warning")

	pipeline := &analysisPipeline{
		request: Request{
			RepoPath:        "/repo",
			ScopeMode:       ScopeModeChangedPackages,
			LicenseDenyList: []string{"MIT"},
		},
		repoPath:         "/repo",
		analysisRepoPath: "/scoped",
		scopeWarnings:    []string{"scope warning"},
		warnings:         []string{"candidate warning"},
		analyzedRoots:    []string{"/scoped/packages/a"},
		cache:            cache,
		reports: []report.Report{{
			Dependencies: []report.DependencyReport{{
				Language:          "js-ts",
				Name:              "dep",
				UsedExportsCount:  1,
				TotalExportsCount: 2,
				License:           &report.DependencyLicense{SPDX: "MIT"},
			}},
		}},
	}

	got, err := pipeline.finalReport()
	if err != nil {
		t.Fatalf("final report: %v", err)
	}
	return got
}

func assertMergedFinalReportMetadata(t *testing.T, got report.Report) {
	t.Helper()

	if got.SchemaVersion != report.SchemaVersion {
		t.Fatalf("expected schema version %q, got %q", report.SchemaVersion, got.SchemaVersion)
	}
	if got.Cache == nil || !got.Cache.Enabled || got.Cache.Hits != 1 {
		t.Fatalf("expected cache metadata preserved, got %#v", got.Cache)
	}
}

func assertMergedFinalReportWarnings(t *testing.T, got report.Report) {
	t.Helper()

	joinedWarnings := strings.Join(got.Warnings, "\n")
	for _, want := range []string{"scope warning", "candidate warning", "cache warning"} {
		if !strings.Contains(joinedWarnings, want) {
			t.Fatalf("expected warning %q in %q", want, joinedWarnings)
		}
	}
}

func assertMergedFinalReportScope(t *testing.T, got report.Report) {
	t.Helper()

	if got.Scope == nil || got.Scope.Mode != ScopeModeChangedPackages {
		t.Fatalf("expected scope metadata, got %#v", got.Scope)
	}
	if len(got.Scope.Packages) != 1 || got.Scope.Packages[0] != "packages/a" {
		t.Fatalf("expected remapped analyzed roots, got %#v", got.Scope.Packages)
	}
}

func assertMergedFinalReportSummary(t *testing.T, got report.Report) {
	t.Helper()

	if got.Summary == nil || got.Summary.DependencyCount != 1 {
		t.Fatalf("expected computed summary, got %#v", got.Summary)
	}
	if len(got.LanguageBreakdown) != 1 || got.LanguageBreakdown[0].Language != "js-ts" {
		t.Fatalf("expected language breakdown, got %#v", got.LanguageBreakdown)
	}
	if got.Dependencies[0].License == nil || !got.Dependencies[0].License.Denied {
		t.Fatalf("expected license deny policy to be applied, got %#v", got.Dependencies[0].License)
	}
}

func TestAnalysisPipelineFinalReportNoResults(t *testing.T) {
	pipeline := &analysisPipeline{
		request: Request{
			RepoPath: "/repo",
		},
		repoPath:         "/repo",
		analysisRepoPath: "/repo",
		cache: &analysisCache{
			metadata: report.CacheMetadata{Enabled: true},
		},
	}

	got, err := pipeline.finalReport()
	if err != nil {
		t.Fatalf("final report: %v", err)
	}
	if got.SchemaVersion != report.SchemaVersion {
		t.Fatalf("expected schema version on empty report, got %q", got.SchemaVersion)
	}
	if got.Cache == nil || !got.Cache.Enabled {
		t.Fatalf("expected cache metadata on empty report, got %#v", got.Cache)
	}
	if got.Scope == nil || got.Scope.Mode != ScopeModePackage {
		t.Fatalf("expected default scope metadata, got %#v", got.Scope)
	}
	if !strings.Contains(strings.Join(got.Warnings, "\n"), "no language adapter produced results") {
		t.Fatalf("expected no-results warning, got %#v", got.Warnings)
	}
	if got.Summary != nil {
		t.Fatalf("expected nil summary for empty report, got %#v", got.Summary)
	}
	if len(got.LanguageBreakdown) != 0 {
		t.Fatalf("expected empty language breakdown, got %#v", got.LanguageBreakdown)
	}
}

func TestAnalysisPipelineCacheMetadataNil(t *testing.T) {
	pipeline := &analysisPipeline{}
	if got := pipeline.cacheMetadata(); got != nil {
		t.Fatalf("expected nil cache metadata, got %#v", got)
	}
}

func TestScopedCandidateRootsChangedPackagesFallsBack(t *testing.T) {
	repo := t.TempDir()
	root := filepath.Join(repo, "packages", "a")
	if err := os.MkdirAll(root, 0o750); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}

	roots, warnings := scopedCandidateRoots(ScopeModeChangedPackages, []string{root}, repo)
	if len(roots) != 1 || roots[0] != root {
		t.Fatalf("expected package roots fallback, got %#v", roots)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "falling back to package scope") {
		t.Fatalf("expected changed-packages fallback warning, got %#v", warnings)
	}
}

func TestScopedCandidateRootsChangedPackagesFallsBackForSingleCommitRepo(t *testing.T) {
	repo := t.TempDir()
	root := filepath.Join(repo, "packages", "a")
	if err := os.MkdirAll(root, 0o750); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("a1\n"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	testutil.RunGit(t, repo, "init")
	testutil.RunGit(t, repo, "config", "user.email", "analysis-test@example.com")
	testutil.RunGit(t, repo, "config", "user.name", "Analysis Test")
	testutil.RunGit(t, repo, "add", ".")
	testutil.RunGit(t, repo, "commit", "-m", "initial commit")

	roots, warnings := scopedCandidateRoots(ScopeModeChangedPackages, []string{root}, repo)
	if len(roots) != 1 || roots[0] != root {
		t.Fatalf("expected package-root fallback, got %#v", roots)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "falling back to package scope") {
		t.Fatalf("expected changed-packages fallback warning, got %#v", warnings)
	}
}

func TestAnnotateRuntimeTraceInvalidFileFails(t *testing.T) {
	tracePath := filepath.Join(t.TempDir(), "trace.ndjson")
	if err := os.WriteFile(tracePath, []byte("{not-json}\n"), 0o600); err != nil {
		t.Fatalf("write invalid trace: %v", err)
	}

	if _, err := annotateRuntimeTraceIfPresent(tracePath, "js-ts", report.Report{}); err == nil {
		t.Fatalf("expected invalid runtime trace to fail")
	}
}
