package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ben-ranford/lopper/internal/analysis"
	"github.com/ben-ranford/lopper/internal/mcp"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/testutil"
	"github.com/ben-ranford/lopper/internal/thresholds"
)

const (
	mcpLodashPackageJSON = "{\n  \"main\": \"index.js\",\n  \"exports\": {\n    \".\": \"./index.js\",\n    \"./map\": \"./map.js\"\n  }\n}\n"
	mcpMapSource         = "import { map } from \"lodash\";\nmap([1], (x) => x)\n"
	mcpPythonSource      = "import requests\nprint('ok')\n"
)

type mcpTestResponse struct {
	Result *struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		StructuredContent json.RawMessage `json:"structuredContent"`
		IsError           bool            `json:"isError,omitempty"`
	} `json:"result,omitempty"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func TestExecuteMCPApplyCodemodMutation(t *testing.T) {
	repo, sourcePath := setupMCPGitLodashFixture(t)

	response := executeMCPTool(t, "lopper_apply_codemod", map[string]any{
		"repoPath":      repo,
		"dependency":    "lodash",
		"confirmApply":  true,
		"cacheEnabled":  false,
		"timeoutMillis": 10000,
	})
	if response.Result == nil || response.Result.IsError {
		t.Fatalf("expected successful codemod mutation, got %#v", response)
	}

	var payload struct {
		AppliedFiles   int                         `json:"appliedFiles"`
		AppliedPatches int                         `json:"appliedPatches"`
		BackupPath     string                      `json:"backupPath"`
		Results        []report.CodemodApplyResult `json:"results"`
	}
	decodeMCPStructuredContent(t, response, &payload)
	if payload.AppliedFiles != 1 || payload.AppliedPatches != 1 || len(payload.Results) == 0 {
		t.Fatalf("unexpected codemod payload: %#v", payload)
	}
	if payload.BackupPath == "" {
		t.Fatalf("expected rollback artifact path in structured payload")
	}
	if _, err := os.Stat(filepath.Join(repo, filepath.FromSlash(payload.BackupPath))); err != nil {
		t.Fatalf("expected rollback artifact to exist: %v", err)
	}
	if got := readTextFile(t, sourcePath); !strings.Contains(got, "import map from \"lodash/map\";") {
		t.Fatalf("expected source rewrite, got %q", got)
	}
}

func TestExecuteMCPApplyPythonCodemodStableDefault(t *testing.T) {
	repo, sourcePath := setupMCPGitPythonFixture(t)

	response := executeMCPTool(t, "lopper_apply_codemod", map[string]any{
		"repoPath":      repo,
		"dependency":    "requests",
		"language":      "python",
		"confirmApply":  true,
		"cacheEnabled":  false,
		"timeoutMillis": 10000,
	})
	if response.Result == nil || response.Result.IsError {
		t.Fatalf("expected successful Python codemod mutation, got %#v", response)
	}

	var payload struct {
		AppliedFiles   int    `json:"appliedFiles"`
		AppliedPatches int    `json:"appliedPatches"`
		BackupPath     string `json:"backupPath"`
	}
	decodeMCPStructuredContent(t, response, &payload)
	if payload.AppliedFiles != 1 || payload.AppliedPatches != 1 || payload.BackupPath == "" {
		t.Fatalf("unexpected Python codemod payload: %#v", payload)
	}
	if got := readTextFile(t, sourcePath); got != "print('ok')\n" {
		t.Fatalf("expected stable-default Python source rewrite, got %q", got)
	}
}

func TestExecuteMCPSaveBaselineMutation(t *testing.T) {
	repo, _ := setupMCPGitLodashFixture(t)

	response := executeMCPTool(t, "lopper_save_baseline", map[string]any{
		"repoPath":          repo,
		"topN":              1,
		"baselineStorePath": ".artifacts/lopper-baselines",
		"baselineLabel":     "nightly",
		"confirmSave":       true,
		"cacheEnabled":      false,
		"timeoutMillis":     10000,
	})
	if response.Result == nil || response.Result.IsError {
		t.Fatalf("expected successful baseline save, got %#v", response)
	}

	var payload struct {
		BaselineKey   string          `json:"baselineKey"`
		SnapshotPath  string          `json:"snapshotPath"`
		ReportSummary *report.Summary `json:"reportSummary"`
	}
	decodeMCPStructuredContent(t, response, &payload)
	if payload.BaselineKey != "label:nightly" || payload.SnapshotPath == "" {
		t.Fatalf("unexpected baseline payload: %#v", payload)
	}
	if payload.ReportSummary == nil || payload.ReportSummary.DependencyCount == 0 {
		t.Fatalf("expected report summary in payload, got %#v", payload.ReportSummary)
	}
	if _, err := os.Stat(payload.SnapshotPath); err != nil {
		t.Fatalf("expected baseline snapshot to exist: %v", err)
	}
}

func TestExecuteMCPSaveDashboardBaselineMutation(t *testing.T) {
	repo, _ := setupMCPGitLodashFixture(t)

	response := executeMCPTool(t, "lopper_save_dashboard_baseline", map[string]any{
		"repoPath":          repo,
		"repos":             []map[string]any{{"name": "fixture", "path": repo, "language": "js-ts"}},
		"topN":              1,
		"baselineStorePath": ".artifacts/lopper-dashboard-baselines",
		"baselineLabel":     "nightly",
		"confirmSave":       true,
		"timeoutMillis":     10000,
	})
	if response.Result == nil || response.Result.IsError {
		t.Fatalf("expected successful dashboard baseline save, got %#v", response)
	}

	var payload struct {
		BaselineKey      string `json:"baselineKey"`
		SnapshotPath     string `json:"snapshotPath"`
		DashboardSummary struct {
			TotalRepos int `json:"total_repos"`
		} `json:"dashboardSummary"`
	}
	decodeMCPStructuredContent(t, response, &payload)
	if payload.BaselineKey != "label:nightly" || payload.SnapshotPath == "" || payload.DashboardSummary.TotalRepos != 1 {
		t.Fatalf("unexpected dashboard baseline payload: %#v", payload)
	}
	if _, err := os.Stat(payload.SnapshotPath); err != nil {
		t.Fatalf("expected dashboard baseline snapshot to exist: %v", err)
	}
}

func TestExecuteMCPBaselineMutationsWithExplicitKeys(t *testing.T) {
	repo, _ := setupMCPGitLodashFixture(t)

	baselineResponse := executeMCPTool(t, "lopper_save_baseline", map[string]any{
		"repoPath":          repo,
		"topN":              1,
		"baselineStorePath": ".artifacts/explicit-baselines",
		"baselineKey":       "manual",
		"confirmSave":       true,
		"cacheEnabled":      false,
		"timeoutMillis":     10000,
	})
	if baselineResponse.Result == nil || baselineResponse.Result.IsError {
		t.Fatalf("expected successful explicit-key baseline save, got %#v", baselineResponse)
	}
	var baselinePayload struct {
		BaselineKey  string `json:"baselineKey"`
		SnapshotPath string `json:"snapshotPath"`
	}
	decodeMCPStructuredContent(t, baselineResponse, &baselinePayload)
	if baselinePayload.BaselineKey != "manual" || baselinePayload.SnapshotPath == "" {
		t.Fatalf("unexpected explicit-key baseline payload: %#v", baselinePayload)
	}
	if _, err := os.Stat(baselinePayload.SnapshotPath); err != nil {
		t.Fatalf("expected explicit-key baseline snapshot to exist: %v", err)
	}

	dashboardResponse := executeMCPTool(t, "lopper_save_dashboard_baseline", map[string]any{
		"repoPath":          repo,
		"repos":             []map[string]any{{"name": "fixture", "path": repo, "language": "js-ts"}},
		"topN":              1,
		"baselineStorePath": ".artifacts/explicit-dashboard-baselines",
		"baselineKey":       "manual-dashboard",
		"confirmSave":       true,
		"timeoutMillis":     10000,
	})
	if dashboardResponse.Result == nil || dashboardResponse.Result.IsError {
		t.Fatalf("expected successful explicit-key dashboard baseline save, got %#v", dashboardResponse)
	}
	var dashboardPayload struct {
		BaselineKey  string `json:"baselineKey"`
		SnapshotPath string `json:"snapshotPath"`
	}
	decodeMCPStructuredContent(t, dashboardResponse, &dashboardPayload)
	if dashboardPayload.BaselineKey != "manual-dashboard" || dashboardPayload.SnapshotPath == "" {
		t.Fatalf("unexpected explicit-key dashboard payload: %#v", dashboardPayload)
	}
	if _, err := os.Stat(dashboardPayload.SnapshotPath); err != nil {
		t.Fatalf("expected explicit-key dashboard snapshot to exist: %v", err)
	}
}

func TestExecuteMCPMutationRejectsDirtyWorktree(t *testing.T) {
	repo, sourcePath := setupMCPGitLodashFixture(t)
	writeTextFile(t, filepath.Join(repo, "README.md"), "dirty\n", 0o644)

	response := executeMCPTool(t, "lopper_apply_codemod", map[string]any{
		"repoPath":      repo,
		"dependency":    "lodash",
		"confirmApply":  true,
		"cacheEnabled":  false,
		"timeoutMillis": 10000,
	})
	if response.Result == nil || !response.Result.IsError {
		t.Fatalf("expected dirty worktree rejection, got %#v", response)
	}
	if !strings.Contains(response.Result.Content[0].Text, "clean git worktree") {
		t.Fatalf("expected clean-worktree error, got %#v", response.Result.Content)
	}
	if got := readTextFile(t, sourcePath); got != mcpMapSource {
		t.Fatalf("expected source to remain unchanged, got %q", got)
	}
}

func TestExecuteMCPMutationUsesCapturedRepoACleanlinessAfterSwap(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("open-directory replacement semantics are not available on Windows")
	}
	repo, _ := setupMCPGitLodashFixture(t)
	writeTextFile(t, filepath.Join(repo, "README.md"), "dirty repo a\n", 0o600)
	repoB := filepath.Join(filepath.Dir(repo), filepath.Base(repo)+"-repo-b")
	if err := os.Mkdir(repoB, 0o750); err != nil {
		t.Fatalf("mkdir repo B: %v", err)
	}
	writeTextFile(t, filepath.Join(repoB, "identity.txt"), "clean repo b\n", 0o600)
	initGitRepo(t, repoB)
	movedRepoA := filepath.Join(filepath.Dir(repo), filepath.Base(repo)+"-repo-a-original")
	viewOpens := 0
	restore := analysis.SetRepositoryViewHandleOpenedHookForTest(func() error {
		viewOpens++
		return repositorySwapHook(repo, movedRepoA, repoB)()
	})
	t.Cleanup(restore)
	viewCloses := 0
	restoreClose := analysis.SetRepositoryViewCloseHookForTest(func() error {
		viewCloses++
		return nil
	})
	t.Cleanup(restoreClose)

	response := executeMCPTool(t, "lopper_apply_codemod", map[string]any{
		"repoPath":     repo,
		"dependency":   "lodash",
		"confirmApply": true,
		"cacheEnabled": false,
	})
	if response.Result == nil || !response.Result.IsError {
		t.Fatalf("expected captured dirty repo A rejection, got %#v", response)
	}
	if !strings.Contains(response.Result.Content[0].Text, "clean git worktree") {
		t.Fatalf("expected dirty-worktree error, got %#v", response.Result.Content)
	}
	if viewOpens != 1 {
		t.Fatalf("repository view opens = %d, want 1", viewOpens)
	}
	if viewCloses != 1 {
		t.Fatalf("repository view closes = %d, want 1", viewCloses)
	}
}

func TestExecuteMCPMutationTransfersSinglePolicyViewAcrossRepositorySwap(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("open-directory replacement semantics are not available on Windows")
	}
	repo, _ := setupMCPGitLodashFixture(t)
	writeTextFile(t, filepath.Join(repo, ".lopper.yml"), "thresholds:\n  low_confidence_warning_percent: 17\n", 0o600)
	testutil.RunGit(t, repo, "add", ".lopper.yml")
	testutil.RunGit(t, repo, "commit", "-m", "add policy")

	parent := filepath.Dir(repo)
	repoB := filepath.Join(parent, filepath.Base(repo)+"-repo-b")
	if err := os.Mkdir(repoB, 0o750); err != nil {
		t.Fatalf("mkdir repo B: %v", err)
	}
	writeTextFile(t, filepath.Join(repoB, indexJSFile), "repo-b must remain unchanged\n", 0o644)
	movedRepoA := filepath.Join(parent, filepath.Base(repo)+"-repo-a-original")
	viewOpens := 0
	restore := analysis.SetRepositoryViewHandleOpenedHookForTest(func() error {
		viewOpens++
		if viewOpens != 1 {
			return nil
		}
		if err := os.Rename(repo, movedRepoA); err != nil {
			return err
		}
		return os.Rename(repoB, repo)
	})
	t.Cleanup(restore)
	viewCloses := 0
	restoreClose := analysis.SetRepositoryViewCloseHookForTest(func() error {
		viewCloses++
		return nil
	})
	t.Cleanup(restoreClose)

	response := executeMCPTool(t, "lopper_apply_codemod", map[string]any{
		"repoPath":      repo,
		"dependency":    "lodash",
		"confirmApply":  true,
		"cacheEnabled":  false,
		"timeoutMillis": 10000,
	})
	if response.Result == nil || response.Result.IsError {
		t.Fatalf("expected successful retained-view mutation, got %#v", response)
	}
	if viewOpens != 1 {
		t.Fatalf("repository view opens = %d, want 1", viewOpens)
	}
	if viewCloses != 1 {
		t.Fatalf("repository view closes = %d, want 1", viewCloses)
	}
	var payload struct {
		BackupPath string `json:"backupPath"`
	}
	decodeMCPStructuredContent(t, response, &payload)
	if !strings.Contains(readTextFile(t, filepath.Join(movedRepoA, indexJSFile)), "lodash/map") {
		t.Fatal("expected codemod write in moved repo A")
	}
	if got := readTextFile(t, filepath.Join(repo, indexJSFile)); got != "repo-b must remain unchanged\n" {
		t.Fatalf("replacement repo B source changed: %q", got)
	}
	if payload.BackupPath == "" {
		t.Fatal("expected rollback artifact path")
	}
	if _, err := os.Stat(filepath.Join(movedRepoA, filepath.FromSlash(payload.BackupPath))); err != nil {
		t.Fatalf("expected rollback artifact in moved repo A: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, ".artifacts")); !os.IsNotExist(err) {
		t.Fatalf("replacement repo B received artifacts: %v", err)
	}
}

func TestMCPMutationPinnedCachePathSurvivesSymlinkRetargetBeforeFirstWrite(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory replacement semantics are covered on Unix")
	}

	repo, _ := setupMCPGitLodashFixture(t)
	allowedTarget := filepath.Join(repo, "allowed-target")
	redirectedTarget := t.TempDir()
	if err := os.MkdirAll(allowedTarget, 0o755); err != nil {
		t.Fatalf("mkdir allowed target: %v", err)
	}
	linkPath := filepath.Join(repo, "allowed-link")
	if err := os.Symlink(filepath.Base(allowedTarget), linkPath); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	cacheOptions, err := analysis.ResolveTrustedCacheOptions(repo, &analysis.CacheOptions{
		Enabled: true,
		Path:    filepath.Join("allowed-link", "cache"),
	})
	if err != nil {
		t.Fatalf("resolve trusted cache options: %v", err)
	}
	req := newMCPAnalyseRequest(mcp.AnalysisMutationRequest{
		RepoPath:   repo,
		Dependency: "lodash",
		Language:   "js-ts",
		Cache:      cacheOptions,
	})
	req.Analyse.Thresholds = thresholds.Defaults()

	if err := os.Remove(linkPath); err != nil {
		t.Fatalf("remove original symlink: %v", err)
	}
	if err := os.Symlink(redirectedTarget, linkPath); err != nil {
		t.Fatalf("retarget cache symlink: %v", err)
	}

	application := &App{Analyzer: analysis.NewService(), Formatter: report.NewFormatter()}
	if _, err := application.executeAnalyse(context.Background(), req); err != nil {
		t.Fatalf("execute analyse with pinned cache path: %v", err)
	}
	if _, err := os.Stat(filepath.Join(allowedTarget, "cache", "keys")); err != nil {
		t.Fatalf("expected pinned keys dir in original target: %v", err)
	}
	if _, err := os.Stat(filepath.Join(allowedTarget, "cache", "objects")); err != nil {
		t.Fatalf("expected pinned objects dir in original target: %v", err)
	}
	if _, err := os.Stat(filepath.Join(redirectedTarget, "cache")); !os.IsNotExist(err) {
		t.Fatalf("expected retargeted outside cache root to remain absent, got err=%v", err)
	}
}

func TestMCPMutationStagesRemainBoundAfterRepositoryReplacement(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("open-directory replacement semantics are not available on Windows")
	}
	t.Run("codemod and rollback", testMCPMutationCodemodAndRollbackRemainBound)
	t.Run("baseline write", testMCPMutationBaselineWriteRemainsBound)
}

func testMCPMutationCodemodAndRollbackRemainBound(t *testing.T) {
	repo, _ := setupMCPGitLodashFixture(t)
	parent := filepath.Dir(repo)
	repoB := filepath.Join(parent, filepath.Base(repo)+"-repo-b")
	if err := os.Mkdir(repoB, 0o750); err != nil {
		t.Fatalf("mkdir repo B: %v", err)
	}
	writeTextFile(t, filepath.Join(repoB, indexJSFile), "repo-b must remain unchanged\n", 0o644)
	movedRepoA := filepath.Join(parent, filepath.Base(repo)+"-repo-a-original")
	repository, err := analysis.ResolveTrustedRepository(repo)
	if err != nil {
		t.Fatalf("authorize repo A: %v", err)
	}
	cacheOptions, err := analysis.ResolveTrustedCacheOptionsForRepository(repository, &analysis.CacheOptions{Enabled: false})
	if err != nil {
		t.Fatalf("resolve disabled cache options: %v", err)
	}
	analyzer := &retargetingMutationAnalyzer{
		repo:      repo,
		movedRepo: movedRepoA,
		repoB:     repoB,
		report:    singleLodashSuggestionReport(indexJSFile),
	}
	runner := &appMCPMutationRunner{app: &App{Analyzer: analyzer, Formatter: report.NewFormatter()}}
	reportData, err := runner.ApplyCodemod(context.Background(), mcp.AnalysisMutationRequest{
		RepoPath:   repo,
		Repository: repository,
		Dependency: "lodash",
		Language:   "js-ts",
		Cache:      cacheOptions,
		Thresholds: thresholds.Defaults(),
		AllowDirty: false,
	})
	if err != nil {
		t.Fatalf("apply codemod after repository replacement: %v", err)
	}
	if analyzer.lastRequest.RepositoryView == nil {
		t.Fatalf("expected analyzer request to retain repository view")
	}
	if got := readTextFile(t, filepath.Join(movedRepoA, indexJSFile)); !strings.Contains(got, "lodash/map") {
		t.Fatalf("expected codemod write in moved repo A, got %q", got)
	}
	if got := readTextFile(t, filepath.Join(repo, indexJSFile)); got != "repo-b must remain unchanged\n" {
		t.Fatalf("expected repo B source to remain untouched, got %q", got)
	}
	apply := requireCodemodApplyReport(t, reportData)
	if apply.BackupPath == "" {
		t.Fatalf("expected rooted rollback artifact, report=%#v", reportData)
	}
	if _, err := os.Stat(filepath.Join(movedRepoA, filepath.FromSlash(apply.BackupPath))); err != nil {
		t.Fatalf("expected rollback artifact in moved repo A: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, ".artifacts")); !os.IsNotExist(err) {
		t.Fatalf("expected replacement repo B to receive no artifacts, stat err=%v", err)
	}
}

func testMCPMutationBaselineWriteRemainsBound(t *testing.T) {
	parent := t.TempDir()
	repo := filepath.Join(parent, "repo")
	if err := os.Mkdir(repo, 0o750); err != nil {
		t.Fatalf("mkdir repo A: %v", err)
	}
	writeTextFile(t, filepath.Join(repo, "identity.txt"), "repo-a\n", 0o644)
	repoB := filepath.Join(parent, "repo-b")
	if err := os.Mkdir(repoB, 0o750); err != nil {
		t.Fatalf("mkdir repo B: %v", err)
	}
	writeTextFile(t, filepath.Join(repoB, "identity.txt"), "repo-b\n", 0o644)
	repoBBaseline := report.BaselineSnapshotPath(filepath.Join(repoB, ".artifacts", "baselines"), "manual-retarget")
	if err := os.MkdirAll(filepath.Dir(repoBBaseline), 0o750); err != nil {
		t.Fatalf("mkdir repo B baseline trap: %v", err)
	}
	writeTextFile(t, repoBBaseline, "{malformed repo-b baseline", 0o600)
	movedRepoA := filepath.Join(parent, "repo-a-original")
	repository, err := analysis.ResolveTrustedRepository(repo)
	if err != nil {
		t.Fatalf("authorize repo A: %v", err)
	}
	cacheOptions, err := analysis.ResolveTrustedCacheOptionsForRepository(repository, &analysis.CacheOptions{Enabled: false})
	if err != nil {
		t.Fatalf("resolve disabled cache options: %v", err)
	}
	analyzer := &retargetingMutationAnalyzer{
		repo:      repo,
		movedRepo: movedRepoA,
		repoB:     repoB,
		report: report.Report{
			SchemaVersion: report.SchemaVersion,
			RepoPath:      repo,
			Dependencies:  []report.DependencyReport{{Name: "dep", UsedExportsCount: 1, TotalExportsCount: 1, UsedPercent: 100}},
		},
	}
	runner := &appMCPMutationRunner{app: &App{Analyzer: analyzer, Formatter: report.NewFormatter()}}
	storePath := filepath.Join(repo, ".artifacts", "baselines")
	_, _, err = runner.SaveBaseline(context.Background(), mcp.AnalysisMutationRequest{
		RepoPath:          repo,
		Repository:        repository,
		TopN:              1,
		Language:          "js-ts",
		Cache:             cacheOptions,
		Thresholds:        thresholds.Defaults(),
		BaselineStorePath: storePath,
		BaselineKey:       "manual-retarget",
	})
	if err != nil {
		t.Fatalf("save baseline after repository replacement: %v", err)
	}
	expectedSnapshot := report.BaselineSnapshotPath(filepath.Join(movedRepoA, ".artifacts", "baselines"), "manual-retarget")
	if _, err := os.Stat(expectedSnapshot); err != nil {
		t.Fatalf("expected baseline in moved repo A: %v", err)
	}
	if got := readTextFile(t, filepath.Join(repo, ".artifacts", "baselines", filepath.Base(repoBBaseline))); got != "{malformed repo-b baseline" {
		t.Fatalf("expected replacement repo B baseline trap to remain untouched, got %q", got)
	}
}

func TestRepositoryAwareAnalysisAndBaselineGuardBranches(t *testing.T) {
	removedRepo := filepath.Join(t.TempDir(), "removed-repo")
	if err := os.Mkdir(removedRepo, 0o750); err != nil {
		t.Fatalf("mkdir removable repository: %v", err)
	}
	removedAuthorization, err := analysis.ResolveTrustedRepository(removedRepo)
	if err != nil {
		t.Fatalf("authorize removable repository: %v", err)
	}
	if err := os.Remove(removedRepo); err != nil {
		t.Fatalf("remove authorized repository: %v", err)
	}
	executeRequest := DefaultRequest()
	executeRequest.RepoPath = removedRepo
	executeRequest.Analyse.repository = removedAuthorization
	if _, err := (&App{}).executeAnalyse(context.Background(), executeRequest); err == nil {
		t.Fatal("expected removed authorized repository rejection before analysis")
	}

	repo := t.TempDir()
	repository, err := analysis.ResolveTrustedRepository(repo)
	if err != nil {
		t.Fatalf("authorize repository: %v", err)
	}
	view, err := analysis.OpenTrustedRepository(context.Background(), repository, repo, nil)
	if err != nil {
		t.Fatalf("open trusted repository: %v", err)
	}
	t.Cleanup(func() {
		if err := view.Close(); err != nil {
			t.Errorf("close trusted repository: %v", err)
		}
	})
	now := time.Date(2026, time.July, 26, 0, 0, 0, 0, time.UTC)
	reportData := report.Report{SchemaVersion: report.SchemaVersion, RepoPath: repo}
	application := &App{}

	externalStore := filepath.Join(t.TempDir(), "baselines")
	externalRequest := AnalyseRequest{
		SaveBaseline:      true,
		BaselineStorePath: externalStore,
		BaselineKey:       "external",
	}
	if _, err := application.saveBaselineIfNeededWithRepository(reportData, repo, externalRequest, now, view); err != nil {
		t.Fatalf("save genuine external baseline with repository view: %v", err)
	}
	if _, err := os.Stat(report.BaselineSnapshotPath(externalStore, "external")); err != nil {
		t.Fatalf("inspect genuine external baseline: %v", err)
	}

	inRepoRequest := AnalyseRequest{
		SaveBaseline:      true,
		BaselineStorePath: filepath.Join(repo, ".artifacts", "baselines"),
	}
	if _, err := application.saveBaselineIfNeededWithRepository(reportData, repo, inRepoRequest, now, view); err == nil {
		t.Fatal("expected missing rooted baseline key rejection")
	}
	inRepoRequest.BaselineKey = "rooted"
	if _, err := application.saveBaselineIfNeededWithRepository(reportData, repo, inRepoRequest, now, view); err != nil {
		t.Fatalf("save rooted baseline: %v", err)
	}
	if _, err := application.saveBaselineIfNeededWithRepository(reportData, repo, inRepoRequest, now, view); err == nil {
		t.Fatal("expected immutable rooted baseline duplicate rejection")
	}

	if err := view.Close(); err != nil {
		t.Fatalf("close trusted repository before write: %v", err)
	}
	inRepoRequest.BaselineKey = "closed"
	if _, err := application.saveBaselineIfNeededWithRepository(reportData, repo, inRepoRequest, now, view); err == nil {
		t.Fatal("expected rooted baseline write through closed repository view to fail")
	}
}

type retargetingMutationAnalyzer struct {
	repo        string
	movedRepo   string
	repoB       string
	report      report.Report
	lastRequest analysis.Request
}

func (a *retargetingMutationAnalyzer) Analyse(_ context.Context, req analysis.Request) (report.Report, error) {
	a.lastRequest = req
	if err := os.Rename(a.repo, a.movedRepo); err != nil {
		return report.Report{}, err
	}
	if err := os.Rename(a.repoB, a.repo); err != nil {
		return report.Report{}, err
	}
	return a.report, nil
}

func TestExecuteMCPReadOnlyToolRejectsMutationArguments(t *testing.T) {
	repo, _ := setupMCPGitLodashFixture(t)
	response := executeMCPTool(t, "lopper_analyse_dependency", map[string]any{
		"repoPath":          repo,
		"dependency":        "lodash",
		"saveBaseline":      true,
		"baselineStorePath": ".artifacts/should-not-exist",
		"cacheEnabled":      false,
	})
	if response.Result == nil || !response.Result.IsError {
		t.Fatalf("expected read-only mutation argument rejection, got %#v", response)
	}
	if !strings.Contains(response.Result.Content[0].Text, "unknown field") {
		t.Fatalf("expected unknown field error, got %#v", response.Result.Content)
	}
	if _, err := os.Stat(filepath.Join(repo, ".artifacts")); !os.IsNotExist(err) {
		t.Fatalf("expected read-only tool not to create artifacts, stat err=%v", err)
	}
}

func TestMCPMutationRunnerHelperBranches(t *testing.T) {
	var nilApp *App
	if nilApp.mcpMutationRunner() != nil {
		t.Fatalf("expected nil app to have no MCP mutation runner")
	}

	if _, err := decodeMCPCommandReport[report.Report]("{", nil); err == nil {
		t.Fatalf("expected invalid JSON output to fail when command succeeded")
	}
	if got := savedSnapshotPath([]string{"unrelated warning"}, baselineSaveWarningPrefix); got != "" {
		t.Fatalf("expected no saved snapshot path, got %q", got)
	}
}

func TestMCPMutationCaptureDoesNotOverwriteAuthorizedCapturedState(t *testing.T) {
	codemodErr := errors.New("captured codemod sentinel")
	lockfileErr := errors.New("captured lockfile sentinel")
	request := mcp.AnalysisMutationRequest{
		RepoPath:                    t.TempDir(),
		CurrentBaselineKey:          "commit:authorized",
		CurrentBaselineKeyCaptured:  true,
		ApplyCodemod:                true,
		CodemodPrecondition:         codemodErr,
		CodemodPreconditionCaptured: true,
		LockfileWarnings:            []string{"captured warning"},
		LockfileDriftErr:            lockfileErr,
		LockfileDriftCaptured:       true,
		Thresholds:                  thresholds.Defaults(),
	}
	captured, err := (&appMCPMutationRunner{}).CaptureAnalysisState(context.Background(), request)
	if err != nil {
		t.Fatalf("capture analysis state: %v", err)
	}
	if captured.CurrentBaselineKey != request.CurrentBaselineKey || !captured.CurrentBaselineKeyCaptured {
		t.Fatalf("current commit capture was overwritten: %#v", captured)
	}
	if !errors.Is(captured.CodemodPrecondition, codemodErr) || !captured.CodemodPreconditionCaptured {
		t.Fatalf("codemod capture was overwritten: %#v", captured.CodemodPrecondition)
	}
	if !errors.Is(captured.LockfileDriftErr, lockfileErr) || !captured.LockfileDriftCaptured ||
		len(captured.LockfileWarnings) != 1 || captured.LockfileWarnings[0] != request.LockfileWarnings[0] {
		t.Fatalf("lockfile capture was overwritten: %#v", captured)
	}
}

func executeMCPTool(t *testing.T, toolName string, arguments map[string]any) mcpTestResponse {
	t.Helper()
	var input bytes.Buffer
	writeMCPTestFrame(t, &input, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      toolName,
			"arguments": arguments,
		},
	})

	var output bytes.Buffer
	application := New(&output, &input)
	req := DefaultRequest()
	req.Mode = ModeMCP
	req.MCP.Features = mustMCPMutationFeatureSet(t)
	if _, err := application.Execute(context.Background(), req); err != nil {
		t.Fatalf("execute MCP server: %v", err)
	}

	return readMCPTestResponse(t, output.Bytes())
}

func writeMCPTestFrame(t *testing.T, writer *bytes.Buffer, value any) {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal MCP request: %v", err)
	}
	if _, err := fmt.Fprintf(writer, "Content-Length: %d\r\n\r\n", len(payload)); err != nil {
		t.Fatalf("write MCP header: %v", err)
	}
	if _, err := writer.Write(payload); err != nil {
		t.Fatalf("write MCP payload: %v", err)
	}
}

func readMCPTestResponse(t *testing.T, data []byte) mcpTestResponse {
	t.Helper()
	reader := bufio.NewReader(bytes.NewReader(data))
	contentLength := -1
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read MCP response header: %v", err)
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(strings.ToLower(key)) != "content-length" {
			continue
		}
		contentLength, err = strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			t.Fatalf("parse content length: %v", err)
		}
	}
	if contentLength < 0 {
		t.Fatalf("missing content length in MCP response %q", string(data))
	}
	payload := make([]byte, contentLength)
	if _, err := io.ReadFull(reader, payload); err != nil {
		t.Fatalf("read MCP response payload: %v", err)
	}
	var response mcpTestResponse
	if err := json.Unmarshal(payload, &response); err != nil {
		t.Fatalf("decode MCP response: %v\n%s", err, string(payload))
	}
	return response
}

func decodeMCPStructuredContent(t *testing.T, response mcpTestResponse, target any) {
	t.Helper()
	if response.Result == nil {
		t.Fatalf("missing MCP result: %#v", response)
	}
	if err := json.Unmarshal(response.Result.StructuredContent, target); err != nil {
		t.Fatalf("decode structured content: %v\n%s", err, string(response.Result.StructuredContent))
	}
}

func setupMCPGitLodashFixture(t *testing.T) (string, string) {
	t.Helper()
	repo := t.TempDir()
	sourcePath := filepath.Join(repo, indexJSFile)
	writeTextFile(t, sourcePath, mcpMapSource, 0o644)

	dependencyRoot := filepath.Join(repo, "node_modules", "lodash")
	mustMkdirAll(t, dependencyRoot)
	writeTextFile(t, filepath.Join(dependencyRoot, "package.json"), mcpLodashPackageJSON, 0o644)
	writeTextFile(t, filepath.Join(dependencyRoot, "index.js"), "export { map } from './map.js'\n", 0o644)
	writeTextFile(t, filepath.Join(dependencyRoot, "map.js"), "export default function map() {}\n", 0o644)

	testutil.RunGit(t, repo, "init")
	testutil.RunGit(t, repo, "config", "user.email", "test@example.com")
	testutil.RunGit(t, repo, "config", "user.name", "Test User")
	testutil.RunGit(t, repo, "add", ".")
	testutil.RunGit(t, repo, "commit", "-m", "fixture")

	return repo, sourcePath
}

func setupMCPGitPythonFixture(t *testing.T) (string, string) {
	t.Helper()
	repo := t.TempDir()
	sourcePath := filepath.Join(repo, "main.py")
	writeTextFile(t, sourcePath, mcpPythonSource, 0o644)
	writeTextFile(t, filepath.Join(repo, "requirements.txt"), "requests==2.32.0\n", 0o644)

	testutil.RunGit(t, repo, "init")
	testutil.RunGit(t, repo, "config", "user.email", "test@example.com")
	testutil.RunGit(t, repo, "config", "user.name", "Test User")
	testutil.RunGit(t, repo, "add", ".")
	testutil.RunGit(t, repo, "commit", "-m", "fixture")

	return repo, sourcePath
}
