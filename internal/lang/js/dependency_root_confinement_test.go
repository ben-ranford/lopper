package js

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestLaterReadersRejectParentSwapAfterDependencyRootResolution(t *testing.T) {
	repo, resolvedRoot := installResolvedDependencyRoot(t)
	swapDependencyParentToSymlink(t, repo)

	t.Run("export package json reader", func(t *testing.T) {
		_, warnings, err := loadPackageJSONForSurface(resolvedRoot, resolvedRoot)
		if err == nil {
			t.Fatal("expected swapped parent symlink to break package.json read")
		}
		if len(warnings) != 1 || !strings.Contains(warnings[0], "unable to read") {
			t.Fatalf("expected stable read warning after parent swap, got %#v", warnings)
		}
	})

	t.Run("entrypoint resolver", func(t *testing.T) {
		if path, ok := resolveEntrypointUnderRoot(resolvedRoot, resolvedRoot, "index.js"); ok || path != "" {
			t.Fatalf("expected swapped parent symlink to break entrypoint resolution, got path=%q ok=%v", path, ok)
		}
	})

	t.Run("codemod subpath resolver", func(t *testing.T) {
		if hasResolvableSubpathFile(resolvedRoot, "map") {
			t.Fatal("expected swapped parent symlink to break subpath file resolution")
		}
	})

	t.Run("license reader", func(t *testing.T) {
		license, provenance, warnings := detectLicenseAndProvenance(resolvedRoot, false)
		if license == nil || !license.Unknown || license.Source != "unknown" {
			t.Fatalf("expected unknown license after parent swap, got %#v", license)
		}
		if provenance == nil || provenance.Source != "unknown" {
			t.Fatalf("expected unknown provenance after parent swap, got %#v", provenance)
		}
		if len(warnings) != 1 || !strings.Contains(warnings[0], dependencyRootOpaqueLayoutWarning) {
			t.Fatalf("expected stable root-resolution warning after parent swap, got %#v", warnings)
		}
	})
}

func installResolvedDependencyRoot(t *testing.T) (string, string) {
	t.Helper()

	repo := t.TempDir()
	depRoot := filepath.Join(repo, "node_modules", "pkg")
	if err := os.MkdirAll(depRoot, 0o755); err != nil {
		t.Fatalf("mkdir dependency root: %v", err)
	}
	testutil.MustWriteFile(t, filepath.Join(depRoot, "package.json"), `{"name":"pkg","exports":{"./map":"./map.js"}}`)
	testutil.MustWriteFile(t, filepath.Join(depRoot, "index.js"), "export const root = 1\n")
	testutil.MustWriteFile(t, filepath.Join(depRoot, "map.js"), "export default 1\n")
	testutil.MustWriteFile(t, filepath.Join(depRoot, "LICENSE"), "MIT License\nPermission is hereby granted...\n")

	resolvedRoot, ok := resolveDependencyRootAtDir(repo, "pkg")
	if !ok || resolvedRoot != depRoot {
		t.Fatalf("expected dependency root to resolve before parent swap, got root=%q ok=%v", resolvedRoot, ok)
	}
	return repo, resolvedRoot
}

func swapDependencyParentToSymlink(t *testing.T, repo string) {
	t.Helper()

	originalNodeModules := filepath.Join(repo, "node_modules")
	backupNodeModules := filepath.Join(repo, "node_modules.real")
	if err := os.Rename(originalNodeModules, backupNodeModules); err != nil {
		t.Fatalf("move node_modules aside: %v", err)
	}

	outsideNodeModules := t.TempDir()
	outsideDepRoot := filepath.Join(outsideNodeModules, "pkg")
	if err := os.MkdirAll(outsideDepRoot, 0o755); err != nil {
		t.Fatalf("mkdir outside dependency root: %v", err)
	}
	testutil.MustWriteFile(t, filepath.Join(outsideDepRoot, "package.json"), `{"name":"outside","license":"GPL-3.0-only","exports":{"./map":"./map.js"}}`)
	testutil.MustWriteFile(t, filepath.Join(outsideDepRoot, "index.js"), "export const escaped = 1\n")
	testutil.MustWriteFile(t, filepath.Join(outsideDepRoot, "map.js"), "export default 2\n")
	testutil.MustWriteFile(t, filepath.Join(outsideDepRoot, "LICENSE"), "GNU GENERAL PUBLIC LICENSE\n")

	if err := os.Symlink(outsideNodeModules, originalNodeModules); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
}
