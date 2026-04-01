package ruby

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRubyDeclaredDependencyAdditionalBranches(t *testing.T) {
	t.Run("load declared dependencies returns bundler error", func(t *testing.T) {
		repo := t.TempDir()
		if err := os.MkdirAll(filepath.Join(repo, gemfileName), 0o755); err != nil {
			t.Fatalf("mkdir Gemfile dir: %v", err)
		}

		if warnings, err := loadDeclaredDependencies(repo, map[string]struct{}{}, map[string]rubyDependencySource{}); err == nil || len(warnings) != 0 {
			t.Fatalf("expected loadDeclaredDependencies error for Gemfile directory, warnings=%#v err=%v", warnings, err)
		}
	})

	t.Run("load gemspec dependencies returns read error", func(t *testing.T) {
		repo := t.TempDir()
		broken := filepath.Join(repo, "broken.gemspec")
		if err := os.Symlink(filepath.Join(repo, "missing-target"), broken); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}

		if warnings, err := loadGemspecDependencies(repo, map[string]struct{}{}); err == nil || len(warnings) != 0 {
			t.Fatalf("expected loadGemspecDependencies read error, warnings=%#v err=%v", warnings, err)
		}
	})

	t.Run("add ruby dependency tracks declaration signals without source kind", func(t *testing.T) {
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
	})
}

func TestRubyParseGemfileDependencyLineBlankDependency(t *testing.T) {
	if dependency, kind, ok := parseGemfileDependencyLine(`gem ''`); ok || dependency != "" || kind != "" {
		t.Fatalf("expected blank gem declaration to be ignored, got (%q, %q, %t)", dependency, kind, ok)
	}
}
