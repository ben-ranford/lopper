package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/analysis"
	"github.com/ben-ranford/lopper/internal/featureflags"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/safeio"
	"github.com/ben-ranford/lopper/internal/testutil"
	"github.com/ben-ranford/lopper/internal/thresholds"
	"github.com/ben-ranford/lopper/internal/workspace"
)

func TestExecuteAnalyseEmitsEffectiveThresholds(t *testing.T) {
	analyzer := &fakeAnalyzer{
		report: report.Report{
			RepoPath: ".",
			Dependencies: []report.DependencyReport{
				{Name: "lodash", UsedExportsCount: 1, TotalExportsCount: 2, UsedPercent: 50},
			},
		},
	}
	application := &App{Analyzer: analyzer, Formatter: report.NewFormatter()}

	req := DefaultRequest()
	req.Mode = ModeAnalyse
	req.Analyse.TopN = 1
	req.Analyse.ScopeMode = ScopeModeChangedPackages
	req.Analyse.Format = report.FormatJSON
	req.Analyse.SuggestOnly = true
	req.Analyse.RuntimeProfile = "browser-import"
	req.Analyse.CacheEnabled = false
	req.Analyse.CachePath = "/tmp/lopper-cache"
	req.Analyse.CacheReadOnly = true
	req.Analyse.Features = mustResolveAppTestFeatures(t, report.ReachabilityVulnerabilityPrioritizationPreviewFeature)
	req.Analyse.PolicySources = []string{"cli", "defaults"}
	req.Analyse.Thresholds = thresholds.Values{
		FailOnIncreasePercent:             -1,
		MaxUncertainImportCount:           thresholds.DefaultMaxUncertainImportCount,
		LowConfidenceWarningPercent:       33,
		MinUsagePercentForRecommendations: 44,
		RemovalCandidateWeightUsage:       0.6,
		RemovalCandidateWeightImpact:      0.2,
		RemovalCandidateWeightConfidence:  0.2,
	}
	featureRegistry, err := featureflags.NewRegistry([]featureflags.Flag{{
		Code:      "LOP-FEAT-0001",
		Name:      "powershell-adapter",
		Lifecycle: featureflags.LifecyclePreview,
	}})
	if err != nil {
		t.Fatalf("new feature registry: %v", err)
	}
	resolvedFeatures, err := featureRegistry.Resolve(featureflags.ResolveOptions{
		Channel: featureflags.ChannelDev,
		Enable:  []string{"powershell-adapter"},
	})
	if err != nil {
		t.Fatalf("resolve feature set: %v", err)
	}
	req.Analyse.Features = resolvedFeatures

	output, err := application.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf(executeAnalyseErrFmt, err)
	}
	assertContainsAll(t, output, []string{`"effectiveThresholds"`, `"effectivePolicy"`, `"sources": [`, `"cli"`, `"lowConfidenceWarningPercent": 33`})
	assertForwardedAnalyseRequest(t, analyzer.lastReq)
	if !analyzer.lastReq.Features.Enabled("powershell-adapter") {
		t.Fatalf("expected feature set to be forwarded to analysis request")
	}
}

func TestExecuteAnalyseAnalyzerError(t *testing.T) {
	expected := errors.New("analyse failed")
	application := &App{
		Analyzer:  &fakeAnalyzer{err: expected},
		Formatter: report.NewFormatter(),
	}
	req := DefaultRequest()
	req.Mode = ModeAnalyse
	req.Analyse.Dependency = "lodash"
	_, err := application.Execute(context.Background(), req)
	if !errors.Is(err, expected) {
		t.Fatalf("expected analyzer error, got %v", err)
	}
}

func TestExecuteAnalysePreservesAbsoluteCachePathOutsideRepoForCLIRequests(t *testing.T) {
	repo := t.TempDir()
	outsideCache := filepath.Join(t.TempDir(), "cache")
	analyzer := &fakeAnalyzer{}
	application := &App{Analyzer: analyzer, Formatter: report.NewFormatter()}

	req := DefaultRequest()
	req.Mode = ModeAnalyse
	req.RepoPath = repo
	req.Analyse.Dependency = "lodash"
	req.Analyse.CacheEnabled = true
	req.Analyse.CachePath = outsideCache

	if _, err := application.Execute(context.Background(), req); err != nil {
		t.Fatalf("expected CLI cache path compatibility, got %v", err)
	}
	if !analyzer.called {
		t.Fatalf("expected analyzer to be called")
	}
	if analyzer.lastReq.Cache == nil {
		t.Fatalf("expected cache options to be forwarded")
	}
	if analyzer.lastReq.Cache.Path != outsideCache {
		t.Fatalf("expected absolute CLI cache path to remain unchanged, got %#v", analyzer.lastReq.Cache)
	}
}

func TestExecuteAnalyseRejectsAbsoluteSymlinkEscapeCachePathForCLIRequests(t *testing.T) {
	repo := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(repo, "tmp")); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	analyzer := &fakeAnalyzer{}
	application := &App{Analyzer: analyzer, Formatter: report.NewFormatter()}

	req := DefaultRequest()
	req.Mode = ModeAnalyse
	req.RepoPath = repo
	req.Analyse.Dependency = "lodash"
	req.Analyse.CacheEnabled = true
	req.Analyse.CachePath = filepath.Join(repo, "tmp", "cache")

	_, err := application.Execute(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), "cachePath must stay within repoPath") {
		t.Fatalf("expected absolute symlink cache rejection, got %v", err)
	}
	if analyzer.called {
		t.Fatalf("expected analyzer to remain uncalled")
	}
}

func TestExecuteAnalyseRejectsCanonicalSymlinkEscapeUnderRequestedRepoAlias(t *testing.T) {
	requestedRepo, canonicalCache, outsideCache := analyseCanonicalAliasCacheEscapeFixture(t)
	analyzer := &fakeAnalyzer{}
	application := &App{Analyzer: analyzer, Formatter: report.NewFormatter()}

	req := DefaultRequest()
	req.Mode = ModeAnalyse
	req.RepoPath = requestedRepo
	req.Analyse.Dependency = "lodash"
	req.Analyse.CacheEnabled = true
	req.Analyse.CachePath = canonicalCache

	_, err := application.Execute(context.Background(), req)
	if !analysis.CachePathSymlinkEscape(err) {
		t.Fatalf("expected CLI symlink escape rejection, got %v", err)
	}
	if analysis.CachePathExternal(err) {
		t.Fatalf("expected CLI not to classify canonical in-repo form as external, got %v", err)
	}
	if analyzer.called {
		t.Fatalf("expected analyzer to remain uncalled")
	}
	if _, statErr := os.Stat(outsideCache); !os.IsNotExist(statErr) {
		t.Fatalf("expected CLI not to create external cache directory, stat err=%v", statErr)
	}
}

func TestExecuteAnalyseRejectsAlternateAbsoluteRepoAliasesThatLaterEscape(t *testing.T) {
	for _, fixture := range appAlternateAbsoluteRepoAliasEscapeFixtures(t) {
		t.Run(fixture.name, func(t *testing.T) {
			analyzer := &fakeAnalyzer{}
			application := &App{Analyzer: analyzer, Formatter: report.NewFormatter()}
			req := DefaultRequest()
			req.Mode = ModeAnalyse
			req.RepoPath = fixture.requestedRepo
			req.Analyse.Dependency = "lodash"
			req.Analyse.CacheEnabled = true
			req.Analyse.CachePath = fixture.cachePath

			_, err := application.Execute(context.Background(), req)
			if !analysis.CachePathSymlinkEscape(err) || analysis.CachePathExternal(err) {
				t.Fatalf("expected CLI alternate-alias escape rejection, got %v", err)
			}
			if analyzer.called {
				t.Fatalf("expected analyzer to remain uncalled")
			}
			if _, statErr := os.Stat(fixture.outsideCache); !os.IsNotExist(statErr) {
				t.Fatalf("expected CLI not to create outside cache, stat err=%v", statErr)
			}
		})
	}
}

