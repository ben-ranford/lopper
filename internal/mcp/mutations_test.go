package mcp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/analysis"
	"github.com/ben-ranford/lopper/internal/dashboard"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/testutil"
	"github.com/ben-ranford/lopper/internal/workspace"
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

func TestRunBaselineSaveToolUsesAuthorizedCommitAndConfigAfterRepositorySwap(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("open-directory replacement semantics are not available on Windows")
	}
	parent := t.TempDir()
	repo := filepath.Join(parent, "repo")
	repoB := filepath.Join(parent, "repo-b")
	for _, root := range []string{repo, repoB} {
		if err := os.Mkdir(root, 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", root, err)
		}
	}
	writeMCPRepoConfigFixture(t, repo, 21, "GHSA-repo-a")
	writeMCPRepoConfigFixture(t, repoB, 88, "GHSA-repo-b")
	initMCPGitRepo(t, repo, "repo-a")
	initMCPGitRepo(t, repoB, "repo-b")
	repoACommit, err := workspace.CurrentCommitSHA(repo)
	if err != nil {
		t.Fatalf("repo A commit: %v", err)
	}
	repoBCommit, err := workspace.CurrentCommitSHA(repoB)
	if err != nil {
		t.Fatalf("repo B commit: %v", err)
	}
	movedRepoA := filepath.Join(parent, "repo-a-original")
	restore := analysis.SetRepositoryViewHandleOpenedHookForTest(func() error {
		if err := os.Rename(repo, movedRepoA); err != nil {
			return err
		}
		return os.Rename(repoB, repo)
	})
	t.Cleanup(restore)

	runner := &fakeMutationRunner{baselineReport: sampleReport(repo)}
	server := NewServer(Options{Features: mustMutationFeatureSet(t, true), MutationRunner: runner})
	result := callToolResult(t, server, toolSaveBaseline, map[string]any{
		"repoPath":          repo,
		"baselineStorePath": ".artifacts/baselines",
		"topN":              1,
		"confirmSave":       true,
		"cacheEnabled":      false,
	})
	if result.IsError {
		t.Fatalf("unexpected baseline save after swap: %#v", result)
	}
	if runner.lastBaseline.BaselineKey != "commit:"+repoACommit || runner.lastBaseline.CurrentBaselineKey != "commit:"+repoACommit {
		t.Fatalf("expected repo A commit-bound baseline request, got %#v", runner.lastBaseline)
	}
	if runner.lastBaseline.Thresholds.LowConfidenceWarningPercent != 21 {
		t.Fatalf("expected repo A thresholds after swap, got %#v", runner.lastBaseline.Thresholds)
	}
	if runner.lastBaseline.BaselineKey == "commit:"+repoBCommit {
		t.Fatalf("expected repo B commit not to leak into baseline key")
	}
}

func TestRunBaselineSaveToolSealsSamePathHeadAndConfigBeforeMutationCapture(t *testing.T) {
	repo := t.TempDir()
	writeMCPRepoConfigFixture(t, repo, 23, "GHSA-config-a")
	initMCPGitRepo(t, repo, "repo-a")
	commitA, err := workspace.CurrentCommitSHA(repo)
	if err != nil {
		t.Fatalf("resolve commit A: %v", err)
	}

	writeMCPRepoConfigFixture(t, repo, 89, "GHSA-config-b")
	if err := os.WriteFile(filepath.Join(repo, "identity.txt"), []byte("repo-b\n"), 0o600); err != nil {
		t.Fatalf("write identity B: %v", err)
	}
	testutil.RunGit(t, repo, "add", ".")
	testutil.RunGit(t, repo, "commit", "-m", "repo-b")
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

	runner := &fakeMutationRunner{baselineReport: sampleReport(repo)}
	server := NewServer(Options{Features: mustMutationFeatureSet(t, true), MutationRunner: runner})
	result := callToolResult(t, server, toolSaveBaseline, map[string]any{
		"repoPath":          repo,
		"baselineStorePath": ".artifacts/baselines",
		"topN":              1,
		"confirmSave":       true,
		"cacheEnabled":      false,
	})
	if result.IsError {
		t.Fatalf("unexpected same-path baseline mutation error: %#v", result)
	}
	if !runner.baselineCalled || runner.lastBaseline.RepositoryView == nil {
		t.Fatalf("mutation did not receive the authoritative borrowed view: %#v", runner.lastBaseline)
	}
	if runner.lastBaseline.Thresholds.LowConfidenceWarningPercent != 23 {
		t.Fatalf("mutation thresholds = %#v, want config A", runner.lastBaseline.Thresholds)
	}
	if runner.lastBaseline.CurrentBaselineKey != "commit:"+commitA ||
		runner.lastBaseline.BaselineKey != "commit:"+commitA ||
		!runner.lastBaseline.CurrentBaselineKeyCaptured {
		t.Fatalf("mutation baseline identity = %#v, want commit A", runner.lastBaseline)
	}
	if runner.lastBaseline.CurrentBaselineKey == "commit:"+commitB {
		t.Fatal("same-path commit B retargeted mutation state")
	}
	if viewOpens != 1 || viewCloses != 1 {
		t.Fatalf("repository view opens/closes = %d/%d, want 1/1", viewOpens, viewCloses)
	}
}

