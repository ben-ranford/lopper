package ruby

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ben-ranford/lopper/internal/testutil"
)

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

	if warnings, err := loadDeclaredDependencies(context.Background(), repo, map[string]struct{}{}, map[string]rubyDependencySource{}); err == nil || len(warnings) != 0 {
		t.Fatalf("expected loadDeclaredDependencies error for Gemfile directory, warnings=%#v err=%v", warnings, err)
	}
}

func testRubyLoadGemspecDependenciesReturnsReadError(t *testing.T) {
	t.Helper()

	repo := t.TempDir()
	broken := filepath.Join(repo, "broken.gemspec")
	if err := os.Symlink(filepath.Join(repo, "missing-target"), broken); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if warnings, err := loadGemspecDependencies(context.Background(), repo, map[string]struct{}{}); err == nil || len(warnings) != 0 {
		t.Fatalf("expected loadGemspecDependencies read error, warnings=%#v err=%v", warnings, err)
	}
}

func TestRubyLoadGemspecDependenciesRespectsContextCancellation(t *testing.T) {
	t.Helper()

	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "demo.gemspec"), "Gem::Specification.new do |spec|\n  spec.add_dependency 'httparty'\nend\n")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if warnings, err := loadGemspecDependencies(ctx, repo, map[string]struct{}{}); !errors.Is(err, context.Canceled) || len(warnings) != 0 {
		t.Fatalf("expected canceled context from loadGemspecDependencies, warnings=%#v err=%v", warnings, err)
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
