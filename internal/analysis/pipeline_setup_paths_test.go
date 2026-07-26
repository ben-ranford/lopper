package analysis

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
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
	if _, err := service.newAnalysisPipeline(context.Background(), Request{
		RepoPath: filepath.Join(t.TempDir(), "missing"),
	}); err == nil {
		t.Fatal("expected newAnalysisPipeline to reject a missing repository")
	}
	for _, tc := range []struct {
		name string
		req  Request
	}{
		{
			name: "unsafe relative config path",
			req: Request{
				RepoPath:   t.TempDir(),
				ConfigPath: filepath.Join("..", "outside.yml"),
				Cache:      &CacheOptions{Enabled: false},
				Repository: mustResolveTrustedRepositoryForPipelineTest(t, t.TempDir()),
			},
		},
		{
			name: "unsafe explicit runtime trace path",
			req: Request{
				RepoPath:                 t.TempDir(),
				RuntimeTracePath:         filepath.Join("..", "runtime.ndjson"),
				RuntimeTracePathExplicit: true,
				Cache:                    &CacheOptions{Enabled: false},
				Repository:               mustResolveTrustedRepositoryForPipelineTest(t, t.TempDir()),
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := tc.req.RepoPath
			tc.req.Repository = mustResolveTrustedRepositoryForPipelineTest(t, repo)
			if _, err := service.newAnalysisPipeline(context.Background(), tc.req); err == nil || !strings.Contains(err.Error(), "repository root") {
				t.Fatalf("expected traversal rejection, got %v", err)
			}
		})
	}
}

func mustResolveTrustedRepositoryForPipelineTest(t *testing.T, repo string) *RepositoryAuthorization {
	t.Helper()
	repository, err := ResolveTrustedRepository(repo)
	if err != nil {
		t.Fatalf("authorize repository: %v", err)
	}
	return repository
}

func TestPipelineRepositoryAuthorizationGuardBranches(t *testing.T) {
	repoA := t.TempDir()
	repoB := t.TempDir()
	repositoryA, repositoryB, cacheA := mustPipelineRepoAuth(t, repoA, repoB)
	assertPipelineRepositoryResolution(t, repoA, repoB, repositoryA, repositoryB, cacheA)
	assertPipelineRepositoryViewResolution(t, repoA, repositoryA, repositoryB, cacheA)
	assertPipelineRemovedRepositoryViewFails(t)
	assertPipelineCacheOptionGuards(t, repoA, repoB, repositoryA, repositoryB, cacheA)
}

func TestNormalizePipelineCacheOptionBranches(t *testing.T) {
	repo := t.TempDir()
	repository, err := ResolveTrustedRepository(repo)
	if err != nil {
		t.Fatalf("authorize repository: %v", err)
	}

	defaultOptions, err := normalizePipelineCacheOptions(repo, Request{Repository: repository})
	if err != nil || !InRepoCacheOptions(defaultOptions) {
		t.Fatalf("normalize default cache options: options=%#v err=%v", defaultOptions, err)
	}
	disabled := &CacheOptions{Enabled: false, Path: filepath.Join(t.TempDir(), "unused")}
	if got, err := normalizePipelineCacheOptions(repo, Request{Repository: repository, Cache: disabled}); err != nil || got != disabled {
		t.Fatalf("normalize disabled cache options: got=%#v err=%v", got, err)
	}
	blankOptions, err := normalizePipelineCacheOptions(repo, Request{
		Repository: repository,
		Cache:      &CacheOptions{Enabled: true, ReadOnly: true},
	})
	if err != nil || !blankOptions.ReadOnly || !InRepoCacheOptions(blankOptions) {
		t.Fatalf("normalize blank-path cache options: options=%#v err=%v", blankOptions, err)
	}
	if got := pinnedScopedCacheRoot(&CacheOptions{Enabled: false}); got != "" {
		t.Fatalf("disabled scoped cache root = %q, want empty", got)
	}
	rootCache, err := ResolveTrustedCacheOptionsForRepository(repository, &CacheOptions{
		Enabled: true,
		Path:    repo,
	})
	if err != nil {
		t.Fatalf("resolve repository-root cache: %v", err)
	}
	if got := pinnedScopedCacheRoot(rootCache); got != "" {
		t.Fatalf("repository-root scoped cache = %q, want empty", got)
	}
}

