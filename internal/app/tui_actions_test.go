package app

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/ui"
)

func TestTUIActionRunnerUsesAnalysePipelineForCodemodApply(t *testing.T) {
	analyzer := &fakeAnalyzer{
		report: report.Report{
			Dependencies: []report.DependencyReport{{Name: "lodash", UsedExportsCount: 1, TotalExportsCount: 1, UsedPercent: 100}},
		},
	}
	application := &App{Analyzer: analyzer, Formatter: report.NewFormatter()}
	runner := application.tuiActionRunner()

	if _, err := runner.ApplyCodemod(context.Background(), ui.CodemodApplyRequest{
		RepoPath:   t.TempDir(),
		Dependency: "lodash",
		TopN:       7,
		Language:   "js-ts",
	}); err != nil {
		t.Fatalf("apply codemod action: %v", err)
	}
	if !analyzer.lastReq.SuggestOnly || analyzer.lastReq.Dependency != "lodash" || analyzer.lastReq.TopN != 7 || analyzer.lastReq.Language != "js-ts" {
		t.Fatalf("expected TUI codemod action to use analyse apply pipeline, got %#v", analyzer.lastReq)
	}
}

func TestTUIActionRunnerSavesBaselineWithLabel(t *testing.T) {
	analyzer := &fakeAnalyzer{
		report: report.Report{
			SchemaVersion: report.SchemaVersion,
			Dependencies:  []report.DependencyReport{{Name: "dep", UsedExportsCount: 1, TotalExportsCount: 1, UsedPercent: 100}},
		},
	}
	application := &App{Analyzer: analyzer, Formatter: report.NewFormatter()}
	runner := application.tuiActionRunner()

	store := t.TempDir()
	_, savedPath, err := runner.SaveBaseline(context.Background(), ui.BaselineSaveRequest{
		RepoPath:          ".",
		TopN:              5,
		Language:          "all",
		BaselineStorePath: store,
		BaselineLabel:     "nightly",
	})
	if err != nil {
		t.Fatalf("save baseline action: %v", err)
	}
	if !strings.Contains(savedPath, "label_nightly.json") {
		t.Fatalf("expected label-based snapshot path, got %q", savedPath)
	}
	if _, err := os.Stat(savedPath); err != nil {
		t.Fatalf("expected saved snapshot to exist: %v", err)
	}
	if analyzer.lastReq.TopN != 5 || analyzer.lastReq.Language != "all" {
		t.Fatalf("expected TUI baseline save to forward summary options, got %#v", analyzer.lastReq)
	}
}
