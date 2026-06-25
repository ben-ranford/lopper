package ui

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/report"
)

type stubSummaryActionRunner struct {
	applyCalls  int
	applyReq    CodemodApplyRequest
	applyReport report.Report
	applyErr    error

	saveCalls  int
	saveReq    BaselineSaveRequest
	saveReport report.Report
	savePath   string
	saveErr    error
}

func (s *stubSummaryActionRunner) ApplyCodemod(_ context.Context, req CodemodApplyRequest) (report.Report, error) {
	s.applyCalls++
	s.applyReq = req
	return s.applyReport, s.applyErr
}

func (s *stubSummaryActionRunner) SaveBaseline(_ context.Context, req BaselineSaveRequest) (report.Report, string, error) {
	s.saveCalls++
	s.saveReq = req
	return s.saveReport, s.savePath, s.saveErr
}

func TestSummaryCodemodApplyCommandRequiresConfirmationAndMergesResults(t *testing.T) {
	applyReport := &report.CodemodApplyReport{
		AppliedFiles:   1,
		AppliedPatches: 2,
		SkippedFiles:   1,
		SkippedPatches: 1,
		BackupPath:     ".artifacts/lopper-codemod-backups/lodash.json",
		Results: []report.CodemodApplyResult{
			{File: "src/index.js", Status: "applied", PatchCount: 2},
			{File: "src/skip.js", Status: "skipped", PatchCount: 1, Message: "reason codes: namespace-import"},
		},
	}
	actions := &stubSummaryActionRunner{
		applyReport: report.Report{
			Dependencies: []report.DependencyReport{
				{Name: "lodash", Language: "js-ts", Codemod: &report.CodemodReport{Apply: applyReport}},
			},
		},
	}

	var out bytes.Buffer
	summary := NewSummary(&out, strings.NewReader(""), &stubAnalyzer{}, report.NewFormatter())
	summary.Actions = actions
	reportView := mapSummaryReportView(report.Report{
		Dependencies: []report.DependencyReport{
			{
				Name:     "lodash",
				Language: "js-ts",
				Codemod: &report.CodemodReport{
					Mode: "suggest-only",
					Suggestions: []report.CodemodSuggestion{
						{File: "src/index.js", Line: 1, FromModule: "lodash", ToModule: "lodash/map"},
					},
				},
			},
		},
	})
	opts := Options{RepoPath: ".", TopN: 20, Language: "all"}
	state := summaryState{page: 1, pageSize: 10, sortMode: sortByWaste, selectedDependency: "js-ts:lodash"}

	if quit, err := summary.handleSummaryInputMutable(context.Background(), &opts, &reportView, &state, "apply-codemod"); err != nil || quit {
		t.Fatalf("expected unconfirmed apply to continue, quit=%v err=%v", quit, err)
	}
	if actions.applyCalls != 0 {
		t.Fatalf("expected unconfirmed apply to avoid mutation, got %d calls", actions.applyCalls)
	}
	if !strings.Contains(out.String(), "requires --confirm") {
		t.Fatalf("expected confirmation message, got %q", out.String())
	}

	out.Reset()
	if quit, err := summary.handleSummaryInputMutable(context.Background(), &opts, &reportView, &state, "apply-codemod --confirm --allow-dirty"); err != nil || quit {
		t.Fatalf("expected confirmed apply to continue, quit=%v err=%v", quit, err)
	}
	if actions.applyCalls != 1 {
		t.Fatalf("expected one apply call, got %d", actions.applyCalls)
	}
	if actions.applyReq.Dependency != "lodash" || actions.applyReq.Language != "js-ts" || !actions.applyReq.AllowDirty {
		t.Fatalf("unexpected apply request: %#v", actions.applyReq)
	}
	if reportView.Dependencies[0].CodemodApply != applyReport {
		t.Fatalf("expected apply report to merge into current view, got %#v", reportView.Dependencies[0].CodemodApply)
	}
	output := out.String()
	if !strings.Contains(output, "Codemod apply results for js-ts:lodash") ||
		!strings.Contains(output, "backup: .artifacts/lopper-codemod-backups/lodash.json") ||
		!strings.Contains(output, "applied src/index.js") ||
		!strings.Contains(output, "skipped src/skip.js") {
		t.Fatalf("expected codemod apply details in output, got %q", output)
	}
}

