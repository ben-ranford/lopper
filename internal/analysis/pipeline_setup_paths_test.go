package analysis

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestAnalysisPipelineAdditionalSetupBranches(t *testing.T) {
	service := &Service{Registry: language.NewRegistry()}
	invalidPattern := string([]byte{0xff})

	if _, err := service.newAnalysisPipeline(context.Background(), Request{
		RepoPath:        ".",
		IncludePatterns: []string{invalidPattern},
	}); err == nil {
		t.Fatalf("expected newAnalysisPipeline to surface applyPathScope failures")
	}
}

func TestNewAnalysisPipelineExcludesRepositoryCacheFromScopedWorkspace(t *testing.T) {
	for _, tc := range []struct {
		name      string
		cachePath string
		include   []string
		exclude   []string
		cacheDir  string
	}{
		{name: "default cache with broad include", include: []string{"**"}, cacheDir: cacheDirName},
		{name: "configured relative cache with exclude-only scope", cachePath: "  .cache/lopper  ", exclude: []string{"ignored/**"}, cacheDir: filepath.Join(".cache", "lopper")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			writeFile(t, filepath.Join(repo, "src", "index.js"), "export const value = 1\n")
			writeFile(t, filepath.Join(repo, tc.cacheDir, "objects", "seed.js"), "export const cached = true\n")

			service, _ := newCacheTestService(t)
			pipeline, err := service.newAnalysisPipeline(context.Background(), Request{
				RepoPath:        filepath.Join(repo, "."),
				Language:        "cachelang",
				IncludePatterns: tc.include,
				ExcludePatterns: tc.exclude,
				Cache:           &CacheOptions{Enabled: true, Path: tc.cachePath},
			})
			if err != nil {
				t.Fatalf("create scoped pipeline: %v", err)
			}
			defer pipeline.cleanup()

			if got, want := pipeline.cache.options.Path, filepath.Join(repo, tc.cacheDir); got != want {
				t.Fatalf("cache root = %q, want %q", got, want)
			}
			if _, err := os.Stat(filepath.Join(pipeline.analysisRepoPath, tc.cacheDir)); !os.IsNotExist(err) {
				t.Fatalf("expected scoped workspace to omit configured cache root, stat err=%v", err)
			}
		})
	}
}

func TestNewAnalysisPipelineIncludesConfiguredCachePathWhenCachingIsDisabled(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "src", "index.js"), "export const value = 1\n")
	writeFile(t, filepath.Join(repo, "src", "generated", "cached.js"), "export const cached = true\n")

	service, _ := newCacheTestService(t)
	pipeline, err := service.newAnalysisPipeline(context.Background(), Request{
		RepoPath:        repo,
		Language:        "cachelang",
		IncludePatterns: []string{"**"},
		Cache:           &CacheOptions{Enabled: false, Path: filepath.Join("src", "generated")},
	})
	if err != nil {
		t.Fatalf("create scoped pipeline: %v", err)
	}
	defer pipeline.cleanup()

	if _, err := os.Stat(filepath.Join(pipeline.analysisRepoPath, "src", "generated", "cached.js")); err != nil {
		t.Fatalf("expected configured cache path to remain in scoped workspace when caching is disabled: %v", err)
	}
}

func TestNewAnalysisPipelineStopsBeforeScopedCopyWhenCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	service := &Service{Registry: language.NewRegistry()}
	if _, err := service.newAnalysisPipeline(ctx, Request{
		RepoPath:        t.TempDir(),
		IncludePatterns: []string{"**/*.js"},
	}); err == nil {
		t.Fatal("expected cancelled scoped pipeline setup to fail")
	}
}

func TestScopedCandidateRootsChangedPackagesSuccessBranch(t *testing.T) {
	repoRoot := t.TempDir()
	rootA := filepath.Join(repoRoot, "packages", "a")
	rootB := filepath.Join(repoRoot, "packages", "b")
	writeFile(t, filepath.Join(rootA, "a.txt"), "a1\n")
	writeFile(t, filepath.Join(rootB, "b.txt"), "b1\n")

	testutil.RunGit(t, repoRoot, "init", "-b", "main")
	testutil.RunGit(t, repoRoot, "config", "user.email", "codex@example.com")
	testutil.RunGit(t, repoRoot, "config", "user.name", "Codex")
	testutil.RunGit(t, repoRoot, "add", ".")
	testutil.RunGit(t, repoRoot, "commit", "-m", "base")

	writeFile(t, filepath.Join(rootA, "a.txt"), "a2\n")
	testutil.RunGit(t, repoRoot, "add", ".")
	testutil.RunGit(t, repoRoot, "commit", "-m", "change package a")
	writeFile(t, filepath.Join(rootB, "b-dirty.txt"), "dirty\n")

	roots, warnings := scopedCandidateRoots(ScopeModeChangedPackages, []string{rootA, rootB}, repoRoot)
	if len(warnings) != 0 {
		t.Fatalf("expected changed-packages resolution without warnings, got %#v", warnings)
	}
	if len(roots) != 2 || roots[0] != rootA || roots[1] != rootB {
		t.Fatalf("expected changed-packages scope to include changed and dirty package roots, got %#v", roots)
	}
}

func TestScopedCandidateRootsUsesExplicitChangedFilesWithoutWorkspaceFallback(t *testing.T) {
	repoRoot := t.TempDir()
	rootA := filepath.Join(repoRoot, "packages", "a")
	rootB := filepath.Join(repoRoot, "packages", "b")
	writeFile(t, filepath.Join(rootA, "a.txt"), "a1\n")
	writeFile(t, filepath.Join(rootB, "b.txt"), "b1\n")

	req := Request{
		ScopeMode:            ScopeModeChangedPackages,
		ChangedFiles:         []string{"packages/b/b.txt"},
		ChangedFilesExplicit: true,
	}
	roots, warnings := scopedCandidateRootsForRequest(req, []string{rootA, rootB}, repoRoot)
	if len(warnings) != 0 {
		t.Fatalf("expected explicit changed-packages roots without warnings, got %#v", warnings)
	}
	if len(roots) != 1 || roots[0] != rootB {
		t.Fatalf("expected explicit changed files to select package b only, got %#v", roots)
	}
}
