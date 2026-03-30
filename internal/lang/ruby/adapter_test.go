package ruby

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/testutil"
)

const (
	rubyAppFile         = "app.rb"
	demoGemspecFile     = "demo.gemspec"
	httpartyRequireLine = "require 'httparty'\n"
)

func TestRubyAdapterDetectBundlerProject(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, gemfileName), "source 'https://rubygems.org'\ngem 'httparty'\n")
	testutil.MustWriteFile(t, filepath.Join(repo, "main.rb"), httpartyRequireLine)

	detection, err := NewAdapter().DetectWithConfidence(context.Background(), repo)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if !detection.Matched {
		t.Fatalf("expected ruby detection to match")
	}
	if detection.Confidence <= 0 {
		t.Fatalf("expected positive confidence, got %d", detection.Confidence)
	}
}

func TestRubyAdapterDetectGemspecProject(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "pkg", demoGemspecFile), "Gem::Specification.new do |spec|\n  spec.add_dependency 'httparty'\nend\n")
	testutil.MustWriteFile(t, filepath.Join(repo, "pkg", "lib", "demo.rb"), httpartyRequireLine)

	detection, err := NewAdapter().DetectWithConfidence(context.Background(), repo)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if !detection.Matched {
		t.Fatalf("expected ruby detection to match")
	}
	if !slices.Contains(detection.Roots, filepath.Join(repo, "pkg")) {
		t.Fatalf("expected gemspec directory root, got %#v", detection.Roots)
	}
}

func TestRubyAdapterAnalyseDependencyAndTopN(t *testing.T) {
	repo := t.TempDir()
	gemfileLines := []string{
		"source 'https://rubygems.org'",
		"gem 'httparty'",
		"gem 'nokogiri'",
		"",
	}
	gemfileContent := strings.Join(gemfileLines, "\n")
	testutil.MustWriteFile(t, filepath.Join(repo, gemfileName), gemfileContent)
	lockfileLines := []string{
		"GEM",
		"  specs:",
		"    httparty (0.22.0)",
		"    nokogiri (1.16.2)",
		"",
	}
	lockfileContent := strings.Join(lockfileLines, "\n")
	testutil.MustWriteFile(t, filepath.Join(repo, "Gemfile.lock"), lockfileContent)
	testutil.MustWriteFile(t, filepath.Join(repo, rubyAppFile), "require 'httparty'\nHTTParty.get('https://example.test')\n")

	adapter := NewAdapter()
	depReport, err := adapter.Analyse(context.Background(), language.Request{RepoPath: repo, Dependency: "httparty"})
	if err != nil {
		t.Fatalf("analyse dependency: %v", err)
	}
	if len(depReport.Dependencies) != 1 {
		t.Fatalf("expected one dependency report, got %d", len(depReport.Dependencies))
	}
	if depReport.Dependencies[0].Language != "ruby" {
		t.Fatalf("expected ruby language, got %q", depReport.Dependencies[0].Language)
	}
	if depReport.Dependencies[0].TotalExportsCount == 0 {
		t.Fatalf("expected require signals to be counted")
	}

	topReport, err := adapter.Analyse(context.Background(), language.Request{RepoPath: repo, TopN: 5})
	if err != nil {
		t.Fatalf("analyse topN: %v", err)
	}
	names := make([]string, 0, len(topReport.Dependencies))
	for _, dep := range topReport.Dependencies {
		names = append(names, dep.Name)
	}
	if !slices.Contains(names, "nokogiri") {
		t.Fatalf("expected Bundler gem from Gemfile in topN output, got %#v", names)
	}
}

func TestRubyAdapterAnalyseGemspecProjectAndDeduplicatesDeclaredDependencies(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, gemfileName), "source 'https://rubygems.org'\ngem 'httparty'\ngem 'rack'\n")
	testutil.MustWriteFile(t, filepath.Join(repo, demoGemspecFile), strings.Join([]string{
		"Gem::Specification.new do |spec|",
		"  spec.add_dependency 'httparty'",
		"  spec.add_runtime_dependency 'nokogiri', '~> 1.16'",
		"  spec.add_development_dependency 'rspec'",
		"end",
		"",
	}, "\n"))
	testutil.MustWriteFile(t, filepath.Join(repo, rubyAppFile), httpartyRequireLine+"HTTParty.get('https://example.test')\n")

	scan, err := scanRepo(context.Background(), repo)
	if err != nil {
		t.Fatalf("scan repo: %v", err)
	}
	if got := sortedDependencyUnion(scan.DeclaredDependencies); !slices.Equal(got, []string{"httparty", "nokogiri", "rack", "rspec"}) {
		t.Fatalf("unexpected declared dependency set: %#v", got)
	}

	reportData, err := NewAdapter().Analyse(context.Background(), language.Request{RepoPath: repo, Dependency: "httparty"})
	if err != nil {
		t.Fatalf("analyse dependency: %v", err)
	}
	if len(reportData.Dependencies) != 1 || reportData.Dependencies[0].TotalExportsCount == 0 {
		t.Fatalf("expected used gemspec dependency report, got %#v", reportData.Dependencies)
	}
}

