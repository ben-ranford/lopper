package mcp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/dashboard"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestRunCodemodApplyToolReturnsStructuredPayload(t *testing.T) {
	repo := t.TempDir()
	applyReport := &report.CodemodApplyReport{
		AppliedFiles:   2,
		AppliedPatches: 3,
		SkippedFiles:   1,
		SkippedPatches: 1,
		FailedFiles:    0,
		FailedPatches:  0,
		BackupPath:     filepath.Join(repo, ".lopper-backups", "codemod"),
		Results: []report.CodemodApplyResult{{
			File:       "src/app.ts",
			Status:     "applied",
			PatchCount: 3,
		}},
	}
	rep := sampleReport(repo)
	rep.Dependencies[0].Codemod = &report.CodemodReport{Mode: "apply", Apply: applyReport}
	runner := &fakeMutationRunner{applyReport: rep}
	server := NewServer(Options{Features: mustMutationFeatureSet(t, true), MutationRunner: runner})

	result := callToolResult(t, server, toolApplyCodemod, map[string]any{
		"repoPath":     repo,
		"dependency":   "lodash",
		"confirmApply": true,
		"allowDirty":   true,
		"include":      []string{"src/**"},
		"cacheEnabled": false,
	})
	if result.IsError {
		t.Fatalf("unexpected codemod apply error: %#v", result)
	}
	if !runner.applyCalled {
		t.Fatalf("expected mutation runner to be called")
	}
	if !runner.lastApply.AllowDirty || runner.lastApply.Dependency != "lodash" {
		t.Fatalf("unexpected mutation request: %#v", runner.lastApply)
	}
	if !slicesEqual(runner.lastApply.IncludePatterns, []string{"src/**"}) {
		t.Fatalf("expected include patterns to be passed through, got %#v", runner.lastApply.IncludePatterns)
	}

	payload, ok := result.StructuredContent.(codemodApplyPayload)
	if !ok {
		t.Fatalf("expected codemod payload, got %#v", result.StructuredContent)
	}
	if payload.AppliedFiles != 2 || payload.AppliedPatches != 3 || payload.SkippedFiles != 1 {
		t.Fatalf("unexpected codemod counts: %#v", payload)
	}
	if payload.BackupPath == "" || len(payload.Results) != 1 {
		t.Fatalf("expected backup and per-file results, got %#v", payload)
	}
	if !strings.Contains(payload.Summary, "2 files changed") {
		t.Fatalf("unexpected codemod summary: %q", payload.Summary)
	}
}

func TestRunBaselineSaveToolReturnsStructuredPayload(t *testing.T) {
	repo := t.TempDir()
	rep := sampleReport(repo)
	runner := &fakeMutationRunner{baselineReport: rep}
	server := NewServer(Options{Features: mustMutationFeatureSet(t, true), MutationRunner: runner})

	result := callToolResult(t, server, toolSaveBaseline, map[string]any{
		"repoPath":          repo,
		"baselineStorePath": "baselines",
		"baselineLabel":     "nightly",
		"topN":              3,
		"confirmSave":       true,
		"cacheEnabled":      false,
		"disableFeatures":   []string{MutationToolsFeature},
		"timeoutMillis":     1000,
		"licenseFailOnDeny": false,
		"licenseDeny":       []string{"GPL-3.0-only"},
		"runtimeTracePath":  "trace.ndjson",
		"runtimeProfile":    "node-import",
		"cacheReadOnly":     true,
		"cachePath":         ".cache/lopper",
		"scopeMode":         "package",
		"language":          "auto",
		"exclude":           []string{"vendor/**"},
		"include":           []string{"src/**"},
	})
	if result.IsError {
		t.Fatalf("unexpected baseline save error: %#v", result)
	}
	if !runner.baselineCalled {
		t.Fatalf("expected baseline runner to be called")
	}
	wantStore := filepath.Join(repo, "baselines")
	wantPath := report.BaselineSnapshotPath(wantStore, "label:nightly")
	if runner.lastBaseline.TopN != 3 || runner.lastBaseline.BaselineStorePath != wantStore || runner.lastBaseline.BaselineKey != "label:nightly" {
		t.Fatalf("unexpected baseline request: %#v", runner.lastBaseline)
	}

	payload, ok := result.StructuredContent.(baselineSavePayload)
	if !ok {
		t.Fatalf("expected baseline payload, got %#v", result.StructuredContent)
	}
	if payload.SnapshotPath != wantPath || payload.BaselineKey != "label:nightly" {
		t.Fatalf("unexpected baseline snapshot details: %#v", payload)
	}
	if payload.ReportSummary == nil || payload.ReportSummary.DependencyCount != 1 {
		t.Fatalf("expected report summary in structured content, got %#v", payload.ReportSummary)
	}
}

