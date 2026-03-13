package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestApplyCodemodIfNeededSuccessWritesRollbackAndSummary(t *testing.T) {
	repo := t.TempDir()
	sourcePath := filepath.Join(repo, "src", "index.js")
	if err := os.MkdirAll(filepath.Dir(sourcePath), 0o755); err != nil {
		t.Fatalf("mkdir source dir: %v", err)
	}
	original := "import { map } from \"lodash\";\nmap([1], (x) => x)\n"
	if err := os.WriteFile(sourcePath, []byte(original), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	reportData := report.Report{
		Dependencies: []report.DependencyReport{
			{
				Name: "lodash",
				Codemod: &report.CodemodReport{
					Mode: "suggest-only",
					Suggestions: []report.CodemodSuggestion{
						{
							File:        filepath.ToSlash(filepath.Join("src", "index.js")),
							Line:        1,
							ImportName:  "map",
							FromModule:  "lodash",
							ToModule:    "lodash/map",
							Original:    "import { map } from \"lodash\";",
							Replacement: "import map from \"lodash/map\";",
						},
					},
					Skips: []report.CodemodSkip{
						{
							File:       filepath.ToSlash(filepath.Join("src", "other.js")),
							Line:       3,
							ImportName: "filter",
							Module:     "lodash",
							ReasonCode: "alias-conflict",
							Message:    "aliased imports are skipped to avoid local-name conflicts",
						},
					},
				},
			},
		},
	}

	req := AnalyseRequest{
		Dependency:   "lodash",
		ApplyCodemod: true,
	}
	updated, err := applyCodemodIfNeeded(context.Background(), reportData, repo, req, time.Date(2026, time.March, 13, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("apply codemod: %v", err)
	}

	content, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatalf("read updated source: %v", err)
	}
	if !strings.Contains(string(content), "import map from \"lodash/map\";") {
		t.Fatalf("expected source rewrite, got %q", string(content))
	}

	applyReport := updated.Dependencies[0].Codemod.Apply
	if applyReport == nil {
		t.Fatalf("expected codemod apply summary")
	}
	if applyReport.AppliedFiles != 1 || applyReport.AppliedPatches != 1 {
		t.Fatalf("unexpected applied summary: %#v", applyReport)
	}
	if applyReport.SkippedFiles != 1 || applyReport.SkippedPatches != 1 {
		t.Fatalf("unexpected skipped summary: %#v", applyReport)
	}
	if applyReport.BackupPath == "" {
		t.Fatalf("expected backup path in apply summary")
	}

	backupData, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(applyReport.BackupPath)))
	if err != nil {
		t.Fatalf("read backup artifact: %v", err)
	}
	var artifact struct {
		Dependency string `json:"dependency"`
		Files      []struct {
			File    string `json:"file"`
			Content string `json:"content"`
		} `json:"files"`
	}
	if err := json.Unmarshal(backupData, &artifact); err != nil {
		t.Fatalf("decode backup artifact: %v", err)
	}
	if artifact.Dependency != "lodash" || len(artifact.Files) != 1 {
		t.Fatalf("unexpected backup artifact payload: %#v", artifact)
	}
	if artifact.Files[0].Content != original {
		t.Fatalf("expected original file content in rollback artifact, got %q", artifact.Files[0].Content)
	}
}

