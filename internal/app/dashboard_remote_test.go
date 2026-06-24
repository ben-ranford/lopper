package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/dashboard"
	"github.com/ben-ranford/lopper/internal/featureflags"
	"github.com/ben-ranford/lopper/internal/testutil"
)

const (
	testHTTPSRepoURL = "https://github.com/org/repo.git"
)

func TestReposFromDashboardConfigRejectsUnsafeRepoURLs(t *testing.T) {
	features := enabledDashboardRemoteReposFeatures(t)
	tests := []struct {
		name    string
		repoURL string
		want    string
	}{
		{name: "http", repoURL: "http://github.com/org/repo.git", want: "unsupported repoUrl protocol"},
		{name: "git", repoURL: "git://github.com/org/repo.git", want: "unsupported repoUrl protocol"},
		{name: "scp", repoURL: "git@github.com:org/repo.git", want: "must use https://, ssh://, or file://"},
		{name: "https credentials", repoURL: "https://token@github.com/org/repo.git", want: "cannot include credentials"},
		{name: "query", repoURL: "https://github.com/org/repo.git?ref=main", want: "query strings or fragments"},
		{name: "file host", repoURL: "file://example.com/tmp/repo.git", want: "host must be empty or localhost"},
		{name: "relative file", repoURL: "file:relative/repo.git", want: "must use https://, ssh://, or file://"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			config := dashboard.LoadedConfig{
				ConfigDir: t.TempDir(),
				Dashboard: dashboard.ConfigDashboard{
					Repos: []dashboard.ConfigRepo{{RepoURL: tc.repoURL}},
				},
			}
			_, err := reposFromDashboardConfig(config, &features)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected repoUrl error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestDashboardRemoteCacheRootRules(t *testing.T) {
	t.Setenv(dashboardRepoCacheEnv, "relative-cache")
	if _, err := dashboardRemoteCacheRoot(); err == nil || !strings.Contains(err.Error(), "absolute path") {
		t.Fatalf("expected relative cache override error, got %v", err)
	}

	absoluteRoot := filepath.Join(t.TempDir(), "cache", "..", "cache")
	t.Setenv(dashboardRepoCacheEnv, absoluteRoot)
	got, err := dashboardRemoteCacheRoot()
	if err != nil {
		t.Fatalf("dashboard remote cache root: %v", err)
	}
	if got != filepath.Clean(absoluteRoot) {
		t.Fatalf("expected cleaned absolute cache root, got %q", got)
	}
}

func TestDashboardRemoteCacheRootDefault(t *testing.T) {
	t.Setenv(dashboardRepoCacheEnv, "")
	got, err := dashboardRemoteCacheRoot()
	if err != nil {
		t.Fatalf("dashboard remote default cache root: %v", err)
	}
	if !filepath.IsAbs(got) || !strings.HasSuffix(got, filepath.Join("lopper", "dashboard", "repos")) {
		t.Fatalf("unexpected default dashboard cache root: %q", got)
	}
}

func TestDashboardRemoteCacheRootDefaultError(t *testing.T) {
	original := dashboardUserCacheDirFn
	t.Cleanup(func() { dashboardUserCacheDirFn = original })
	expectedErr := errors.New("cache dir unavailable")
	dashboardUserCacheDirFn = func() (string, error) { return "", expectedErr }

	t.Setenv(dashboardRepoCacheEnv, "")
	if _, err := dashboardRemoteCacheRoot(); !errors.Is(err, expectedErr) {
		t.Fatalf("expected user cache dir error, got %v", err)
	}
}

func TestNewDashboardRepoMaterializerUsesCacheRoot(t *testing.T) {
	original := dashboardRemoteCacheRootFn
	t.Cleanup(func() { dashboardRemoteCacheRootFn = original })

	expectedErr := errors.New("cache root failed")
	dashboardRemoteCacheRootFn = func() (string, error) { return "", expectedErr }
	if _, err := newDashboardRepoMaterializer(); !errors.Is(err, expectedErr) {
		t.Fatalf("expected cache root error, got %v", err)
	}

	cacheRoot := t.TempDir()
	dashboardRemoteCacheRootFn = func() (string, error) { return cacheRoot, nil }
	materializer, err := newDashboardRepoMaterializer()
	if err != nil {
		t.Fatalf("new dashboard repo materializer: %v", err)
	}
	if materializer.cacheRoot != cacheRoot || materializer.gitPath == "" {
		t.Fatalf("unexpected materializer: %#v", materializer)
	}
}

func TestNewDashboardRepoMaterializerGitPathError(t *testing.T) {
	originalCacheRoot := dashboardRemoteCacheRootFn
	originalGitPath := resolveDashboardGitBinaryFn
	t.Cleanup(func() {
		dashboardRemoteCacheRootFn = originalCacheRoot
		resolveDashboardGitBinaryFn = originalGitPath
	})

	expectedErr := errors.New("git missing")
	dashboardRemoteCacheRootFn = func() (string, error) { return t.TempDir(), nil }
	resolveDashboardGitBinaryFn = func() (string, error) { return "", expectedErr }
	if _, err := newDashboardRepoMaterializer(); !errors.Is(err, expectedErr) {
		t.Fatalf("expected git path error, got %v", err)
	}
}

func TestPrepareDashboardExecutionPlanReportsMaterializerConstructionError(t *testing.T) {
	original := newDashboardRepoMaterializerFn
	t.Cleanup(func() { newDashboardRepoMaterializerFn = original })
	expectedErr := errors.New("git unavailable")
	newDashboardRepoMaterializerFn = func() (*dashboardRepoMaterializer, error) {
		return nil, expectedErr
	}

	application := &App{Analyzer: &mapAnalyzer{}}
	repos := []dashboard.RepoInput{
		{Name: "local", Path: "./local"},
		{Name: "remote", RepoURL: testHTTPSRepoURL},
	}
	plan := application.prepareDashboardExecutionPlan(context.Background(), DashboardRequest{}, repos)
	results := application.executeDashboardAnalysisPlan(context.Background(), plan)
	if len(results) != 2 {
		t.Fatalf("expected two dashboard results, got %#v", results)
	}
	if results[0].Err != nil {
		t.Fatalf("expected path repo to remain analyzable, got %v", results[0].Err)
	}
	if !errors.Is(results[1].Err, expectedErr) {
		t.Fatalf("expected remote materializer error, got %v", results[1].Err)
	}
}

func TestExecuteDashboardMaterializesRepoURL(t *testing.T) {
	cacheRoot := t.TempDir()
	t.Setenv(dashboardRepoCacheEnv, cacheRoot)
	remoteRepo := initDashboardRemoteGitRepo(t)
	configPath := filepath.Join(t.TempDir(), "lopper-org.yml")
	config := "dashboard:\n  repos:\n    - repoUrl: " + fileURL(remoteRepo) + "\n      name: Fixture Remote\n      language: go\n  output: json\n"
	testutil.MustWriteFile(t, configPath, config)

	analyzer := &mapAnalyzer{}
	application := &App{Analyzer: analyzer}
	req := DefaultRequest()
	req.Mode = ModeDashboard
	req.Dashboard.ConfigPath = configPath
	req.Dashboard.Format = "json"
	req.Dashboard.Features = enabledDashboardRemoteReposFeatures(t)

	output, err := application.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("execute dashboard with repoUrl: %v", err)
	}
	if len(analyzer.calls) != 1 {
		t.Fatalf("expected one analyzer call, got %#v", analyzer.calls)
	}

	checkoutPath := analyzer.calls[0].RepoPath
	if !strings.HasPrefix(checkoutPath, cacheRoot+string(filepath.Separator)) {
		t.Fatalf("expected checkout under cache root %q, got %q", cacheRoot, checkoutPath)
	}
	if _, err := os.Stat(filepath.Join(checkoutPath, ".git")); err != nil {
		t.Fatalf("expected materialized git checkout at %q: %v", checkoutPath, err)
	}

	reportData := dashboard.Report{}
	if err := json.Unmarshal([]byte(output), &reportData); err != nil {
		t.Fatalf("unmarshal dashboard report: %v", err)
	}
	if len(reportData.Repos) != 1 {
		t.Fatalf("expected one repo result, got %#v", reportData.Repos)
	}
	got := reportData.Repos[0]
	if got.Name != "Fixture Remote" || got.Language != "go" || got.Path != checkoutPath || got.Error != "" {
		t.Fatalf("unexpected materialized repo result: %#v", got)
	}
}

func TestExecuteDashboardReportsRepoURLCheckoutFailure(t *testing.T) {
	cacheRoot := t.TempDir()
	t.Setenv(dashboardRepoCacheEnv, cacheRoot)
	missingRemote := filepath.Join(t.TempDir(), "missing.git")
	configPath := filepath.Join(t.TempDir(), "lopper-org.yml")
	config := "dashboard:\n  repos:\n    - repoUrl: " + fileURL(missingRemote) + "\n      name: Missing Remote\n  output: json\n"
	testutil.MustWriteFile(t, configPath, config)

	analyzer := &mapAnalyzer{}
	application := &App{Analyzer: analyzer}
	req := DefaultRequest()
	req.Mode = ModeDashboard
	req.Dashboard.ConfigPath = configPath
	req.Dashboard.Format = "json"
	req.Dashboard.Features = enabledDashboardRemoteReposFeatures(t)

	output, err := application.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("repoUrl checkout failure should still render dashboard output: %v", err)
	}
	if len(analyzer.calls) != 0 {
		t.Fatalf("expected failed materialization to skip analysis, got calls %#v", analyzer.calls)
	}

	reportData := dashboard.Report{}
	if err := json.Unmarshal([]byte(output), &reportData); err != nil {
		t.Fatalf("unmarshal dashboard report: %v", err)
	}
	if len(reportData.Repos) != 1 {
		t.Fatalf("expected one repo result, got %#v", reportData.Repos)
	}
	if !strings.Contains(reportData.Repos[0].Error, "materialize repoUrl") {
		t.Fatalf("expected per-repo materialization error, got %#v", reportData.Repos[0])
	}
	if len(reportData.SourceWarnings) != 1 || !strings.Contains(reportData.SourceWarnings[0], "materialize repoUrl") {
		t.Fatalf("expected dashboard warning for materialization failure, got %#v", reportData.SourceWarnings)
	}
}

func TestDashboardRepoMaterializerRefreshesUsableCheckout(t *testing.T) {
	cacheRoot := t.TempDir()
	spec := mustParseDashboardRepoURL(t, testHTTPSRepoURL)
	checkoutPath := mustDashboardCheckoutPath(t, cacheRoot, spec)
	if err := os.MkdirAll(filepath.Join(checkoutPath, ".git"), 0o750); err != nil {
		t.Fatalf("mkdir checkout git dir: %v", err)
	}

	var commands []string
	withFakeDashboardGit(t, func(ctx context.Context, gitPath string, args ...string) (*exec.Cmd, error) {
		commands = append(commands, strings.Join(args, " "))
		if strings.Contains(strings.Join(args, " "), "remote get-url origin") {
			return exec.CommandContext(ctx, fixedTestBinary(t, "printf"), testHTTPSRepoURL), nil
		}
		return exec.CommandContext(ctx, fixedTestBinary(t, "true")), nil
	})

	materializer := &dashboardRepoMaterializer{cacheRoot: cacheRoot, gitPath: "/usr/bin/git"}
	got, err := materializer.Materialize(context.Background(), testHTTPSRepoURL)
	if err != nil {
		t.Fatalf("materialize existing checkout: %v", err)
	}
	if got != checkoutPath {
		t.Fatalf("expected checkout path %q, got %q", checkoutPath, got)
	}
	assertCommandContains(t, commands, "fetch --prune --depth=1 origin HEAD")
	assertCommandContains(t, commands, "checkout --detach --force FETCH_HEAD")
	assertCommandContains(t, commands, "reset --hard FETCH_HEAD")
	assertCommandContains(t, commands, "clean -fdx")
}

func TestDashboardRepoMaterializerRefreshFailure(t *testing.T) {
	cacheRoot := t.TempDir()
	spec := mustParseDashboardRepoURL(t, testHTTPSRepoURL)
	checkoutPath := mustDashboardCheckoutPath(t, cacheRoot, spec)
	if err := os.MkdirAll(filepath.Join(checkoutPath, ".git"), 0o750); err != nil {
		t.Fatalf("mkdir checkout git dir: %v", err)
	}

	withFakeDashboardGit(t, func(ctx context.Context, gitPath string, args ...string) (*exec.Cmd, error) {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "remote get-url origin") {
			return exec.CommandContext(ctx, fixedTestBinary(t, "printf"), testHTTPSRepoURL), nil
		}
		if strings.Contains(joined, "fetch --prune") {
			return exec.CommandContext(ctx, fixedTestBinary(t, "false")), nil
		}
		return exec.CommandContext(ctx, fixedTestBinary(t, "true")), nil
	})

	materializer := &dashboardRepoMaterializer{cacheRoot: cacheRoot, gitPath: "/usr/bin/git"}
	got, err := materializer.Materialize(context.Background(), testHTTPSRepoURL)
	if got != checkoutPath {
		t.Fatalf("expected checkout path on refresh failure, got %q", got)
	}
	if err == nil || !strings.Contains(err.Error(), "fetch remote repo") {
		t.Fatalf("expected fetch failure, got %v", err)
	}
}

