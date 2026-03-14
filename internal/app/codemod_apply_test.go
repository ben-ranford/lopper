package app

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestApplyCodemodIfNeededSuccessWritesRollbackAndSummary(t *testing.T) {
	repo := t.TempDir()
	sourcePath := filepath.Join(repo, "src", indexJSFile)
	mustMkdirAll(t, filepath.Dir(sourcePath))
	original := importLodashLineWithLF + "map([1], (x) => x)\n"
	writeTextFile(t, sourcePath, original, 0o644)

	reportData := report.Report{
		Dependencies: []report.DependencyReport{
			{
				Name: "lodash",
				Codemod: &report.CodemodReport{
					Mode: codemodSuggestOnlyMode,
					Suggestions: []report.CodemodSuggestion{
						{
							File:        filepath.ToSlash(filepath.Join("src", indexJSFile)),
							Line:        1,
							ImportName:  "map",
							FromModule:  "lodash",
							ToModule:    lodashMapModule,
							Original:    importLodashLine,
							Replacement: importLodashMapLine,
						},
					},
					Skips: []report.CodemodSkip{
						{
							File:       filepath.ToSlash(filepath.Join("src", "other.js")),
							Line:       3,
							ImportName: "filter",
							Module:     "lodash",
							ReasonCode: aliasConflictReason,
							Message:    "aliased imports are skipped to avoid local-name conflicts",
						},
					},
				},
			},
		},
	}

	updated, err := applyCodemodIfNeeded(context.Background(), reportData, repo, AnalyseRequest{Dependency: "lodash", ApplyCodemod: true}, time.Date(2026, time.March, 13, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("apply codemod: %v", err)
	}

	assertFileContains(t, sourcePath, importLodashMapLine)

	applyReport := requireCodemodApplyReport(t, updated)
	assertCodemodApplyCounts(t, applyReport, codemodApplyCounts{
		AppliedFiles:   1,
		AppliedPatches: 1,
		SkippedFiles:   1,
		SkippedPatches: 1,
	})
	assertRollbackArtifact(t, repo, applyReport.BackupPath, "lodash", original)
}

func TestApplyCodemodIfNeededMismatchReturnsErrorAndNoMutation(t *testing.T) {
	repo := t.TempDir()
	sourcePath := filepath.Join(repo, indexJSFile)
	content := "import { map } from \"lodash-es\";\n"
	writeTextFile(t, sourcePath, content, 0o644)

	updated, err := applyCodemodIfNeeded(context.Background(), singleLodashSuggestionReport(indexJSFile), repo, AnalyseRequest{Dependency: "lodash", ApplyCodemod: true}, time.Date(2026, time.March, 13, 12, 0, 0, 0, time.UTC))
	if !errors.Is(err, ErrCodemodApplyFailed) {
		t.Fatalf("expected codemod apply failure, got %v", err)
	}
	if got := readTextFile(t, sourcePath); got != content {
		t.Fatalf("expected source to remain unchanged, got %q", got)
	}

	applyReport := requireCodemodApplyReport(t, updated)
	assertCodemodApplyCounts(t, applyReport, codemodApplyCounts{
		FailedFiles:   1,
		FailedPatches: 1,
	})
	if applyReport.BackupPath != "" {
		t.Fatalf("expected no backup artifact for failed pre-write apply, got %q", applyReport.BackupPath)
	}
}

func TestApplyCodemodIfNeededSkipsOnlyProducesSummary(t *testing.T) {
	reportData := report.Report{
		Dependencies: []report.DependencyReport{
			{
				Name: "lodash",
				Codemod: &report.CodemodReport{
					Mode: codemodSuggestOnlyMode,
					Skips: []report.CodemodSkip{
						{File: srcAJSFile, Line: 1, ImportName: "map", Module: "lodash", ReasonCode: aliasConflictReason},
						{File: srcAJSFile, Line: 2, ImportName: "filter", Module: "lodash", ReasonCode: aliasConflictReason},
						{File: srcAJSFile, Line: 3, ImportName: "reduce", Module: "lodash", ReasonCode: "needs-manual-review"},
						{File: "src/b.js", Line: 1, ImportName: "uniq", Module: "lodash", ReasonCode: aliasConflictReason},
					},
				},
			},
		},
	}

	updated, err := applyCodemodIfNeeded(context.Background(), reportData, t.TempDir(), AnalyseRequest{Dependency: "lodash", ApplyCodemod: true}, time.Date(2026, time.March, 13, 12, 30, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("apply codemod skips-only: %v", err)
	}

	applyReport := requireCodemodApplyReport(t, updated)
	assertCodemodApplyCounts(t, applyReport, codemodApplyCounts{
		SkippedFiles:   2,
		SkippedPatches: 4,
	})
	if applyReport.BackupPath != "" {
		t.Fatalf("expected no backup path for skip-only apply, got %q", applyReport.BackupPath)
	}
	if len(applyReport.Results) != 2 {
		t.Fatalf("expected grouped skip results, got %#v", applyReport.Results)
	}
	if applyReport.Results[0].File != srcAJSFile || applyReport.Results[0].PatchCount != 3 {
		t.Fatalf("unexpected first skip result: %#v", applyReport.Results[0])
	}
	if applyReport.Results[0].Message != "reason codes: alias-conflict, needs-manual-review" {
		t.Fatalf("unexpected grouped skip message: %q", applyReport.Results[0].Message)
	}
}

func TestExecuteAnalyseForwardsSuggestOnlyWhenApplyingCodemod(t *testing.T) {
	analyzer := &fakeAnalyzer{
		report: report.Report{
			Dependencies: []report.DependencyReport{
				{Name: "lodash", UsedExportsCount: 1, TotalExportsCount: 1, UsedPercent: 100},
			},
		},
	}
	application := &App{Analyzer: analyzer, Formatter: report.NewFormatter()}

	req := DefaultRequest()
	req.Mode = ModeAnalyse
	req.RepoPath = t.TempDir()
	req.Analyse.Dependency = "lodash"
	req.Analyse.ApplyCodemod = true
	req.Analyse.AllowDirty = true
	req.Analyse.Format = report.FormatJSON

	if _, err := application.Execute(context.Background(), req); err != nil {
		t.Fatalf("execute analyse with apply codemod: %v", err)
	}
	if !analyzer.lastReq.SuggestOnly {
		t.Fatalf("expected apply-codemod to force codemod suggestion generation")
	}
}

func TestValidateCodemodApplyPreconditions(t *testing.T) {
	nonRepo := t.TempDir()
	if err := validateCodemodApplyPreconditions(context.Background(), nonRepo, AnalyseRequest{}); err != nil {
		t.Fatalf("expected no precondition error when apply mode is disabled, got %v", err)
	}
	if err := validateCodemodApplyPreconditions(context.Background(), nonRepo, AnalyseRequest{ApplyCodemod: true}); err != nil {
		t.Fatalf("expected non-git directory to pass apply preconditions, got %v", err)
	}

	repo := t.TempDir()
	initCodemodGitRepo(t, repo)
	writeTextFile(t, filepath.Join(repo, "tracked.txt"), "tracked\n", 0o644)
	testutil.RunGit(t, repo, "add", "tracked.txt")
	testutil.RunGit(t, repo, "commit", "-m", "tracked")

	if err := validateCodemodApplyPreconditions(context.Background(), repo, AnalyseRequest{ApplyCodemod: true}); err != nil {
		t.Fatalf("expected clean git worktree to pass, got %v", err)
	}

	writeTextFile(t, filepath.Join(repo, "dirty.txt"), "dirty\n", 0o644)
	err := validateCodemodApplyPreconditions(context.Background(), repo, AnalyseRequest{ApplyCodemod: true})
	if !errors.Is(err, ErrDirtyWorktree) {
		t.Fatalf("expected dirty-worktree error, got %v", err)
	}
	for i := range 6 {
		writeTextFile(t, filepath.Join(repo, "dirty-extra-"+string(rune('a'+i))+".txt"), "dirty\n", 0o644)
	}
	err = validateCodemodApplyPreconditions(context.Background(), repo, AnalyseRequest{ApplyCodemod: true})
	if err == nil || !strings.Contains(err.Error(), "+2 more") {
		t.Fatalf("expected truncated dirty file summary, got %v", err)
	}
	if err := validateCodemodApplyPreconditions(context.Background(), repo, AnalyseRequest{ApplyCodemod: true, AllowDirty: true}); err != nil {
		t.Fatalf("expected allow-dirty to bypass precondition failure, got %v", err)
	}
}

func TestEnsureCleanWorktreeForCodemodPropagatesGitErrors(t *testing.T) {
	originalResolve := resolveGitBinaryPathFn
	originalExec := execGitCommandContextFn
	resolveGitBinaryPathFn = func() (string, error) { return gitBinaryPath, nil }
	execGitCommandContextFn = func(ctx context.Context, gitPath string, args ...string) (*exec.Cmd, error) {
		commandLine := strings.Join(args, " ")
		if strings.Contains(commandLine, "rev-parse --is-inside-work-tree") {
			return exec.CommandContext(ctx, "/usr/bin/printf", "true"), nil
		}
		return exec.CommandContext(ctx, "/bin/sh", "-c", "exit 2"), nil
	}
	defer func() {
		resolveGitBinaryPathFn = originalResolve
		execGitCommandContextFn = originalExec
	}()

	err := ensureCleanWorktreeForCodemod(context.Background(), t.TempDir(), false)
	if err == nil || !strings.Contains(err.Error(), "run git rev-parse --verify --quiet HEAD") {
		t.Fatalf("expected git helper error to be returned, got %v", err)
	}
}

func TestCodemodApplyPathAndRollbackErrorBranches(t *testing.T) {
	repo := t.TempDir()
	writeTextFile(t, filepath.Join(repo, indexJSFile), importLodashLineWithLF, 0o644)

	blockedPath := filepath.Join(repo, codemodRollbackDir)
	mustMkdirAll(t, filepath.Dir(blockedPath))
	writeTextFile(t, blockedPath, "blocked\n", 0o600)

	_, err := applyCodemodIfNeeded(context.Background(), singleLodashSuggestionReport(indexJSFile), repo, AnalyseRequest{Dependency: "lodash", ApplyCodemod: true, AllowDirty: true}, time.Now())
	if err == nil || !strings.Contains(err.Error(), "write codemod rollback artifact") {
		t.Fatalf("expected rollback artifact error, got %v", err)
	}
}

func initCodemodGitRepo(t *testing.T, repo string) {
	t.Helper()
	testutil.RunGit(t, repo, "init")
	testutil.RunGit(t, repo, "config", "user.email", "codex@example.com")
	testutil.RunGit(t, repo, "config", "user.name", "Codex")
}

func singleLodashSuggestionReport(file string) report.Report {
	return report.Report{
		Dependencies: []report.DependencyReport{
			{
				Name: "lodash",
				Codemod: &report.CodemodReport{
					Mode: codemodSuggestOnlyMode,
					Suggestions: []report.CodemodSuggestion{
						{
							File:        file,
							Line:        1,
							ImportName:  "map",
							FromModule:  "lodash",
							ToModule:    lodashMapModule,
							Original:    importLodashLine,
							Replacement: importLodashMapLine,
						},
					},
				},
			},
		},
	}
}