func TestExecuteAnalyseRejectsUnsafeRelativeAdvisorySourcePathWithRepositoryView(t *testing.T) {
	repo := t.TempDir()
	analyzer := &fakeAnalyzer{}
	application := &App{Analyzer: analyzer, Formatter: report.NewFormatter()}

	req := DefaultRequest()
	req.Mode = ModeAnalyse
	req.RepoPath = repo
	req.Analyse.Dependency = "lodash"
	req.Analyse.CacheEnabled = false
	req.Analyse.AdvisorySourcePath = filepath.Join("..", "outside-advisories.json")
	req.Analyse.Features = mustResolveAppTestFeatures(t, report.ReachabilityVulnerabilityPrioritizationPreviewFeature)

	_, err := application.Execute(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), "repository root") {
		t.Fatalf("expected advisory traversal rejection, got %v", err)
	}
	if analyzer.called {
		t.Fatalf("expected analyzer to remain uncalled")
	}
}

func TestExecuteAnalyseRejectsUnsafeRelativeBaselineStorePathWithRepositoryView(t *testing.T) {
	repo := t.TempDir()
	analyzer := &fakeAnalyzer{report: report.Report{}}
	application := &App{Analyzer: analyzer, Formatter: report.NewFormatter()}

	req := DefaultRequest()
	req.Mode = ModeAnalyse
	req.RepoPath = repo
	req.Analyse.Dependency = "lodash"
	req.Analyse.CacheEnabled = false
	req.Analyse.SaveBaseline = true
	req.Analyse.BaselineStorePath = filepath.Join("..", "outside-baselines")
	req.Analyse.BaselineLabel = "nightly"

	_, err := application.Execute(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), "repository root") {
		t.Fatalf("expected baseline store traversal rejection, got %v", err)
	}
	if !analyzer.called {
		t.Fatalf("expected analyzer to run before postprocess baseline rejection")
	}
}

func TestExecuteAnalyseClassifiesExternalCacheAliasesIntoRepoBeforeAnalyzerInvocation(t *testing.T) {
	repo := t.TempDir()
	cacheSubdir := filepath.Join(repo, ".cache", "lopper")
	if err := os.MkdirAll(cacheSubdir, 0o750); err != nil {
		t.Fatalf("mkdir cache subdir: %v", err)
	}
	for _, tc := range repoCacheAliasFixtures(repo, cacheSubdir) {
		t.Run(tc.name, func(t *testing.T) {
			cacheAlias := mustSymlinkCacheAlias(t, tc.target)
			analyzer := &fakeAnalyzer{}
			req := DefaultRequest()
			req.Mode = ModeAnalyse
			req.RepoPath = repo
			req.Analyse.Dependency = "lodash"
			req.Analyse.CacheEnabled = true
			req.Analyse.CachePath = cacheAlias
			req.Analyse.IncludePatterns = []string{"**"}

			_, err := (&App{Analyzer: analyzer, Formatter: report.NewFormatter()}).Execute(context.Background(), req)
			assertCLIRepoCacheAliasOutcome(t, err, analyzer, tc.wantReject)
			assertRepoCachePreparationSkipped(t, tc.target, "cache preparation")
		})
	}
}

func TestExecuteAnalysePinsScopedRelativeCachePathAndReusesRepoRoot(t *testing.T) {
	repo := writeScopedCacheFixtureRepo(t)
	application := &App{Analyzer: analysis.NewService(), Formatter: report.NewFormatter()}

	req := DefaultRequest()
	req.Mode = ModeAnalyse
	req.RepoPath = repo
	req.Analyse.Dependency = "lodash"
	req.Analyse.Format = report.FormatJSON
	req.Analyse.CacheEnabled = true
	req.Analyse.CachePath = filepath.Join(".cache", "lopper")
	req.Analyse.IncludePatterns = []string{"src/**"}
	req.Analyse.ExcludePatterns = []string{"vendor/**"}

	first := executeAnalyseReport(t, application, req)
	if first.Cache == nil || first.Cache.Path != filepath.Join(".cache", "lopper") || first.Cache.Misses != 1 || first.Cache.Writes != 1 {
		t.Fatalf("expected first scoped CLI run to write through the repo-root relative cache path, got %#v", first.Cache)
	}
	for _, dir := range []string{"keys", "objects"} {
		if _, err := os.Stat(filepath.Join(repo, ".cache", "lopper", dir)); err != nil {
			t.Fatalf("expected %s dir in repo-root scoped cache path: %v", dir, err)
		}
	}

	second := executeAnalyseReport(t, application, req)
	if second.Cache == nil || second.Cache.Path != filepath.Join(".cache", "lopper") || second.Cache.Hits != 1 || second.Cache.Misses != 0 {
		t.Fatalf("expected second scoped CLI run to reuse the repo-root relative cache path, got %#v", second.Cache)
	}
}

func analyseCanonicalAliasCacheEscapeFixture(t *testing.T) (requestedRepo, canonicalCache, outsideCache string) {
	t.Helper()

	canonicalParent := t.TempDir()
	canonicalRepo := filepath.Join(canonicalParent, "repo")
	if err := os.MkdirAll(canonicalRepo, 0o755); err != nil {
		t.Fatalf("mkdir canonical repo: %v", err)
	}
	canonicalRepo, err := filepath.EvalSymlinks(canonicalRepo)
	if err != nil {
		t.Fatalf("resolve canonical repo: %v", err)
	}
	requestedParent := filepath.Join(t.TempDir(), "requested-parent")
	if err := os.Symlink(canonicalParent, requestedParent); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(canonicalRepo, "tmp")); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	return filepath.Join(requestedParent, "repo"),
		filepath.Join(canonicalRepo, "tmp", "cache"),
		filepath.Join(outside, "cache")
}

type appAlternateRepoAliasEscapeFixture struct {
	name          string
	requestedRepo string
	cachePath     string
	outsideCache  string
}

func appAlternateAbsoluteRepoAliasEscapeFixtures(t *testing.T) []appAlternateRepoAliasEscapeFixture {
	t.Helper()
	repoParent := t.TempDir()
	repo := filepath.Join(repoParent, "repo")
	if err := os.Mkdir(repo, 0o750); err != nil {
		t.Fatalf("mkdir arbitrary-alias repo: %v", err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(repo, "tmp")); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	aliasParent := filepath.Join(t.TempDir(), "alternate-parent")
	if err := os.Symlink(repoParent, aliasParent); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	fixtures := []appAlternateRepoAliasEscapeFixture{{
		name:          "arbitrary alias",
		requestedRepo: repo,
		cachePath:     filepath.Join(aliasParent, "repo", "tmp", "cache"),
		outsideCache:  filepath.Join(outside, "cache"),
	}}

	systemRequestedRepo := t.TempDir()
	systemCanonicalRepo, err := filepath.EvalSymlinks(systemRequestedRepo)
	if err != nil {
		t.Fatalf("resolve system-alias repo: %v", err)
	}
	if filepath.Clean(systemRequestedRepo) == filepath.Clean(systemCanonicalRepo) {
		return fixtures
	}
	systemOutside := t.TempDir()
	if err := os.Symlink(systemOutside, filepath.Join(systemRequestedRepo, "tmp")); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	return append(fixtures, appAlternateRepoAliasEscapeFixture{
		name:          "system absolute alias",
		requestedRepo: systemCanonicalRepo,
		cachePath:     filepath.Join(systemRequestedRepo, "tmp", "cache"),
		outsideCache:  filepath.Join(systemOutside, "cache"),
	})
}

func TestExecuteAnalysePinsScopedDefaultCachePathAndReusesRepoRoot(t *testing.T) {
	repo := writeScopedCacheFixtureRepo(t)
	application := &App{Analyzer: analysis.NewService(), Formatter: report.NewFormatter()}
	canonicalRepo, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatalf("resolve canonical repo path: %v", err)
	}
	expectedPinnedPath := filepath.Join(canonicalRepo, ".lopper-cache")

	req := DefaultRequest()
	req.Mode = ModeAnalyse
	req.RepoPath = repo
	req.Analyse.Dependency = "lodash"
	req.Analyse.Format = report.FormatJSON
	req.Analyse.CacheEnabled = true
	req.Analyse.IncludePatterns = []string{"src/**"}
	req.Analyse.ExcludePatterns = []string{"vendor/**"}

	first := executeAnalyseReport(t, application, req)
	if first.Cache == nil || first.Cache.Path != expectedPinnedPath || first.Cache.Misses != 1 || first.Cache.Writes != 1 {
		t.Fatalf("expected first scoped CLI run to pin and write the repo-root default cache path, got %#v", first.Cache)
	}
	for _, dir := range []string{"keys", "objects"} {
		if _, err := os.Stat(filepath.Join(repo, ".lopper-cache", dir)); err != nil {
			t.Fatalf("expected %s dir in repo-root default cache path: %v", dir, err)
		}
	}

	second := executeAnalyseReport(t, application, req)
	if second.Cache == nil || second.Cache.Path != expectedPinnedPath || second.Cache.Hits != 1 || second.Cache.Misses != 0 {
		t.Fatalf("expected second scoped CLI run to reuse the repo-root default cache path, got %#v", second.Cache)
	}
}

