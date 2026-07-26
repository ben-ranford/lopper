package analysis

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/safeio"
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

func TestAnalysisPipelinePersistRuntimeTraceAndCleanupWithOwnedRepositoryView(t *testing.T) {
	repo := t.TempDir()
	repository, err := ResolveTrustedRepository(repo)
	if err != nil {
		t.Fatalf("authorize repository: %v", err)
	}
	view, err := OpenTrustedRepository(context.Background(), repository, repo, nil)
	if err != nil {
		t.Fatalf("open trusted repository view: %v", err)
	}

	runtimeTracePath := filepath.Join(view.ExecutionPath(), "trace", "runtime.ndjson")
	runtimeStatePath := runtimeTracePath + ".state.json"
	writeFile(t, runtimeTracePath, "{\"module\":\"lodash/map\"}\n")
	writeFile(t, runtimeStatePath, "{\"captured\":true}\n")

	cleanupCalled := 0
	pipeline := &analysisPipeline{
		executionRepoPath: view.ExecutionPath(),
		repositoryView:    view,
		ownsRepository:    true,
		request:           Request{RuntimeTestCommand: "npm test", RuntimeTracePathExplicit: true},
		cleanupFn: func() {
			cleanupCalled++
		},
	}

	warnings, err := pipeline.persistRuntimeTrace(runtimeTracePath)
	if err != nil {
		t.Fatalf("persist runtime trace: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("expected no runtime trace persistence warnings, got %#v", warnings)
	}

	if data, err := os.ReadFile(filepath.Join(repo, "trace", "runtime.ndjson")); err != nil || string(data) != "{\"module\":\"lodash/map\"}\n" {
		t.Fatalf("persist runtime trace report: data=%q err=%v", data, err)
	}
	if data, err := os.ReadFile(filepath.Join(repo, "trace", "runtime.ndjson.state.json")); err != nil || string(data) != "{\"captured\":true}\n" {
		t.Fatalf("persist runtime trace state: data=%q err=%v", data, err)
	}

	snapshotPath := view.ExecutionPath()
	if err := pipeline.cleanup(); err != nil {
		t.Fatalf("pipeline cleanup: %v", err)
	}
	if cleanupCalled != 1 {
		t.Fatalf("expected cleanupFn once, got %d", cleanupCalled)
	}
	if _, err := os.Stat(snapshotPath); !os.IsNotExist(err) {
		t.Fatalf("expected owned repository view cleanup to remove snapshot, stat err=%v", err)
	}
}

func TestAnalysisPipelineCleanupWithoutOwnedRepositoryViewAndRuntimeTraceSkip(t *testing.T) {
	cleanupCalled := 0
	pipeline := &analysisPipeline{
		executionRepoPath: t.TempDir(),
		cleanupFn: func() {
			cleanupCalled++
		},
	}

	warnings, err := pipeline.persistRuntimeTrace(filepath.Join(t.TempDir(), "outside", "runtime.ndjson"))
	if err != nil {
		t.Fatalf("unexpected external runtime trace persistence error: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("expected no external runtime trace persistence warnings, got %#v", warnings)
	}

	if err := pipeline.cleanup(); err != nil {
		t.Fatalf("pipeline cleanup without owned repository view: %v", err)
	}
	if cleanupCalled != 1 {
		t.Fatalf("expected cleanupFn once without owned repository view, got %d", cleanupCalled)
	}
}

func TestAnalysisPipelinePersistRuntimeTraceRefusesSymlinkedRepositoryTarget(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, "trace-target"), 0o750); err != nil {
		t.Fatalf("mkdir trace target: %v", err)
	}
	if err := os.Symlink("trace-target", filepath.Join(repo, "trace")); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	repository, err := ResolveTrustedRepository(repo)
	if err != nil {
		t.Fatalf("authorize repository: %v", err)
	}
	view, err := OpenTrustedRepository(context.Background(), repository, repo, nil)
	if err != nil {
		t.Fatalf("open trusted repository view: %v", err)
	}
	t.Cleanup(func() {
		if err := view.Close(); err != nil {
			t.Errorf("close repository view: %v", err)
		}
	})

	runtimeTracePath := filepath.Join(view.ExecutionPath(), "trace-target", "runtime.ndjson")
	writeFile(t, runtimeTracePath, "{\"module\":\"lodash/map\"}\n")
	pipeline := &analysisPipeline{
		executionRepoPath: view.ExecutionPath(),
		repositoryView:    view,
		request:           Request{RuntimeTestCommand: "npm test", RuntimeTracePathExplicit: true},
	}
	_, err = pipeline.persistRuntimeTrace(filepath.Join(view.ExecutionPath(), "trace", "runtime.ndjson"))
	if err == nil || !strings.Contains(err.Error(), "persist requested runtime trace") {
		t.Fatalf("expected symlinked repository target persistence error, got %v", err)
	}

	if _, err := os.Stat(filepath.Join(repo, "trace-target", "runtime.ndjson")); !os.IsNotExist(err) {
		t.Fatalf("symlinked repository trace target was written: %v", err)
	}
}