func TestRunBaselineSaveToolJoinsRunnerAndRepositoryViewCloseFailures(t *testing.T) {
	repo := t.TempDir()
	runErr := errors.New("baseline runner sentinel")
	closeErr := errors.New("mutation repository close sentinel")
	closeCalls := 0
	restore := analysis.SetRepositoryViewCloseHookForTest(func() error {
		closeCalls++
		return closeErr
	})
	t.Cleanup(restore)

	runner := &fakeMutationRunner{baselineErr: runErr}
	server := NewServer(Options{Features: mustMutationFeatureSet(t, true), MutationRunner: runner})
	result := callToolResult(t, server, toolSaveBaseline, map[string]any{
		"repoPath":          repo,
		"baselineStorePath": "baselines",
		"baselineLabel":     "close-error",
		"confirmSave":       true,
		"cacheEnabled":      false,
	})
	if !result.IsError || len(result.Content) == 0 {
		t.Fatalf("expected joined mutation failure, got %#v", result)
	}
	if !strings.Contains(result.Content[0].Text, runErr.Error()) || !strings.Contains(result.Content[0].Text, closeErr.Error()) {
		t.Fatalf("expected runner and close errors, got %q", result.Content[0].Text)
	}
	if closeCalls != 1 {
		t.Fatalf("repository close calls = %d, want 1", closeCalls)
	}
}

func TestRunCodemodApplyToolKeepsPinnedPolicyAndPropagatesRepositoryViewCloseFailure(t *testing.T) {
	repo := t.TempDir()
	configPath := filepath.Join(repo, ".lopper.yml")
	if err := os.WriteFile(configPath, []byte("thresholds:\n  lockfile_drift_policy: off\n"), 0o600); err != nil {
		t.Fatalf("write initial policy: %v", err)
	}
	closeErr := errors.New("policy drift close sentinel")
	closeCalls := 0
	restore := analysis.SetRepositoryViewCloseHookForTest(func() error {
		closeCalls++
		return closeErr
	})
	t.Cleanup(restore)

	runner := &fakeMutationRunner{
		captureFn: func(req AnalysisMutationRequest) (AnalysisMutationRequest, error) {
			err := os.WriteFile(configPath, []byte("thresholds:\n  lockfile_drift_policy: warn\n"), 0o600)
			return req, err
		},
	}
	server := NewServer(Options{Features: mustMutationFeatureSet(t, true), MutationRunner: runner})
	result := callToolResult(t, server, toolApplyCodemod, map[string]any{
		"repoPath":     repo,
		"dependency":   "lodash",
		"confirmApply": true,
		"cacheEnabled": false,
	})
	if !result.IsError || len(result.Content) == 0 {
		t.Fatalf("expected repository-view close failure, got %#v", result)
	}
	if strings.Contains(result.Content[0].Text, "policy changed while capturing mutation preconditions") ||
		!strings.Contains(result.Content[0].Text, closeErr.Error()) {
		t.Fatalf("expected only the close failure after pinned-policy execution, got %q", result.Content[0].Text)
	}
	if closeCalls != 1 {
		t.Fatalf("repository close calls = %d, want 1", closeCalls)
	}
	if !runner.applyCalled {
		t.Fatal("expected mutation runner to execute with pinned policy")
	}
	if runner.lastApply.Thresholds.LockfileDriftPolicy != "off" {
		t.Fatalf("mutation policy = %q, want pinned off policy", runner.lastApply.Thresholds.LockfileDriftPolicy)
	}
}