func TestExecuteAnalysePinsRelativeCachePathFromCanonicalDotRepoPath(t *testing.T) {
	repo := writeScopedCacheFixtureRepo(t)
	canonicalRepo := chdirCanonicalWorkspace(t, repo)
	application := &App{Analyzer: analysis.NewService(), Formatter: report.NewFormatter()}

	req := DefaultRequest()
	req.Mode = ModeAnalyse
	req.RepoPath = "."
	req.Analyse.Dependency = "lodash"
	req.Analyse.Format = report.FormatJSON
	req.Analyse.CacheEnabled = true
	req.Analyse.CachePath = filepath.Join(".cache", "lopper")
	req.Analyse.IncludePatterns = []string{"src/**"}

	reportData := executeAnalyseReport(t, application, req)
	if reportData.Cache == nil || reportData.Cache.Path != filepath.Join(".cache", "lopper") || reportData.Cache.Misses != 1 || reportData.Cache.Writes != 1 {
		t.Fatalf("expected scoped CLI run to pin relative cache path from repo dot, got %#v", reportData.Cache)
	}
	for _, dir := range []string{"keys", "objects"} {
		if _, err := os.Stat(filepath.Join(canonicalRepo, ".cache", "lopper", dir)); err != nil {
			t.Fatalf("expected %s dir in canonical repo-root relative cache path: %v", dir, err)
		}
	}
}

func TestExecuteAnalyseScopedAbsoluteCachePathOutsideRepoReusesExternalPath(t *testing.T) {
	repo := writeScopedCacheFixtureRepo(t)
	outsideCache := filepath.Join(t.TempDir(), "cache")
	application := &App{Analyzer: analysis.NewService(), Formatter: report.NewFormatter()}

	req := DefaultRequest()
	req.Mode = ModeAnalyse
	req.RepoPath = repo
	req.Analyse.Dependency = "lodash"
	req.Analyse.Format = report.FormatJSON
	req.Analyse.CacheEnabled = true
	req.Analyse.CachePath = outsideCache
	req.Analyse.IncludePatterns = []string{"src/**"}
	req.Analyse.ExcludePatterns = []string{"vendor/**"}

	first := executeAnalyseReport(t, application, req)
	if first.Cache == nil || first.Cache.Path != outsideCache || first.Cache.Misses != 1 || first.Cache.Writes != 1 {
		t.Fatalf("expected first scoped CLI run to use the external absolute cache path, got %#v", first.Cache)
	}
	for _, dir := range []string{"keys", "objects"} {
		if _, err := os.Stat(filepath.Join(outsideCache, dir)); err != nil {
			t.Fatalf("expected %s dir in external scoped cache path: %v", dir, err)
		}
	}

	second := executeAnalyseReport(t, application, req)
	if second.Cache == nil || second.Cache.Path != outsideCache || second.Cache.Hits != 1 || second.Cache.Misses != 0 {
		t.Fatalf("expected second scoped CLI run to reuse the external absolute cache path, got %#v", second.Cache)
	}
}

func TestPrepareAnalyseCacheOptionsAcceptsAuthenticatedExternalCacheForRepository(t *testing.T) {
	repo := writeScopedCacheFixtureRepo(t)
	repository, err := analysis.ResolveTrustedRepository(repo)
	if err != nil {
		t.Fatalf("authorize repository: %v", err)
	}
	outsideCache := filepath.Join(t.TempDir(), "cache")
	options, err := prepareAnalyseCacheOptions(repo, AnalyseRequest{
		CacheEnabled: true,
		CachePath:    outsideCache,
		repository:   repository,
	})
	if err != nil {
		t.Fatalf("expected authenticated external cache path to be accepted, got %v", err)
	}
	if options == nil || options.Path != outsideCache {
		t.Fatalf("expected authenticated external cache options, got %#v", options)
	}
	if analysis.InRepoCacheOptions(options) {
		t.Fatalf("expected external cache options, got in-repository pin %#v", options)
	}
}

func TestExecuteAnalyseOutputFile(t *testing.T) {
	analyzer := &fakeAnalyzer{
		report: report.Report{
			RepoPath: ".",
			Dependencies: []report.DependencyReport{
				{Name: "lodash", UsedExportsCount: 1, TotalExportsCount: 2, UsedPercent: 50},
			},
		},
	}
	application := &App{Analyzer: analyzer, Formatter: report.NewFormatter()}
	outputPath := filepath.Join(t.TempDir(), "reports", "analyse.json")

	req := DefaultRequest()
	req.Mode = ModeAnalyse
	req.Analyse.TopN = 1
	req.Analyse.Format = report.FormatJSON
	req.Analyse.OutputPath = outputPath

	output, err := application.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf(executeAnalyseErrFmt, err)
	}
	if !strings.Contains(output, outputPath) {
		t.Fatalf("expected output file confirmation, got %q", output)
	}
	data, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read analyse output file: %v", err)
	}
	if !strings.Contains(string(data), `"name": "lodash"`) {
		t.Fatalf("expected analyse JSON content, got %q", string(data))
	}
}

func executeAnalyseReport(t *testing.T, application *App, req Request) report.Report {
	t.Helper()
	output, err := application.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf(executeAnalyseErrFmt, err)
	}
	var reportData report.Report
	if err := json.Unmarshal([]byte(output), &reportData); err != nil {
		t.Fatalf("decode analyse report: %v", err)
	}
	return reportData
}

func writeScopedCacheFixtureRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	writeFixture := func(relPath, content string) {
		path := filepath.Join(repo, relPath)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", relPath, err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", relPath, err)
		}
	}
	writeFixture("package.json", "{\n  \"name\": \"demo\"\n}\n")
	writeFixture(filepath.Join("src", "index.js"), "import { map } from \"lodash\"\nmap([1], (x) => x)\n")
	writeFixture(filepath.Join("node_modules", "lodash", "package.json"), "{\n  \"main\": \"index.js\"\n}\n")
	writeFixture(filepath.Join("node_modules", "lodash", "index.js"), "export function map() {}\n")
	return repo
}