func TestDashboardRepoMaterializerValidationAndCacheErrors(t *testing.T) {
	materializer := &dashboardRepoMaterializer{cacheRoot: t.TempDir(), gitPath: "/usr/bin/git"}
	if checkoutPath, err := materializer.Materialize(context.Background(), "notaurl"); err == nil || checkoutPath != "" {
		t.Fatalf("expected invalid URL without checkout path, path=%q err=%v", checkoutPath, err)
	}

	materializer.cacheRoot = "relative-cache"
	if _, err := materializer.Materialize(context.Background(), testHTTPSRepoURL); err == nil || !strings.Contains(err.Error(), "cache root") {
		t.Fatalf("expected relative cache root error, got %v", err)
	}

	blocker := filepath.Join(t.TempDir(), "blocked-cache")
	testutil.MustWriteFile(t, blocker, "x")
	materializer.cacheRoot = blocker
	checkoutPath, err := materializer.Materialize(context.Background(), testHTTPSRepoURL)
	if checkoutPath == "" {
		t.Fatalf("expected deterministic checkout path with blocked cache")
	}
	if err == nil || !strings.Contains(err.Error(), "create dashboard repo cache") {
		t.Fatalf("expected cache mkdir error, got %v", err)
	}
}

func TestDashboardRepoMaterializerRemoveErrors(t *testing.T) {
	originalRemoveAll := dashboardRemoveAllFn
	t.Cleanup(func() { dashboardRemoveAllFn = originalRemoveAll })
	resetErr := errors.New("remove denied")
	dashboardRemoveAllFn = func(string) error { return resetErr }

	materializer := &dashboardRepoMaterializer{cacheRoot: t.TempDir(), gitPath: "/usr/bin/git"}
	if _, err := materializer.Materialize(context.Background(), testHTTPSRepoURL); !errors.Is(err, resetErr) {
		t.Fatalf("expected reset remove error, got %v", err)
	}
}

