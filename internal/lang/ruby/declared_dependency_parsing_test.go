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

const gemspecRegressionLimitBytes int64 = 1 * 1024 * 1024

func TestRubyDeclaredDependencyAdditionalBranches(t *testing.T) {
	t.Run("load declared dependencies returns bundler error", func(t *testing.T) {
		testRubyLoadDeclaredDependenciesReturnsBundlerError(t)
	})

	t.Run("load gemspec dependencies returns read error", func(t *testing.T) {
		testRubyLoadGemspecDependenciesReturnsReadError(t)
	})

	t.Run("add ruby dependency tracks declaration signals without source kind", func(t *testing.T) {
		testRubyAddRubyDependencyTracksDeclarationSignals(t)
	})
}

func testRubyLoadDeclaredDependenciesReturnsBundlerError(t *testing.T) {
	t.Helper()

	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, gemfileName), 0o755); err != nil {
		t.Fatalf("mkdir Gemfile dir: %v", err)
	}

	if _, err := NewAdapter().Analyse(context.Background(), language.Request{RepoPath: repo}); err == nil {
		t.Fatal("expected Analyse error for Gemfile directory")
	}
}

func testRubyLoadGemspecDependenciesReturnsReadError(t *testing.T) {
	t.Helper()

	repo := t.TempDir()
	broken := filepath.Join(repo, "broken.gemspec")
	if err := os.Symlink(filepath.Join(repo, "missing-target"), broken); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if _, err := NewAdapter().Analyse(context.Background(), language.Request{RepoPath: repo}); err == nil {
		t.Fatal("expected Analyse read error for broken gemspec")
	}
}

func TestRubyLoadGemspecDependenciesSkipsOversizedGemspec(t *testing.T) {
	for _, filename := range []string{"oversized.gemspec", "oversized.GEMSPEC", "oversized.GeMsPeC", "oversized.gem\u017fpec"} {
		t.Run(filename, func(t *testing.T) {
			repo := t.TempDir()
			testutil.MustWritePaddedFile(t, filepath.Join(repo, filename), "spec.add_dependency 'oversized'\n", gemspecRegressionLimitBytes+1)

			result, err := NewAdapter().Analyse(context.Background(), language.Request{RepoPath: repo})
			if err != nil {
				t.Fatalf("Analyse: %v", err)
			}
			for _, dependency := range result.Dependencies {
				if dependency.Name == "oversized" {
					t.Fatalf("expected oversized gemspec dependency to be skipped, got %#v", result.Dependencies)
				}
			}
			joinedWarnings := strings.Join(result.Warnings, "\n")
			if !strings.Contains(joinedWarnings, "skipped "+filename) || !strings.Contains(joinedWarnings, "exceeds") {
				t.Fatalf("expected oversized gemspec warning, got %#v", result.Warnings)
			}
		})
	}
}

func TestRubyLoadGemspecDependenciesParsesExactLimitOneLineGemspec(t *testing.T) {
	t.Helper()

	repo := t.TempDir()
	testutil.MustWritePaddedFile(t, filepath.Join(repo, "exact.gemspec"), "spec.add_dependency 'exact_limit'", gemspecRegressionLimitBytes)
	testutil.MustWriteFile(t, filepath.Join(repo, rubyAppFile), "require 'exact_limit'\n")

	result, err := NewAdapter().Analyse(context.Background(), language.Request{RepoPath: repo, Dependency: "exact-limit"})
	if err != nil {
		t.Fatalf("Analyse: %v", err)
	}
	if len(result.Warnings) != 0 {
		t.Fatalf("expected no warnings for exact-limit gemspec, got %#v", result.Warnings)
	}
	if !rubyReportHasDependency(result, "exact-limit") {
		t.Fatalf("expected exact-limit dependency from exact-limit gemspec, got %#v", result.Dependencies)
	}
}

func TestRubyLoadGemspecDependenciesRespectsContextCancellation(t *testing.T) {
	t.Helper()

	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "demo.gemspec"), "Gem::Specification.new do |spec|\n  spec.add_dependency 'httparty'\nend\n")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := NewAdapter().Analyse(ctx, language.Request{RepoPath: repo}); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled context from Analyse, got %v", err)
	}
}

func rubyReportHasDependency(result report.Result, name string) bool {
	for _, dependency := range result.Dependencies {
		if dependency.Name == name {
			return true
		}
	}
	return false
}

func testRubyAddRubyDependencyTracksDeclarationSignals(t *testing.T) {
	t.Helper()

	sources := map[string]rubyDependencySource{}

	addRubyDependency(nil, sources, "rack", "unknown", gemfileName)
	info := sources["rack"]
	if !info.DeclaredGemfile || info.Rubygems || info.Git || info.Path {
		t.Fatalf("unexpected gemfile-only source tracking: %#v", info)
	}

	addRubyDependency(nil, sources, "rack", "unknown", gemfileLockName)
	info = sources["rack"]
	if !info.DeclaredLock {
		t.Fatalf("expected gemfile lock declaration tracking, got %#v", info)
	}

	addRubyDependency(nil, sources, "", rubyDependencySourceRubygems, gemfileName)
	if len(sources) != 1 {
		t.Fatalf("expected empty dependency to be ignored, got %#v", sources)
	}
}

func TestRubyParseGemfileDependencyLineBlankDependency(t *testing.T) {
	if dependency, kind, ok := parseGemfileDependencyLine(`gem ''`); ok || dependency != "" || kind != "" {
		t.Fatalf("expected blank gem declaration to be ignored, got (%q, %q, %t)", dependency, kind, ok)
	}
}
