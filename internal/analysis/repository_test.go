package analysis

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/safeio"
)

func TestRepositoryViewRetainsRootAndProvidesRootedOperations(t *testing.T) {
	repo, outside, customCache := setupRepositoryViewFixture(t)
	view, snapshotPath := openRepositoryViewFixture(t, repo, customCache)
	assertRepositoryViewPaths(t, view, repo, outside, snapshotPath)
	assertRepositoryViewOperations(t, view, repo)
	assertRepositorySnapshotContents(t, snapshotPath, customCache)
	assertRepositoryViewClose(t, view, snapshotPath)
}

func TestRepositoryAuthorizationAndViewGuardBranches(t *testing.T) {
	t.Run("nil view helpers", testRepositoryNilViewGuards)
	t.Run("authorization mismatches", testRepositoryAuthorizationMismatches)
	t.Run("open hook and repository validation", testRepositoryOpenHookAndPathValidation)
}

func setupRepositoryViewFixture(t *testing.T) (repo string, outside string, customCache string) {
	t.Helper()
	parent := t.TempDir()
	repo = filepath.Join(parent, "repo")
	if err := os.MkdirAll(filepath.Join(repo, "nested"), 0o750); err != nil {
		t.Fatalf("mkdir repository: %v", err)
	}
	writeFile(t, filepath.Join(repo, "nested", "source.txt"), "repo-a\n")
	writeFile(t, filepath.Join(repo, defaultAnalysisCacheDirName, "keys", "default.json"), "{}\n")
	customCache = filepath.Join(".cache", "lopper")
	writeFile(t, filepath.Join(repo, customCache, "objects", "custom.json"), "{}\n")
	if err := os.Symlink(filepath.Join("nested", "source.txt"), filepath.Join(repo, "source-link")); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	outside = t.TempDir()
	writeFile(t, filepath.Join(outside, "outside.txt"), "outside\n")
	if err := os.Symlink(filepath.Join(outside, "outside.txt"), filepath.Join(repo, "absolute-link")); err != nil {
		t.Fatalf("create absolute symlink: %v", err)
	}
	if err := os.Symlink(filepath.Join("..", filepath.Base(outside), "outside.txt"), filepath.Join(repo, "upward-link")); err != nil {
		t.Fatalf("create upward symlink: %v", err)
	}
	return repo, outside, customCache
}

func openRepositoryViewFixture(t *testing.T, repo, customCache string) (*RepositoryView, string) {
	t.Helper()
	repository, err := ResolveTrustedRepository(repo)
	if err != nil {
		t.Fatalf("authorize repository: %v", err)
	}
	if got := TrustedRepositoryPath(repository); got != filepath.Clean(repo) {
		t.Fatalf("unexpected requested repository path: %q", got)
	}
	cacheOptions, err := ResolveTrustedCacheOptionsForRepository(repository, &CacheOptions{Enabled: true, Path: customCache})
	if err != nil {
		t.Fatalf("resolve cache options: %v", err)
	}
	view, err := OpenTrustedRepository(context.Background(), repository, repo, cacheOptions)
	if err != nil {
		t.Fatalf("open trusted repository: %v", err)
	}
	t.Cleanup(func() {
		if err := view.Close(); err != nil {
			t.Errorf("close repository view: %v", err)
		}
	})
	return view, view.ExecutionPath()
}