func TestDashboardRepoMaterializerCloneCleanupError(t *testing.T) {
	originalRemoveAll := dashboardRemoveAllFn
	t.Cleanup(func() { dashboardRemoveAllFn = originalRemoveAll })
	cleanupErr := errors.New("cleanup denied")
	removeCalls := 0
	dashboardRemoveAllFn = func(path string) error {
		removeCalls++
		if removeCalls == 2 {
			return cleanupErr
		}
		return os.RemoveAll(path)
	}
	withFakeDashboardGit(t, func(ctx context.Context, gitPath string, args ...string) (*exec.Cmd, error) {
		if strings.Contains(strings.Join(args, " "), "clone --no-tags") {
			return exec.CommandContext(ctx, fixedTestBinary(t, "false")), nil
		}
		return exec.CommandContext(ctx, fixedTestBinary(t, "true")), nil
	})

	materializer := &dashboardRepoMaterializer{cacheRoot: t.TempDir(), gitPath: "/usr/bin/git"}
	_, err := materializer.Materialize(context.Background(), testHTTPSRepoURL)
	if err == nil || !strings.Contains(err.Error(), "clone remote repo") || !strings.Contains(err.Error(), "cleanup failed") || !errors.Is(err, cleanupErr) {
		t.Fatalf("expected clone and cleanup failure, got %v", err)
	}
}