func TestExecuteAnalyseRejectsAbsoluteOutputUnderRequestedRepoSymlinkOutsideWorkingDirectory(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.MkdirAll(filepath.Join(outside, "nested"), 0o755); err != nil {
		t.Fatalf("mkdir outside nested: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(repo, "reports")); err != nil {
		t.Fatalf("create repo reports symlink: %v", err)
	}
	cwd := t.TempDir()
	chdirCanonicalWorkspace(t, cwd)

	analyzer := &fakeAnalyzer{
		report: report.Report{
			RepoPath: repo,
			Dependencies: []report.DependencyReport{
				{Name: "lodash", UsedExportsCount: 1, TotalExportsCount: 2, UsedPercent: 50},
			},
		},
	}
	application := &App{Analyzer: analyzer, Formatter: report.NewFormatter()}
	outputPath := filepath.Join(repo, "reports", "nested", "analyse.json")

	req := DefaultRequest()
	req.Mode = ModeAnalyse
	req.RepoPath = repo
	req.Analyse.TopN = 1
	req.Analyse.Format = report.FormatJSON
	req.Analyse.OutputPath = outputPath

	_, err := application.Execute(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), "output root contains symlink") {
		t.Fatalf("expected repo-root symlink rejection, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(outside, "nested", "analyse.json")); !os.IsNotExist(statErr) {
		t.Fatalf("expected outside analyse output to remain absent, got err=%v", statErr)
	}
}

func TestExecuteAnalyseForwardsRustRecommendationThreshold(t *testing.T) {
	analyzer := &fakeAnalyzer{
		report: report.Report{
			RepoPath: ".",
			Dependencies: []report.DependencyReport{
				{Name: "serde", Language: "rust", UsedExportsCount: 1, TotalExportsCount: 4, UsedPercent: 25},
			},
		},
	}
	application := &App{Analyzer: analyzer, Formatter: report.NewFormatter()}

	req := DefaultRequest()
	req.Mode = ModeAnalyse
	req.Analyse.Language = "rust"
	req.Analyse.Dependency = "serde"
	req.Analyse.Format = report.FormatJSON
	req.Analyse.Thresholds = thresholds.Values{
		FailOnIncreasePercent:             -1,
		MaxUncertainImportCount:           thresholds.DefaultMaxUncertainImportCount,
		LowConfidenceWarningPercent:       35,
		MinUsagePercentForRecommendations: 70,
	}

	if _, err := application.Execute(context.Background(), req); err != nil {
		t.Fatalf(executeAnalyseErrFmt, err)
	}
	if analyzer.lastReq.MinUsagePercentForRecommendations == nil || *analyzer.lastReq.MinUsagePercentForRecommendations != 70 {
		t.Fatalf("expected min-usage threshold to be forwarded for rust analysis, got %#v", analyzer.lastReq.MinUsagePercentForRecommendations)
	}
}

func TestExecuteAnalyseForwardsFeatureFlags(t *testing.T) {
	analyzer := &fakeAnalyzer{
		report: report.Report{
			RepoPath:      ".",
			Dependencies:  []report.DependencyReport{{Name: "rxswift", Language: "swift"}},
			SchemaVersion: "0.1.0",
		},
	}
	application := &App{Analyzer: analyzer, Formatter: report.NewFormatter()}

	registry, err := featureflags.NewRegistry([]featureflags.Flag{{
		Code:      "LOP-FEAT-0001",
		Name:      "swift-carthage",
		Lifecycle: featureflags.LifecyclePreview,
	}})
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	resolved, err := registry.Resolve(featureflags.ResolveOptions{
		Channel: featureflags.ChannelDev,
		Enable:  []string{"swift-carthage"},
	})
	if err != nil {
		t.Fatalf("resolve feature set: %v", err)
	}

	req := DefaultRequest()
	req.Mode = ModeAnalyse
	req.Analyse.TopN = 1
	req.Analyse.Format = report.FormatJSON
	req.Analyse.Features = resolved

	if _, err := application.Execute(context.Background(), req); err != nil {
		t.Fatalf(executeAnalyseErrFmt, err)
	}
	if !analyzer.lastReq.Features.Enabled("swift-carthage") {
		t.Fatalf("expected analyse request features to be forwarded, got %#v", analyzer.lastReq.Features)
	}
}

func TestExecuteAnalyseLockfileDriftWarnPolicy(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "package.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write package.json: %v", err)
	}
	analyzer := &fakeAnalyzer{
		report: report.Report{
			RepoPath: ".",
			Dependencies: []report.DependencyReport{
				{Name: "lodash", UsedExportsCount: 1, TotalExportsCount: 2, UsedPercent: 50},
			},
		},
	}
	application := &App{Analyzer: analyzer, Formatter: report.NewFormatter()}

	req := DefaultRequest()
	req.Mode = ModeAnalyse
	req.RepoPath = repo
	req.Analyse.TopN = 1
	req.Analyse.Format = report.FormatJSON
	req.Analyse.Thresholds.LockfileDriftPolicy = "warn"

	output, err := application.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("execute analyse with lockfile drift warn: %v", err)
	}
	if !strings.Contains(output, "lockfile drift detected") {
		t.Fatalf("expected lockfile drift warning in output, got %q", output)
	}
}

func TestExecuteAnalyseLockfileDriftWarnPolicyToleratesOversizedManifestInspection(t *testing.T) {
	output, analyzer, err := executeAnalyseWithOversizedManifestInspection(t, "warn")
	if err != nil {
		t.Fatalf("execute analyse with oversized lockfile drift warn: %v", err)
	}
	if !analyzer.called {
		t.Fatalf("expected warn-mode analysis to continue after oversized manifest inspection")
	}
	if !strings.Contains(output, "unable to safely inspect manifest during lockfile drift analysis") {
		t.Fatalf("expected oversized manifest warning in output, got %q", output)
	}
	if !strings.Contains(output, "file exceeds size limit") {
		t.Fatalf("expected oversized manifest size-limit detail in output, got %q", output)
	}
}

func TestExecuteAnalyseLockfileDriftWarnPolicyPreservesEarlierDirectoryDriftBeforeOversizedManifestInspection(t *testing.T) {
	output, analyzer, err := executeAnalyseWithLockfileDriftSetup(t, "warn", func(repo string) {
		writeFile(t, filepath.Join(repo, "a-drift", manifestFileName), demoPackageJSON)
		writeFile(t, filepath.Join(repo, "a-drift", lockfileName), demoPackageJSON)
		writeFile(t, filepath.Join(repo, "z-oversized", pyprojectManifestName), oversizedManifestBody("[tool.poetry]\nname = \"demo\"\n", "# filler\n", 8))
		writeFile(t, filepath.Join(repo, "z-oversized", poetryLockName), "# lock\n")
		initGitRepo(t, repo)
		writeFile(t, filepath.Join(repo, "a-drift", manifestFileName), demoPackageJSONUpdated)
	})
	if err != nil {
		t.Fatalf("execute analyse with earlier drift plus oversized manifest warn: %v", err)
	}
	if !analyzer.called {
		t.Fatalf("expected warn-mode analysis to continue after earlier drift plus oversized manifest inspection")
	}
	if !strings.Contains(output, "a-drift: package.json changed while no matching lockfile changed") {
		t.Fatalf("expected earlier lockfile drift warning in output, got %q", output)
	}
	if !strings.Contains(output, "unable to safely inspect manifest during lockfile drift analysis") {
		t.Fatalf("expected oversized manifest warning in output, got %q", output)
	}
}

func TestExecuteAnalyseLockfileDriftWarnPolicyPreservesSameDirectoryDriftBeforeOversizedManifestInspection(t *testing.T) {
	output, analyzer, err := executeAnalyseWithLockfileDriftSetup(t, "warn", func(repo string) {
		writeFile(t, filepath.Join(repo, manifestFileName), demoPackageJSON)
		writeFile(t, filepath.Join(repo, lockfileName), demoPackageJSON)
		writeFile(t, filepath.Join(repo, pyprojectManifestName), oversizedManifestBody("[tool.poetry]\nname = \"demo\"\n", "# filler\n", 8))
		writeFile(t, filepath.Join(repo, poetryLockName), "# lock\n")
		initGitRepo(t, repo)
		writeFile(t, filepath.Join(repo, manifestFileName), demoPackageJSONUpdated)
	})
	if err != nil {
		t.Fatalf("execute analyse with same-directory drift plus oversized manifest warn: %v", err)
	}
	if !analyzer.called {
		t.Fatalf("expected warn-mode analysis to continue after same-directory drift plus oversized manifest inspection")
	}
	if !strings.Contains(output, "package.json changed while no matching lockfile changed") {
		t.Fatalf("expected same-directory lockfile drift warning in output, got %q", output)
	}
	if !strings.Contains(output, "unable to safely inspect manifest during lockfile drift analysis") {
		t.Fatalf("expected oversized manifest warning in output, got %q", output)
	}
}

func TestExecuteAnalyseLockfileDriftFailPolicy(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module example.com/demo\n\ngo 1.22\n\nrequire github.com/some/dep v1.0.0\n"), 0o600); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	analyzer := &fakeAnalyzer{
		report: report.Report{
			Dependencies: []report.DependencyReport{
				{Name: "dep", UsedExportsCount: 1, TotalExportsCount: 2, UsedPercent: 50},
			},
		},
	}
	application := &App{Analyzer: analyzer, Formatter: report.NewFormatter()}

	req := DefaultRequest()
	req.Mode = ModeAnalyse
	req.RepoPath = repo
	req.Analyse.TopN = 1
	req.Analyse.Thresholds.LockfileDriftPolicy = "fail"

	_, err := application.Execute(context.Background(), req)
	if !errors.Is(err, ErrLockfileDrift) {
		t.Fatalf("expected ErrLockfileDrift, got %v", err)
	}
	if analyzer.called {
		t.Fatalf("expected pre-analysis lockfile check to fail before analyzer execution")
	}
}