func assertRepositoryViewPaths(t *testing.T, view *RepositoryView, repo, outside, snapshotPath string) {
	t.Helper()
	if snapshotPath == "" || snapshotPath == repo {
		t.Fatalf("expected independent execution snapshot, got %q", snapshotPath)
	}
	wantSnapshotFile := filepath.Join(snapshotPath, "nested", "source.txt")
	for _, tc := range []struct {
		name string
		path string
		want string
	}{
		{name: "relative snapshot path", path: filepath.Join("nested", "source.txt"), want: wantSnapshotFile},
		{name: "absolute snapshot path", path: filepath.Join(repo, "nested", "source.txt"), want: wantSnapshotFile},
		{name: "external path preserved", path: filepath.Join(outside, "outside.txt"), want: filepath.Join(outside, "outside.txt")},
		{name: "empty path preserved", path: "", want: ""},
	} {
		if got, err := view.SnapshotPath(tc.path); err != nil || got != tc.want {
			t.Fatalf("%s: got %q err=%v want %q", tc.name, got, err, tc.want)
		}
	}
	if _, err := view.SnapshotPath(filepath.Join("..", "outside.txt")); err == nil || !strings.Contains(err.Error(), "repository root") {
		t.Fatalf("expected unsafe relative snapshot rejection, got %v", err)
	}
	for _, path := range []string{"nested/source.txt", filepath.Join(repo, "nested", "source.txt")} {
		relativePath, ok := view.RepositoryRelativePath(path)
		if !ok || relativePath != filepath.Join("nested", "source.txt") {
			t.Fatalf("expected repository-relative path for %q, got %q ok=%t", path, relativePath, ok)
		}
	}
	for _, path := range []string{"../outside", filepath.Join(outside, "outside.txt"), ""} {
		if relativePath, ok := view.RepositoryRelativePath(path); ok {
			t.Fatalf("expected %q to remain outside repository, got %q", path, relativePath)
		}
	}
	if got := view.DisplayPath(filepath.Join("nested", "source.txt")); got != filepath.Join(repo, "nested", "source.txt") {
		t.Fatalf("unexpected display path: %q", got)
	}
}

func assertRepositoryViewOperations(t *testing.T, view *RepositoryView, repo string) {
	t.Helper()
	data, err := view.ReadFile(filepath.Join("nested", "source.txt"))
	if err != nil || string(data) != "repo-a\n" {
		t.Fatalf("read retained repository file: data=%q err=%v", data, err)
	}
	snapshotData, err := view.ReadExecutionFile(filepath.Join("nested", "source.txt"))
	if err != nil || string(snapshotData) != "repo-a\n" {
		t.Fatalf("read retained execution file: data=%q err=%v", snapshotData, err)
	}
	if _, err := view.ReadExecutionFile("../outside"); err == nil {
		t.Fatal("expected execution snapshot traversal rejection")
	}
	if info, err := view.Lstat("nested"); err != nil || !info.IsDir() {
		t.Fatalf("lstat retained repository directory: info=%#v err=%v", info, err)
	}
	if _, err := view.Lstat("../outside"); err == nil {
		t.Fatal("expected repository traversal lstat rejection")
	}
	if err := view.EnsureDir(filepath.Join(".artifacts", "nested"), 0o750); err != nil {
		t.Fatalf("create rooted repository directory: %v", err)
	}
	if err := view.WriteFile(filepath.Join(".artifacts", "nested", "result.txt"), []byte("bound\n"), 0o600); err != nil {
		t.Fatalf("write rooted repository file: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(repo, ".artifacts", "nested", "result.txt")); err != nil || string(got) != "bound\n" {
		t.Fatalf("inspect rooted repository write: data=%q err=%v", got, err)
	}
	writeRoot, err := view.OpenWriteRoot(".artifacts", false)
	if err != nil {
		t.Fatalf("open retained write root: %v", err)
	}
	if err := writeRoot.Close(); err != nil {
		t.Fatalf("close retained write root: %v", err)
	}
}

func assertRepositorySnapshotContents(t *testing.T, snapshotPath, customCache string) {
	t.Helper()
	if linkInfo, err := os.Lstat(filepath.Join(snapshotPath, "source-link")); err != nil || linkInfo.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("expected safe relative symlink in snapshot: info=%#v err=%v", linkInfo, err)
	}
	for _, omitted := range []string{"absolute-link", "upward-link", defaultAnalysisCacheDirName, customCache} {
		if _, err := os.Lstat(filepath.Join(snapshotPath, omitted)); !os.IsNotExist(err) {
			t.Fatalf("expected %q omitted from repository snapshot, stat err=%v", omitted, err)
		}
	}
}

func assertRepositoryViewClose(t *testing.T, view *RepositoryView, snapshotPath string) {
	t.Helper()
	if err := view.Close(); err != nil {
		t.Fatalf("close repository view: %v", err)
	}
	if err := view.Close(); err != nil {
		t.Fatalf("close repository view twice: %v", err)
	}
	if _, err := os.Stat(snapshotPath); !os.IsNotExist(err) {
		t.Fatalf("expected execution snapshot cleanup, stat err=%v", err)
	}
}

