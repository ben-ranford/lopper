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

func TestRubyAdapterDetectBundlerProject(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, gemfileName), "source 'https://rubygems.org'\ngem 'httparty'\n")
	testutil.MustWriteFile(t, filepath.Join(repo, "main.rb"), "require 'httparty'\n")

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
	testutil.MustWriteFile(t, filepath.Join(repo, "app.rb"), "require 'httparty'\nHTTParty.get('https://example.test')\n")

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

func TestRubyAdditionalBranches(t *testing.T) {
	t.Run("detect and root signals return path errors", func(t *testing.T) {
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
	})

	t.Run("analyse empty repo path fails when cwd is gone", func(t *testing.T) {
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

	t.Run("scan and walk helper branches", func(t *testing.T) {
		repo := t.TempDir()
		testutil.MustWriteFile(t, filepath.Join(repo, gemfileName), "source 'https://rubygems.org'\ngem 'httparty'\n")

		unreadableRuby := filepath.Join(repo, "app.rb")
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
	})

	t.Run("detect stops after max files", func(t *testing.T) {
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
	})

	t.Run("require parsing and risk recommendations", func(t *testing.T) {
		declared := map[string]struct{}{"foo": {}}
		imports := parseRequires([]byte("require 'foo/bar'\n"), "app.rb", declared)
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
	})
}