func TestNormalizePipelineCacheOptionsRejectsScopedRepoRootCachePath(t *testing.T) {
	repoRoot := t.TempDir()
	req := Request{
		RepoPath:        repoRoot,
		IncludePatterns: []string{"src/**"},
		Cache: &CacheOptions{
			Enabled: true,
			Path:    repoRoot,
		},
	}
	if !requestUsesScopedWorkspace(req) {
		t.Fatalf("expected scoped workspace request for %#v", req.IncludePatterns)
	}
	cacheOptions, err := resolvePipelineCacheOptions(repoRoot, req)
	if err != nil {
		t.Fatalf("resolve pipeline cache options: %v", err)
	}
	if !scopedCacheTargetsRepositoryRoot(cacheOptions) {
		t.Fatalf("expected repo-root cache detection, repo=%q cache=%#v", repoRoot, cacheOptions)
	}

	_, err = normalizePipelineCacheOptions(repoRoot, req)
	if err == nil || !strings.Contains(err.Error(), "scoped analysis does not allow cachePath at the repository root") {
		t.Fatalf("expected scoped repo-root cache rejection, got %v", err)
	}
}

func TestResolveTrustedCacheOptionsRejectsRelativeExternalTraversal(t *testing.T) {
	repoRoot := t.TempDir()
	outsideCache := filepath.Join(t.TempDir(), "outside-cache")
	options, err := ResolveTrustedCacheOptions(repoRoot, &CacheOptions{
		Enabled: true,
		Path:    filepath.Join("..", filepath.Base(outsideCache)),
	})
	if options != nil {
		t.Fatalf("expected relative external traversal rejection, got %#v", options)
	}
	if err == nil || !CachePathExternal(err) {
		t.Fatalf("expected authenticated external-path rejection, got %v", err)
	}
	if _, statErr := os.Stat(outsideCache); !os.IsNotExist(statErr) {
		t.Fatalf("expected no outside cache directory, stat err=%v", statErr)
	}
}

func TestPinnedScopedCacheRootUsesCanonicalRepoAndPinnedPaths(t *testing.T) {
	repoRoot := t.TempDir()
	aliasRoot := t.TempDir()
	repoAlias := filepath.Join(aliasRoot, "repo")
	if err := os.Symlink(repoRoot, repoAlias); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	cacheRoot := filepath.Join(repoRoot, ".cache", "lopper")
	if err := os.MkdirAll(cacheRoot, 0o755); err != nil {
		t.Fatalf("mkdir cache root: %v", err)
	}
	cacheRoot, err := filepath.EvalSymlinks(cacheRoot)
	if err != nil {
		t.Fatalf("resolve canonical cache root: %v", err)
	}
	cacheOptions, err := ResolveTrustedCacheOptions(repoAlias, &CacheOptions{
		Enabled: true,
		Path:    cacheRoot,
	})
	if err != nil {
		t.Fatalf("resolve trusted cache options: %v", err)
	}

	relativeRoot := pinnedScopedCacheRoot(cacheOptions)
	if relativeRoot != filepath.Join(".cache", "lopper") {
		t.Fatalf("expected canonical relative scoped cache root, got %q", relativeRoot)
	}
}