func TestAnalysisPipelinePersistRuntimeTraceSurfacesMissingExplicitTrace(t *testing.T) {
	repo := t.TempDir()
	repository, err := ResolveTrustedRepository(repo)
	if err != nil {
		t.Fatalf("authorize repository: %v", err)
	}
	view, err := OpenTrustedRepository(context.Background(), repository, repo, nil)
	if err != nil {
		t.Fatalf("open trusted repository view: %v", err)
	}
	t.Cleanup(func() {
		if err := view.Close(); err != nil {
			t.Errorf("close repository view: %v", err)
		}
	})

	pipeline := &analysisPipeline{
		executionRepoPath: view.ExecutionPath(),
		repositoryView:    view,
		request:           Request{RuntimeTestCommand: "npm test", RuntimeTracePathExplicit: true},
	}
	_, err = pipeline.persistRuntimeTrace(filepath.Join(view.ExecutionPath(), "trace", "missing.ndjson"))
	if err == nil || !strings.Contains(err.Error(), "persist requested runtime trace") {
		t.Fatalf("expected missing trace persistence error, got %v", err)
	}
}

func TestAnalysisPipelinePersistRuntimeTraceAllowsMissingDefaultTrace(t *testing.T) {
	repo := t.TempDir()
	repository, err := ResolveTrustedRepository(repo)
	if err != nil {
		t.Fatalf("authorize repository: %v", err)
	}
	view, err := OpenTrustedRepository(context.Background(), repository, repo, nil)
	if err != nil {
		t.Fatalf("open trusted repository view: %v", err)
	}
	t.Cleanup(func() {
		if err := view.Close(); err != nil {
			t.Errorf("close repository view: %v", err)
		}
	})

	pipeline := &analysisPipeline{
		executionRepoPath: view.ExecutionPath(),
		repositoryView:    view,
		request:           Request{RuntimeTestCommand: "npm test"},
	}
	warnings, err := pipeline.persistRuntimeTrace(filepath.Join(view.ExecutionPath(), "trace", "missing.ndjson"))
	if err != nil {
		t.Fatalf("expected missing default trace persistence to stay best-effort, got %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings for missing default trace persistence, got %#v", warnings)
	}
}

func TestAnalysisPipelinePersistRuntimeTraceAllowsDefaultWriteFailure(t *testing.T) {
	repo := t.TempDir()
	repository, err := ResolveTrustedRepository(repo)
	if err != nil {
		t.Fatalf("authorize repository: %v", err)
	}
	view, err := OpenTrustedRepository(context.Background(), repository, repo, nil)
	if err != nil {
		t.Fatalf("open trusted repository view: %v", err)
	}
	t.Cleanup(func() {
		if err := view.Close(); err != nil {
			t.Errorf("close repository view: %v", err)
		}
	})

	runtimeTracePath := filepath.Join(view.ExecutionPath(), "trace", "runtime.ndjson")
	writeFile(t, runtimeTracePath, "{\"module\":\"lodash/map\"}\n")
	if err := os.MkdirAll(filepath.Join(repo, "trace", "runtime.ndjson"), 0o750); err != nil {
		t.Fatalf("mkdir blocking runtime trace path: %v", err)
	}
	pipeline := &analysisPipeline{
		executionRepoPath: view.ExecutionPath(),
		repositoryView:    view,
		request:           Request{RuntimeTestCommand: "npm test"},
	}
	warnings, err := pipeline.persistRuntimeTrace(runtimeTracePath)
	if err != nil {
		t.Fatalf("expected default trace write failure to stay best-effort, got %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings for default trace write failure, got %#v", warnings)
	}
}