func testRepositoryNilViewGuards(t *testing.T) {
	if TrustedRepositoryPath(nil) != "" {
		t.Fatal("expected nil repository authorization to have no path")
	}
	var nilView *RepositoryView
	if nilView.ExecutionPath() != "" || nilView.canonicalPath() != "" || nilView.DisplayPath(".") != "" {
		t.Fatal("expected nil repository view path helpers to return empty paths")
	}
	if roots := nilView.repositoryRoots(); len(roots) != 0 {
		t.Fatalf("expected nil repository roots, got %#v", roots)
	}
	if got, err := nilView.SnapshotPath("path"); err != nil || got != "path" {
		t.Fatalf("expected nil snapshot mapping to preserve path, got %q err=%v", got, err)
	}
	if _, ok := nilView.RepositoryRelativePath("path"); ok {
		t.Fatal("expected nil repository view not to authorize paths")
	}
	for _, tc := range []struct {
		name string
		run  func() error
	}{
		{name: "write root", run: func() error { _, err := nilView.OpenWriteRoot(".", false); return err }},
		{name: "read file", run: func() error { _, err := nilView.ReadFile("file"); return err }},
		{name: "read execution file", run: func() error { _, err := nilView.ReadExecutionFile("file"); return err }},
		{name: "lstat", run: func() error { _, err := nilView.Lstat("file"); return err }},
		{name: "write file", run: func() error { return nilView.WriteFile("file", nil, 0o600) }},
		{name: "ensure dir", run: func() error { return nilView.EnsureDir("dir", 0o750) }},
	} {
		if err := tc.run(); err == nil {
			t.Fatalf("expected nil repository %s rejection", tc.name)
		}
	}
	if err := nilView.Close(); err != nil {
		t.Fatalf("close nil repository view: %v", err)
	}
	if isSafeRepositoryRelativePath(filepath.Join(string(os.PathSeparator), "absolute"), true) {
		t.Fatal("expected absolute repository-relative path rejection")
	}
	if isSafeRepositoryRelativePath(".", false) {
		t.Fatal("expected repository root rejection when root is disallowed")
	}
}

func testRepositoryAuthorizationMismatches(t *testing.T) {
	repoA := t.TempDir()
	repoB := t.TempDir()
	repositoryA, err := ResolveTrustedRepository(repoA)
	if err != nil {
		t.Fatalf("authorize repo A: %v", err)
	}
	repositoryB, err := ResolveTrustedRepository(repoB)
	if err != nil {
		t.Fatalf("authorize repo B: %v", err)
	}
	if err := useTrustedRepository(repoB, repositoryA); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("expected repository authorization mismatch, got %v", err)
	}
	if err := useTrustedRepository(repoA, nil); err == nil {
		t.Fatal("expected missing repository authorization rejection")
	}
	if _, err := OpenTrustedRepository(context.Background(), repositoryA, repoB, nil); err == nil {
		t.Fatal("expected mismatched repository path rejection")
	}
	cacheB, err := ResolveTrustedDefaultCacheOptionsForRepository(repositoryB, false)
	if err != nil {
		t.Fatalf("resolve repo B cache options: %v", err)
	}
	if _, err := OpenTrustedRepository(context.Background(), repositoryA, repoA, cacheB); err == nil || !strings.Contains(err.Error(), "cache pin does not match") {
		t.Fatalf("expected cache authorization mismatch, got %v", err)
	}
}

func testRepositoryOpenHookAndPathValidation(t *testing.T) {
	repo := t.TempDir()
	repository, err := ResolveTrustedRepository(repo)
	if err != nil {
		t.Fatalf("authorize repository: %v", err)
	}
	expectedHookErr := errors.New("stop after retained handle")
	previousHook := repositoryViewHandleOpenedFn
	repositoryViewHandleOpenedFn = func() error { return expectedHookErr }
	t.Cleanup(func() {
		repositoryViewHandleOpenedFn = previousHook
	})
	if _, err := OpenTrustedRepository(context.Background(), repository, repo, nil); !errors.Is(err, expectedHookErr) {
		t.Fatalf("expected retained-handle hook error, got %v", err)
	}
	repositoryViewHandleOpenedFn = previousHook

	filePath := filepath.Join(t.TempDir(), "repo-file")
	writeFile(t, filePath, "not a directory\n")
	if _, err := ResolveTrustedRepository(filePath); err == nil {
		t.Fatal("expected file repository rejection")
	}
	if _, err := ResolveTrustedRepository(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("expected missing repository rejection")
	}
}