func TestSummarySaveBaselineCommandSupportsLabelAndDefaultCommitKey(t *testing.T) {
	actions := &stubSummaryActionRunner{
		saveReport: report.Report{
			Dependencies: []report.DependencyReport{{Name: "dep", UsedExportsCount: 1, TotalExportsCount: 1, UsedPercent: 100}},
		},
		savePath: ".artifacts/lopper-baselines/label_nightly.json",
	}

	var out bytes.Buffer
	summary := NewSummary(&out, strings.NewReader(""), &stubAnalyzer{}, report.NewFormatter())
	summary.Actions = actions
	reportView := summaryReportView{}
	opts := Options{RepoPath: ".", TopN: 20, Language: "all"}
	state := summaryState{page: 1, pageSize: 10, sortMode: sortByWaste}

	if quit, err := summary.handleSummaryInputMutable(context.Background(), &opts, &reportView, &state, "save-baseline nightly"); err != nil || quit {
		t.Fatalf("expected save label to continue, quit=%v err=%v", quit, err)
	}
	if actions.saveReq.BaselineStorePath != defaultTUIBaselineStorePath || actions.saveReq.BaselineLabel != "nightly" {
		t.Fatalf("unexpected label save request: %#v", actions.saveReq)
	}
	if opts.BaselineStorePath != defaultTUIBaselineStorePath {
		t.Fatalf("expected options to remember baseline store, got %q", opts.BaselineStorePath)
	}
	if !strings.Contains(out.String(), "Saved baseline label:nightly") {
		t.Fatalf("expected saved baseline message, got %q", out.String())
	}

	out.Reset()
	if quit, err := summary.handleSummaryInputMutable(context.Background(), &opts, &reportView, &state, "save-baseline --store custom-baselines"); err != nil || quit {
		t.Fatalf("expected default-key save to continue, quit=%v err=%v", quit, err)
	}
	if !strings.HasPrefix(actions.saveReq.BaselineKey, "commit:") || actions.saveReq.BaselineLabel != "" {
		t.Fatalf("expected default commit key save request, got %#v", actions.saveReq)
	}
	if actions.saveReq.BaselineStorePath != "custom-baselines" {
		t.Fatalf("expected explicit store to win, got %q", actions.saveReq.BaselineStorePath)
	}
}

func TestSummaryCompareBaselineCommandRefreshesReportAndDetailDeltas(t *testing.T) {
	tmp := t.TempDir()
	baselineReport := report.Report{
		SchemaVersion: report.SchemaVersion,
		GeneratedAt:   mustParseTime(t, "2024-01-01T00:00:00Z"),
		RepoPath:      "/repo",
		Dependencies: []report.DependencyReport{
			{
				Name:              "alpha",
				UsedExportsCount:  1,
				TotalExportsCount: 10,
				UsedPercent:       10,
				RuntimeUsage:      &report.RuntimeUsage{LoadCount: 1, Correlation: report.RuntimeCorrelationStaticOnly},
			},
		},
	}
	baselinePath, err := report.SaveSnapshot(tmp, "label:base", baselineReport, mustParseTime(t, "2024-01-02T00:00:00Z"))
	if err != nil {
		t.Fatalf("save baseline snapshot: %v", err)
	}

	currentReport := report.Report{
		SchemaVersion: report.SchemaVersion,
		GeneratedAt:   mustParseTime(t, "2024-02-01T00:00:00Z"),
		RepoPath:      "/repo",
		Dependencies: []report.DependencyReport{
			{
				Name:              "alpha",
				UsedExportsCount:  3,
				TotalExportsCount: 10,
				UsedPercent:       30,
				RuntimeUsage:      &report.RuntimeUsage{LoadCount: 3, Correlation: report.RuntimeCorrelationRuntimeOnly, RuntimeOnly: true},
			},
		},
	}

	var out bytes.Buffer
	summary := NewSummary(&out, strings.NewReader(""), &stubAnalyzer{report: currentReport}, report.NewFormatter())
	reportView := mapSummaryReportView(currentReport)
	opts := Options{RepoPath: ".", TopN: 20, Language: "all"}
	state := summaryState{page: 1, pageSize: 10, sortMode: sortByWaste}

	if quit, err := summary.handleSummaryInputMutable(context.Background(), &opts, &reportView, &state, "compare-baseline --file "+baselinePath); err != nil || quit {
		t.Fatalf("expected compare to continue, quit=%v err=%v", quit, err)
	}
	if reportView.BaselineComparison == nil {
		t.Fatalf("expected baseline comparison to refresh current view")
	}
	if !strings.Contains(out.String(), "Baseline comparison refreshed") {
		t.Fatalf("expected compare refresh message, got %q", out.String())
	}

	out.Reset()
	detail := NewDetail(&out, nil, ".", "auto")
	if err := detail.showLoadedSummary("alpha", reportView); err != nil {
		t.Fatalf("show refreshed detail: %v", err)
	}
	if !strings.Contains(out.String(), "Runtime baseline delta") || !strings.Contains(out.String(), "load count delta: +2") {
		t.Fatalf("expected refreshed detail to include runtime baseline delta, got %q", out.String())
	}
}

func TestSummaryDetailShowsCodemodAction(t *testing.T) {
	dep := detailDependencyView{
		Name:     "lodash",
		Language: "js-ts",
		Codemod: &detailCodemodView{
			Mode: "suggest-only",
			Suggestions: []detailCodemodSuggestionView{
				{File: "src/index.js", Line: 1, FromModule: "lodash", ToModule: "lodash/map"},
			},
		},
	}
	var out bytes.Buffer
	if err := printCodemod(&out, dep.Codemod, detailCodemodActionTarget(dep)); err != nil {
		t.Fatalf("print codemod: %v", err)
	}
	if !strings.Contains(out.String(), "action: apply-codemod js-ts:lodash --confirm") {
		t.Fatalf("expected explicit codemod apply action, got %q", out.String())
	}
}
