package ruby

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

	if warnings, coverageGaps, err := loadDeclaredDependencies(context.Background(), repo, map[string]struct{}{}, map[string]rubyDependencySource{}); err == nil || len(warnings) != 0 || len(coverageGaps) != 0 {
		t.Fatalf("expected loadDeclaredDependencies error for Gemfile directory, warnings=%#v coverageGaps=%#v err=%v", warnings, coverageGaps, err)
	}
}

func testRubyLoadGemspecDependenciesReturnsReadError(t *testing.T) {
	t.Helper()

	repo := t.TempDir()
	broken := filepath.Join(repo, "broken.gemspec")
	if err := os.Symlink(filepath.Join(repo, "missing-target"), broken); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if warnings, coverageGaps, err := loadGemspecDependencies(context.Background(), repo, map[string]struct{}{}); err == nil || len(warnings) != 0 || len(coverageGaps) != 0 {
		t.Fatalf("expected loadGemspecDependencies read error, warnings=%#v coverageGaps=%#v err=%v", warnings, coverageGaps, err)
	}
}

func TestRubyLoadGemspecDependenciesSkipsOversizedGemspec(t *testing.T) {
	for _, filename := range []string{"oversized.gemspec", "oversized.GEMSPEC", "oversized.GeMsPeC", "oversized.gem\u017fpec"} {
		t.Run(filename, func(t *testing.T) {
			repo := t.TempDir()
			testutil.MustWritePaddedFile(t, filepath.Join(repo, filename), "spec.add_dependency 'oversized'\n", gemspecRegressionLimitBytes+1)

			out := map[string]struct{}{}
			warnings, coverageGaps, err := loadGemspecDependencies(context.Background(), repo, out)
			if err != nil {
				t.Fatalf("loadGemspecDependencies: %v", err)
			}
			if _, ok := out["oversized"]; ok {
				t.Fatalf("expected oversized gemspec dependency to be skipped, got %#v", out)
			}
			if len(coverageGaps) != 1 || coverageGaps[0].Code != report.CoverageGapRubyOversizedGemspec || coverageGaps[0].Path != filename {
				t.Fatalf("expected typed oversized gemspec coverage gap, got %#v", coverageGaps)
			}
			joinedWarnings := strings.Join(warnings, "\n")
			if !strings.Contains(joinedWarnings, "skipped "+filename) || !strings.Contains(joinedWarnings, "exceeds") {
				t.Fatalf("expected oversized gemspec warning, got %#v", warnings)
			}
		})
	}
}

func TestRubyLoadGemspecDependenciesParsesExactLimitOneLineGemspec(t *testing.T) {
	t.Helper()

	repo := t.TempDir()
	testutil.MustWritePaddedFile(t, filepath.Join(repo, "exact.gemspec"), "spec.add_dependency 'exact_limit'", gemspecRegressionLimitBytes)

	out := map[string]struct{}{}
	warnings, coverageGaps, err := loadGemspecDependencies(context.Background(), repo, out)
	if err != nil {
		t.Fatalf("loadGemspecDependencies: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings for exact-limit gemspec, got %#v", warnings)
	}
	if len(coverageGaps) != 0 {
		t.Fatalf("expected no coverage gaps for exact-limit gemspec, got %#v", coverageGaps)
	}
	if _, ok := out["exact-limit"]; !ok {
		t.Fatalf("expected exact-limit dependency from exact-limit gemspec, got %#v", out)
	}
}

func TestRubyLoadGemspecDependenciesRespectsContextCancellation(t *testing.T) {
	t.Helper()

	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "demo.gemspec"), "Gem::Specification.new do |spec|\n  spec.add_dependency 'httparty'\nend\n")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if warnings, coverageGaps, err := loadGemspecDependencies(ctx, repo, map[string]struct{}{}); !errors.Is(err, context.Canceled) || len(warnings) != 0 || len(coverageGaps) != 0 {
		t.Fatalf("expected canceled context from loadGemspecDependencies, warnings=%#v coverageGaps=%#v err=%v", warnings, coverageGaps, err)
	}
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