func TestRunDashboardBaselineSaveToolReturnsStructuredPayload(t *testing.T) {
	repo := t.TempDir()
	childRepo := t.TempDir()
	store := filepath.Join(repo, "dashboard-baselines")
	runner := &fakeMutationRunner{
		dashboardReport: dashboard.Report{
			Summary: dashboard.Summary{
				TotalRepos: 1,
				TotalDeps:  2,
			},
		},
	}
	server := NewServer(Options{Features: mustMutationFeatureSet(t, true), MutationRunner: runner})

	result := callToolResult(t, server, toolSaveDashboardBaseline, map[string]any{
		"repoPath":          repo,
		"repos":             []map[string]any{{"name": "app", "path": childRepo, "language": "js-ts"}},
		"baselineStorePath": store,
		"baselineKey":       "release/1",
		"confirmSave":       true,
		"topN":              4,
		"defaultLanguage":   "js-ts",
	})
	if result.IsError {
		t.Fatalf("unexpected dashboard baseline save error: %#v", result)
	}
	if !runner.dashboardCalled {
		t.Fatalf("expected dashboard runner to be called")
	}
	if runner.lastDashboard.TopN != 4 || runner.lastDashboard.DefaultLanguage != "js-ts" || len(runner.lastDashboard.Repos) != 1 {
		t.Fatalf("unexpected dashboard request: %#v", runner.lastDashboard)
	}

	payload, ok := result.StructuredContent.(dashboardBaselineSavePayload)
	if !ok {
		t.Fatalf("expected dashboard baseline payload, got %#v", result.StructuredContent)
	}
	if payload.SnapshotPath != dashboard.BaselineSnapshotPath(store, "release/1") || payload.DashboardSummary.TotalRepos != 1 {
		t.Fatalf("unexpected dashboard payload: %#v", payload)
	}
}