func TestRubyAdapterWarnsOnUnparseableGemspecDependency(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, demoGemspecFile), strings.Join([]string{
		"Gem::Specification.new do |spec|",
		"  spec.add_dependency SOME_CONST",
		"end",
		"",
	}, "\n"))

	scan, err := scanRepo(context.Background(), repo)
	if err != nil {
		t.Fatalf("scan repo: %v", err)
	}
	joinedWarnings := strings.Join(scan.Warnings, "\n")
	if !strings.Contains(joinedWarnings, "could not confidently parse gemspec dependency declaration in "+demoGemspecFile+":2") {
		t.Fatalf("expected gemspec parse warning, got %#v", scan.Warnings)
	}
}

func TestRubyAdditionalBranches(t *testing.T) {
	t.Run("detect and root signals return path errors", testRubyDetectAndRootSignalsReturnPathErrors)
	t.Run("analyse empty repo path fails when cwd is gone", testRubyAnalyseEmptyRepoPathFailsWhenCWDIsGone)
	t.Run("scan and walk helper branches", testRubyScanAndWalkHelperBranches)
	t.Run("detect stops after max files", testRubyDetectStopsAfterMaxFiles)
	t.Run("require parsing and risk recommendations", testRubyRequireParsingAndRiskRecommendations)
}

func testRubyDetectAndRootSignalsReturnPathErrors(t *testing.T) {
	repoFile := filepath.Join(t.TempDir(), "repo-file")
	if err := os.WriteFile(repoFile, []byte("x"), 0o600); err != nil {
		t.Fatalf("write repo file: %v", err)
	}
	if _, err := NewAdapter().Detect(context.Background(), repoFile); err == nil {
		t.Fatalf("expected detect to fail for non-directory repo path")
	}
	if _, err := NewAdapter().DetectWithConfidence(context.Background(), repoFile); err == nil {
		t.Fatalf("expected detect-with-confidence to fail for non-directory repo path")
	}
}

func testRubyAnalyseEmptyRepoPathFailsWhenCWDIsGone(t *testing.T) {
	testutil.ChdirRemovedDir(t)

	if _, err := NewAdapter().Analyse(context.Background(), language.Request{}); err == nil {
		t.Fatalf("expected analyse to fail when cwd cannot be resolved")
	}
}

func testRubyScanAndWalkHelperBranches(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, gemfileName), "source 'https://rubygems.org'\ngem 'httparty'\n")

	unreadableRuby := filepath.Join(repo, rubyAppFile)
	if err := os.WriteFile(unreadableRuby, []byte("require 'httparty'\n"), 0o000); err != nil {
		t.Fatalf("write unreadable ruby file: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(unreadableRuby, 0o600); err != nil {
			t.Fatalf("restore unreadable ruby perms: %v", err)
		}
	})
	if _, err := scanRepo(context.Background(), repo); err == nil {
		t.Fatalf("expected unreadable ruby file to fail scan")
	}

	skipDir := filepath.Join(repo, ".bundle")
	if err := os.MkdirAll(skipDir, 0o755); err != nil {
		t.Fatalf("mkdir skip dir: %v", err)
	}
	visitedSkipDir := false
	if err := walkRubyRepoFiles(context.Background(), repo, func(path string, entry os.DirEntry) error {
		if strings.Contains(path, ".bundle") {
			visitedSkipDir = true
		}
		return nil
	}); err != nil {
		t.Fatalf("walk ruby repo files: %v", err)
	}
	if visitedSkipDir {
		t.Fatalf("expected .bundle directory to be skipped")
	}
}

func testRubyDetectStopsAfterMaxFiles(t *testing.T) {
	repo := t.TempDir()
	for i := 0; i <= maxDetectFiles; i++ {
		testutil.MustWriteFile(t, filepath.Join(repo, "lib", "f"+strconv.Itoa(i)+".rb"), "puts 'x'\n")
	}

	detection, err := NewAdapter().DetectWithConfidence(context.Background(), repo)
	if err != nil {
		t.Fatalf("detect large repo: %v", err)
	}
	if !detection.Matched || detection.Confidence == 0 {
		t.Fatalf("expected ruby detection in large repo, got %#v", detection)
	}
}

func testRubyRequireParsingAndRiskRecommendations(t *testing.T) {
	declared := map[string]struct{}{"foo": {}}
	imports := parseRequires([]byte("require 'foo/bar'\n"), rubyAppFile, declared)
	if len(imports) != 1 || imports[0].Name != "bar" || imports[0].Local != "bar" {
		t.Fatalf("expected nested require to use trailing module segment, got %#v", imports)
	}

	dep, warnings := buildDependencyReport("foo", scanResult{
		Files: []fileScan{{
			Imports: []importBinding{{
				Dependency: "foo",
				Module:     "foo",
				Name:       "*",
				Local:      "*",
				Wildcard:   true,
			}},
			Usage: map[string]int{"*": 1},
		}},
		DeclaredDependencies: map[string]struct{}{"foo": {}},
		ImportedDependencies: map[string]struct{}{"foo": {}},
	})
	if len(warnings) != 0 || len(dep.RiskCues) == 0 || len(dep.Recommendations) == 0 {
		t.Fatalf("expected runtime-require risk cues and recommendations, got dep=%#v warnings=%#v", dep, warnings)
	}
	if got := resolveRemovalCandidateWeights(nil); got != report.DefaultRemovalCandidateWeights() {
		t.Fatalf("expected default removal weights, got %#v", got)
	}
}