func TestExecuteAnalyseLockfileDriftFailPolicyRejectsOversizedManifestInspection(t *testing.T) {
	_, analyzer, err := executeAnalyseWithOversizedManifestInspection(t, "fail")
	if !errors.Is(err, safeio.ErrFileTooLarge) {
		t.Fatalf("expected oversized manifest inspection to remain fatal in fail mode, got %v", err)
	}
	if analyzer.called {
		t.Fatalf("expected fail-mode oversized manifest inspection to stop before analyzer execution")
	}
}

func executeAnalyseWithOversizedManifestInspection(t *testing.T, policy string) (string, *fakeAnalyzer, error) {
	return executeAnalyseWithLockfileDriftSetup(t, policy, func(repo string) {
		if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte(oversizedGoModManifestBody()), 0o600); err != nil {
			t.Fatalf("write go.mod: %v", err)
		}
	})
}

func newAnalyseLockfileDriftTestApp() (*App, *fakeAnalyzer) {
	analyzer := &fakeAnalyzer{
		report: report.Report{
			RepoPath: ".",
			Dependencies: []report.DependencyReport{
				{Name: "dep", UsedExportsCount: 1, TotalExportsCount: 2, UsedPercent: 50},
			},
		},
	}
	return &App{Analyzer: analyzer, Formatter: report.NewFormatter()}, analyzer
}

func executeAnalyseWithLockfileDriftSetup(t *testing.T, policy string, setup func(repo string)) (string, *fakeAnalyzer, error) {
	t.Helper()

	repo := t.TempDir()
	setup(repo)
	application, analyzer := newAnalyseLockfileDriftTestApp()

	req := DefaultRequest()
	req.Mode = ModeAnalyse
	req.RepoPath = repo
	req.Analyse.TopN = 1
	req.Analyse.Format = report.FormatJSON
	req.Analyse.Thresholds.LockfileDriftPolicy = policy

	output, err := application.Execute(context.Background(), req)
	return output, analyzer, err
}

func oversizedGoModManifestBody() string {
	return "module example.com/demo\n\ngo 1.22\n\nrequire github.com/some/dep v1.0.0\n" +
		strings.Repeat("// filler\n", oversizedLockfileDriftManifestBytes/10)
}

func TestExecuteAnalyseReturnsFormattedOutputWhenSaveBaselineValidationFails(t *testing.T) {
	analyzer := &fakeAnalyzer{
		report: report.Report{
			RepoPath:      ".",
			Dependencies:  []report.DependencyReport{{Name: "dep", UsedExportsCount: 1, TotalExportsCount: 2, UsedPercent: 50}},
			SchemaVersion: "0.1.0",
		},
	}
	application := &App{Analyzer: analyzer, Formatter: report.NewFormatter()}

	req := DefaultRequest()
	req.Mode = ModeAnalyse
	req.Analyse.TopN = 1
	req.Analyse.Format = report.FormatJSON
	req.Analyse.SaveBaseline = true

	output, err := application.Execute(context.Background(), req)
	if err == nil {
		t.Fatalf("expected save-baseline validation error")
	}
	if !strings.Contains(err.Error(), saveBaselineStoreErr) {
		t.Fatalf("unexpected save-baseline error: %v", err)
	}
	if !strings.Contains(output, "\"dependencies\"") {
		t.Fatalf("expected formatted output to be returned alongside error, got %q", output)
	}
}

func TestExecuteAnalyseReturnsFormatterErrorWhenNoPriorError(t *testing.T) {
	analyzer := &fakeAnalyzer{
		report: report.Report{
			RepoPath:     ".",
			Dependencies: []report.DependencyReport{{Name: "dep", UsedExportsCount: 1, TotalExportsCount: 2, UsedPercent: 50}},
		},
	}
	application := &App{Analyzer: analyzer, Formatter: report.NewFormatter()}

	req := DefaultRequest()
	req.Mode = ModeAnalyse
	req.Analyse.TopN = 1
	req.Analyse.Format = report.Format("invalid")

	_, err := application.Execute(context.Background(), req)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "unknown format") {
		t.Fatalf("expected formatter error, got %v", err)
	}
}

func TestExecuteAnalyseApplyBaselineErrorPreservesOriginalWhenFormatFails(t *testing.T) {
	analyzer := &fakeAnalyzer{
		report: report.Report{
			RepoPath: ".",
			Dependencies: []report.DependencyReport{
				{Name: "dep", UsedExportsCount: 1, TotalExportsCount: 2, UsedPercent: 50},
			},
		},
	}
	application := &App{Analyzer: analyzer, Formatter: report.NewFormatter()}
	req := DefaultRequest()
	req.Mode = ModeAnalyse
	req.Analyse.TopN = 1
	req.Analyse.Format = report.Format("invalid")
	req.Analyse.BaselinePath = filepath.Join(t.TempDir(), missingBaselineFileName)

	_, err := application.Execute(context.Background(), req)
	if err == nil {
		t.Fatalf("expected apply-baseline error")
	}
	if strings.Contains(strings.ToLower(err.Error()), "unknown format") {
		t.Fatalf("expected original baseline error, got %v", err)
	}
}

func TestExecuteAnalyseFailOnIncreasePreservesOriginalWhenFormatFails(t *testing.T) {
	delta := 5.0
	analyzer := &fakeAnalyzer{
		report: report.Report{
			RepoPath:             ".",
			WasteIncreasePercent: &delta,
			Dependencies:         []report.DependencyReport{{Name: "dep", UsedExportsCount: 1, TotalExportsCount: 2, UsedPercent: 50}},
		},
	}
	application := &App{Analyzer: analyzer, Formatter: report.NewFormatter()}
	req := DefaultRequest()
	req.Mode = ModeAnalyse
	req.Analyse.TopN = 1
	req.Analyse.Format = report.Format("invalid")
	req.Analyse.Thresholds.FailOnIncreasePercent = 1

	_, err := application.Execute(context.Background(), req)
	if !errors.Is(err, ErrFailOnIncrease) {
		t.Fatalf("expected ErrFailOnIncrease, got %v", err)
	}
}

func TestExecuteAnalyseSaveBaselineErrorPreservesOriginalWhenFormatFails(t *testing.T) {
	analyzer := &fakeAnalyzer{
		report: report.Report{
			RepoPath: ".",
			Dependencies: []report.DependencyReport{
				{Name: "dep", UsedExportsCount: 1, TotalExportsCount: 2, UsedPercent: 50},
			},
		},
	}
	application := &App{Analyzer: analyzer, Formatter: report.NewFormatter()}
	req := DefaultRequest()
	req.Mode = ModeAnalyse
	req.Analyse.TopN = 1
	req.Analyse.Format = report.Format("invalid")
	req.Analyse.SaveBaseline = true

	_, err := application.Execute(context.Background(), req)
	if err == nil {
		t.Fatalf("expected save-baseline error")
	}
	if !strings.Contains(err.Error(), saveBaselineStoreErr) {
		t.Fatalf("expected save-baseline store error, got %v", err)
	}
}

func TestExecuteAnalyseBootstrapBaselineStoreOnFirstSave(t *testing.T) {
	analyzer := &fakeAnalyzer{
		report: report.Report{
			RepoPath:      ".",
			Dependencies:  []report.DependencyReport{{Name: "dep", UsedExportsCount: 1, TotalExportsCount: 2, UsedPercent: 50}},
			SchemaVersion: "0.1.0",
		},
	}
	application := &App{Analyzer: analyzer, Formatter: report.NewFormatter()}
	baselineStore := t.TempDir()

	req := DefaultRequest()
	req.Mode = ModeAnalyse
	req.RepoPath = "."
	req.Analyse.TopN = 1
	req.Analyse.Format = report.FormatJSON
	req.Analyse.SaveBaseline = true
	req.Analyse.BaselineStorePath = baselineStore

	output, err := application.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("expected bootstrap execute to succeed, got %v", err)
	}
	if !strings.Contains(output, "saved immutable baseline snapshot:") {
		t.Fatalf("expected baseline save warning in output, got %q", output)
	}
	if _, err := os.Stat(report.BaselineSnapshotPath(baselineStore, resolveCurrentBaselineKey("."))); err != nil {
		t.Fatalf("expected initial baseline snapshot to be written: %v", err)
	}
}