func TestRepositoryAuthorizationRejectsCopiedOrReassignedWrappers(t *testing.T) {
	repoA := t.TempDir()
	repoB := t.TempDir()
	authorizationA, err := ResolveTrustedRepository(repoA)
	if err != nil {
		t.Fatalf("authorize repo A: %v", err)
	}
	authorizationB, err := ResolveTrustedRepository(repoB)
	if err != nil {
		t.Fatalf("authorize repo B: %v", err)
	}
	copiedValue := *authorizationA
	copiedAuthorization := &copiedValue
	if err := useTrustedRepository(repoA, copiedAuthorization); err == nil {
		t.Fatal("expected copied authorization wrapper rejection")
	}

	*authorizationA, *authorizationB = *authorizationB, *authorizationA
	if err := useTrustedRepository(repoA, authorizationA); err == nil {
		t.Fatal("expected reassigned authorization wrapper rejection")
	}
}

func TestRepositoryAuthorizationMatchesFilesystemEquivalentAliasOnCaseInsensitiveSystems(t *testing.T) {
	repoParent := t.TempDir()
	repoBase := "Repo"
	repoPath := filepath.Join(repoParent, repoBase)
	if err := os.Mkdir(repoPath, 0o750); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	aliasPath := filepath.Join(repoParent, strings.ToLower(repoBase))
	if aliasPath == repoPath {
		t.Skip("filesystem preserves identical casing only")
	}
	aliasInfo, err := os.Stat(aliasPath)
	if err != nil {
		t.Skipf("alternate-case alias unavailable: %v", err)
	}
	repoInfo, err := os.Stat(repoPath)
	if err != nil {
		t.Fatalf("stat canonical repo: %v", err)
	}
	if !os.SameFile(repoInfo, aliasInfo) {
		t.Skip("filesystem is case-sensitive")
	}

	authorization, err := ResolveTrustedRepository(repoPath)
	if err != nil {
		t.Fatalf("authorize repo: %v", err)
	}
	if err := useTrustedRepository(aliasPath, authorization); err != nil {
		t.Fatalf("expected alternate-case alias acceptance, got %v", err)
	}
}

func TestResolveAuthorizedRepositoryReusesProvidedOrCacheBoundIdentity(t *testing.T) {
	repoA := t.TempDir()
	repoB := t.TempDir()

	authorizationA, err := ResolveTrustedRepository(repoA)
	if err != nil {
		t.Fatalf("authorize repo A: %v", err)
	}
	if got, err := ResolveAuthorizedRepository(repoA, authorizationA, nil); err != nil || got != authorizationA {
		t.Fatalf("expected provided authorization reuse, got auth=%#v err=%v", got, err)
	}
	if _, err := ResolveAuthorizedRepository(repoB, authorizationA, nil); err == nil {
		t.Fatal("expected provided authorization mismatch rejection")
	}

	cacheA, err := ResolveTrustedDefaultCacheOptionsForRepository(authorizationA, false)
	if err != nil {
		t.Fatalf("resolve repo A cache options: %v", err)
	}
	cacheBound, err := ResolveAuthorizedRepository(repoA, nil, cacheA)
	if err != nil {
		t.Fatalf("resolve cache-bound authorization: %v", err)
	}
	if cacheBound == nil || cacheBound.authorizationState() != authorizationA.authorizationState() {
		t.Fatalf("expected cache-bound authorization to reuse repo A identity, got %#v", cacheBound)
	}
	if _, err := ResolveAuthorizedRepository(repoB, nil, cacheA); err == nil {
		t.Fatal("expected cache-bound authorization mismatch rejection")
	}
	if fresh, err := ResolveAuthorizedRepository(repoA, nil, nil); err != nil || fresh == nil {
		t.Fatalf("expected fresh authorization when no prior identity exists, auth=%#v err=%v", fresh, err)
	}
}