func TestMutationToolErrorsReturnStructuredPayloads(t *testing.T) {
	repo := t.TempDir()
	runner := &fakeMutationRunner{
		applyReport:     sampleReport(repo),
		applyErr:        errors.New("apply failed"),
		baselineReport:  sampleReport(repo),
		baselinePath:    filepath.Join(repo, "saved-baseline.json"),
		baselineErr:     errors.New("baseline save failed"),
		dashboardReport: dashboard.Report{Summary: dashboard.Summary{TotalRepos: 1}},
		dashboardPath:   filepath.Join(repo, "saved-dashboard.json"),
		dashboardErr:    errors.New("dashboard save failed"),
	}
	server := NewServer(Options{Features: mustMutationFeatureSet(t, true), MutationRunner: runner})

	applyResult := callToolResult(t, server, toolApplyCodemod, map[string]any{
		"repoPath":     repo,
		"dependency":   "lodash",
		"confirmApply": true,
		"cacheEnabled": false,
	})
	if !applyResult.IsError {
		t.Fatalf("expected codemod apply error")
	}
	applyPayload, ok := applyResult.StructuredContent.(codemodApplyPayload)
	if !ok {
		t.Fatalf("expected codemod payload, got %#v", applyResult.StructuredContent)
	}
	if applyPayload.Error == nil || applyPayload.Error.Code != errorCodeToolFailed || !strings.Contains(applyPayload.Summary, "failed") {
		t.Fatalf("expected structured error payload, got %#v", applyPayload)
	}

	baselineResult := callToolResult(t, server, toolSaveBaseline, map[string]any{
		"repoPath":          repo,
		"baselineStorePath": "baselines",
		"baselineKey":       "release",
		"confirmSave":       true,
		"cacheEnabled":      false,
	})
	if !baselineResult.IsError {
		t.Fatalf("expected baseline save error")
	}
	baselinePayload, ok := baselineResult.StructuredContent.(baselineSavePayload)
	if !ok || baselinePayload.Error == nil || baselinePayload.SnapshotPath != runner.baselinePath {
		t.Fatalf("expected structured baseline error payload, got %#v", baselineResult.StructuredContent)
	}

	dashboardResult := callToolResult(t, server, toolSaveDashboardBaseline, map[string]any{
		"repoPath":          repo,
		"repos":             []map[string]any{{"path": repo}},
		"baselineStorePath": "dashboard-baselines",
		"baselineKey":       "release",
		"confirmSave":       true,
	})
	if !dashboardResult.IsError {
		t.Fatalf("expected dashboard baseline save error")
	}
	dashboardPayload, ok := dashboardResult.StructuredContent.(dashboardBaselineSavePayload)
	if !ok || dashboardPayload.Error == nil || dashboardPayload.SnapshotPath != runner.dashboardPath {
		t.Fatalf("expected structured dashboard error payload, got %#v", dashboardResult.StructuredContent)
	}
}