func TestExecuteAnalyseRetainsAuthorizedRepositoryAcrossPostprocess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("open-directory replacement semantics are not available on Windows")
	}
	t.Run("advisory load", testExecuteAnalyseRetainsAuthorizedRepositoryAcrossAdvisoryLoad)
	t.Run("baseline save uses repo a commit", testExecuteAnalyseRetainsAuthorizedRepositoryAcrossBaselineSave)
	t.Run("codemod writes and rollback stay on repo a", testExecuteAnalyseRetainsAuthorizedRepositoryAcrossCodemod)
}

func testExecuteAnalyseRetainsAuthorizedRepositoryAcrossAdvisoryLoad(t *testing.T) {
	repo, repoB, movedRepoA := setupRetargetedRepoPair(t)
	mustMkdirAll(t, filepath.Join(repo, "security"))
	mustMkdirAll(t, filepath.Join(repoB, "security"))
	writeTextFile(t, filepath.Join(repoB, indexJSFile), "repo-b must remain unchanged\n", 0o644)
	advisoryRelPath := filepath.Join("security", "advisories.yml")
	writeTextFile(t, filepath.Join(repo, advisoryRelPath), "advisories:\n  - id: GHSA-repo-a\n    package: lodash\n    ecosystem: npm\n    severity: high\n", 0o600)
	writeTextFile(t, filepath.Join(repoB, advisoryRelPath), "{malformed repo-b advisories", 0o600)
	analyzer := newRetargetingMutationAnalyzer(repo, movedRepoA, repoB, report.Report{
		SchemaVersion: report.SchemaVersion,
		RepoPath:      repo,
		Dependencies:  []report.DependencyReport{{Name: "lodash", Language: "js-ts", UsedExportsCount: 1, TotalExportsCount: 1, UsedPercent: 100}},
	})
	req := DefaultRequest()
	req.Mode = ModeAnalyse
	req.RepoPath = repo
	req.Analyse.Dependency = "lodash"
	req.Analyse.Format = report.FormatJSON
	req.Analyse.CacheEnabled = false
	req.Analyse.AdvisorySourcePath = advisoryRelPath
	req.Analyse.Features = mustResolveAppTestFeatures(t, report.ReachabilityVulnerabilityPrioritizationPreviewFeature)
	output, err := (&App{Analyzer: analyzer, Formatter: report.NewFormatter()}).executeAnalyse(context.Background(), req)
	if err != nil {
		t.Fatalf("execute analyse with advisory swap: %v", err)
	}
	if !strings.Contains(output, "GHSA-repo-a") {
		t.Fatalf("expected advisory annotation from moved repo A, got %q", output)
	}
}

func testExecuteAnalyseRetainsAuthorizedRepositoryAcrossBaselineSave(t *testing.T) {
	repo, repoB, movedRepoA := setupRetargetedRepoPair(t)
	writeTextFile(t, filepath.Join(repoB, indexJSFile), "repo-b must remain unchanged\n", 0o644)
	writeTextFile(t, filepath.Join(repoB, "identity.txt"), "repo-b\n", 0o644)
	testutil.RunGit(t, repoB, "init")
	testutil.RunGit(t, repoB, "config", "user.email", "test@example.com")
	testutil.RunGit(t, repoB, "config", "user.name", "Test User")
	testutil.RunGit(t, repoB, "add", ".")
	testutil.RunGit(t, repoB, "commit", "-m", "repo-b fixture")
	repoBCommit, err := workspace.CurrentCommitSHA(repoB)
	if err != nil {
		t.Fatalf("repo B commit: %v", err)
	}
	repoACommit, err := workspace.CurrentCommitSHA(repo)
	if err != nil {
		t.Fatalf("repo A commit: %v", err)
	}
	if repoACommit == repoBCommit {
		t.Fatal("expected distinct repo A and repo B commits")
	}
	analyzer := newRetargetingMutationAnalyzer(repo, movedRepoA, repoB, report.Report{
		SchemaVersion: report.SchemaVersion,
		GeneratedAt:   testTime(),
		RepoPath:      repo,
		Dependencies:  []report.DependencyReport{{Name: "lodash", UsedExportsCount: 1, TotalExportsCount: 1, UsedPercent: 100}},
	})
	req := DefaultRequest()
	req.Mode = ModeAnalyse
	req.RepoPath = repo
	req.Analyse.TopN = 1
	req.Analyse.Format = report.FormatJSON
	req.Analyse.CacheEnabled = false
	req.Analyse.SaveBaseline = true
	req.Analyse.BaselineStorePath = filepath.Join(".artifacts", "baselines")
	if _, err := (&App{Analyzer: analyzer, Formatter: report.NewFormatter()}).executeAnalyse(context.Background(), req); err != nil {
		t.Fatalf("execute analyse with baseline save swap: %v", err)
	}
	catalog, err := report.ListBaselineSnapshots(filepath.Join(movedRepoA, ".artifacts", "baselines"), 10)
	if err != nil {
		t.Fatalf("list moved repo A snapshots: %v", err)
	}
	if len(catalog.Snapshots) != 1 || catalog.Snapshots[0].Key != "commit:"+repoACommit {
		t.Fatalf("expected moved repo A snapshot keyed by repo A commit, got %#v", catalog.Snapshots)
	}
	unexpectedSnapshot := report.BaselineSnapshotPath(filepath.Join(repo, ".artifacts", "baselines"), "commit:"+repoBCommit)
	if _, err := os.Stat(unexpectedSnapshot); !os.IsNotExist(err) {
		t.Fatalf("expected replacement repo B snapshot to remain absent, stat err=%v", err)
	}
}

func testExecuteAnalyseRetainsAuthorizedRepositoryAcrossCodemod(t *testing.T) {
	repo, repoB, movedRepoA := setupRetargetedRepoPair(t)
	writeTextFile(t, filepath.Join(repo, indexJSFile), importLodashLineWithLF, 0o644)
	writeTextFile(t, filepath.Join(repoB, indexJSFile), "repo-b must remain unchanged\n", 0o644)
	analyzer := newRetargetingMutationAnalyzer(repo, movedRepoA, repoB, singleLodashSuggestionReport(indexJSFile))
	req := DefaultRequest()
	req.Mode = ModeAnalyse
	req.RepoPath = repo
	req.Analyse.Dependency = "lodash"
	req.Analyse.ApplyCodemod = true
	req.Analyse.AllowDirty = true
	req.Analyse.Format = report.FormatJSON
	req.Analyse.CacheEnabled = false
	output, err := (&App{Analyzer: analyzer, Formatter: report.NewFormatter()}).executeAnalyse(context.Background(), req)
	if err != nil {
		t.Fatalf("execute analyse with codemod swap: %v", err)
	}
	if !strings.Contains(readTextFile(t, filepath.Join(movedRepoA, indexJSFile)), "lodash/map") {
		t.Fatalf("expected codemod write in moved repo A")
	}
	if got := readTextFile(t, filepath.Join(repo, indexJSFile)); got != "repo-b must remain unchanged\n" {
		t.Fatalf("expected repo B source to remain untouched, got %q", got)
	}
	if !strings.Contains(output, ".artifacts/lopper-codemod-backups/") {
		t.Fatalf("expected rollback artifact path in output, got %q", output)
	}
	if _, err := os.Stat(filepath.Join(repo, ".artifacts")); !os.IsNotExist(err) {
		t.Fatalf("expected replacement repo B to receive no rollback artifacts, stat err=%v", err)
	}
}

func setupRetargetedRepoPair(t *testing.T) (repo string, repoB string, movedRepoA string) {
	t.Helper()
	repo, _ = setupMCPGitLodashFixture(t)
	parent := filepath.Dir(repo)
	repoB = filepath.Join(parent, filepath.Base(repo)+"-repo-b")
	if err := os.Mkdir(repoB, 0o750); err != nil {
		t.Fatalf("mkdir repo B: %v", err)
	}
	movedRepoA = filepath.Join(parent, filepath.Base(repo)+"-repo-a-original")
	return repo, repoB, movedRepoA
}