func TestSetRepositoryViewHandleOpenedHookForTestRestoresPreviousHook(t *testing.T) {
	restore := SetRepositoryViewHandleOpenedHookForTest(func() error {
		return errors.New("hooked")
	})
	restore()
	repository, err := ResolveTrustedRepository(t.TempDir())
	if err != nil {
		t.Fatalf("authorize repository: %v", err)
	}
	if view, err := OpenTrustedRepository(context.Background(), repository, TrustedRepositoryPath(repository), nil); err != nil {
		t.Fatalf("open trusted repository after restore: %v", err)
	} else if err := view.Close(); err != nil {
		t.Fatalf("close trusted repository after restore: %v", err)
	}

	restore = SetRepositoryViewHandleOpenedHookForTest(nil)
	if view, err := OpenTrustedRepository(context.Background(), repository, TrustedRepositoryPath(repository), nil); err != nil {
		t.Fatalf("open trusted repository with nil hook replacement: %v", err)
	} else if err := view.Close(); err != nil {
		t.Fatalf("close trusted repository with nil hook replacement: %v", err)
	}
	restore()
}

func TestRepositoryAuthorizationHelperRejectsNilState(t *testing.T) {
	if newRepositoryAuthorization(nil) != nil {
		t.Fatal("expected nil repository state to produce no authorization wrapper")
	}

	malformed := &RepositoryAuthorization{}
	if TrustedRepositoryPath(malformed) != "" {
		t.Fatalf("expected malformed repository authorization to expose no trusted path")
	}
	if malformed.matchesPath(t.TempDir()) {
		t.Fatalf("expected malformed repository authorization not to match paths")
	}
	if err := useTrustedRepository(t.TempDir(), malformed); err == nil {
		t.Fatalf("expected malformed repository authorization rejection")
	}
	validRepo := t.TempDir()
	validAuthorization := newRepositoryAuthorization(&repositoryAuthState{
		paths: trustedRepoPaths{
			requestedPath: validRepo,
			canonicalPath: validRepo,
			canonicalInfo: mustRepositoryDirInfo(t, validRepo),
		},
		nonce: 1,
	})
	if err := useTrustedRepository(string([]byte{0}), validAuthorization); err == nil {
		t.Fatalf("expected invalid repository path normalization failure")
	}
	filePath := filepath.Join(t.TempDir(), "repo-file")
	writeFile(t, filePath, "not a directory\n")
	if malformed := newRepositoryAuthorization(&repositoryAuthState{
		paths: trustedRepoPaths{
			requestedPath: filepath.Dir(filePath),
			canonicalPath: filepath.Dir(filePath),
			canonicalInfo: mustRepositoryDirInfo(t, filepath.Dir(filePath)),
		},
	}); malformed.matchesPath(filePath) {
		t.Fatalf("expected repository authorization not to match non-directory file path")
	}
}