func TestAnalysisMutationCaptureFailureClosesAuthoritativeRepositoryView(t *testing.T) {
	repo := t.TempDir()
	captureErr := errors.New("capture precondition sentinel")
	viewOpens := 0
	restore := analysis.SetRepositoryViewHandleOpenedHookForTest(func() error {
		viewOpens++
		return nil
	})
	t.Cleanup(restore)
	viewCloses := 0
	restoreClose := analysis.SetRepositoryViewCloseHookForTest(func() error {
		viewCloses++
		return nil
	})
	t.Cleanup(restoreClose)

	runner := &fakeMutationRunner{captureErr: captureErr}
	server := NewServer(Options{Features: mustMutationFeatureSet(t, true), MutationRunner: runner})
	result := callToolResult(t, server, toolApplyCodemod, map[string]any{
		"repoPath":     repo,
		"dependency":   "lodash",
		"confirmApply": true,
		"cacheEnabled": false,
	})
	assertMutationToolErrorContains(t, result, captureErr.Error())
	if viewOpens != 1 {
		t.Fatalf("repository view opens = %d, want 1", viewOpens)
	}
	if viewCloses != 1 {
		t.Fatalf("repository view closes = %d, want 1", viewCloses)
	}
}

func TestMutationPolicyTraceRecordsEveryOverrideWithStableSource(t *testing.T) {
	args := mutationAnalysisArguments{
		LowConfidenceWarningPercent:       intPtr(11),
		MinUsagePercentForRecommendations: intPtr(22),
		MaxUncertainImportCount:           intPtr(3),
		ScoreWeightUsage:                  floatPtr(0.5),
		ScoreWeightImpact:                 floatPtr(0.3),
		ScoreWeightConfidence:             floatPtr(0.2),
		LicenseDeny:                       []string{"GPL-3.0-only"},
		LicenseFailOnDeny:                 boolPtr(true),
		LicenseProvenanceRegistry:         boolPtr(true),
	}
	trace := mutationPolicyTrace(args)
	wantFields := []string{
		"thresholds.low_confidence_warning_percent",
		"thresholds.min_usage_percent_for_recommendations",
		"thresholds.max_uncertain_import_count",
		"removal_candidate_weights.usage",
		"removal_candidate_weights.impact",
		"removal_candidate_weights.confidence",
		"license.deny",
		"license.fail_on_deny",
		"license.include_registry_provenance",
	}
	if len(trace) != len(wantFields) {
		t.Fatalf("policy trace length = %d, want %d: %#v", len(trace), len(wantFields), trace)
	}
	for index, field := range wantFields {
		if trace[index].Field != field || trace[index].Source != "mcp" {
			t.Fatalf("policy trace[%d] = %#v, want field %q from mcp", index, trace[index], field)
		}
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

func TestMutationToolValidationBranches(t *testing.T) {
	repo := t.TempDir()
	server := NewServer(Options{Features: mustMutationFeatureSet(t, true), MutationRunner: &fakeMutationRunner{}})

	cases := []struct {
		name string
		run  func() toolCallResult
		want string
	}{
		{
			name: "codemod decode error",
			run: func() toolCallResult {
				return server.runCodemodApplyTool(context.Background(), jsonRaw(`{"repoPath":1}`))
			},
		},
		{
			name: "codemod timeout",
			run: func() toolCallResult {
				return server.runCodemodApplyTool(context.Background(), mustJSON(t, map[string]any{
					"repoPath":      repo,
					"dependency":    "lodash",
					"confirmApply":  true,
					"timeoutMillis": -1,
				}))
			},
			want: "timeout",
		},
		{
			name: "codemod missing repo",
			run: func() toolCallResult {
				return server.runCodemodApplyTool(context.Background(), mustJSON(t, map[string]any{
					"repoPath":     filepath.Join(repo, "missing"),
					"dependency":   "lodash",
					"confirmApply": true,
				}))
			},
			want: "stat repoPath",
		},
		{
			name: "codemod missing dependency",
			run: func() toolCallResult {
				return server.runCodemodApplyTool(context.Background(), mustJSON(t, map[string]any{
					"repoPath":     repo,
					"confirmApply": true,
				}))
			},
			want: "dependency",
		},
		{
			name: "codemod topN",
			run: func() toolCallResult {
				return server.runCodemodApplyTool(context.Background(), mustJSON(t, map[string]any{
					"repoPath":     repo,
					"dependency":   "lodash",
					"topN":         2,
					"confirmApply": true,
				}))
			},
			want: "topN",
		},
		{
			name: "baseline decode error",
			run: func() toolCallResult {
				return server.runBaselineSaveTool(context.Background(), jsonRaw(`{"repoPath":1}`))
			},
		},
		{
			name: "baseline missing confirmation",
			run: func() toolCallResult {
				return server.runBaselineSaveTool(context.Background(), mustJSON(t, map[string]any{
					"repoPath":          repo,
					"baselineStorePath": "store",
				}))
			},
			want: "confirmSave",
		},
		{
			name: "baseline timeout",
			run: func() toolCallResult {
				return server.runBaselineSaveTool(context.Background(), mustJSON(t, map[string]any{
					"repoPath":          repo,
					"baselineStorePath": "store",
					"baselineKey":       "release",
					"confirmSave":       true,
					"timeoutMillis":     -1,
				}))
			},
			want: "timeout",
		},
		{
			name: "baseline bad scope",
			run: func() toolCallResult {
				return server.runBaselineSaveTool(context.Background(), mustJSON(t, map[string]any{
					"repoPath":          repo,
					"baselineStorePath": "store",
					"baselineKey":       "release",
					"scopeMode":         "bad",
					"confirmSave":       true,
				}))
			},
			want: "scopeMode",
		},
		{
			name: "dashboard decode error",
			run: func() toolCallResult {
				return server.runDashboardBaselineSaveTool(context.Background(), jsonRaw(`{"repoPath":1}`))
			},
		},
		{
			name: "dashboard missing confirmation",
			run: func() toolCallResult {
				return server.runDashboardBaselineSaveTool(context.Background(), mustJSON(t, map[string]any{
					"repoPath":          repo,
					"baselineStorePath": "store",
				}))
			},
			want: "confirmSave",
		},
		{
			name: "dashboard timeout",
			run: func() toolCallResult {
				return server.runDashboardBaselineSaveTool(context.Background(), mustJSON(t, map[string]any{
					"repoPath":          repo,
					"repos":             []map[string]any{{"path": repo}},
					"baselineStorePath": "store",
					"baselineKey":       "release",
					"confirmSave":       true,
					"timeoutMillis":     -1,
				}))
			},
			want: "timeout",
		},
		{
			name: "dashboard feature disabled",
			run: func() toolCallResult {
				return NewServer(Options{Features: mustMutationFeatureSet(t, false), MutationRunner: &fakeMutationRunner{}}).runDashboardBaselineSaveTool(context.Background(), mustJSON(t, map[string]any{
					"repoPath":          repo,
					"repos":             []map[string]any{{"path": repo}},
					"baselineStorePath": "store",
					"baselineKey":       "release",
					"confirmSave":       true,
				}))
			},
			want: MutationToolsFeature,
		},
		{
			name: "dashboard missing source",
			run: func() toolCallResult {
				return server.runDashboardBaselineSaveTool(context.Background(), mustJSON(t, map[string]any{
					"repoPath":          repo,
					"baselineStorePath": "store",
					"baselineKey":       "release",
					"confirmSave":       true,
				}))
			},
			want: "repos or configPath",
		},
		{
			name: "baseline missing runner",
			run: func() toolCallResult {
				return NewServer(Options{Features: mustMutationFeatureSet(t, true)}).runBaselineSaveTool(context.Background(), mustJSON(t, map[string]any{
					"repoPath":          repo,
					"baselineStorePath": "store",
					"baselineKey":       "release",
					"confirmSave":       true,
				}))
			},
			want: "runner",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertMutationToolErrorContains(t, tc.run(), tc.want)
		})
	}
}

func TestResolveMutationAnalysisTargetBranches(t *testing.T) {
	repo := t.TempDir()
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
	assertMutationAnalysisValidationErrors(t, server, repo)
	assertMutationExternalCacheCompatibility(t, server, repo)
	assertMutationDashboardValidationErrors(t, server, repo)
}

func assertMutationAnalysisValidationErrors(t *testing.T, server *Server, repo string) {
	t.Helper()
	for _, tc := range []struct {
		name string
		args mutationAnalysisArguments
		want string
	}{
		{"bad repo", mutationAnalysisArguments{RepoPath: filepath.Join(repo, "missing"), Dependency: "dep"}, "stat repoPath"},
		{"missing dependency", mutationAnalysisArguments{RepoPath: repo}, "dependency"},
		{"bad scope", mutationAnalysisArguments{RepoPath: repo, Dependency: "dep", ScopeMode: "bad"}, "scopeMode"},
		{"bad threshold", mutationAnalysisArguments{RepoPath: repo, Dependency: "dep", LowConfidenceWarningPercent: intPtr(101)}, "low_confidence"},
		{"unknown feature", mutationAnalysisArguments{RepoPath: repo, Dependency: "dep", EnableFeatures: []string{"missing"}}, "unknown feature"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := server.resolveAnalysisMutationRequest(context.Background(), tc.args, mutationAnalysisKindDependency)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q error, got %v", tc.want, err)
			}
		})
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := server.resolveAnalysisMutationRequest(cancelled, mutationAnalysisArguments{RepoPath: repo, Dependency: "dep"}, mutationAnalysisKindDependency); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled analysis request, got %v", err)
	}
}

