package js

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestJSAdapterAdditionalBranchCoverage(t *testing.T) {
	t.Run("detect helpers return real errors", testJSDetectHelpersReturnRealErrors)
	t.Run("detect handles eof cap and skipped dirs", testJSDetectHandlesEOFCapAndSkippedDirs)
	t.Run("non package root signals and usage caps", testJSNonPackageRootSignalsAndUsageCaps)
	t.Run("analyse warning and normalize branches", testJSAnalyseWarningAndNormalizeBranches)
	t.Run("usage and export helpers", testJSUsageAndExportHelpers)
	t.Run("dependency collector and resolution helpers", testJSDependencyCollectorAndResolutionHelpers)
	t.Run("analyse rejects symlinked dependency root", testJSAnalyseRejectsSymlinkedDependencyRoot)
	t.Run("suggest only rejects symlinked dependency root", testJSSuggestOnlyRejectsSymlinkedDependencyRoot)
}

func testJSDetectHelpersReturnRealErrors(t *testing.T) {
	if _, err := NewAdapter().DetectWithConfidence(context.Background(), filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatalf("expected missing repo to fail detection")
	}

	repoFile := filepath.Join(t.TempDir(), "repo-file")
	if err := os.WriteFile(repoFile, []byte("x"), 0o644); err != nil {
		t.Fatalf("write repo file: %v", err)
	}
	detection := language.Detection{}
	roots := map[string]struct{}{}
	if addRootSignalDetection(repoFile, &detection, roots) == nil {
		t.Fatalf("expected non-directory repo path to fail root signal detection")
	}

	rootSignalDirRepo := t.TempDir()
	for _, name := range []string{jsPackageFile, "tsconfig.json"} {
		if err := os.Mkdir(filepath.Join(rootSignalDirRepo, name), 0o755); err != nil {
			t.Fatalf("mkdir root signal dir %s: %v", name, err)
		}
	}
	detection = language.Detection{}
	roots = map[string]struct{}{}
	if err := addRootSignalDetection(rootSignalDirRepo, &detection, roots); err != nil {
		t.Fatalf("add root signal detection for directories: %v", err)
	}
	if detection.Matched || detection.Confidence != 0 || len(roots) != 0 {
		t.Fatalf("expected directory-shaped root signals to be ignored, detection=%#v roots=%#v", detection, roots)
	}
}

func testJSDetectHandlesEOFCapAndSkippedDirs(t *testing.T) {
	repo := t.TempDir()
	for i := 0; i <= 256; i++ {
		path := filepath.Join(repo, "pkg", fmt.Sprintf("f-%03d.js", i))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir js dir: %v", err)
		}
		if err := os.WriteFile(path, []byte("export const x = 1\n"), 0o644); err != nil {
			t.Fatalf("write js file: %v", err)
		}
	}

	detection, err := NewAdapter().DetectWithConfidence(context.Background(), repo)
	if err != nil {
		t.Fatalf("detect with confidence on capped repo: %v", err)
	}
	if !detection.Matched || detection.Confidence == 0 {
		t.Fatalf("expected detection to survive EOF cap normalization, got %#v", detection)
	}

	skipRepo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(skipRepo, ".next"), 0o755); err != nil {
		t.Fatalf("mkdir skipped dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skipRepo, ".next", testIndexJS), []byte("export const x = 1\n"), 0o644); err != nil {
		t.Fatalf("write skipped file: %v", err)
	}
	detection = language.Detection{}
	if err := scanFilesForJSDetection(skipRepo, &detection, map[string]struct{}{}); err != nil {
		t.Fatalf("scan skipped repo: %v", err)
	}
	if detection.Matched {
		t.Fatalf("expected skipped detection dir not to contribute matches, got %#v", detection)
	}
}