func TestDashboardRepoMaterializerReplacesMismatchedCheckout(t *testing.T) {
	cacheRoot := t.TempDir()
	spec := mustParseDashboardRepoURL(t, testHTTPSRepoURL)
	checkoutPath := mustDashboardCheckoutPath(t, cacheRoot, spec)
	if err := os.MkdirAll(filepath.Join(checkoutPath, ".git"), 0o750); err != nil {
		t.Fatalf("mkdir checkout git dir: %v", err)
	}
	testutil.MustWriteFile(t, filepath.Join(checkoutPath, "stale.txt"), "stale")

	var commands []string
	withFakeDashboardGit(t, func(ctx context.Context, gitPath string, args ...string) (*exec.Cmd, error) {
		joined := strings.Join(args, " ")
		commands = append(commands, joined)
		if strings.Contains(joined, "remote get-url origin") {
			return exec.CommandContext(ctx, fixedTestBinary(t, "printf"), "https://github.com/other/repo.git"), nil
		}
		return exec.CommandContext(ctx, fixedTestBinary(t, "true")), nil
	})

	materializer := &dashboardRepoMaterializer{cacheRoot: cacheRoot, gitPath: "/usr/bin/git"}
	got, err := materializer.Materialize(context.Background(), testHTTPSRepoURL)
	if err != nil {
		t.Fatalf("materialize mismatched checkout: %v", err)
	}
	if got != checkoutPath {
		t.Fatalf("expected checkout path %q, got %q", checkoutPath, got)
	}
	if _, err := os.Stat(filepath.Join(checkoutPath, "stale.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected stale checkout to be removed before clone, stat err=%v", err)
	}
	assertCommandContains(t, commands, "clone --no-tags --depth=1 -- "+testHTTPSRepoURL)
}