func assertMutationExternalCacheCompatibility(t *testing.T, server *Server, repo string) {
	t.Helper()
	outsideCache := filepath.Join(t.TempDir(), "cache")
	request, err := server.resolveAnalysisMutationRequest(context.Background(), mutationAnalysisArguments{RepoPath: repo, Dependency: "dep", CacheEnabled: boolPtr(true), CachePath: outsideCache, CacheReadOnly: true}, mutationAnalysisKindDependency)
	if err != nil {
		t.Fatalf("resolve mutation request with external cache path: %v", err)
	}
	if request.Cache == nil || !request.Cache.Enabled || request.Cache.Path != outsideCache || !request.Cache.ReadOnly {
		t.Fatalf("expected external absolute cache path to be preserved for mutations, got %#v", request)
	}
	symlinkOutside := t.TempDir()
	if err := os.Symlink(symlinkOutside, filepath.Join(repo, "tmp")); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	_, err = server.resolveAnalysisMutationRequest(context.Background(), mutationAnalysisArguments{RepoPath: repo, Dependency: "dep", CacheEnabled: boolPtr(true), CachePath: filepath.Join(repo, "tmp", "cache")}, mutationAnalysisKindDependency)
	if err == nil || !strings.Contains(err.Error(), "cachePath must stay within repoPath") {
		t.Fatalf("expected symlinked cache escape rejection, got %v", err)
	}
}

