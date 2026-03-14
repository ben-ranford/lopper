package js

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
)

func TestJSAdapterAdditionalBranchCoverage(t *testing.T) {
	t.Run("detect helpers return real errors", func(t *testing.T) {
		if _, err := NewAdapter().DetectWithConfidence(context.Background(), filepath.Join(t.TempDir(), "missing")); err == nil {
			t.Fatalf("expected missing repo to fail detection")
		}

		repoFile := filepath.Join(t.TempDir(), "repo-file")
		if err := os.WriteFile(repoFile, []byte("x"), 0o644); err != nil {
			t.Fatalf("write repo file: %v", err)
		}
		detection := language.Detection{}
		roots := map[string]struct{}{}
		if err := addRootSignalDetection(repoFile, &detection, roots); err == nil {
			t.Fatalf("expected non-directory repo path to fail root signal detection")
		}
	})

	t.Run("detect handles eof cap and skipped dirs", func(t *testing.T) {
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
	})

	t.Run("non package root signals and usage caps", func(t *testing.T) {
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
	})

	t.Run("analyse warning and normalize branches", func(t *testing.T) {
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
	})

	t.Run("usage and export helpers", func(t *testing.T) {
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
	})

	t.Run("dependency collector and resolution helpers", func(t *testing.T) {
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

		collector := newDependencyCollector()
		collector.recordImport(repo, importer, ImportBinding{Module: "dep"})
		collector.recordImport(repo, filepath.Join(t.TempDir(), testIndexJS), ImportBinding{Module: "dep"})
		if _, ok := collector.missing["dep"]; ok {
			t.Fatalf("expected already-found dependency not to be recorded as missing")
		}

		cacheCollector := newDependencyCollector()
		req := dependencyResolutionRequest{RepoPath: repo, ImporterPath: importer, Dependency: "dep"}
		if first := cacheCollector.cachedDependencyRoot(req); first == "" {
			t.Fatalf("expected cached dependency root to resolve")
		}
		if second := cacheCollector.cachedDependencyRoot(req); second == "" || len(cacheCollector.cache) != 1 {
			t.Fatalf("expected cached dependency root reuse, got second=%q cache=%#v", second, cacheCollector.cache)
		}

		custom := &report.RemovalCandidateWeights{Usage: 1, Impact: 2, Confidence: 3}
		got := resolveRemovalCandidateWeights(custom)
		if got == report.DefaultRemovalCandidateWeights() {
			t.Fatalf("expected non-nil weights to normalize instead of using defaults")
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
		deadDir := filepath.Join(t.TempDir(), "dead-resolution")
		if err := os.MkdirAll(deadDir, 0o755); err != nil {
			t.Fatalf("mkdir dead resolution dir: %v", err)
		}
		if err := os.Chdir(deadDir); err != nil {
			t.Fatalf("chdir dead resolution dir: %v", err)
		}
		if err := os.RemoveAll(deadDir); err != nil {
			t.Fatalf("remove dead resolution dir: %v", err)
		}
		if got := resolveDependencyRootFromImporter(dependencyResolutionRequest{RepoPath: ".", ImporterPath: "src/index.js", Dependency: "dep"}); got != "" {
			t.Fatalf("expected repo abs resolution failure to return empty root, got %q", got)
		}
		if got := resolveDependencyRootFromImporter(dependencyResolutionRequest{RepoPath: repo, ImporterPath: "src/index.js", Dependency: "dep"}); got != "" {
			t.Fatalf("expected importer abs resolution failure to return empty root, got %q", got)
		}
	})
}