func TestAnalysisPipelinePersistRuntimeTraceSurfacesExplicitWriteFailure(t *testing.T) {
	repo := t.TempDir()
	repository, err := ResolveTrustedRepository(repo)
	if err != nil {
		t.Fatalf("authorize repository: %v", err)
	}
	view, err := OpenTrustedRepository(context.Background(), repository, repo, nil)
	if err != nil {
		t.Fatalf("open trusted repository view: %v", err)
	}
	t.Cleanup(func() {
		if err := view.Close(); err != nil {
			t.Errorf("close repository view: %v", err)
		}
	})

	runtimeTracePath := filepath.Join(view.ExecutionPath(), "trace", "runtime.ndjson")
	writeFile(t, runtimeTracePath, "{\"module\":\"lodash/map\"}\n")
	if err := os.MkdirAll(filepath.Join(repo, "trace", "runtime.ndjson"), 0o750); err != nil {
		t.Fatalf("mkdir blocking runtime trace path: %v", err)
	}

	pipeline := &analysisPipeline{
		executionRepoPath: view.ExecutionPath(),
		repositoryView:    view,
		request:           Request{RuntimeTestCommand: "npm test", RuntimeTracePathExplicit: true},
	}
	_, err = pipeline.persistRuntimeTrace(runtimeTracePath)
	if err == nil || !strings.Contains(err.Error(), "persist requested runtime trace") {
		t.Fatalf("expected explicit runtime trace write failure, got %v", err)
	}
}

func TestAnalysisPipelinePersistRuntimeTraceExplicitlyAllowsMissingState(t *testing.T) {
	repo := t.TempDir()
	repository, err := ResolveTrustedRepository(repo)
	if err != nil {
		t.Fatalf("authorize repository: %v", err)
	}
	view, err := OpenTrustedRepository(context.Background(), repository, repo, nil)
	if err != nil {
		t.Fatalf("open trusted repository view: %v", err)
	}
	t.Cleanup(func() {
		if err := view.Close(); err != nil {
			t.Errorf("close repository view: %v", err)
		}
	})

	runtimeTracePath := filepath.Join(view.ExecutionPath(), "trace", "runtime.ndjson")
	writeFile(t, runtimeTracePath, "{\"module\":\"lodash/map\"}\n")
	pipeline := &analysisPipeline{
		executionRepoPath: view.ExecutionPath(),
		repositoryView:    view,
		request:           Request{RuntimeTestCommand: "npm test", RuntimeTracePathExplicit: true},
	}
	warnings, err := pipeline.persistRuntimeTrace(runtimeTracePath)
	if err != nil {
		t.Fatalf("persist explicit runtime trace without state: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings for missing explicit state file, got %#v", warnings)
	}
}