func assertMutationDashboardValidationErrors(t *testing.T, server *Server, repo string) {
	t.Helper()
	for _, tc := range []struct {
		name string
		args dashboardBaselineSaveArguments
		want string
	}{
		{"bad repo", dashboardBaselineSaveArguments{RepoPath: filepath.Join(repo, "missing"), Repos: []DashboardRepoInput{{Path: repo}}, BaselineStorePath: "store", BaselineKey: "key"}, "stat repoPath"},
		{"bad child repo", dashboardBaselineSaveArguments{RepoPath: repo, Repos: []DashboardRepoInput{{Path: "https://example.com/repo"}}, BaselineStorePath: "store", BaselineKey: "key"}, "local filesystem"},
		{"bad topN", dashboardBaselineSaveArguments{RepoPath: repo, Repos: []DashboardRepoInput{{Path: repo}}, BaselineStorePath: "store", BaselineKey: "key", TopN: intPtr(0)}, "topN"},
		{"bad store", dashboardBaselineSaveArguments{RepoPath: repo, Repos: []DashboardRepoInput{{Path: repo}}, BaselineStorePath: "https://example.com/store", BaselineKey: "key"}, "local filesystem"},
		{"unknown feature", dashboardBaselineSaveArguments{RepoPath: repo, Repos: []DashboardRepoInput{{Path: repo}}, BaselineStorePath: "store", BaselineKey: "key", EnableFeatures: []string{"missing"}}, "unknown feature"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := server.resolveDashboardMutationRequest(context.Background(), tc.args)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q error, got %v", tc.want, err)
			}
		})
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := server.resolveDashboardMutationRequest(cancelled, dashboardBaselineSaveArguments{
		RepoPath:          repo,
		Repos:             []DashboardRepoInput{{Path: repo}},
		BaselineStorePath: "store",
		BaselineKey:       "key",
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled dashboard request, got %v", err)
	}
}

func TestResolveAnalysisMutationRequestRejectsCanonicalSymlinkEscapeUnderRequestedRepoAlias(t *testing.T) {
	requestedRepo, canonicalCache, outsideCache := mcpCanonicalAliasCacheEscapeFixture(t)
	server := NewServer(Options{Features: mustMutationFeatureSet(t, true), MutationRunner: &fakeMutationRunner{}})

	args := mutationAnalysisArguments{
		RepoPath:     requestedRepo,
		Dependency:   "dep",
		CacheEnabled: boolPtr(true),
		CachePath:    canonicalCache,
	}
	_, err := server.resolveAnalysisMutationRequest(context.Background(), args, mutationAnalysisKindDependency)
	if !analysis.CachePathSymlinkEscape(err) {
		t.Fatalf("expected canonical-form mutation symlink escape rejection, got %v", err)
	}
	if analysis.CachePathExternal(err) {
		t.Fatalf("expected mutation not to classify canonical in-repo form as external, got %v", err)
	}
	if _, statErr := os.Stat(outsideCache); !os.IsNotExist(statErr) {
		t.Fatalf("expected MCP mutation not to create external cache directory, stat err=%v", statErr)
	}
}