func TestApplyCodemodIfNeededMismatchReturnsErrorAndNoMutation(t *testing.T) {
	repo := t.TempDir()
	sourcePath := filepath.Join(repo, "index.js")
	content := "import { map } from \"lodash-es\";\n"
	if err := os.WriteFile(sourcePath, []byte(content), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	reportData := singleLodashSuggestionReport("index.js")

	req := AnalyseRequest{
		Dependency:   "lodash",
		ApplyCodemod: true,
	}
	updated, err := applyCodemodIfNeeded(context.Background(), reportData, repo, req, time.Date(2026, time.March, 13, 12, 0, 0, 0, time.UTC))
	if !errors.Is(err, ErrCodemodApplyFailed) {
		t.Fatalf("expected codemod apply failure, got %v", err)
	}

	after, readErr := os.ReadFile(sourcePath)
	if readErr != nil {
		t.Fatalf("read source after failed apply: %v", readErr)
	}
	if string(after) != content {
		t.Fatalf("expected source to remain unchanged, got %q", string(after))
	}

	applyReport := updated.Dependencies[0].Codemod.Apply
	if applyReport == nil || applyReport.FailedFiles != 1 || applyReport.FailedPatches != 1 {
		t.Fatalf("expected failed apply summary, got %#v", applyReport)
	}
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
					Mode: "suggest-only",
					Skips: []report.CodemodSkip{
						{
							File:       "src/a.js",
							Line:       1,
							ImportName: "map",
							Module:     "lodash",
							ReasonCode: "alias-conflict",
						},
						{
							File:       "src/a.js",
							Line:       2,
							ImportName: "filter",
							Module:     "lodash",
							ReasonCode: "alias-conflict",
						},
						{
							File:       "src/a.js",
							Line:       3,
							ImportName: "reduce",
							Module:     "lodash",
							ReasonCode: "needs-manual-review",
						},
						{
							File:       "src/b.js",
							Line:       1,
							ImportName: "uniq",
							Module:     "lodash",
							ReasonCode: "alias-conflict",
						},
					},
				},
			},
		},
	}

	updated, err := applyCodemodIfNeeded(context.Background(), reportData, t.TempDir(), AnalyseRequest{Dependency: "lodash", ApplyCodemod: true}, time.Date(2026, time.March, 13, 12, 30, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("apply codemod skips-only: %v", err)
	}

	applyReport := updated.Dependencies[0].Codemod.Apply
	if applyReport == nil {
		t.Fatalf("expected codemod apply summary")
	}
	if applyReport.AppliedFiles != 0 || applyReport.AppliedPatches != 0 {
		t.Fatalf("expected no applied patches, got %#v", applyReport)
	}
	if applyReport.SkippedFiles != 2 || applyReport.SkippedPatches != 4 {
		t.Fatalf("unexpected skipped summary: %#v", applyReport)
	}
	if applyReport.FailedFiles != 0 || applyReport.FailedPatches != 0 {
		t.Fatalf("expected no failed patches, got %#v", applyReport)
	}
	if applyReport.BackupPath != "" {
		t.Fatalf("expected no backup path for skip-only apply, got %q", applyReport.BackupPath)
	}
	if len(applyReport.Results) != 2 {
		t.Fatalf("expected grouped skip results, got %#v", applyReport.Results)
	}
	if applyReport.Results[0].File != "src/a.js" || applyReport.Results[0].PatchCount != 3 {
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
	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("tracked\n"), 0o644); err != nil {
		t.Fatalf("write tracked file: %v", err)
	}
	testutil.RunGit(t, repo, "add", "tracked.txt")
	testutil.RunGit(t, repo, "commit", "-m", "tracked")

	if err := validateCodemodApplyPreconditions(context.Background(), repo, AnalyseRequest{ApplyCodemod: true}); err != nil {
		t.Fatalf("expected clean git worktree to pass, got %v", err)
	}

	if err := os.WriteFile(filepath.Join(repo, "dirty.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatalf("write dirty file: %v", err)
	}
	err := validateCodemodApplyPreconditions(context.Background(), repo, AnalyseRequest{ApplyCodemod: true})
	if !errors.Is(err, ErrDirtyWorktree) {
		t.Fatalf("expected dirty-worktree error, got %v", err)
	}
	for i := range 6 {
		filePath := filepath.Join(repo, "dirty-extra-"+string(rune('a'+i))+".txt")
		if err := os.WriteFile(filePath, []byte("dirty\n"), 0o644); err != nil {
			t.Fatalf("write extra dirty file: %v", err)
		}
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

func TestCodemodApplyHelpers(t *testing.T) {
	t.Run("find codemod report", func(t *testing.T) {
		if got := findCodemodReport(nil, "lodash"); got != nil {
			t.Fatalf("expected nil codemod report for nil report input")
		}

		fallback := &report.CodemodReport{Mode: "apply"}
		reportData := report.Report{
			Dependencies: []report.DependencyReport{
				{Name: "left-pad"},
				{Name: "lodash", Codemod: fallback},
			},
		}
		if got := findCodemodReport(&reportData, "missing"); got != fallback {
			t.Fatalf("expected first available codemod fallback, got %#v", got)
		}
		if got := findCodemodReport(&reportData, "lodash"); got != fallback {
			t.Fatalf("expected dependency-specific codemod report, got %#v", got)
		}
		if got := findCodemodReport(&report.Report{Dependencies: []report.DependencyReport{{Name: "only"}}}, "missing"); got != nil {
			t.Fatalf("expected nil codemod fallback when no dependency has codemod data, got %#v", got)
		}
	})

	t.Run("build codemod skip results", func(t *testing.T) {
		if got := buildCodemodSkipResults(nil); len(got) != 0 {
			t.Fatalf("expected no skip results for nil input, got %#v", got)
		}

		results := buildCodemodSkipResults([]report.CodemodSkip{
			{File: "b.js", ReasonCode: "beta"},
			{File: "a.js", ReasonCode: " alpha "},
			{File: "a.js", ReasonCode: "beta"},
			{File: "a.js", ReasonCode: "beta"},
			{File: "a.js", ReasonCode: ""},
		})
		if len(results) != 2 {
			t.Fatalf("expected grouped skip results, got %#v", results)
		}
		if results[0].File != "a.js" || results[0].PatchCount != 4 {
			t.Fatalf("unexpected grouped skip result: %#v", results[0])
		}
		if results[0].Message != "reason codes: alpha, beta" {
			t.Fatalf("expected deduplicated sorted reason codes, got %q", results[0].Message)
		}
		if results[1].File != "b.js" || results[1].Message != "reason codes: beta" {
			t.Fatalf("unexpected second skip result: %#v", results[1])
		}
	})

	t.Run("apply suggestions to content", func(t *testing.T) {
		suggestions := []report.CodemodSuggestion{
			{File: "index.js", Line: 1, Original: "import { map } from \"lodash\";", Replacement: "import map from \"lodash/map\";"},
		}
		updated, err := applySuggestionsToContent("import { map } from \"lodash\";\r\nmap()\r\n", suggestions)
		if err != nil {
			t.Fatalf("apply suggestions with CRLF: %v", err)
		}
		if !strings.Contains(updated, "\r\n") || !strings.Contains(updated, "import map from \"lodash/map\";") {
			t.Fatalf("expected CRLF-preserving updated content, got %q", updated)
		}

		_, err = applySuggestionsToContent("import { map } from \"lodash\";\n", []report.CodemodSuggestion{{File: "index.js", Line: 3, Original: "x", Replacement: "y"}})
		if err == nil || !strings.Contains(err.Error(), "out of range") {
			t.Fatalf("expected line-range error, got %v", err)
		}
		_, err = applySuggestionsToContent("import { map } from \"lodash\";\n", []report.CodemodSuggestion{{File: "index.js", Line: 0, Original: "x", Replacement: "y"}})
		if err == nil || !strings.Contains(err.Error(), "out of range") {
			t.Fatalf("expected zero-line range error, got %v", err)
		}

		_, err = applySuggestionsToContent("import { map } from \"lodash\";\n", []report.CodemodSuggestion{{File: "index.js", Line: 1, Original: "import { filter } from \"lodash\";", Replacement: "import filter from \"lodash/filter\";"}})
		if err == nil || !strings.Contains(err.Error(), "source line mismatch") {
			t.Fatalf("expected source mismatch error, got %v", err)
		}
	})

	t.Run("prepare codemod files", func(t *testing.T) {
		repo := t.TempDir()
		if err := os.WriteFile(filepath.Join(repo, "index.js"), []byte("import { map } from \"lodash\";\n"), 0o644); err != nil {
			t.Fatalf("write source: %v", err)
		}
		suggestions := []report.CodemodSuggestion{
			{File: "index.js", Line: 1, Original: "import { map } from \"lodash\";", Replacement: "import map from \"lodash/map\";"},
			{File: "../escape.js", Line: 1, Original: "x", Replacement: "y"},
			{File: "missing.js", Line: 1, Original: "x", Replacement: "y"},
		}

		prepared, failures := prepareCodemodFiles(repo, suggestions)
		if len(prepared) != 1 {
			t.Fatalf("expected one prepared file, got %#v", prepared)
		}
		if prepared[0].patchCount != 1 || !strings.Contains(prepared[0].updated, "lodash/map") {
			t.Fatalf("unexpected prepared file payload: %#v", prepared[0])
		}
		if len(failures) != 2 {
			t.Fatalf("expected two preparation failures, got %#v", failures)
		}
	})

	t.Run("prepare codemod files same-line tie ordering", func(t *testing.T) {
		repo := t.TempDir()
		if err := os.WriteFile(filepath.Join(repo, "index.js"), []byte("import { map } from \"lodash\";\n"), 0o644); err != nil {
			t.Fatalf("write source: %v", err)
		}
		suggestions := []report.CodemodSuggestion{
			{File: "index.js", Line: 1, ImportName: "zeta", Original: "import { map } from \"lodash\";", Replacement: "import zeta from \"lodash/zeta\";"},
			{File: "index.js", Line: 1, ImportName: "alpha", Original: "import { map } from \"lodash\";", Replacement: "import alpha from \"lodash/alpha\";"},
		}

		prepared, failures := prepareCodemodFiles(repo, suggestions)
		if len(prepared) != 0 {
			t.Fatalf("expected no prepared files when same-line suggestions conflict, got %#v", prepared)
		}
		if len(failures) != 1 || failures[0].PatchCount != 2 {
			t.Fatalf("expected one grouped failure for conflicting same-line suggestions, got %#v", failures)
		}
	})

	t.Run("prepare codemod files stat failure", func(t *testing.T) {
		repo := t.TempDir()
		fifoPath := filepath.Join(repo, "index.js")
		if err := syscall.Mkfifo(fifoPath, 0o644); err != nil {
			t.Fatalf("mkfifo source: %v", err)
		}

		writerDone := make(chan struct{})
		writerErrs := make(chan error, 3)
		go func() {
			defer close(writerDone)
			file, err := os.OpenFile(fifoPath, os.O_WRONLY, 0)
			if err != nil {
				writerErrs <- err
				return
			}
			if _, err := file.WriteString("import { map } from \"lodash\";\n"); err != nil {
				writerErrs <- err
			}
			if err := os.Remove(fifoPath); err != nil && !os.IsNotExist(err) {
				writerErrs <- err
			}
			if err := file.Close(); err != nil {
				writerErrs <- err
			}
		}()

		prepared, failures := prepareCodemodFiles(repo, []report.CodemodSuggestion{
			{File: "index.js", Line: 1, Original: "import { map } from \"lodash\";", Replacement: "import map from \"lodash/map\";"},
		})
		<-writerDone
		close(writerErrs)
		for err := range writerErrs {
			t.Fatalf("fifo writer error: %v", err)
		}

		if len(prepared) != 0 {
			t.Fatalf("expected no prepared files when stat fails, got %#v", prepared)
		}
		if len(failures) != 1 || !strings.Contains(strings.ToLower(failures[0].Message), "no such") {
			t.Fatalf("expected stat failure result, got %#v", failures)
		}
	})

	t.Run("prepare codemod files different-line ordering", func(t *testing.T) {
		repo := t.TempDir()
		if err := os.WriteFile(filepath.Join(repo, "index.js"), []byte("import { map } from \"lodash\";\nmap(items)\n"), 0o644); err != nil {
			t.Fatalf("write source: %v", err)
		}
		suggestions := []report.CodemodSuggestion{
			{File: "index.js", Line: 2, ImportName: "mapCall", Original: "map(items)", Replacement: "lodashMap(items)"},
			{File: "index.js", Line: 1, ImportName: "mapImport", Original: "import { map } from \"lodash\";", Replacement: "import lodashMap from \"lodash/map\";"},
		}

		prepared, failures := prepareCodemodFiles(repo, suggestions)
		if len(failures) != 0 {
			t.Fatalf("expected no preparation failures, got %#v", failures)
		}
		if len(prepared) != 1 {
			t.Fatalf("expected one prepared file, got %#v", prepared)
		}
		if prepared[0].patchCount != 2 {
			t.Fatalf("expected both patches to be prepared, got %#v", prepared[0])
		}
		if prepared[0].updated != "import lodashMap from \"lodash/map\";\nlodashMap(items)\n" {
			t.Fatalf("unexpected updated content after ordered apply: %q", prepared[0].updated)
		}
	})

	t.Run("apply prepared files and rollback artifact", func(t *testing.T) {
		repo := t.TempDir()
		okPath := filepath.Join(repo, "index.js")
		if err := os.WriteFile(okPath, []byte("before\n"), 0o644); err != nil {
			t.Fatalf("write source: %v", err)
		}

		preparedFiles := []preparedCodemodFile{
			{file: "index.js", absPath: okPath, updated: "after\n", patchCount: 1, mode: 0o644},
			{file: "missing.js", absPath: filepath.Join(repo, "missing", "nested.js"), updated: "nope\n", patchCount: 1, mode: 0o644},
		}
		applied, failures := applyPreparedCodemodFiles(preparedFiles, nil)
		if len(applied) != 1 || applied[0].Status != codemodApplyStatusApplied {
			t.Fatalf("expected one applied file result, got %#v", applied)
		}
		if len(failures) != 1 || failures[0].Status != codemodApplyStatusFailed {
			t.Fatalf("expected one failed file result, got %#v", failures)
		}

		content, err := os.ReadFile(okPath)
		if err != nil {
			t.Fatalf("read applied file: %v", err)
		}
		if string(content) != "after\n" {
			t.Fatalf("expected updated file content, got %q", string(content))
		}

		rollbackFiles := []preparedCodemodFile{
			{file: "index.js", original: "before\n", mode: 0o644},
		}
		backupPath, err := writeCodemodRollbackArtifact(repo, "lodash/map", rollbackFiles, time.Date(2026, time.March, 13, 13, 0, 0, 0, time.UTC))
		if err != nil {
			t.Fatalf("write rollback artifact: %v", err)
		}
		if !strings.Contains(backupPath, "lodash-map") {
			t.Fatalf("expected sanitized dependency name in backup path, got %q", backupPath)
		}
		if _, err := os.Stat(filepath.Join(repo, filepath.FromSlash(backupPath))); err != nil {
			t.Fatalf("expected rollback artifact file to exist: %v", err)
		}

		emptyPath, err := writeCodemodRollbackArtifact(repo, "lodash", nil, time.Now())
		if err != nil || emptyPath != "" {
			t.Fatalf("expected empty rollback artifact result, got path=%q err=%v", emptyPath, err)
		}
	})

	t.Run("path and sorting helpers", func(t *testing.T) {
		repo := t.TempDir()
		if _, err := resolveCodemodFilePath(repo, ""); err == nil {
			t.Fatalf("expected empty path resolution error")
		}
		if _, err := resolveCodemodFilePath(repo, filepath.Join(repo, "index.js")); err == nil {
			t.Fatalf("expected absolute path resolution error")
		}
		if _, err := resolveCodemodFilePath(repo, "../escape.js"); err == nil {
			t.Fatalf("expected escaping path resolution error")
		}
		resolved, err := resolveCodemodFilePath(repo, filepath.Join("src", "index.js"))
		if err != nil || resolved != filepath.Join(repo, "src", "index.js") {
			t.Fatalf("expected relative path resolution, got path=%q err=%v", resolved, err)
		}

		values := uniqueSortedStrings([]string{"beta", "alpha", "beta", "", " alpha "})
		if !reflect.DeepEqual(values, []string{"alpha", "beta"}) {
			t.Fatalf("unexpected unique sorted values: %#v", values)
		}
		if got := uniqueSortedStrings(nil); len(got) != 0 {
			t.Fatalf("expected empty unique-sorted result for nil input, got %#v", got)
		}
		if got := sanitizeArtifactName(""); got != "codemod" {
			t.Fatalf("expected blank artifact name fallback, got %q", got)
		}
		if got := sanitizeArtifactName("///"); got != "codemod" {
			t.Fatalf("expected fallback artifact name, got %q", got)
		}

		results := []report.CodemodApplyResult{
			{File: "b.js", Status: codemodApplyStatusFailed, PatchCount: 1},
			{File: "a.js", Status: codemodApplyStatusSkipped, PatchCount: 2},
			{File: "a.js", Status: codemodApplyStatusApplied, PatchCount: 1},
			{File: "a.js", Status: codemodApplyStatusApplied, PatchCount: 3},
		}
		sortCodemodApplyResults(results)
		if results[0].Status != codemodApplyStatusApplied || results[1].Status != codemodApplyStatusApplied || results[2].Status != codemodApplyStatusSkipped {
			t.Fatalf("unexpected sort order: %#v", results)
		}
		if results[0].PatchCount != 1 || results[1].PatchCount != 3 {
			t.Fatalf("expected patch-count tie-break ordering, got %#v", results)
		}

		err = codemodApplyError([]report.CodemodApplyResult{{File: "bad.js", Status: codemodApplyStatusFailed, Message: "boom"}})
		if !errors.Is(err, ErrCodemodApplyFailed) || !strings.Contains(err.Error(), "bad.js: boom") {
			t.Fatalf("expected wrapped codemod apply error, got %v", err)
		}
		if err := codemodApplyError([]report.CodemodApplyResult{{File: "skip.js", Status: codemodApplyStatusSkipped}}); !errors.Is(err, ErrCodemodApplyFailed) {
			t.Fatalf("expected bare codemod apply error for skip-only results, got %v", err)
		}
		if err := codemodApplyError(nil); !errors.Is(err, ErrCodemodApplyFailed) {
			t.Fatalf("expected bare codemod apply error, got %v", err)
		}
	})

	t.Run("file and no-op helpers", func(t *testing.T) {
		repo := t.TempDir()
		targetPath := filepath.Join(repo, "atomic.txt")
		if err := writeFileAtomically(targetPath, "written\n", 0o640); err != nil {
			t.Fatalf("write file atomically: %v", err)
		}
		info, err := os.Stat(targetPath)
		if err != nil {
			t.Fatalf("stat atomically written file: %v", err)
		}
		if info.Mode().Perm() != 0o640 {
			t.Fatalf("expected atomically written file mode 0640, got %#o", info.Mode().Perm())
		}
		if err := writeFileAtomically(filepath.Join(repo, "missing", "atomic.txt"), "written\n", 0o640); err == nil {
			t.Fatalf("expected atomic write into missing directory to fail")
		}
		existingDir := filepath.Join(repo, "existing-dir")
		if err := os.MkdirAll(existingDir, 0o755); err != nil {
			t.Fatalf("mkdir existing target dir: %v", err)
		}
		if err := writeFileAtomically(existingDir, "written\n", 0o640); err == nil {
			t.Fatalf("expected atomic rename into existing directory target to fail")
		}

		blockedRepo := t.TempDir()
		blockedPath := filepath.Join(blockedRepo, codemodRollbackDir)
		if err := os.MkdirAll(filepath.Dir(blockedPath), 0o755); err != nil {
			t.Fatalf("mkdir parent for blocked rollback path: %v", err)
		}
		if err := os.WriteFile(blockedPath, []byte("blocked\n"), 0o600); err != nil {
			t.Fatalf("write blocked rollback path file: %v", err)
		}
		_, err = writeCodemodRollbackArtifact(blockedRepo, "lodash", []preparedCodemodFile{{file: "index.js", original: "before\n", mode: 0o644}}, time.Now())
		if err == nil {
			t.Fatalf("expected rollback artifact write to fail when backup path is blocked by a file")
		}

		readonlyRepo := t.TempDir()
		readonlyDir := filepath.Join(readonlyRepo, codemodRollbackDir)
		if err := os.MkdirAll(readonlyDir, 0o700); err != nil {
			t.Fatalf("mkdir readonly rollback dir: %v", err)
		}
		t.Cleanup(func() {
			if err := os.Chmod(readonlyDir, 0o700); err != nil && !os.IsNotExist(err) {
				t.Errorf("restore readonly rollback dir permissions: %v", err)
			}
		})
		if err := os.Chmod(readonlyDir, 0o500); err != nil {
			t.Fatalf("chmod readonly rollback dir: %v", err)
		}
		_, err = writeCodemodRollbackArtifact(readonlyRepo, "lodash", []preparedCodemodFile{{file: "index.js", original: "before\n", mode: 0o644}}, time.Now())
		if err == nil {
			t.Fatalf("expected rollback artifact write to fail when rollback dir is not writable")
		}

		reportData := report.Report{
			Dependencies: []report.DependencyReport{
				{Name: "lodash"},
			},
		}
		updated, err := applyCodemodIfNeeded(context.Background(), reportData, repo, AnalyseRequest{Dependency: "lodash", ApplyCodemod: false, AllowDirty: true}, time.Now())
		if err != nil {
			t.Fatalf("expected applyCodemodIfNeeded no-op when apply mode is disabled, got %v", err)
		}
		if !reflect.DeepEqual(updated, reportData) {
			t.Fatalf("expected report to remain unchanged when apply mode is disabled, got %#v", updated)
		}
		updated, err = applyCodemodIfNeeded(context.Background(), reportData, repo, AnalyseRequest{Dependency: "lodash", ApplyCodemod: true, AllowDirty: true}, time.Now())
		if err != nil {
			t.Fatalf("expected codemod apply no-op without codemod report, got %v", err)
		}
		if updated.Dependencies[0].Codemod != nil {
			t.Fatalf("expected no codemod apply summary when dependency has no codemod report, got %#v", updated.Dependencies[0].Codemod)
		}
	})
}

func TestCodemodApplyPathAndRollbackErrorBranches(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "index.js"), []byte("import { map } from \"lodash\";\n"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	blockedPath := filepath.Join(repo, codemodRollbackDir)
	if err := os.MkdirAll(filepath.Dir(blockedPath), 0o755); err != nil {
		t.Fatalf("mkdir parent for blocked rollback path: %v", err)
	}
	if err := os.WriteFile(blockedPath, []byte("blocked\n"), 0o600); err != nil {
		t.Fatalf("write blocked rollback path file: %v", err)
	}

	reportData := singleLodashSuggestionReport("index.js")
	_, err := applyCodemodIfNeeded(context.Background(), reportData, repo, AnalyseRequest{Dependency: "lodash", ApplyCodemod: true, AllowDirty: true}, time.Now())
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
					Mode: "suggest-only",
					Suggestions: []report.CodemodSuggestion{
						{
							File:        file,
							Line:        1,
							ImportName:  "map",
							FromModule:  "lodash",
							ToModule:    "lodash/map",
							Original:    "import { map } from \"lodash\";",
							Replacement: "import map from \"lodash/map\";",
						},
					},
				},
			},
		},
	}
}