func TestDashboardRepoMaterializerPinCheckoutErrors(t *testing.T) {
	tests := []struct {
		name    string
		failOn  string
		wantErr string
	}{
		{name: "checkout", failOn: "checkout --detach", wantErr: "checkout remote repo"},
		{name: "reset", failOn: "reset --hard", wantErr: "reset remote repo checkout"},
		{name: "clean", failOn: "clean -fdx", wantErr: "clean remote repo checkout"},
	}
	spec := mustParseDashboardRepoURL(t, testHTTPSRepoURL)

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			withFakeDashboardGit(t, func(ctx context.Context, gitPath string, args ...string) (*exec.Cmd, error) {
				if strings.Contains(strings.Join(args, " "), tc.failOn) {
					return exec.CommandContext(ctx, fixedTestBinary(t, "false")), nil
				}
				return exec.CommandContext(ctx, fixedTestBinary(t, "true")), nil
			})

			materializer := &dashboardRepoMaterializer{cacheRoot: t.TempDir(), gitPath: "/usr/bin/git"}
			err := materializer.pinCheckout(context.Background(), t.TempDir(), spec, "HEAD")
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected %q error, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestDashboardRepoMaterializerRunGitConstructorError(t *testing.T) {
	expectedErr := errors.New("construct git")
	withFakeDashboardGit(t, func(ctx context.Context, gitPath string, args ...string) (*exec.Cmd, error) {
		return nil, expectedErr
	})

	materializer := &dashboardRepoMaterializer{cacheRoot: t.TempDir(), gitPath: "/usr/bin/git"}
	if _, err := materializer.runGit(context.Background(), "status"); !errors.Is(err, expectedErr) {
		t.Fatalf("expected constructor error, got %v", err)
	}
}

func TestDashboardRepoURLParsingBranches(t *testing.T) {
	tests := [][2]string{
		{"   ", "repoUrl is required"},
		{"://repo", "missing protocol scheme"},
		{"https://[::1", "missing ']'"},
		{"https:///org/repo.git", "host is required"},
		{"https://github.com", "path is required"},
		{"ssh://git:secret@github.com/org/repo.git", "cannot include passwords"},
		{"file://user@localhost/tmp/repo.git", "cannot include credentials"},
		{"file://localhost", "path must be absolute"},
	}
	for _, tc := range tests {
		t.Run(tc[1], func(t *testing.T) {
			if _, err := parseDashboardRepoURL(tc[0]); err == nil || !strings.Contains(err.Error(), tc[1]) {
				t.Fatalf("expected parse error containing %q, got %v", tc[1], err)
			}
		})
	}

	spec := mustParseDashboardRepoURL(t, "ssh://git@github.com/org/repo.git")
	if spec.scheme != "ssh" || spec.name != "repo" || spec.normalized != "ssh://git@github.com/org/repo.git" {
		t.Fatalf("unexpected ssh repo URL spec: %#v", spec)
	}

	fileSpec := mustParseDashboardRepoURL(t, fileURL(filepath.Join(t.TempDir(), "fixture repo.git")))
	if fileSpec.scheme != "file" || fileSpec.name != "fixture repo" {
		t.Fatalf("unexpected file repo URL spec: %#v", fileSpec)
	}

	hostFallback := mustParseDashboardRepoURL(t, "https://github.com/org/.git")
	if hostFallback.name != "github.com" {
		t.Fatalf("expected host fallback name, got %#v", hostFallback)
	}

	fileFallback := mustParseDashboardRepoURL(t, "file:///")
	if fileFallback.name != "file:///" {
		t.Fatalf("expected file URL fallback name, got %#v", fileFallback)
	}
}

func TestDashboardCheckoutHelpers(t *testing.T) {
	spec := mustParseDashboardRepoURL(t, testHTTPSRepoURL)
	root := t.TempDir()
	first := mustDashboardCheckoutPath(t, root, spec)
	second := mustDashboardCheckoutPath(t, root, spec)
	if first != second || !strings.HasPrefix(first, root+string(filepath.Separator)) {
		t.Fatalf("expected deterministic checkout path under root, first=%q second=%q root=%q", first, second, root)
	}
	if sanitized := sanitizeDashboardCheckoutName(" ../repo name:with/slash "); sanitized != "repo-name-with-slash" {
		t.Fatalf("unexpected sanitized checkout name: %q", sanitized)
	}
	if sanitized := sanitizeDashboardCheckoutName("!!!"); sanitized != "repo" {
		t.Fatalf("expected empty sanitized name fallback, got %q", sanitized)
	}
	if pathWithinDir(root, filepath.Dir(root)) {
		t.Fatalf("expected parent directory to be outside cache root")
	}
	if !pathWithinDir(root, root) {
		t.Fatalf("expected cache root to be within itself")
	}
	if _, err := dashboardCheckoutPath("relative", spec); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("expected relative checkout root error, got %v", err)
	}

	fileSpec := mustParseDashboardRepoURL(t, fileURL(filepath.Join(t.TempDir(), "repo.git")))
	if args := gitConfigArgsForURL(fileSpec.normalized); len(args) != 2 || args[0] != "-c" {
		t.Fatalf("expected file protocol git config args, got %#v", args)
	}
	if args := gitArgsForURL(testHTTPSRepoURL, "status"); len(args) != 1 || args[0] != "status" {
		t.Fatalf("expected passthrough https git args, got %#v", args)
	}
}

