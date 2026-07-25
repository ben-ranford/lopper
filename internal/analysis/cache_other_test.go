//go:build !windows

package analysis

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveCacheStorageRootAcceptsUnixExplicitPathsThatLookWindowsLike(t *testing.T) {
	repo := t.TempDir()
	canonicalRepo, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatalf("resolve canonical repo: %v", err)
	}

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() {
		if chdirErr := os.Chdir(cwd); chdirErr != nil {
			t.Fatalf("restore cwd: %v", chdirErr)
		}
	})

	workDir := t.TempDir()
	if err := os.Chdir(workDir); err != nil {
		t.Fatalf("chdir workdir: %v", err)
	}

	for _, rawPath := range []string{`C:cache`, `\cache`, `\\server`, `C:\cache `, `C:\cache.`, `\\server\share\dir `, `\\server\share\dir.`} {
		options := resolvedCacheOptions{
			Path:         rawPath,
			ExplicitPath: true,
			ReadOnly:     true,
		}
		resolved, err := resolveCacheStorageRoot(options, repo, canonicalRepo)
		if err != nil {
			t.Fatalf("resolveCacheStorageRoot(%q): %v", rawPath, err)
		}
		want, err := filepath.Abs(rawPath)
		if err != nil {
			t.Fatalf("Abs(%q): %v", rawPath, err)
		}
		if resolved != want {
			t.Fatalf("resolveCacheStorageRoot(%q) = %q, want %q", rawPath, resolved, want)
		}
	}
}