func newRetargetingMutationAnalyzer(repo, movedRepoA, repoB string, rep report.Report) *retargetingMutationAnalyzer {
	return &retargetingMutationAnalyzer{
		repo:      repo,
		movedRepo: movedRepoA,
		repoB:     repoB,
		report:    rep,
	}
}

func TestExecuteAnalyseCapturesGitSensitiveStateBeforeEarliestViewHook(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("open-directory replacement semantics are not available on Windows")
	}
	t.Run("dirty repo a is not replaced by clean repo b", testExecuteAnalyseCapturesDirtyRepoStateBeforeSwap)
	t.Run("empty repo a commit is captured and never replaced by repo b head", testExecuteAnalyseCapturesEmptyRepoCommitBeforeSwap)
	t.Run("repo a lockfile drift is not replaced by repo b state", testExecuteAnalyseCapturesLockfileDriftBeforeSwap)
}

type repoCacheAliasFixture struct {
	name       string
	target     string
	wantReject bool
}

func repoCacheAliasFixtures(repo, cacheSubdir string) []repoCacheAliasFixture {
	return []repoCacheAliasFixture{
		{name: "repo root", target: repo, wantReject: true},
		{name: "repo subdir", target: cacheSubdir},
	}
}

func mustSymlinkCacheAlias(t *testing.T, target string) string {
	t.Helper()
	cacheAlias := filepath.Join(t.TempDir(), "external-cache-alias")
	if err := os.Symlink(target, cacheAlias); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	return cacheAlias
}

func assertCLIRepoCacheAliasOutcome(t *testing.T, err error, analyzer *fakeAnalyzer, wantReject bool) {
	t.Helper()
	if wantReject {
		if err == nil || !strings.Contains(err.Error(), "scoped analysis does not allow cachePath at the repository root") {
			t.Fatalf("expected external repo-root alias rejection, got %v", err)
		}
		if analyzer.called {
			t.Fatalf("expected analyzer to remain uncalled")
		}
		return
	}
	if err != nil {
		t.Fatalf("execute with external alias to repo subdir: %v", err)
	}
	if !analyzer.called || !analysis.InRepoCacheOptions(analyzer.lastReq.Cache) {
		t.Fatalf("expected analyzer to receive an opaque in-repo cache pin, request=%#v", analyzer.lastReq)
	}
}

func assertRepoCachePreparationSkipped(t *testing.T, target, label string) {
	t.Helper()
	for _, dir := range []string{"keys", "objects"} {
		if _, statErr := os.Stat(filepath.Join(target, dir)); !os.IsNotExist(statErr) {
			t.Fatalf("expected %s not to create %s under %s, stat err=%v", label, dir, target, statErr)
		}
	}
}

func testExecuteAnalyseCapturesDirtyRepoStateBeforeSwap(t *testing.T) {
	repo, _ := setupMCPGitLodashFixture(t)
	writeTextFile(t, filepath.Join(repo, "README.md"), "dirty repo a\n", 0o600)
	repoB := filepath.Join(filepath.Dir(repo), filepath.Base(repo)+"-repo-b")
	if err := os.Mkdir(repoB, 0o750); err != nil {
		t.Fatalf("mkdir repo B: %v", err)
	}
	writeTextFile(t, filepath.Join(repoB, "identity.txt"), "clean repo b\n", 0o600)
	initGitRepo(t, repoB)
	restoreSwap := installRepositorySwap(t, repo, repoB)

	req := DefaultRequest()
	req.Mode = ModeAnalyse
	req.RepoPath = repo
	req.Analyse.Dependency = "lodash"
	req.Analyse.ApplyCodemod = true
	req.Analyse.CacheEnabled = false
	_, err := (&App{Analyzer: &fakeAnalyzer{}, Formatter: report.NewFormatter()}).executeAnalyse(context.Background(), req)
	restoreSwap()
	if !errors.Is(err, ErrDirtyWorktree) {
		t.Fatalf("expected repo A dirty-worktree rejection, got %v", err)
	}
}

func testExecuteAnalyseCapturesEmptyRepoCommitBeforeSwap(t *testing.T) {
	parent := t.TempDir()
	repo := filepath.Join(parent, "repo")
	repoB := filepath.Join(parent, "repo-b")
	createRepos(t, repo, repoB)
	writeTextFile(t, filepath.Join(repo, "identity.txt"), "non-git repo a\n", 0o600)
	writeTextFile(t, filepath.Join(repoB, "identity.txt"), "git repo b\n", 0o600)
	initGitRepo(t, repoB)
	repoBCommit := resolveCurrentBaselineKey(repoB)
	restoreSwap := installRepositorySwap(t, repo, repoB)

	req := DefaultRequest()
	req.Mode = ModeAnalyse
	req.RepoPath = repo
	req.Analyse.TopN = 1
	req.Analyse.Format = report.FormatJSON
	req.Analyse.CacheEnabled = false
	req.Analyse.SaveBaseline = true
	req.Analyse.BaselineStorePath = filepath.Join(repo, ".artifacts", "baselines")
	analyzer := &fakeAnalyzer{report: report.Report{SchemaVersion: report.SchemaVersion, RepoPath: repo}}
	_, err := (&App{Analyzer: analyzer, Formatter: report.NewFormatter()}).executeAnalyse(context.Background(), req)
	restoreSwap()
	if err == nil || !strings.Contains(err.Error(), "baseline key is required") {
		t.Fatalf("expected captured empty repo A commit, got %v", err)
	}
	if _, statErr := os.Stat(report.BaselineSnapshotPath(filepath.Join(repo, ".artifacts", "baselines"), repoBCommit)); !os.IsNotExist(statErr) {
		t.Fatalf("replacement repo B received a commit-keyed baseline: %v", statErr)
	}
}

func testExecuteAnalyseCapturesLockfileDriftBeforeSwap(t *testing.T) {
	parent := t.TempDir()
	repo := filepath.Join(parent, "repo")
	repoB := filepath.Join(parent, "repo-b")
	createRepos(t, repo, repoB)
	writeTextFile(t, filepath.Join(repo, "package.json"), "{\"dependencies\":{\"lodash\":\"1.0.0\"}}\n", 0o600)
	writeTextFile(t, filepath.Join(repoB, "identity.txt"), "clean repo b\n", 0o600)
	restoreSwap := installRepositorySwap(t, repo, repoB)

	req := DefaultRequest()
	req.Mode = ModeAnalyse
	req.RepoPath = repo
	req.Analyse.CacheEnabled = false
	req.Analyse.Thresholds.LockfileDriftPolicy = "fail"
	_, err := (&App{Analyzer: &fakeAnalyzer{}, Formatter: report.NewFormatter()}).executeAnalyse(context.Background(), req)
	restoreSwap()
	if !errors.Is(err, ErrLockfileDrift) {
		t.Fatalf("expected captured repo A lockfile drift, got %v", err)
	}
}