func TestServiceTrustedCachePinKeepsExecutionBoundAfterRequestedRepoParentAliasSwap(t *testing.T) {
	requestedRepo, canonicalRepo, redirectedRepo, requestedParent, cacheOptions, expectedCanonicalPath := setupTrustedCacheAliasSwapFixture(t)
	service, adapter := newRecordingPipelineService(t, "bound-repo")
	req := Request{RepoPath: requestedRepo, Language: adapter.id, Cache: cacheOptions}
	assertPipelineServiceCacheMiss(t, service, req, 0, "repo-a-original")
	writeFile(t, filepath.Join(canonicalRepo, "identity.js"), "repo-a-updated\n")
	if err := os.Remove(requestedParent); err != nil {
		t.Fatalf("remove requested parent alias: %v", err)
	}
	if err := os.Symlink(filepath.Dir(redirectedRepo), requestedParent); err != nil {
		t.Fatalf("retarget requested parent alias: %v", err)
	}
	assertPipelineServiceCacheMiss(t, service, req, 0, "repo-a-updated")
	if len(adapter.detectPaths) != 2 || adapter.detectPaths[0] == canonicalRepo || adapter.detectPaths[1] == canonicalRepo {
		t.Fatalf("expected both detections to use handle-derived snapshots, got %#v", adapter.detectPaths)
	}
	if len(adapter.analysePaths) != 2 || adapter.analysePaths[0] != adapter.detectPaths[0] || adapter.analysePaths[1] != adapter.detectPaths[1] {
		t.Fatalf("expected each adapter invocation to use its detected snapshot, detect=%#v analyse=%#v", adapter.detectPaths, adapter.analysePaths)
	}
	assertCacheDirsExist(t, expectedCanonicalPath)
	if _, statErr := os.Stat(filepath.Join(redirectedRepo, ".cache")); !os.IsNotExist(statErr) {
		t.Fatalf("expected redirected repo cache to remain absent, stat err=%v", statErr)
	}
}

func mustPipelineRepoAuth(t *testing.T, repoA, repoB string) (*RepositoryAuthorization, *RepositoryAuthorization, *CacheOptions) {
	t.Helper()
	repositoryA, err := ResolveTrustedRepository(repoA)
	if err != nil {
		t.Fatalf("authorize repo A: %v", err)
	}
	repositoryB, err := ResolveTrustedRepository(repoB)
	if err != nil {
		t.Fatalf("authorize repo B: %v", err)
	}
	cacheA, err := ResolveTrustedCacheOptionsForRepository(repositoryA, &CacheOptions{Enabled: true, Path: filepath.Join(".cache", "lopper")})
	if err != nil {
		t.Fatalf("resolve repo A cache: %v", err)
	}
	return repositoryA, repositoryB, cacheA
}

func assertPipelineRepositoryResolution(t *testing.T, repoA, repoB string, repositoryA, repositoryB *RepositoryAuthorization, cacheA *CacheOptions) {
	t.Helper()
	if got, err := resolvePipelineRepository(repoA, Request{Repository: repositoryA}); err != nil || got != repositoryA {
		t.Fatalf("resolve supplied repository authorization: got=%p err=%v", got, err)
	}
	if _, err := resolvePipelineRepository(repoB, Request{Repository: repositoryA}); err == nil {
		t.Fatal("expected supplied repository authorization mismatch")
	}
	if got, err := resolvePipelineRepository(repoA, Request{Cache: cacheA}); err != nil || got.authorizationState() != repositoryA.authorizationState() {
		t.Fatalf("resolve repository from cache pin: got=%p err=%v", got, err)
	}
	if _, err := resolvePipelineRepository(repoB, Request{Cache: cacheA}); err == nil {
		t.Fatal("expected cache-derived repository authorization mismatch")
	}
}

func assertPipelineRepositoryViewResolution(t *testing.T, repoA string, repositoryA, repositoryB *RepositoryAuthorization, cacheA *CacheOptions) {
	t.Helper()
	borrowedView := &RepositoryView{state: repositoryA.authorizationState()}
	if got, owns, err := resolvePipelineRepositoryView(context.Background(), repoA, Request{Repository: repositoryA, RepositoryView: borrowedView}); err != nil || owns || got != borrowedView {
		t.Fatalf("resolve borrowed repository view: got=%p owns=%t err=%v", got, owns, err)
	}
	if _, _, err := resolvePipelineRepositoryView(context.Background(), repoA, Request{Repository: repositoryB, RepositoryView: borrowedView}); err == nil {
		t.Fatal("expected borrowed repository view mismatch")
	}
	openedView, owns, err := resolvePipelineRepositoryView(context.Background(), repoA, Request{Repository: repositoryA, Cache: cacheA})
	if err != nil || !owns {
		t.Fatalf("open owned repository view: owns=%t err=%v", owns, err)
	}
	if err := openedView.Close(); err != nil {
		t.Fatalf("close owned repository view: %v", err)
	}
}