func testJSNonPackageRootSignalsAndUsageCaps(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "jsconfig.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write jsconfig: %v", err)
	}
	detection, err := NewAdapter().DetectWithConfidence(context.Background(), repo)
	if err != nil {
		t.Fatalf("detect jsconfig repo: %v", err)
	}
	if !detection.Matched || detection.Confidence < 20 {
		t.Fatalf("expected jsconfig root signal confidence, got %#v", detection)
	}

	uncertain := make([]report.ImportUse, 0, 6)
	for i := 0; i < 6; i++ {
		uncertain = append(uncertain, report.ImportUse{Locations: []report.Location{{File: testIndexJS, Line: i + 1}}})
	}
	summary := summarizeUsageUncertainty(ScanResult{Files: []FileScan{{UncertainImports: uncertain}}})
	if summary == nil || len(summary.Samples) != 5 {
		t.Fatalf("expected uncertainty samples to cap at five, got %#v", summary)
	}
}

func testJSAnalyseWarningAndNormalizeBranches(t *testing.T) {
	repo := t.TempDir()
	reportData, err := NewAdapter().Analyse(context.Background(), language.Request{RepoPath: repo, TopN: 3})
	if err != nil {
		t.Fatalf("analyse empty repo: %v", err)
	}
	if len(reportData.Dependencies) != 0 {
		t.Fatalf("expected no dependencies for empty repo, got %#v", reportData.Dependencies)
	}
	if len(reportData.Warnings) == 0 {
		t.Fatalf("expected no dependency data warning")
	}

	if _, err := NewAdapter().Analyse(context.Background(), language.Request{RepoPath: filepath.Join(t.TempDir(), "missing"), TopN: 1}); err == nil {
		t.Fatalf("expected scan failure for missing repo path")
	}

	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(originalWD); err != nil {
			t.Fatalf("restore wd %s: %v", originalWD, err)
		}
	})
	deadDir := filepath.Join(t.TempDir(), "dead")
	if err := os.MkdirAll(deadDir, 0o755); err != nil {
		t.Fatalf("mkdir dead dir: %v", err)
	}
	if err := os.Chdir(deadDir); err != nil {
		t.Fatalf("chdir dead dir: %v", err)
	}
	if err := os.RemoveAll(deadDir); err != nil {
		t.Fatalf("remove dead dir: %v", err)
	}
	if _, err := NewAdapter().Analyse(context.Background(), language.Request{}); err == nil {
		t.Fatalf("expected analyse to fail when cwd cannot be resolved")
	}
}

func testJSUsageAndExportHelpers(t *testing.T) {
	summary := summarizeUsageUncertainty(ScanResult{
		Files: []FileScan{{
			UncertainImports: []report.ImportUse{{}},
		}},
	})
	if summary == nil || summary.UncertainImportUses != 1 || len(summary.Samples) != 0 {
		t.Fatalf("expected uncertainty summary without samples, got %#v", summary)
	}
	if got := totalExportCount(ExportSurface{IncludesWildcard: true}); got != 0 {
		t.Fatalf("expected wildcard surfaces to report zero total exports, got %d", got)
	}
	if got := exportUsedPercent(ExportSurface{Names: map[string]struct{}{}}, map[string]struct{}{"map": {}}, 0); got != 0 {
		t.Fatalf("expected zero total exports to yield zero used percent, got %f", got)
	}
}