func TestRepositoryOpenHelpersFailClosed(t *testing.T) {
	expectedErr := errors.New("inspect failed")
	failingRoot := &snapshotRootStub{
		lstat: func(string) (os.FileInfo, error) {
			return nil, expectedErr
		},
	}
	if _, err := inspectTrustedRepositoryRoot(failingRoot, &repositoryAuthState{}); !errors.Is(err, expectedErr) {
		t.Fatalf("expected retained-root inspection error, got %v", err)
	}

	repo := t.TempDir()
	root, err := safeio.OpenRoot(repo)
	if err != nil {
		t.Fatalf("open snapshot source root: %v", err)
	}
	t.Cleanup(func() {
		if err := root.Close(); err != nil {
			t.Errorf("close snapshot source root: %v", err)
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	executionPath, executionRoot, warnings, err := openRepositoryExecutionSnapshot(ctx, root, nil)
	if err == nil || executionPath != "" || executionRoot != nil || len(warnings) != 0 {
		t.Fatalf("expected cancelled execution snapshot open, path=%q root=%#v warnings=%#v err=%v", executionPath, executionRoot, warnings, err)
	}

	repository, err := ResolveTrustedRepository(repo)
	if err != nil {
		t.Fatalf("authorize repository: %v", err)
	}
	state := repository.authorizationState()
	openedInfo := state.paths.canonicalInfo
	if err := os.RemoveAll(repo); err != nil {
		t.Fatalf("remove repository before verification: %v", err)
	}
	if err := verifyRepositoryAfterSnapshot(context.Background(), state, openedInfo, false, RepositoryGitMetadata{}); err == nil {
		t.Fatal("expected missing repository verification failure")
	}
}

func TestRepositoryViewMapsExecutionPathsToAuthorizedDisplayIdentity(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "nested", "config.yml"), "thresholds: {}\n")
	repository, err := ResolveTrustedRepository(repo)
	if err != nil {
		t.Fatalf("resolve trusted repository: %v", err)
	}
	view, err := OpenTrustedRepository(context.Background(), repository, repo, nil)
	if err != nil {
		t.Fatalf("open trusted repository: %v", err)
	}
	t.Cleanup(func() {
		if err := view.Close(); err != nil {
			t.Errorf("close trusted repository: %v", err)
		}
	})
	if err := ValidateRepositoryView(repository, view); err != nil {
		t.Fatalf("validate matching repository view: %v", err)
	}
	otherRepository, err := ResolveTrustedRepository(t.TempDir())
	if err != nil {
		t.Fatalf("resolve other trusted repository: %v", err)
	}
	if err := ValidateRepositoryView(otherRepository, view); err == nil {
		t.Fatal("expected mismatched repository view rejection")
	}
	if err := ValidateRepositoryView(repository, nil); err == nil {
		t.Fatal("expected nil repository view rejection")
	}
	if got := view.canonicalPath(); got == "" {
		t.Fatal("expected retained view canonical path")
	}

	snapshotPath := filepath.Join(view.ExecutionPath(), "nested", "config.yml")
	relativePath, ok := view.RepositoryRelativePath(snapshotPath)
	if !ok || relativePath != filepath.Join("nested", "config.yml") {
		t.Fatalf("execution path did not map to repository relative identity: %q, %t", relativePath, ok)
	}
	if got := view.StablePath(snapshotPath); got != filepath.Join(repo, "nested", "config.yml") {
		t.Fatalf("stable execution path = %q", got)
	}
	if got, err := view.SnapshotPath(snapshotPath); err != nil || got != snapshotPath {
		t.Fatalf("snapshot path remap = %q err=%v, want %q", got, err, snapshotPath)
	}
}

func TestRepositoryViewCloseHookRunsOnceAndPropagatesError(t *testing.T) {
	repo := t.TempDir()
	repository, err := ResolveTrustedRepository(repo)
	if err != nil {
		t.Fatalf("resolve trusted repository: %v", err)
	}
	view, err := OpenTrustedRepository(context.Background(), repository, repo, nil)
	if err != nil {
		t.Fatalf("open trusted repository: %v", err)
	}
	closeErr := errors.New("repository view close sentinel")
	closeCalls := 0
	restore := SetRepositoryViewCloseHookForTest(func() error {
		closeCalls++
		return closeErr
	})
	t.Cleanup(restore)

	if err := view.Close(); !errors.Is(err, closeErr) {
		t.Fatalf("first close error = %v", err)
	}
	if err := view.Close(); !errors.Is(err, closeErr) {
		t.Fatalf("second close error = %v", err)
	}
	if closeCalls != 1 {
		t.Fatalf("close hook calls = %d, want 1", closeCalls)
	}
}

func TestRepositoryViewCloseHookAcceptsNilReplacement(t *testing.T) {
	restore := SetRepositoryViewCloseHookForTest(nil)
	t.Cleanup(restore)

	repo := t.TempDir()
	repository, err := ResolveTrustedRepository(repo)
	if err != nil {
		t.Fatalf("resolve trusted repository: %v", err)
	}
	view, err := OpenTrustedRepository(context.Background(), repository, repo, nil)
	if err != nil {
		t.Fatalf("open trusted repository: %v", err)
	}
	if err := view.Close(); err != nil {
		t.Fatalf("close repository view with nil hook replacement: %v", err)
	}
}

func mustRepositoryDirInfo(t *testing.T, path string) os.FileInfo {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("stat repository directory %s: %v", path, err)
	}
	return info
}