func assertPipelineRemovedRepositoryViewFails(t *testing.T) {
	t.Helper()
	removedRepo := filepath.Join(t.TempDir(), "removed-repo")
	if err := os.Mkdir(removedRepo, 0o750); err != nil {
		t.Fatalf("create removable repository: %v", err)
	}
	removedAuthorization, err := ResolveTrustedRepository(removedRepo)
	if err != nil {
		t.Fatalf("authorize removable repository: %v", err)
	}
	if err := os.Remove(removedRepo); err != nil {
		t.Fatalf("remove authorized repository: %v", err)
	}
	if _, _, err := resolvePipelineRepositoryView(context.Background(), removedRepo, Request{Repository: removedAuthorization}); err == nil {
		t.Fatal("expected removed authorized repository to fail opening")
	}
}

func assertPipelineCacheOptionGuards(t *testing.T, repoA, repoB string, repositoryA, repositoryB *RepositoryAuthorization, cacheA *CacheOptions) {
	t.Helper()
	if _, err := resolvePipelineCacheOptions(repoA, Request{Repository: repositoryB, Cache: cacheA}); err == nil {
		t.Fatal("expected cache/repository pin mismatch")
	}
	repositoryAClone, err := ResolveTrustedRepository(repoA)
	if err != nil {
		t.Fatalf("authorize repo A clone: %v", err)
	}
	if _, err := resolvePipelineCacheOptions(repoA, Request{Repository: repositoryAClone, Cache: cacheA}); err == nil {
		t.Fatal("expected independently minted repository authorization to reject repo A cache pin")
	}
	if _, err := resolvePipelineCacheOptions(repoB, Request{Repository: repositoryA, Cache: &CacheOptions{Enabled: true, Path: ".cache"}}); err == nil {
		t.Fatal("expected pipeline repository path mismatch")
	}
	if _, err := resolvePipelineCacheOptions(filepath.Join(repoA, "missing"), Request{Cache: &CacheOptions{Enabled: true, Path: ".cache"}}); err == nil {
		t.Fatal("expected pipeline cache resolution for missing repository to fail")
	}
	resolved, err := resolvePipelineCacheOptions(repoA, Request{Cache: &CacheOptions{Enabled: true, Path: ".cache"}})
	if err != nil || !InRepoCacheOptions(resolved) {
		t.Fatalf("resolve cache with pipeline-created authorization: options=%#v err=%v", resolved, err)
	}
}

func setupTrustedCacheAliasSwapFixture(t *testing.T) (string, string, string, string, *CacheOptions, string) {
	t.Helper()
	canonicalParent := t.TempDir()
	canonicalRepo := filepath.Join(canonicalParent, "repo")
	if err := os.MkdirAll(canonicalRepo, 0o755); err != nil {
		t.Fatalf("mkdir canonical repo: %v", err)
	}
	writeFile(t, filepath.Join(canonicalRepo, "identity.js"), "repo-a-original\n")
	redirectedParent := t.TempDir()
	redirectedRepo := filepath.Join(redirectedParent, "repo")
	if err := os.MkdirAll(redirectedRepo, 0o755); err != nil {
		t.Fatalf("mkdir redirected repo: %v", err)
	}
	writeFile(t, filepath.Join(redirectedRepo, "identity.js"), "repo-a-original\n")
	requestedParent := filepath.Join(t.TempDir(), "requested-parent")
	if err := os.Symlink(canonicalParent, requestedParent); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	requestedRepo := filepath.Join(requestedParent, "repo")
	canonicalRepoResolved, err := filepath.EvalSymlinks(canonicalRepo)
	if err != nil {
		t.Fatalf("resolve canonical repo: %v", err)
	}
	cacheOptions, err := ResolveTrustedCacheOptions(requestedRepo, &CacheOptions{Enabled: true, Path: filepath.Join(".cache", "lopper")})
	if err != nil {
		t.Fatalf("resolve trusted cache options: %v", err)
	}
	expectedCanonicalPath, err := resolvePathWithinExistingTree(filepath.Join(canonicalRepoResolved, ".cache", "lopper"))
	if err != nil {
		t.Fatalf("resolve expected canonical cache path: %v", err)
	}
	if cacheOptions.trustedPinnedPath() != expectedCanonicalPath {
		t.Fatalf("expected original canonical pin %q, got %q", expectedCanonicalPath, cacheOptions.trustedPinnedPath())
	}
	return requestedRepo, canonicalRepoResolved, redirectedRepo, requestedParent, cacheOptions, expectedCanonicalPath
}