func testJSDependencyCollectorAndResolutionHelpers(t *testing.T) {
	repo := t.TempDir()
	importer := filepath.Join(repo, "src", testIndexJS)
	if err := os.MkdirAll(filepath.Dir(importer), 0o755); err != nil {
		t.Fatalf("mkdir importer dir: %v", err)
	}
	if err := os.WriteFile(importer, []byte("import 'dep'\n"), 0o644); err != nil {
		t.Fatalf("write importer: %v", err)
	}
	depRoot := filepath.Join(repo, "node_modules", "dep")
	if err := os.MkdirAll(depRoot, 0o755); err != nil {
		t.Fatalf("mkdir dep root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(depRoot, "package.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write dep package: %v", err)
	}

	testJSDependencyCollectorTracksResolvedDeps(t, repo, importer)
	testJSResolveRemovalCandidateWeights(t)
	testJSResolveDependencyRootFailures(t, repo)
}

func testJSDependencyCollectorTracksResolvedDeps(t *testing.T, repo, importer string) {
	t.Helper()

	collector := newDependencyCollector()
	collector.recordImport(repo, importer, ImportBinding{Module: "dep"})
	collector.recordImport(repo, filepath.Join(t.TempDir(), testIndexJS), ImportBinding{Module: "dep"})
	if _, ok := collector.missing["dep"]; ok {
		t.Fatalf("expected already-found dependency not to be recorded as missing")
	}

	cacheCollector := newDependencyCollector()
	req := dependencyResolutionRequest{RepoPath: repo, ImporterPath: importer, Dependency: "dep"}
	first, status := cacheCollector.cachedDependencyRoot(req)
	if first == "" || status != dependencyRootFound {
		t.Fatalf("expected cached dependency root to resolve")
	}
	if second, secondStatus := cacheCollector.cachedDependencyRoot(req); second == "" || secondStatus != dependencyRootFound || len(cacheCollector.cache) != 1 {
		t.Fatalf("expected cached dependency root reuse, got second=%q status=%v cache=%#v", second, secondStatus, cacheCollector.cache)
	}
}

func testJSResolveRemovalCandidateWeights(t *testing.T) {
	t.Helper()

	custom := &report.RemovalCandidateWeights{Usage: 1, Impact: 2, Confidence: 3}
	got := shared.ResolveRemovalCandidateWeights(custom)
	if got == report.DefaultRemovalCandidateWeights() {
		t.Fatalf("expected non-nil weights to normalize instead of using defaults")
	}
}

func testJSResolveDependencyRootFailures(t *testing.T, repo string) {
	t.Helper()

	testutil.ChdirRemovedDir(t)
	if got := resolveDependencyRootFromImporter(dependencyResolutionRequest{RepoPath: ".", ImporterPath: "src/index.js", Dependency: "dep"}); got != "" {
		t.Fatalf("expected repo abs resolution failure to return empty root, got %q", got)
	}
	if got := resolveDependencyRootFromImporter(dependencyResolutionRequest{RepoPath: repo, ImporterPath: "src/index.js", Dependency: "dep"}); got != "" {
		t.Fatalf("expected importer abs resolution failure to return empty root, got %q", got)
	}
}

func testJSAnalyseRejectsSymlinkedDependencyRoot(t *testing.T) {
	t.Helper()

	repo := t.TempDir()
	outside := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, testIndexJS), "import { map } from \"lodash\"\nmap([1], Boolean)\n")

	outsideDepRoot := filepath.Join(outside, "node_modules", "lodash")
	if err := os.MkdirAll(outsideDepRoot, 0o755); err != nil {
		t.Fatalf("mkdir outside dependency root: %v", err)
	}
	testutil.MustWriteFile(t, filepath.Join(outsideDepRoot, testPackageJSONName), `{
  "name": "outside-lodash",
  "version": "9.9.9",
  "license": "GPL-3.0-only",
  "exports": {
    "./map": "./map.js"
  }
}`)
	testutil.MustWriteFile(t, filepath.Join(outsideDepRoot, "index.js"), "export const escaped = 1\n")
	testutil.MustWriteFile(t, filepath.Join(outsideDepRoot, "map.js"), "export default function map() {}\n")
	testutil.MustWriteFile(t, filepath.Join(outsideDepRoot, "LICENSE"), "GNU GENERAL PUBLIC LICENSE\n")

	nodeModules := filepath.Join(repo, "node_modules")
	if err := os.MkdirAll(nodeModules, 0o755); err != nil {
		t.Fatalf("mkdir node_modules: %v", err)
	}
	if err := os.Symlink(outsideDepRoot, filepath.Join(nodeModules, "lodash")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	result, err := NewAdapter().Analyse(context.Background(), language.Request{
		RepoPath:   repo,
		Dependency: "lodash",
	})
	if err != nil {
		t.Fatalf("analyse symlinked dependency root: %v", err)
	}
	if len(result.Dependencies) != 1 {
		t.Fatalf("expected one dependency report, got %#v", result.Dependencies)
	}

	dep := result.Dependencies[0]
	if dep.TotalExportsCount != 0 || dep.UsedExportsCount != 1 {
		t.Fatalf("expected outside exports to be ignored, got used=%d total=%d", dep.UsedExportsCount, dep.TotalExportsCount)
	}
	if dep.License == nil || !dep.License.Unknown || dep.License.Source != "unknown" {
		t.Fatalf("expected unknown license for symlinked dependency root, got %#v", dep.License)
	}
	if dep.Provenance == nil || dep.Provenance.Source != "unknown" {
		t.Fatalf("expected unknown provenance for symlinked dependency root, got %#v", dep.Provenance)
	}
	joinedSignals := strings.Join(dep.Provenance.Signals, "\n")
	if strings.Contains(joinedSignals, "outside-lodash") || strings.Contains(joinedSignals, "9.9.9") {
		t.Fatalf("expected outside provenance signals to be ignored, got %#v", dep.Provenance)
	}
	joinedWarnings := strings.Join(result.Warnings, "\n")
	if !strings.Contains(joinedWarnings, dependencyRootOpaqueLayoutWarning) {
		t.Fatalf("expected symlink root warning, got %#v", result.Warnings)
	}
	if strings.Contains(joinedWarnings, "outside-lodash") || strings.Contains(joinedWarnings, "9.9.9") {
		t.Fatalf("expected warnings to avoid outside metadata, got %#v", result.Warnings)
	}
}