func TestMutationValidationBranches(t *testing.T) {
	repo := t.TempDir()
	server := NewServer(Options{Features: mustMutationFeatureSet(t, true), MutationRunner: &fakeMutationRunner{}})

	if result := server.runCodemodApplyTool(context.Background(), jsonRaw(`{"repoPath":1}`)); !result.IsError {
		t.Fatalf("expected codemod decode error")
	}
	if result := server.runCodemodApplyTool(context.Background(), mustJSON(t, map[string]any{
		"repoPath":      repo,
		"dependency":    "lodash",
		"confirmApply":  true,
		"timeoutMillis": -1,
	})); !result.IsError || !strings.Contains(result.Content[0].Text, "timeout") {
		t.Fatalf("expected codemod timeout rejection, got %#v", result)
	}
	if result := server.runCodemodApplyTool(context.Background(), mustJSON(t, map[string]any{
		"repoPath":     filepath.Join(repo, "missing"),
		"dependency":   "lodash",
		"confirmApply": true,
	})); !result.IsError || !strings.Contains(result.Content[0].Text, "stat repoPath") {
		t.Fatalf("expected codemod request resolution error, got %#v", result)
	}
	if result := server.runCodemodApplyTool(context.Background(), mustJSON(t, map[string]any{
		"repoPath":     repo,
		"confirmApply": true,
	})); !result.IsError || !strings.Contains(result.Content[0].Text, "dependency") {
		t.Fatalf("expected missing dependency rejection, got %#v", result)
	}
	if result := server.runCodemodApplyTool(context.Background(), mustJSON(t, map[string]any{
		"repoPath":     repo,
		"dependency":   "lodash",
		"topN":         2,
		"confirmApply": true,
	})); !result.IsError || !strings.Contains(result.Content[0].Text, "topN") {
		t.Fatalf("expected topN rejection, got %#v", result)
	}
	if result := server.runBaselineSaveTool(context.Background(), jsonRaw(`{"repoPath":1}`)); !result.IsError {
		t.Fatalf("expected baseline decode error")
	}
	if result := server.runBaselineSaveTool(context.Background(), mustJSON(t, map[string]any{
		"repoPath":          repo,
		"baselineStorePath": "store",
	})); !result.IsError || !strings.Contains(result.Content[0].Text, "confirmSave") {
		t.Fatalf("expected missing baseline confirmation, got %#v", result)
	}
	if result := server.runBaselineSaveTool(context.Background(), mustJSON(t, map[string]any{
		"repoPath":          repo,
		"baselineStorePath": "store",
		"baselineKey":       "release",
		"confirmSave":       true,
		"timeoutMillis":     -1,
	})); !result.IsError || !strings.Contains(result.Content[0].Text, "timeout") {
		t.Fatalf("expected baseline timeout rejection, got %#v", result)
	}
	if result := server.runBaselineSaveTool(context.Background(), mustJSON(t, map[string]any{
		"repoPath":          repo,
		"baselineStorePath": "store",
		"baselineKey":       "release",
		"scopeMode":         "bad",
		"confirmSave":       true,
	})); !result.IsError || !strings.Contains(result.Content[0].Text, "scopeMode") {
		t.Fatalf("expected baseline request resolution error, got %#v", result)
	}
	if result := server.runDashboardBaselineSaveTool(context.Background(), jsonRaw(`{"repoPath":1}`)); !result.IsError {
		t.Fatalf("expected dashboard decode error")
	}
	if result := server.runDashboardBaselineSaveTool(context.Background(), mustJSON(t, map[string]any{
		"repoPath":          repo,
		"baselineStorePath": "store",
	})); !result.IsError || !strings.Contains(result.Content[0].Text, "confirmSave") {
		t.Fatalf("expected missing dashboard confirmation, got %#v", result)
	}
	if result := server.runDashboardBaselineSaveTool(context.Background(), mustJSON(t, map[string]any{
		"repoPath":          repo,
		"repos":             []map[string]any{{"path": repo}},
		"baselineStorePath": "store",
		"baselineKey":       "release",
		"confirmSave":       true,
		"timeoutMillis":     -1,
	})); !result.IsError || !strings.Contains(result.Content[0].Text, "timeout") {
		t.Fatalf("expected dashboard timeout rejection, got %#v", result)
	}
	if result := NewServer(Options{Features: mustMutationFeatureSet(t, false), MutationRunner: &fakeMutationRunner{}}).runDashboardBaselineSaveTool(context.Background(), mustJSON(t, map[string]any{
		"repoPath":          repo,
		"repos":             []map[string]any{{"path": repo}},
		"baselineStorePath": "store",
		"baselineKey":       "release",
		"confirmSave":       true,
	})); !result.IsError || !strings.Contains(result.Content[0].Text, MutationToolsFeature) {
		t.Fatalf("expected dashboard feature rejection, got %#v", result)
	}
	if result := server.runDashboardBaselineSaveTool(context.Background(), mustJSON(t, map[string]any{
		"repoPath":          repo,
		"baselineStorePath": "store",
		"baselineKey":       "release",
		"confirmSave":       true,
	})); !result.IsError || !strings.Contains(result.Content[0].Text, "repos or configPath") {
		t.Fatalf("expected dashboard source rejection, got %#v", result)
	}
	if result := NewServer(Options{Features: mustMutationFeatureSet(t, true)}).runBaselineSaveTool(context.Background(), mustJSON(t, map[string]any{
		"repoPath":          repo,
		"baselineStorePath": "store",
		"baselineKey":       "release",
		"confirmSave":       true,
	})); !result.IsError || !strings.Contains(result.Content[0].Text, "runner") {
		t.Fatalf("expected missing runner rejection, got %#v", result)
	}

	cases := []struct {
		name string
		args mutationAnalysisArguments
		kind mutationAnalysisKind
		want string
	}{
		{"missing dependency", mutationAnalysisArguments{RepoPath: repo}, mutationAnalysisKindDependency, "dependency"},
		{"dependency target success", mutationAnalysisArguments{RepoPath: repo, Dependency: "dep"}, mutationAnalysisKindTopOrDependency, ""},
		{"top with dependency and topN", mutationAnalysisArguments{RepoPath: repo, Dependency: "dep", TopN: intPtr(2)}, mutationAnalysisKindTopOrDependency, "topN"},
		{"invalid topN", mutationAnalysisArguments{RepoPath: repo, TopN: intPtr(0)}, mutationAnalysisKindTopOrDependency, "topN"},
		{"unknown kind", mutationAnalysisArguments{RepoPath: repo}, mutationAnalysisKind("bad"), "unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dependency, _, err := resolveMutationAnalysisTarget(tc.args, tc.kind)
			if tc.want == "" {
				if err != nil || dependency == "" {
					t.Fatalf("expected dependency target success, dependency=%q err=%v", dependency, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q error, got %v", tc.want, err)
			}
		})
	}
}

func TestResolveMutationRequestValidationBranches(t *testing.T) {
	repo := t.TempDir()
	server := NewServer(Options{Features: mustMutationFeatureSet(t, true), MutationRunner: &fakeMutationRunner{}})

	analysisCases := []struct {
		name string
		args mutationAnalysisArguments
		want string
	}{
		{"bad repo", mutationAnalysisArguments{RepoPath: filepath.Join(repo, "missing"), Dependency: "dep"}, "stat repoPath"},
		{"missing dependency", mutationAnalysisArguments{RepoPath: repo}, "dependency"},
		{"bad scope", mutationAnalysisArguments{RepoPath: repo, Dependency: "dep", ScopeMode: "bad"}, "scopeMode"},
		{"bad threshold", mutationAnalysisArguments{RepoPath: repo, Dependency: "dep", LowConfidenceWarningPercent: intPtr(101)}, "low_confidence"},
		{"unknown feature", mutationAnalysisArguments{RepoPath: repo, Dependency: "dep", EnableFeatures: []string{"missing"}}, "unknown feature"},
	}
	for _, tc := range analysisCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := server.resolveAnalysisMutationRequest(context.Background(), tc.args, mutationAnalysisKindDependency)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q error, got %v", tc.want, err)
			}
		})
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := server.resolveAnalysisMutationRequest(cancelled, mutationAnalysisArguments{RepoPath: repo, Dependency: "dep"}, mutationAnalysisKindDependency)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled analysis request, got %v", err)
	}

	dashboardCases := []struct {
		name string
		args dashboardBaselineSaveArguments
		want string
	}{
		{"bad repo", dashboardBaselineSaveArguments{RepoPath: filepath.Join(repo, "missing"), Repos: []DashboardRepoInput{{Path: repo}}, BaselineStorePath: "store", BaselineKey: "key"}, "stat repoPath"},
		{"bad child repo", dashboardBaselineSaveArguments{RepoPath: repo, Repos: []DashboardRepoInput{{Path: "https://example.com/repo"}}, BaselineStorePath: "store", BaselineKey: "key"}, "local filesystem"},
		{"bad topN", dashboardBaselineSaveArguments{RepoPath: repo, Repos: []DashboardRepoInput{{Path: repo}}, BaselineStorePath: "store", BaselineKey: "key", TopN: intPtr(0)}, "topN"},
		{"bad store", dashboardBaselineSaveArguments{RepoPath: repo, Repos: []DashboardRepoInput{{Path: repo}}, BaselineStorePath: "https://example.com/store", BaselineKey: "key"}, "local filesystem"},
		{"unknown feature", dashboardBaselineSaveArguments{RepoPath: repo, Repos: []DashboardRepoInput{{Path: repo}}, BaselineStorePath: "store", BaselineKey: "key", EnableFeatures: []string{"missing"}}, "unknown feature"},
	}
	for _, tc := range dashboardCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := server.resolveDashboardMutationRequest(context.Background(), tc.args)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q error, got %v", tc.want, err)
			}
		})
	}

	cancelled, cancel = context.WithCancel(context.Background())
	cancel()
	_, err = server.resolveDashboardMutationRequest(cancelled, dashboardBaselineSaveArguments{
		RepoPath:          repo,
		Repos:             []DashboardRepoInput{{Path: repo}},
		BaselineStorePath: "store",
		BaselineKey:       "key",
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled dashboard request, got %v", err)
	}
}