func newRecordingPipelineService(t *testing.T, id string) (*Service, *recordingRepoAdapter) {
	t.Helper()
	adapter := &recordingRepoAdapter{id: id}
	registry := language.NewRegistry()
	if err := registry.Register(adapter); err != nil {
		t.Fatalf("register recording adapter: %v", err)
	}
	return &Service{Registry: registry}, adapter
}

func assertPipelineServiceCacheMiss(t *testing.T, service *Service, req Request, expectedHits int, expectedName string) {
	t.Helper()
	result, err := service.Analyse(context.Background(), req)
	if err != nil {
		t.Fatalf("analyse through requested repo alias: %v", err)
	}
	if result.Cache == nil || result.Cache.Hits != expectedHits || result.Cache.Misses != 1 || result.Cache.Writes != 1 {
		t.Fatalf("unexpected cache metadata: %#v", result.Cache)
	}
	if len(result.Dependencies) != 1 || result.Dependencies[0].Name != expectedName {
		t.Fatalf("unexpected adapter result: %#v", result.Dependencies)
	}
}

func TestServiceTrustedCachePinRejectsCanonicalRepositoryReplacement(t *testing.T) {
	parent := t.TempDir()
	repo := filepath.Join(parent, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	cacheOptions, err := ResolveTrustedCacheOptions(repo, &CacheOptions{
		Enabled: true,
		Path:    filepath.Join(".cache", "lopper"),
	})
	if err != nil {
		t.Fatalf("resolve trusted cache options: %v", err)
	}

	originalRepo := filepath.Join(parent, "repo-original")
	if err := os.Rename(repo, originalRepo); err != nil {
		t.Fatalf("move original repo: %v", err)
	}
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatalf("create replacement repo: %v", err)
	}

	adapter := &recordingRepoAdapter{id: "bound-repo"}
	registry := language.NewRegistry()
	if err := registry.Register(adapter); err != nil {
		t.Fatalf("register recording adapter: %v", err)
	}
	_, err = (&Service{Registry: registry}).Analyse(context.Background(), Request{
		RepoPath: repo,
		Language: adapter.id,
		Cache:    cacheOptions,
	})
	if err == nil || !strings.Contains(err.Error(), "trusted cache repository changed after validation") {
		t.Fatalf("expected canonical repository identity rejection, got %v", err)
	}
	if len(adapter.detectPaths) != 0 || len(adapter.analysePaths) != 0 {
		t.Fatalf("expected repository replacement rejection before adapter invocation, detect=%#v analyse=%#v", adapter.detectPaths, adapter.analysePaths)
	}
	if _, statErr := os.Stat(filepath.Join(repo, ".cache")); !os.IsNotExist(statErr) {
		t.Fatalf("expected replacement repo cache to remain absent, stat err=%v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(originalRepo, ".cache")); !os.IsNotExist(statErr) {
		t.Fatalf("expected original repo cache to remain absent, stat err=%v", statErr)
	}
}

func TestServiceRepositoryHandleRemainsBoundAcrossCanonicalReplacement(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("open-directory replacement semantics are not available on Windows")
	}
	repo, repoB, movedRepoA, cacheOptions := setupPipelineCanonicalReplacementFixture(t)
	previousHook := repositoryViewHandleOpenedFn
	repositoryViewHandleOpenedFn = func() error {
		if err := os.Rename(repo, movedRepoA); err != nil {
			return err
		}
		return os.Rename(repoB, repo)
	}
	t.Cleanup(func() {
		repositoryViewHandleOpenedFn = previousHook
	})
	service, adapter := newRecordingPipelineService(t, "bound-handle")
	request := Request{RepoPath: repo, Language: adapter.id, Cache: cacheOptions}
	assertPipelineServiceCacheMiss(t, service, request, 0, "repo-a")
	assertCacheDirsExist(t, filepath.Join(movedRepoA, ".cache", "lopper"))
	if _, statErr := os.Stat(filepath.Join(repo, ".cache")); !os.IsNotExist(statErr) {
		t.Fatalf("expected replacement repo B to receive no cache directories, stat err=%v", statErr)
	}
	repositoryViewHandleOpenedFn = previousHook
	if _, err := service.Analyse(context.Background(), request); err == nil || !strings.Contains(err.Error(), "trusted cache repository changed after validation") {
		t.Fatalf("expected second run to reject replacement repo B, got %v", err)
	}
	if len(adapter.detectPaths) != 1 || len(adapter.analysePaths) != 1 {
		t.Fatalf("expected replacement repo B to receive no adapter reads, detect=%#v analyse=%#v", adapter.detectPaths, adapter.analysePaths)
	}
	if _, statErr := os.Stat(filepath.Join(repo, ".cache")); !os.IsNotExist(statErr) {
		t.Fatalf("expected replacement repo B to receive no cache directories after second run, stat err=%v", statErr)
	}
}

