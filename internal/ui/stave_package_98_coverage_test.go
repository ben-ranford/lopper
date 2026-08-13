package ui

import (
	"context"
	"errors"
	"github.com/ben-ranford/lopper/internal/report"
	"io"
	"strings"
	"testing"
)

func TestFinalLegacyCoverageBranches(t *testing.T) {
	if _, ok := isDetailCommand("open   "); ok {
		t.Fatal("blank detail accepted")
	}
	if _, err := parseSummaryBaselineArguments([]string{"--wat"}, &summaryAction{}, summaryActionCompareBaseline); err == nil {
		t.Fatal("unknown baseline option accepted")
	}
	w := &failAfterWriter{failAt: 0, err: errors.New("write")}
	if err := printRuntimeDelta(w, &detailRuntimeDeltaView{Comparable: true}); err == nil {
		t.Fatal("runtime writer error swallowed")
	}
	if err := printCodemod(w, &detailCodemodView{Mode: "apply", Suggestions: []detailCodemodSuggestionView{{File: "x"}}}, "go:x"); err == nil {
		t.Fatal("codemod writer error swallowed")
	}
	opts, key, err := buildSummaryBaselineCompareOptions(Options{BaselineStorePath: "/tmp/store"}, summaryAction{baselineTarget: "nightly"})
	if err != nil || key != "nightly" || opts.BaselineKey != "nightly" {
		t.Fatalf("target baseline branch: %+v %q %v", opts, key, err)
	}
}

func TestFinalWriterSweeps(t *testing.T) {
	apply := &report.CodemodApplyReport{AppliedFiles: 1, AppliedPatches: 1, BackupPath: "b", Results: []report.CodemodApplyResult{{Status: "ok", File: "x", PatchCount: 1, Message: "m"}}}
	c := &detailCodemodView{Mode: "apply", Suggestions: []detailCodemodSuggestionView{{File: "x", Line: 1, FromModule: "a", ToModule: "b"}}, Skips: []detailCodemodSkipView{{File: "y", Line: 2, ReasonCode: "r", Message: "m"}}, Apply: apply}
	for i := 0; i < 40; i++ {
		if err := printCodemod(&failAfterWriter{failAt: i, err: errors.New("write")}, c, "go:x"); err != nil {
			t.Logf("printCodemod failAt=%d: %v", i, err)
		}
	}
	n := 1
	d := &detailRuntimeDeltaView{Comparable: true, BaselinePresent: true, CurrentPresent: true, BaselineLoadCount: &n, CurrentLoadCount: &n, LoadCountDelta: &n, NewRuntimeLoads: true, RemovedRuntimeLoads: true, RuntimeOnlyRegression: true, RuntimeOnlyImprovement: true, ModulesAdded: []report.RuntimeModuleDelta{{Module: "m"}}, ModulesRemoved: []report.RuntimeModuleDelta{{Module: "m"}}, ModulesChanged: []report.RuntimeModuleDelta{{Module: "m"}}}
	for i := 0; i < 60; i++ {
		if err := printRuntimeDelta(&failAfterWriter{failAt: i, err: errors.New("write")}, d); err != nil {
			t.Logf("printRuntimeDelta failAt=%d: %v", i, err)
		}
	}
}

func TestFinalSummaryDetailWriterError(t *testing.T) {
	s := NewSummary(&failAfterWriter{failAt: 0, err: errors.New("write")}, strings.NewReader(""), &stubAnalyzer{}, report.NewFormatter())
	view := mapSummaryReportView(report.Report{Dependencies: []report.DependencyReport{{Name: "x", Language: "js-ts", UsedPercent: 1}}})
	state := summaryState{}
	if _, err := s.handleSummaryInputMutable(context.Background(), &Options{Language: "all"}, &view, &state, "open js-ts:x"); err == nil {
		t.Fatal("detail writer error swallowed")
	}
}

func TestStaveHelperBranches(t *testing.T) {
	for _, width := range []int{20, 50, 90} {
		if n, err := staveHelpNodes(width, width < 40); err != nil || len(n) == 0 {
			t.Fatalf("help %d: %v", width, err)
		}
		if got := staveKeyHint(width); got == "" {
			t.Fatal("empty key hint")
		}
	}
	for _, in := range []staveSummaryInteraction{{}, {error: "bad"}, {pendingConfirm: "confirm"}, {commandMode: true, filterBuffer: "filter"}, {status: "ok"}} {
		if _, err := staveFeedbackNode(in, 80, false); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := staveApplicationTree(nil, "summary"); err != nil {
		t.Fatal(err)
	}
	if !staveHasFeedback(staveSummaryInteraction{status: "ok"}) || staveHasFeedback(staveSummaryInteraction{}) {
		t.Fatal("feedback predicate")
	}
	if got, ok := staveSelectedDetail(summaryReportView{Dependencies: []summaryDependencyView{{Language: "go", Name: "a"}}}, "go:a"); !ok || got.Name != "a" {
		t.Fatal("detail lookup")
	}
	for _, tc := range [][3]int{{0, 0, 5}, {9, 2, 2}, {-1, 2, 1}} {
		_ = clampStaveRow(tc[0], tc[1])
	}
	for _, tc := range [][3]int{{0, 0, 3}, {5, 2, 2}, {5, 2, 10}, {5, 4, 2}} {
		_, _ = staveVisibleRows(tc[0], tc[1], tc[2])
	}
}

func TestSummaryDetailAndBaselineErrorBranches(t *testing.T) {
	s := NewSummary(io.Discard, nil, &stubAnalyzer{err: errors.New("analysis")}, report.NewFormatter())
	view := &summaryReportView{}
	state := &summaryState{}
	if quit, err := s.handleSummaryDetailInput(&Options{RepoPath: "."}, view, state, "not-detail"); quit || err != nil {
		t.Fatalf("unknown detail command: %v %v", quit, err)
	}
	if _, err := applySummaryBaselineIfNeeded(report.Report{}, Options{BaselinePath: "/definitely/missing/baseline"}); err == nil {
		t.Fatal("baseline error not returned")
	}
	s2 := NewSummary(&failAfterWriter{failAt: 0, err: errors.New("write")}, nil, nil, nil)
	if err := s2.runSummaryCodemodApply(context.Background(), &Options{RepoPath: "."}, nil, summaryAction{dependency: "go:x", confirm: true}); err == nil {
		t.Fatal("nil runner error branch")
	}
}
