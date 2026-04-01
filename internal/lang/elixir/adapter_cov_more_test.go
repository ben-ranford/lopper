package elixir

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestElixirAdditionalZeroHitBranches(t *testing.T) {
	t.Run("detect root files surfaces umbrella expansion errors", func(t *testing.T) {
		repo := t.TempDir()
		testutil.MustWriteFile(t, filepath.Join(repo, mixExsName), "defmodule Demo.MixProject do\n  use Mix.Project\n  def project, do: [apps_path: \"[\"]\nend\n")

		if _, err := detectFromRootFiles(repo, &language.Detection{}, map[string]struct{}{}); err == nil {
			t.Fatalf("expected invalid umbrella apps_path glob to fail detection")
		}
	})

	t.Run("detect root files surfaces mix.lock stat errors", func(t *testing.T) {
		repo := t.TempDir()
		testutil.MustWriteFile(t, filepath.Join(repo, mixExsName), "defmodule Demo.MixProject do\n  use Mix.Project\nend\n")
		lockPath := filepath.Join(repo, mixLockName)
		if err := os.Symlink(mixLockName, lockPath); err != nil {
			t.Fatalf("create looping mix.lock symlink: %v", err)
		}

		if _, err := detectFromRootFiles(repo, &language.Detection{}, map[string]struct{}{}); err == nil {
			t.Fatalf("expected mix.lock stat error to fail detection")
		}
	})

	t.Run("analyse rejects invalid repo path", func(t *testing.T) {
		if _, err := NewAdapter().Analyse(context.Background(), language.Request{RepoPath: "\x00"}); err == nil {
			t.Fatalf("expected invalid repo path to fail analysis")
		}
	})

	t.Run("analyse returns scan errors for escaping source symlinks", func(t *testing.T) {
		repo := t.TempDir()
		outsideDir := t.TempDir()
		testutil.MustWriteFile(t, filepath.Join(repo, mixExsName), "defmodule Demo.MixProject do\n  use Mix.Project\nend\n")
		testutil.MustWriteFile(t, filepath.Join(outsideDir, "outside.ex"), "alias Foo.Bar\n")
		linkPath := filepath.Join(repo, "lib", "demo.ex")
		if err := os.MkdirAll(filepath.Dir(linkPath), 0o755); err != nil {
			t.Fatalf("mkdir lib dir: %v", err)
		}
		if err := os.Symlink(filepath.Join(outsideDir, "outside.ex"), linkPath); err != nil {
			t.Fatalf("create source symlink: %v", err)
		}

		if _, err := NewAdapter().Analyse(context.Background(), language.Request{RepoPath: repo, TopN: 1}); err == nil {
			t.Fatalf("expected escaping source symlink to fail analysis")
		}
	})
}