func createRepos(t *testing.T, paths ...string) {
	t.Helper()
	for _, path := range paths {
		if err := os.Mkdir(path, 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
	}
}

func installRepositorySwap(t *testing.T, repo, repoB string) func() {
	t.Helper()
	movedRepoA := filepath.Join(filepath.Dir(repo), filepath.Base(repo)+"-repo-a-original")
	restore := analysis.SetRepositoryViewHandleOpenedHookForTest(repositorySwapHook(repo, movedRepoA, repoB))
	t.Cleanup(restore)
	return restore
}

func TestExecuteAnalysePersistsAbsoluteInRepoOutputThroughRetainedView(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("open-directory replacement semantics are not available on Windows")
	}
	parent := t.TempDir()
	repo := filepath.Join(parent, "repo")
	repoB := filepath.Join(parent, "repo-b")
	for _, path := range []string{repo, repoB} {
		if err := os.Mkdir(path, 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
	}
	writeTextFile(t, filepath.Join(repo, "identity.txt"), "repo a\n", 0o600)
	writeTextFile(t, filepath.Join(repoB, "identity.txt"), "repo b\n", 0o600)
	movedRepoA := filepath.Join(parent, "repo-a-original")
	analyzer := &retargetingMutationAnalyzer{
		repo:      repo,
		movedRepo: movedRepoA,
		repoB:     repoB,
		report:    report.Report{SchemaVersion: report.SchemaVersion, RepoPath: repo},
	}
	outputPath := filepath.Join(repo, "reports", "analyse.json")
	req := DefaultRequest()
	req.Mode = ModeAnalyse
	req.RepoPath = repo
	req.Analyse.Format = report.FormatJSON
	req.Analyse.OutputPath = outputPath
	req.Analyse.CacheEnabled = false

	status, err := (&App{Analyzer: analyzer, Formatter: report.NewFormatter()}).executeAnalyse(context.Background(), req)
	if err != nil {
		t.Fatalf("execute analyse with output swap: %v", err)
	}
	if status != "analyse report written to "+outputPath {
		t.Fatalf("unexpected output status %q", status)
	}
	if _, err := os.Stat(filepath.Join(movedRepoA, "reports", "analyse.json")); err != nil {
		t.Fatalf("expected output in moved repo A: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, "reports", "analyse.json")); !os.IsNotExist(err) {
		t.Fatalf("replacement repo B received analyse output: %v", err)
	}
}

func TestExecuteAnalyseRetainsSamePathHeadAndConfigFromOpenedView(t *testing.T) {
	repo, _ := setupMCPGitLodashFixture(t)
	configPath := filepath.Join(repo, ".lopper.yml")
	writeTextFile(t, configPath, "thresholds:\n  low_confidence_warning_percent: 13\n", 0o600)
	testutil.RunGit(t, repo, "add", ".lopper.yml")
	testutil.RunGit(t, repo, "commit", "-m", "config A")
	commitA, err := workspace.CurrentCommitSHA(repo)
	if err != nil {
		t.Fatalf("resolve commit A: %v", err)
	}

	writeTextFile(t, configPath, "thresholds:\n  low_confidence_warning_percent: 91\n", 0o600)
	testutil.RunGit(t, repo, "add", ".lopper.yml")
	testutil.RunGit(t, repo, "commit", "-m", "config B")
	commitB, err := workspace.CurrentCommitSHA(repo)
	if err != nil {
		t.Fatalf("resolve commit B: %v", err)
	}
	testutil.RunGit(t, repo, "checkout", "--detach", commitA)

	viewOpens := 0
	restoreOpen := analysis.SetRepositoryViewHandleOpenedHookForTest(func() error {
		viewOpens++
		testutil.RunGit(t, repo, "checkout", "--detach", commitB)
		return nil
	})
	t.Cleanup(restoreOpen)
	viewCloses := 0
	restoreClose := analysis.SetRepositoryViewCloseHookForTest(func() error {
		viewCloses++
		return nil
	})
	t.Cleanup(restoreClose)

	analyzer := &snapshotConfigAnalyzer{
		report: report.Report{
			SchemaVersion: report.SchemaVersion,
			GeneratedAt:   testTime(),
			RepoPath:      repo,
			Dependencies:  []report.DependencyReport{{Name: "lodash", UsedExportsCount: 1, TotalExportsCount: 1, UsedPercent: 100}},
		},
	}
	baselineStore := filepath.Join(repo, ".artifacts", "baselines")
	req := DefaultRequest()
	req.Mode = ModeAnalyse
	req.RepoPath = repo
	req.Analyse.TopN = 1
	req.Analyse.ConfigPath = configPath
	req.Analyse.Format = report.FormatJSON
	req.Analyse.CacheEnabled = false
	req.Analyse.SaveBaseline = true
	req.Analyse.BaselineStorePath = baselineStore
	req.Analyse.Thresholds.LockfileDriftPolicy = "off"

	if _, err := (&App{Analyzer: analyzer, Formatter: report.NewFormatter()}).executeAnalyse(context.Background(), req); err != nil {
		t.Fatalf("execute same-path drift analysis: %v", err)
	}
	if analyzer.config != "thresholds:\n  low_confidence_warning_percent: 13\n" {
		t.Fatalf("analyzer config = %q, want config A", analyzer.config)
	}
	if liveConfig := readTextFile(t, configPath); liveConfig != "thresholds:\n  low_confidence_warning_percent: 91\n" {
		t.Fatalf("live config = %q, want config B after hook", liveConfig)
	}
	catalog, err := report.ListBaselineSnapshots(baselineStore, 10)
	if err != nil {
		t.Fatalf("list saved baselines: %v", err)
	}
	snapshots := catalog.Snapshots
	if len(snapshots) != 1 || snapshots[0].Key != "commit:"+commitA {
		t.Fatalf("saved baselines = %#v, want commit A", snapshots)
	}
	if snapshots[0].Key == "commit:"+commitB {
		t.Fatal("same-path commit B retargeted baseline key")
	}
	if viewOpens != 1 || viewCloses != 1 {
		t.Fatalf("repository view opens/closes = %d/%d, want 1/1", viewOpens, viewCloses)
	}
}

type snapshotConfigAnalyzer struct {
	report report.Report
	config string
}

func (a *snapshotConfigAnalyzer) Analyse(_ context.Context, req analysis.Request) (report.Report, error) {
	snapshotConfigPath, err := req.RepositoryView.SnapshotPath(req.ConfigPath)
	if err != nil {
		return report.Report{}, err
	}
	config, err := os.ReadFile(snapshotConfigPath)
	if err != nil {
		return report.Report{}, err
	}
	a.config = string(config)
	return a.report, nil
}

func repositorySwapHook(repo, movedRepo, replacementRepo string) func() error {
	return func() error {
		if err := os.Rename(repo, movedRepo); err != nil {
			return err
		}
		return os.Rename(replacementRepo, repo)
	}
}

func TestCompleteAnalyseExecutionPrefersRunErrorOverFeatureValidationError(t *testing.T) {
	application := &App{Formatter: report.NewFormatter()}
	runErr := errors.New("post-stage failed")
	req := AnalyseRequest{Format: report.FormatCycloneDX}

	_, err := application.completeAnalyseExecution(context.Background(), "", req, report.Report{}, runErr)
	if !errors.Is(err, runErr) {
		t.Fatalf("expected run error to win, got %v", err)
	}
}

func TestCompleteAnalyseExecutionReturnsFeatureValidationErrorWithoutRunError(t *testing.T) {
	application := &App{Formatter: report.NewFormatter()}
	req := AnalyseRequest{Format: report.FormatCycloneDX}

	_, err := application.completeAnalyseExecution(context.Background(), "", req, report.Report{}, nil)
	if err == nil || !strings.Contains(err.Error(), "requires --enable-feature") {
		t.Fatalf("expected feature validation error, got %v", err)
	}
}

func TestCompleteAnalyseExecutionPrefersRunErrorOverFormatError(t *testing.T) {
	application := &App{Formatter: report.NewFormatter()}
	runErr := errors.New("post-stage failed")
	req := AnalyseRequest{Format: report.Format("weird")}

	_, err := application.completeAnalyseExecution(context.Background(), "", req, report.Report{}, runErr)
	if !errors.Is(err, runErr) {
		t.Fatalf("expected run error to win, got %v", err)
	}
}

func TestCompleteAnalyseExecutionReturnsFormatErrorWithoutRunError(t *testing.T) {
	application := &App{Formatter: report.NewFormatter()}
	req := AnalyseRequest{Format: report.Format("weird")}

	_, err := application.completeAnalyseExecution(context.Background(), "", req, report.Report{}, nil)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "unknown format") {
		t.Fatalf("expected format error, got %v", err)
	}
}

func TestCompleteAnalyseExecutionReturnsPersistError(t *testing.T) {
	application := &App{Formatter: report.NewFormatter()}
	workspace := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(workspace, "reports")); err != nil {
		t.Fatalf("create reports symlink: %v", err)
	}
	req := AnalyseRequest{
		Format:     report.FormatJSON,
		OutputPath: filepath.Join(workspace, "reports", "analyse.json"),
	}

	_, err := application.completeAnalyseExecution(context.Background(), workspace, req, report.Report{}, nil)
	if err == nil || !strings.Contains(err.Error(), "output root contains symlink") {
		t.Fatalf("expected persist error, got %v", err)
	}
}