func TestResolveAnalysisMutationRequestRejectsAlternateAbsoluteRepoAliasesThatLaterEscape(t *testing.T) {
	for _, fixture := range mcpAlternateAbsoluteRepoAliasEscapeFixtures(t) {
		t.Run(fixture.name, func(t *testing.T) {
			server := NewServer(Options{Features: mustMutationFeatureSet(t, true), MutationRunner: &fakeMutationRunner{}})
			args := mutationAnalysisArguments{
				RepoPath:     fixture.requestedRepo,
				Dependency:   "dep",
				CacheEnabled: boolPtr(true),
				CachePath:    fixture.cachePath,
			}
			_, err := server.resolveAnalysisMutationRequest(context.Background(), args, mutationAnalysisKindDependency)
			if !analysis.CachePathSymlinkEscape(err) || analysis.CachePathExternal(err) {
				t.Fatalf("expected MCP mutation alternate-alias escape rejection, got %v", err)
			}
			if _, statErr := os.Stat(fixture.outsideCache); !os.IsNotExist(statErr) {
				t.Fatalf("expected MCP mutation not to create outside cache, stat err=%v", statErr)
			}
		})
	}
}

func TestCodemodMutationClassifiesExternalCacheAliasesIntoRepoBeforeRunnerInvocation(t *testing.T) {
	repo := t.TempDir()
	cacheSubdir := filepath.Join(repo, ".cache", "lopper")
	if err := os.MkdirAll(cacheSubdir, 0o750); err != nil {
		t.Fatalf("mkdir cache subdir: %v", err)
	}
	for _, tc := range mcpRepoCacheAliasFixtures(repo, cacheSubdir) {
		t.Run(tc.name, func(t *testing.T) {
			cacheAlias := mustMCPCacheAlias(t, tc.target)
			runner := &fakeMutationRunner{applyReport: sampleReport(repo)}
			server := NewServer(Options{Features: mustMutationFeatureSet(t, true), MutationRunner: runner})
			result := callToolResult(t, server, toolApplyCodemod, map[string]any{
				"repoPath":     repo,
				"dependency":   "lodash",
				"cachePath":    cacheAlias,
				"cacheEnabled": true,
				"include":      []string{"**"},
				"confirmApply": true,
			})
			assertMCPMutationRepoCacheAliasOutcome(t, result, runner.applyCalled, analysis.InRepoCacheOptions(runner.lastApply.Cache), tc.wantReject)
			assertMCPCacheLayoutAbsent(t, tc.target)
		})
	}
}

func assertMCPMutationRepoCacheAliasOutcome(t *testing.T, result toolCallResult, called, inRepo bool, wantReject bool) {
	t.Helper()
	if wantReject {
		if !result.IsError || !strings.Contains(result.Content[0].Text, "scoped analysis does not allow cachePath at the repository root") || called {
			t.Fatalf("expected external repo-root alias rejection, got %#v called=%t", result, called)
		}
		return
	}
	if result.IsError || !called || !inRepo {
		t.Fatalf("expected in-repo cache pin for mutation alias, result=%#v called=%t inRepo=%t", result, called, inRepo)
	}
}

func TestResolveLocalMutationPath(t *testing.T) {
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
}

func TestResolveBaselineMutationTarget(t *testing.T) {
	repo := t.TempDir()
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
}

func TestValidateDashboardMutationRepos(t *testing.T) {
	repo := t.TempDir()
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
}

func TestMutationPayloadSummaryHelpers(t *testing.T) {
	repo := t.TempDir()
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

func assertMutationToolErrorContains(t *testing.T, result toolCallResult, want string) {
	t.Helper()
	if !result.IsError {
		t.Fatalf("expected mutation tool error, got %#v", result)
	}
	if want == "" {
		return
	}
	if len(result.Content) == 0 || !strings.Contains(result.Content[0].Text, want) {
		t.Fatalf("expected %q error, got %#v", want, result)
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