func enabledDashboardRemoteReposFeatures(t *testing.T) featureflags.Set {
	t.Helper()
	features, err := featureflags.DefaultRegistry().Resolve(featureflags.ResolveOptions{
		Channel: featureflags.ChannelDev,
		Enable:  []string{DashboardRemoteReposPreviewFeature},
	})
	if err != nil {
		t.Fatalf("resolve dashboard remote repos feature: %v", err)
	}
	return features
}

func initDashboardRemoteGitRepo(t *testing.T) string {
	t.Helper()
	repo := filepath.Join(t.TempDir(), "remote")
	if err := os.MkdirAll(repo, 0o750); err != nil {
		t.Fatalf("mkdir remote repo: %v", err)
	}
	testutil.RunGit(t, repo, "init")
	testutil.MustWriteFile(t, filepath.Join(repo, "go.mod"), "module example.com/fixture\n\ngo 1.22\n")
	testutil.RunGit(t, repo, "add", ".")
	testutil.RunGit(t, repo, "-c", "user.name=Lopper Test", "-c", "user.email=lopper@example.invalid", "commit", "-m", "initial")
	return repo
}

func fileURL(path string) string {
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(path)}).String()
}

func withFakeDashboardGit(t *testing.T, fake func(context.Context, string, ...string) (*exec.Cmd, error)) {
	t.Helper()
	original := execDashboardGitCommandFn
	execDashboardGitCommandFn = fake
	t.Cleanup(func() { execDashboardGitCommandFn = original })
}

func fixedTestBinary(t *testing.T, name string) string {
	t.Helper()
	candidates := map[string][]string{
		"true":   {"/usr/bin/true", "/bin/true"},
		"false":  {"/usr/bin/false", "/bin/false"},
		"printf": {"/usr/bin/printf", "/bin/printf"},
	}
	for _, candidate := range candidates[name] {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return candidate
		}
	}
	t.Fatalf("test binary %q not found", name)
	return ""
}

func mustParseDashboardRepoURL(t *testing.T, repoURL string) dashboardRepoURLSpec {
	t.Helper()
	spec, err := parseDashboardRepoURL(repoURL)
	if err != nil {
		t.Fatalf("parse dashboard repo URL %q: %v", repoURL, err)
	}
	return spec
}

func mustDashboardCheckoutPath(t *testing.T, cacheRoot string, spec dashboardRepoURLSpec) string {
	t.Helper()
	checkoutPath, err := dashboardCheckoutPath(cacheRoot, spec)
	if err != nil {
		t.Fatalf("dashboard checkout path: %v", err)
	}
	return checkoutPath
}

func assertCommandContains(t *testing.T, commands []string, expected string) {
	t.Helper()
	for _, command := range commands {
		if strings.Contains(command, expected) {
			return
		}
	}
	t.Fatalf("expected command containing %q, got %#v", expected, commands)
}
