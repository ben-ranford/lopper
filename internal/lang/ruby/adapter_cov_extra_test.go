package ruby

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestRubyAdapterMetadataAndDetect(t *testing.T) {
	adapter := NewAdapter()
	if adapter.ID() != "ruby" {
		t.Fatalf("unexpected adapter id: %q", adapter.ID())
	}
	if len(adapter.Aliases()) != 1 || adapter.Aliases()[0] != "rb" {
		t.Fatalf("unexpected aliases: %#v", adapter.Aliases())
	}

	repo := t.TempDir()
	ok, err := adapter.Detect(context.Background(), repo)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if ok {
		t.Fatalf("expected detect=false for empty repo")
	}
}

func TestRubyAdapterHelperBranches(t *testing.T) {
	declared := map[string]struct{}{"httparty": {}}
	if got := dependencyFromRequire("httparty/client", declared); got != "httparty" {
		t.Fatalf("expected root dependency mapping, got %q", got)
	}
	if got := dependencyFromRequire("json", declared); got != "" {
		t.Fatalf("expected undeclared dependency to be ignored, got %q", got)
	}
	if got := dependencyFromRequire("./local", declared); got != "" {
		t.Fatalf("expected relative dependency to be ignored, got %q", got)
	}
	if got := dependencyFromRequire("/usr/lib/ruby", declared); got != "" {
		t.Fatalf("expected absolute dependency to be ignored, got %q", got)
	}
	if got := dependencyFromRequire("", declared); got != "" {
		t.Fatalf("expected empty dependency for empty module, got %q", got)
	}

	if !shouldSkipDir(".bundle") {
		t.Fatalf("expected .bundle to be skipped")
	}
	if shouldSkipDir("src") {
		t.Fatalf("did not expect src to be skipped")
	}

	weights := resolveRemovalCandidateWeights(nil)
	if weights != report.DefaultRemovalCandidateWeights() {
		t.Fatalf("expected default weights")
	}

	custom := report.RemovalCandidateWeights{Usage: 0.1, Impact: 0.2, Confidence: 0.7}
	normalized := resolveRemovalCandidateWeights(&custom)
	if normalized.Confidence == 0 {
		t.Fatalf("expected non-zero normalized confidence weight")
	}

	recs := buildRecommendations(report.DependencyReport{
		Name:          "httparty",
		UnusedImports: []report.ImportUse{{Name: "httparty", Module: "httparty"}},
		RiskCues:      []report.RiskCue{{Code: "dynamic-require"}},
	})
	if len(recs) != 2 {
		t.Fatalf("expected recommendation branches to trigger, got %#v", recs)
	}
}

func TestRubyScanAndBundlerFileBranches(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, gemfileName), "gem 'httparty'\n")
	testutil.MustWriteFile(t, filepath.Join(repo, "app.rb"), "require_relative './local'\nrequire 'httparty/client'\nrequire 'json'\n")

	scan, err := scanRepo(context.Background(), repo)
	if err != nil {
		t.Fatalf("scan repo: %v", err)
	}
	if _, ok := scan.ImportedDependencies["httparty"]; !ok {
		t.Fatalf("expected imported dependency root mapping, got %#v", scan.ImportedDependencies)
	}
	if _, ok := scan.ImportedDependencies["json"]; ok {
		t.Fatalf("expected stdlib/undeclared dependency to be ignored, got %#v", scan.ImportedDependencies)
	}
	if len(scan.Files) != 1 || len(scan.Files[0].Imports) != 1 {
		t.Fatalf("expected only one tracked import, got %#v", scan.Files)
	}
	if scan.Files[0].Imports[0].Wildcard {
		t.Fatalf("expected literal require import to not be wildcard")
	}

	ctx := testutil.CanceledContext()
	if _, err := scanRepo(ctx, repo); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled context error, got %v", err)
	}

	if bytes, err := readBundlerFile(repo, "missing.file"); err != nil || len(bytes) != 0 {
		t.Fatalf("expected missing Bundler file to return nil bytes and nil error")
	}
}

func TestRubyAnalyseWarningsAndErrorBranches(t *testing.T) {
	adapter := NewAdapter()
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, gemfileName), "gem 'rack'\n")

	reportData, err := adapter.Analyse(context.Background(), language.Request{RepoPath: repo})
	if err != nil {
		t.Fatalf("analyse: %v", err)
	}
	if len(reportData.Warnings) == 0 {
		t.Fatalf("expected warnings when dependency target is missing")
	}
	joinedWarnings := strings.Join(reportData.Warnings, "\n")
	if !strings.Contains(joinedWarnings, "no Ruby files found for analysis") {
		t.Fatalf("expected no Ruby files warning, got %#v", reportData.Warnings)
	}
	if !strings.Contains(joinedWarnings, "no dependency or top-N target provided") {
		t.Fatalf("expected missing target warning, got %#v", reportData.Warnings)
	}

	invalidGemfilePath := filepath.Join(repo, gemfileName)
	if err := os.Remove(filepath.Join(repo, gemfileName)); err != nil {
		t.Fatalf("remove gemfile: %v", err)
	}
	if err := os.MkdirAll(invalidGemfilePath, 0o750); err != nil {
		t.Fatalf("mkdir gemfile dir: %v", err)
	}
	if err := loadBundlerDependencies(repo, map[string]struct{}{}); err == nil {
		t.Fatalf("expected loadBundlerDependencies to fail for directory Gemfile")
	}
	if err := os.RemoveAll(invalidGemfilePath); err != nil {
		t.Fatalf("cleanup gemfile dir: %v", err)
	}
	testutil.MustWriteFile(t, filepath.Join(repo, gemfileName), "gem 'rack'\n")
	lockDir := filepath.Join(repo, gemfileLockName)
	if err := os.MkdirAll(lockDir, 0o750); err != nil {
		t.Fatalf("mkdir lock dir: %v", err)
	}
	if err := loadBundlerDependencies(repo, map[string]struct{}{}); err == nil {
		t.Fatalf("expected loadBundlerDependencies to fail for directory Gemfile.lock")
	}

	if !shouldSkipDir(".git") {
		t.Fatalf("expected common skip dir to be skipped")
	}
}

func TestRubyDetectAndAnalyseMissingRepoErrors(t *testing.T) {
	adapter := NewAdapter()
	missingRepo := filepath.Join(t.TempDir(), "missing")
	if _, err := adapter.DetectWithConfidence(context.Background(), missingRepo); err == nil {
		t.Fatalf("expected detect error for missing repo")
	}
	if _, err := adapter.Analyse(context.Background(), language.Request{RepoPath: missingRepo, TopN: 1}); err == nil {
		t.Fatalf("expected analyse error for missing repo")
	}

	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, gemfileName), "gem 'httparty'\n")
	testutil.MustWriteFile(t, filepath.Join(repo, "app.rb"), "require 'httparty'\n")
	ctx := testutil.CanceledContext()
	if _, err := adapter.DetectWithConfidence(ctx, repo); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled detect error, got %v", err)
	}
}