func setupPipelineCanonicalReplacementFixture(t *testing.T) (string, string, string, *CacheOptions) {
	t.Helper()
	parent := t.TempDir()
	repo := filepath.Join(parent, "repo")
	if err := os.Mkdir(repo, 0o750); err != nil {
		t.Fatalf("mkdir repo A: %v", err)
	}
	writeFile(t, filepath.Join(repo, "identity.js"), "repo-a\n")
	repoB := filepath.Join(parent, "repo-b")
	if err := os.Mkdir(repoB, 0o750); err != nil {
		t.Fatalf("mkdir repo B: %v", err)
	}
	writeFile(t, filepath.Join(repoB, "identity.js"), "repo-b\n")
	cacheOptions, err := ResolveTrustedCacheOptions(repo, &CacheOptions{Enabled: true, Path: filepath.Join(".cache", "lopper")})
	if err != nil {
		t.Fatalf("resolve trusted cache options: %v", err)
	}
	return repo, repoB, filepath.Join(parent, "repo-a-original"), cacheOptions
}

type recordingRepoAdapter struct {
	id           string
	detectPaths  []string
	analysePaths []string
}

func (a *recordingRepoAdapter) ID() string {
	return a.id
}

func (a *recordingRepoAdapter) Aliases() []string {
	return nil
}

func (a *recordingRepoAdapter) Detect(_ context.Context, repoPath string) (bool, error) {
	a.detectPaths = append(a.detectPaths, repoPath)
	return true, nil
}

func (a *recordingRepoAdapter) Analyse(_ context.Context, req language.Request) (report.Report, error) {
	a.analysePaths = append(a.analysePaths, req.RepoPath)
	identity, err := os.ReadFile(filepath.Join(req.RepoPath, "identity.js"))
	if err != nil {
		return report.Report{}, err
	}
	return report.Report{
		Dependencies: []report.DependencyReport{{
			Name:              strings.TrimSpace(string(identity)),
			UsedExportsCount:  1,
			TotalExportsCount: 1,
			UsedPercent:       100,
		}},
	}, nil
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

func TestScopeMetadataIncludesRepoRootAsDot(t *testing.T) {
	repoRoot := t.TempDir()
	metadata := scopeMetadata("unexpected", repoRoot, []string{
		filepath.Join(repoRoot, "packages", "b"),
		repoRoot,
	})
	if metadata == nil {
		t.Fatalf("expected scope metadata")
	}
	if metadata.Mode != ScopeModePackage {
		t.Fatalf("expected scope mode normalization to package, got %q", metadata.Mode)
	}
	if len(metadata.Packages) != 2 || metadata.Packages[0] != "." || metadata.Packages[1] != "packages/b" {
		t.Fatalf("expected repo root package to map to dot, got %#v", metadata.Packages)
	}
}

func TestScopeMetadataDropsInvalidRepoPaths(t *testing.T) {
	metadata := scopeMetadata(ScopeModePackage, string([]byte{0}), []string{"/repo/pkg"})
	if metadata == nil {
		t.Fatalf("expected scope metadata")
	}
	if len(metadata.Packages) != 0 {
		t.Fatalf("expected invalid repo path to drop package entries, got %#v", metadata.Packages)
	}
}