func TestMutationPathAndPayloadHelpers(t *testing.T) {
	repo := t.TempDir()
	absStore := filepath.Join(repo, "store")
	resolved, err := resolveLocalMutationPath(repo, absStore, "baselineStorePath")
	if err != nil || resolved != absStore {
		t.Fatalf("resolve absolute path: path=%q err=%v", resolved, err)
	}
	for _, rawPath := range []string{"", "https://example.com/store", "bad\x00path"} {
		if _, err := resolveLocalMutationPath(repo, rawPath, "baselineStorePath"); err == nil {
			t.Fatalf("expected local path validation error for %q", rawPath)
		}
	}
	if _, _, err := resolveBaselineMutationTarget(repo, "store", "key", "label", "baseline"); err == nil {
		t.Fatalf("expected key/label conflict")
	}
	if store, key, err := resolveBaselineMutationTarget(repo, "store", "explicit", "", "baseline"); err != nil || store != filepath.Join(repo, "store") || key != "explicit" {
		t.Fatalf("unexpected explicit key resolution: store=%q key=%q err=%v", store, key, err)
	}
	if _, _, err := resolveBaselineMutationTarget(repo, "store", "", "", "baseline"); err == nil {
		t.Fatalf("expected non-git repo key resolution error")
	}
	gitRepo := t.TempDir()
	writeGitFixture(t, gitRepo)
	store, key, err := resolveBaselineMutationTarget(gitRepo, "store", "", "", "baseline")
	if err != nil || store != filepath.Join(gitRepo, "store") || !strings.HasPrefix(key, "commit:") {
		t.Fatalf("unexpected current commit key resolution: store=%q key=%q err=%v", store, key, err)
	}

	for _, repos := range [][]DashboardRepoInput{
		{{Path: ""}},
		{{Path: "https://example.com/repo"}},
		{{Path: "bad\x00path"}},
	} {
		if err := validateDashboardMutationRepos(repos); err == nil {
			t.Fatalf("expected dashboard repo validation error for %#v", repos)
		}
	}
	if err := validateDashboardMutationRepos([]DashboardRepoInput{{Path: repo}}); err != nil {
		t.Fatalf("expected local dashboard repo path, got %v", err)
	}

	if got := summarizeCodemodApply("dep", nil, nil); !strings.Contains(got, "no codemod changes") {
		t.Fatalf("unexpected empty codemod summary: %q", got)
	}
	if got := summarizeSnapshotSave("baseline", "key", "path", errors.New("save failed")); !strings.Contains(got, "failed") {
		t.Fatalf("unexpected failed snapshot summary: %q", got)
	}
	if errPayload := structuredError(errors.New("boom")); errPayload == nil || errPayload.Code != errorCodeToolFailed {
		t.Fatalf("unexpected structured error: %#v", errPayload)
	}
	if titleCaseFirst("") != "" {
		t.Fatalf("expected empty title-case result")
	}
	rep := sampleReport(repo)
	rep.Dependencies = append([]report.DependencyReport{{Name: "other", Codemod: &report.CodemodReport{Apply: &report.CodemodApplyReport{AppliedFiles: 1}}}}, rep.Dependencies...)
	if apply := findCodemodApplyReport(rep, "lodash"); apply != nil {
		t.Fatalf("expected dependency-specific codemod lookup to skip other dependency, got %#v", apply)
	}
}

func jsonRaw(value string) []byte {
	return []byte(value)
}

func writeGitFixture(t *testing.T, repo string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("fixture\n"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	testutil.RunGit(t, repo, "init")
	testutil.RunGit(t, repo, "config", "user.email", "test@example.com")
	testutil.RunGit(t, repo, "config", "user.name", "Test User")
	testutil.RunGit(t, repo, "add", ".")
	testutil.RunGit(t, repo, "commit", "-m", "fixture")
}