func TestAnalysisPipelinePersistRuntimeTraceWarnsWhenExplicitStateWriteFails(t *testing.T) {
	repo := t.TempDir()
	repository, err := ResolveTrustedRepository(repo)
	if err != nil {
		t.Fatalf("authorize repository: %v", err)
	}
	view, err := OpenTrustedRepository(context.Background(), repository, repo, nil)
	if err != nil {
		t.Fatalf("open trusted repository view: %v", err)
	}
	t.Cleanup(func() {
		if err := view.Close(); err != nil {
			t.Errorf("close repository view: %v", err)
		}
	})

	runtimeTracePath := filepath.Join(view.ExecutionPath(), "trace", "runtime.ndjson")
	writeFile(t, runtimeTracePath, "{\"module\":\"lodash/map\"}\n")
	writeFile(t, runtimeTracePath+".state.json", "{\"captured\":true}\n")
	if err := os.MkdirAll(filepath.Join(repo, "trace", "runtime.ndjson.state.json"), 0o750); err != nil {
		t.Fatalf("mkdir blocking state path: %v", err)
	}

	pipeline := &analysisPipeline{
		executionRepoPath: view.ExecutionPath(),
		repositoryView:    view,
		request:           Request{RuntimeTestCommand: "npm test", RuntimeTracePathExplicit: true},
	}
	warnings, err := pipeline.persistRuntimeTrace(runtimeTracePath)
	if err != nil {
		t.Fatalf("persist explicit runtime trace with blocked state path: %v", err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "runtime trace state was not persisted") {
		t.Fatalf("expected explicit state write warning, got %#v", warnings)
	}
}

func TestAnalysisPipelinePersistRuntimeTraceWarnsWhenExplicitStateReadFails(t *testing.T) {
	repo := t.TempDir()
	repository, err := ResolveTrustedRepository(repo)
	if err != nil {
		t.Fatalf("authorize repository: %v", err)
	}
	view, err := OpenTrustedRepository(context.Background(), repository, repo, nil)
	if err != nil {
		t.Fatalf("open trusted repository view: %v", err)
	}
	t.Cleanup(func() {
		if err := view.Close(); err != nil {
			t.Errorf("close repository view: %v", err)
		}
	})

	runtimeTracePath := filepath.Join(view.ExecutionPath(), "trace", "runtime.ndjson")
	writeFile(t, runtimeTracePath, "{\"module\":\"lodash/map\"}\n")
	if err := os.MkdirAll(runtimeTracePath+".state.json", 0o750); err != nil {
		t.Fatalf("mkdir state directory in execution snapshot: %v", err)
	}

	pipeline := &analysisPipeline{
		executionRepoPath: view.ExecutionPath(),
		repositoryView:    view,
		request:           Request{RuntimeTestCommand: "npm test", RuntimeTracePathExplicit: true},
	}
	warnings, err := pipeline.persistRuntimeTrace(runtimeTracePath)
	if err != nil {
		t.Fatalf("persist explicit runtime trace with unreadable state path: %v", err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "runtime trace state was not persisted") {
		t.Fatalf("expected explicit state read warning, got %#v", warnings)
	}
}

func TestAnalysisPipelinePersistRuntimeTraceAllowsDefaultStateWriteFailure(t *testing.T) {
	repo := t.TempDir()
	repository, err := ResolveTrustedRepository(repo)
	if err != nil {
		t.Fatalf("authorize repository: %v", err)
	}
	view, err := OpenTrustedRepository(context.Background(), repository, repo, nil)
	if err != nil {
		t.Fatalf("open trusted repository view: %v", err)
	}
	t.Cleanup(func() {
		if err := view.Close(); err != nil {
			t.Errorf("close repository view: %v", err)
		}
	})

	runtimeTracePath := filepath.Join(view.ExecutionPath(), "trace", "runtime.ndjson")
	writeFile(t, runtimeTracePath, "{\"module\":\"lodash/map\"}\n")
	writeFile(t, runtimeTracePath+".state.json", "{\"captured\":true}\n")
	if err := os.MkdirAll(filepath.Join(repo, "trace", "runtime.ndjson.state.json"), 0o750); err != nil {
		t.Fatalf("mkdir blocking state path: %v", err)
	}

	pipeline := &analysisPipeline{
		executionRepoPath: view.ExecutionPath(),
		repositoryView:    view,
		request:           Request{RuntimeTestCommand: "npm test"},
	}
	warnings, err := pipeline.persistRuntimeTrace(runtimeTracePath)
	if err != nil {
		t.Fatalf("expected default state write failure to stay best-effort, got %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings for default state write failure, got %#v", warnings)
	}
}

func TestAnalysisPipelinePersistRuntimeTraceAllowsDefaultStateReadFailure(t *testing.T) {
	repo := t.TempDir()
	repository, err := ResolveTrustedRepository(repo)
	if err != nil {
		t.Fatalf("authorize repository: %v", err)
	}
	view, err := OpenTrustedRepository(context.Background(), repository, repo, nil)
	if err != nil {
		t.Fatalf("open trusted repository view: %v", err)
	}
	t.Cleanup(func() {
		if err := view.Close(); err != nil {
			t.Errorf("close repository view: %v", err)
		}
	})

	runtimeTracePath := filepath.Join(view.ExecutionPath(), "trace", "runtime.ndjson")
	writeFile(t, runtimeTracePath, "{\"module\":\"lodash/map\"}\n")
	if err := os.MkdirAll(runtimeTracePath+".state.json", 0o750); err != nil {
		t.Fatalf("mkdir state directory in execution snapshot: %v", err)
	}

	pipeline := &analysisPipeline{
		executionRepoPath: view.ExecutionPath(),
		repositoryView:    view,
		request:           Request{RuntimeTestCommand: "npm test"},
	}
	warnings, err := pipeline.persistRuntimeTrace(runtimeTracePath)
	if err != nil {
		t.Fatalf("expected default state read failure to stay best-effort, got %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings for default state read failure, got %#v", warnings)
	}
}

func TestAnalysisPipelinePersistRuntimeTraceNoopsWithoutCaptureContext(t *testing.T) {
	if warnings, err := (*analysisPipeline)(nil).persistRuntimeTrace("trace.ndjson"); err != nil || len(warnings) != 0 {
		t.Fatalf("expected nil pipeline runtime trace noop, warnings=%#v err=%v", warnings, err)
	}
	pipeline := &analysisPipeline{}
	if warnings, err := pipeline.persistRuntimeTrace("trace.ndjson"); err != nil || len(warnings) != 0 {
		t.Fatalf("expected missing repository view runtime trace noop, warnings=%#v err=%v", warnings, err)
	}
	repo := t.TempDir()
	repository, err := ResolveTrustedRepository(repo)
	if err != nil {
		t.Fatalf("authorize repository: %v", err)
	}
	view, err := OpenTrustedRepository(context.Background(), repository, repo, nil)
	if err != nil {
		t.Fatalf("open trusted repository view: %v", err)
	}
	t.Cleanup(func() {
		if err := view.Close(); err != nil {
			t.Errorf("close repository view: %v", err)
		}
	})
	pipeline = &analysisPipeline{repositoryView: view}
	if warnings, err := pipeline.persistRuntimeTrace(""); err != nil || len(warnings) != 0 {
		t.Fatalf("expected blank runtime trace noop, warnings=%#v err=%v", warnings, err)
	}
	if warnings, err := pipeline.persistRuntimeTrace(filepath.Join(view.ExecutionPath(), "trace", "runtime.ndjson")); err != nil || len(warnings) != 0 {
		t.Fatalf("expected missing runtime command noop, warnings=%#v err=%v", warnings, err)
	}
	pipeline.request.RuntimeTestCommand = "npm test"
	if warnings, err := pipeline.persistRuntimeTrace(view.ExecutionPath()); err != nil || len(warnings) != 0 {
		t.Fatalf("expected repository-root runtime trace noop, warnings=%#v err=%v", warnings, err)
	}
	if warnings, err := pipeline.persistRuntimeTrace(filepath.Join(t.TempDir(), "outside", "runtime.ndjson")); err != nil || len(warnings) != 0 {
		t.Fatalf("expected external runtime trace noop, warnings=%#v err=%v", warnings, err)
	}
}

func TestRemapAnalyzedRootsMapsExecutionRootToAuthorizedRepository(t *testing.T) {
	got := remapAnalyzedRoots([]string{"/execution-snapshot"}, "/execution-snapshot", "/authorized-repo")
	if len(got) != 1 || got[0] != "/authorized-repo" {
		t.Fatalf("remapped execution root = %#v, want authorized repository root", got)
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

	if _, err := annotateRuntimeTraceIfPresent(tracePath, "js-ts", report.Report{}, false); err == nil {
		t.Fatalf("expected invalid runtime trace to fail")
	}
}

func TestAnnotateRuntimeTraceOversizedFileFails(t *testing.T) {
	tracePath := filepath.Join(t.TempDir(), "trace.ndjson")
	if err := os.WriteFile(tracePath, []byte(oversizedRuntimeTraceContentForAnalysisTest()), 0o600); err != nil {
		t.Fatalf("write oversized trace: %v", err)
	}

	if _, err := annotateRuntimeTraceIfPresent(tracePath, "js-ts", report.Report{}, false); !errors.Is(err, safeio.ErrFileTooLarge) {
		t.Fatalf("expected oversized runtime trace to fail with ErrFileTooLarge, got %v", err)
	}
}

func TestAnnotateRuntimeTraceSkipsUnsupportedLanguageBeforeReadingTrace(t *testing.T) {
	tracePath := filepath.Join(t.TempDir(), "trace.ndjson")
	if err := os.WriteFile(tracePath, []byte("{not-json}\n"), 0o600); err != nil {
		t.Fatalf("write invalid trace: %v", err)
	}

	annotated, err := annotateRuntimeTraceIfPresent(tracePath, "python", report.Report{}, false)
	if err != nil {
		t.Fatalf("expected disabled Python runtime trace to skip invalid file, got %v", err)
	}
	if len(annotated.Warnings) != 0 {
		t.Fatalf("expected no warnings when unsupported trace is skipped, got %#v", annotated.Warnings)
	}
}

func oversizedRuntimeTraceContentForAnalysisTest() string {
	const maxRuntimeTraceBytes = 8 * 1024 * 1024
	line := "{\"module\":\"lodash/map\"}\n"
	repeat := maxRuntimeTraceBytes/len(line) + 1
	return strings.Repeat(line, repeat)
}