func testJSSuggestOnlyRejectsSymlinkedDependencyRoot(t *testing.T) {
	t.Helper()

	repo := t.TempDir()
	outside := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, testIndexJS), "import { map } from \"lodash\"\nmap([1], Boolean)\n")

	outsideDepRoot := filepath.Join(outside, "node_modules", "lodash")
	if err := os.MkdirAll(outsideDepRoot, 0o755); err != nil {
		t.Fatalf("mkdir outside dependency root: %v", err)
	}
	testutil.MustWriteFile(t, filepath.Join(outsideDepRoot, testPackageJSONName), `{
  "name": "outside-lodash",
  "version": "9.9.9",
  "exports": {
    "./map": "./map.js"
  }
}`)
	testutil.MustWriteFile(t, filepath.Join(outsideDepRoot, "map.js"), "export default function map() {}\n")

	nodeModules := filepath.Join(repo, "node_modules")
	if err := os.MkdirAll(nodeModules, 0o755); err != nil {
		t.Fatalf("mkdir node_modules: %v", err)
	}
	if err := os.Symlink(outsideDepRoot, filepath.Join(nodeModules, "lodash")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	result, err := NewAdapter().Analyse(context.Background(), language.Request{
		RepoPath:       repo,
		Dependency:     "lodash",
		SuggestOnly:    true,
		RuntimeProfile: runtimeProfileNodeImport,
	})
	if err != nil {
		t.Fatalf("analyse suggest-only symlinked dependency root: %v", err)
	}
	dep := result.Dependencies[0]
	if dep.Codemod == nil {
		t.Fatalf("expected codemod report in suggest-only mode")
	}
	if len(dep.Codemod.Suggestions) != 0 {
		t.Fatalf("expected no codemod suggestions from outside exports, got %#v", dep.Codemod.Suggestions)
	}
	if len(dep.Codemod.Skips) == 0 {
		t.Fatalf("expected unresolved-export codemod skip, got %#v", dep.Codemod)
	}
	if dep.Codemod.Skips[0].ReasonCode != codemodReasonNoSubpathTarget {
		t.Fatalf("expected no-subpath-target skip, got %#v", dep.Codemod.Skips)
	}
}
