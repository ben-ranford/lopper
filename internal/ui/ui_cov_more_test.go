package ui

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/report"
)

const uiWriteFailed = "write failed"
const runtimeOverlapCode = "runtime-overlap"
const runtimeOverlapSummary = runtimeOverlapCode + ": score=100.0 weight=0.200 contribution=20.0"

func TestUIAdditionalOutputBranches(t *testing.T) {
	var out bytes.Buffer
	if err := printRemovalCandidate(&out, nil); err != nil {
		t.Fatalf("print nil removal candidate: %v", err)
	}
	if !strings.Contains(out.String(), noneLabel) {
		t.Fatalf("expected nil removal candidate to render %q, got %q", noneLabel, out.String())
	}

	writeErr := errors.New(uiWriteFailed)
	if err := writeNoneAndBlankLine(&failAfterWriter{failAt: 0, err: writeErr}); !errors.Is(err, writeErr) {
		t.Fatalf("expected initial writer error to propagate, got %v", err)
	}
	if err := writeNoneAndBlankLine(&failAfterWriter{failAt: 1, err: writeErr}); !errors.Is(err, writeErr) {
		t.Fatalf("expected blank-line writer error to propagate, got %v", err)
	}
}

func TestRenderSummaryOutputSuccess(t *testing.T) {
	rep := report.Report{
		Dependencies: []report.DependencyReport{
			{Name: "dep", UsedExportsCount: 1, TotalExportsCount: 1, UsedPercent: 100},
		},
	}

	var out bytes.Buffer
	summary := NewSummary(&out, strings.NewReader(""), &stubAnalyzer{report: rep}, report.NewFormatter())
	if err := summary.renderSummaryOutput(mapSummaryReportView(rep), summaryState{sortMode: sortByWaste, page: 1, pageSize: 10}); err != nil {
		t.Fatalf("render summary output: %v", err)
	}
	if !strings.Contains(out.String(), "Lopper TUI (summary)") {
		t.Fatalf("expected rendered summary frame, got %q", out.String())
	}
}

func TestSummaryUnknownCommandWriteError(t *testing.T) {
	writeErr := errors.New(uiWriteFailed)
	summary := NewSummary(&failAfterWriter{failAt: 0, err: writeErr}, strings.NewReader(""), &stubAnalyzer{report: report.Report{}}, report.NewFormatter())
	_, err := summary.handleSummaryInput(context.Background(), Options{RepoPath: "."}, &summaryState{}, "noop")
	if !errors.Is(err, writeErr) {
		t.Fatalf("expected unknown-command write failure, got %v", err)
	}
}

func TestUIDetailAdditionalBranches(t *testing.T) {
	t.Run("render list empty placeholder", testUIRenderListEmptyPlaceholder)
	t.Run("print codemod nil placeholder", testUIPrintCodemodNilPlaceholder)
	t.Run("reachability signals without rationale", testUIReachabilitySignalsWithoutRationale)
	t.Run("reachability signals empty", testUIReachabilitySignalsEmpty)
	t.Run("removal candidate without rationale", testUIRemovalCandidateWithoutRationale)
}

func TestUIDetailAdditionalWriteErrorBranches(t *testing.T) {
	writeErr := errors.New(uiWriteFailed)

	if err := writeLines(&failAfterWriter{failAt: 0, err: writeErr}, []string{"line"}); !errors.Is(err, writeErr) {
		t.Fatalf("expected writeLines error, got %v", err)
	}

	if err := printReachabilitySignals(&failAfterWriter{failAt: 0, err: writeErr}, []detailReachabilitySignalView{{Code: runtimeOverlapCode}}); !errors.Is(err, writeErr) {
		t.Fatalf("expected signals header write failure, got %v", err)
	}

	if err := printReachabilitySignals(&failAfterWriter{failAt: 1, err: writeErr}, []detailReachabilitySignalView{{Code: runtimeOverlapCode}}); !errors.Is(err, writeErr) {
		t.Fatalf("expected signals row write failure, got %v", err)
	}

	if err := printReachabilitySignals(&failAfterWriter{failAt: 2, err: writeErr}, []detailReachabilitySignalView{{Code: runtimeOverlapCode, Rationale: "because"}}); !errors.Is(err, writeErr) {
		t.Fatalf("expected signals rationale write failure, got %v", err)
	}

	if err := printReachabilityConfidence(&failAfterWriter{failAt: 1, err: writeErr}, &detailReachabilityConfidenceView{Model: "reachability-v2", Score: 72.5}); !errors.Is(err, writeErr) {
		t.Fatalf("expected confidence writeLines failure, got %v", err)
	}

	if err := printReachabilityConfidence(&failAfterWriter{failAt: 3, err: writeErr}, &detailReachabilityConfidenceView{
		Model:   "reachability-v2",
		Score:   72.5,
		Signals: []detailReachabilitySignalView{{Code: runtimeOverlapCode}},
	}); !errors.Is(err, writeErr) {
		t.Fatalf("expected confidence signal write failure, got %v", err)
	}
}

func TestUIViewModelAdditionalBranches(t *testing.T) {
	t.Run("default summary formatter", testUIDefaultSummaryFormatter)
	t.Run("codemod mapping", testUICodemodMapping)
	t.Run("reachability mapping", testUIReachabilityMapping)
	t.Run("summary formatter errors propagate", testUISummaryFormatterErrorsPropagate)
}

func testUIRenderListEmptyPlaceholder(t *testing.T) {
	t.Helper()

	var out bytes.Buffer
	if err := renderList[string](&out, "Empty", nil, func(_ io.Writer, _ string) error { return nil }); err != nil {
		t.Fatalf("render empty list: %v", err)
	}
	text := out.String()
	if !strings.Contains(text, "Empty (0)") || !strings.Contains(text, noneLabel) {
		t.Fatalf("expected empty list placeholder output, got %q", text)
	}
}

func testUIPrintCodemodNilPlaceholder(t *testing.T) {
	t.Helper()

	var out bytes.Buffer
	if err := printCodemod(&out, nil); err != nil {
		t.Fatalf("print nil codemod: %v", err)
	}
	if !strings.Contains(out.String(), noneLabel) {
		t.Fatalf("expected nil codemod to render %q, got %q", noneLabel, out.String())
	}
}

func testUIReachabilitySignalsWithoutRationale(t *testing.T) {
	t.Helper()

	var out bytes.Buffer
	if err := printReachabilitySignals(&out, []detailReachabilitySignalView{{
		Code:         runtimeOverlapCode,
		Score:        100,
		Weight:       0.2,
		Contribution: 20,
	}}); err != nil {
		t.Fatalf("print reachability signals: %v", err)
	}
	text := out.String()
	if !strings.Contains(text, runtimeOverlapSummary) {
		t.Fatalf("expected signal output, got %q", text)
	}
	if strings.Contains(text, "rationale:") {
		t.Fatalf("expected blank rationale to be omitted, got %q", text)
	}
}

func testUIReachabilitySignalsEmpty(t *testing.T) {
	t.Helper()

	var out bytes.Buffer
	if err := printReachabilitySignals(&out, nil); err != nil {
		t.Fatalf("print empty reachability signals: %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("expected no output for empty signals, got %q", out.String())
	}
}

func testUIRemovalCandidateWithoutRationale(t *testing.T) {
	t.Helper()

	var out bytes.Buffer
	if err := printRemovalCandidate(&out, &detailRemovalCandidateView{Score: 80, Usage: 70, Impact: 60, Confidence: 90}); err != nil {
		t.Fatalf("print removal candidate without rationale: %v", err)
	}
	text := out.String()
	if strings.Contains(text, "rationale:") {
		t.Fatalf("expected rationale section to be omitted, got %q", text)
	}
	if !strings.Contains(text, "confidence: 90.0") {
		t.Fatalf("expected removal candidate details, got %q", text)
	}
}

func testUIDefaultSummaryFormatter(t *testing.T) {
	t.Helper()

	formatter := newSummaryFormatter(nil)
	rendered, err := formatter(summaryDisplayView{
		Dependencies: []summaryDependencyView{{Name: "dep", UsedPercent: 100}},
	})
	if err != nil {
		t.Fatalf("format summary with default formatter: %v", err)
	}
	if rendered == "" {
		t.Fatalf("expected formatted output")
	}
}

func testUICodemodMapping(t *testing.T) {
	t.Helper()

	apply := &report.CodemodApplyReport{
		AppliedFiles: 1,
		Results: []report.CodemodApplyResult{{
			File:       "go.mod",
			Status:     "applied",
			PatchCount: 2,
		}},
	}

	if got := summaryCodemodApplyView(&report.CodemodReport{Apply: apply}); got != apply {
		t.Fatalf("expected apply report pointer to flow through, got %#v", got)
	}

	got := summaryCodemodViewToReport(apply)
	if got == nil || got.Apply != apply {
		t.Fatalf("expected codemod report to wrap apply pointer, got %#v", got)
	}
}

func testUIReachabilityMapping(t *testing.T) {
	t.Helper()

	confidence := &report.ReachabilityConfidence{
		Model:          "reachability-v2",
		Score:          72.5,
		Summary:        "runtime evidence found",
		RationaleCodes: []string{runtimeOverlapCode},
		Signals: []report.ReachabilitySignal{{
			Code:         runtimeOverlapCode,
			Score:        100,
			Weight:       0.2,
			Contribution: 20,
			Rationale:    "runtime trace overlap",
		}},
	}

	got := mapDetailReachabilityConfidence(confidence)
	if got == nil {
		t.Fatalf("expected mapped reachability confidence")
	}
	if got.Model != confidence.Model || got.Score != confidence.Score || got.Summary != confidence.Summary {
		t.Fatalf("unexpected mapped confidence: %#v", got)
	}
	if len(got.RationaleCodes) != 1 || got.RationaleCodes[0] != runtimeOverlapCode {
		t.Fatalf("unexpected rationale codes: %#v", got.RationaleCodes)
	}
	if len(got.Signals) != 1 {
		t.Fatalf("expected one mapped signal, got %#v", got.Signals)
	}
	if got.Signals[0].Code != runtimeOverlapCode || got.Signals[0].Rationale != "runtime trace overlap" {
		t.Fatalf("unexpected mapped signal: %#v", got.Signals[0])
	}
}

func testUISummaryFormatterErrorsPropagate(t *testing.T) {
	t.Helper()

	rep := report.Report{
		Dependencies: []report.DependencyReport{{
			Name:              "dep",
			UsedExportsCount:  1,
			TotalExportsCount: 1,
			UsedPercent:       100,
		}},
	}

	formatErr := errors.New("format summary failed")
	summary := NewSummary(io.Discard, strings.NewReader(""), &stubAnalyzer{report: rep}, report.NewFormatter())
	summary.Formatter = func(summaryDisplayView) (string, error) {
		return "", formatErr
	}

	state := summaryState{sortMode: sortByName, page: 1, pageSize: 10}
	reportView := mapSummaryReportView(rep)

	if _, err := summary.renderSummary(reportView, state); !errors.Is(err, formatErr) {
		t.Fatalf("expected renderSummary to return formatter error, got %v", err)
	}
	if err := summary.renderSummaryOutput(reportView, state); !errors.Is(err, formatErr) {
		t.Fatalf("expected renderSummaryOutput to return formatter error, got %v", err)
	}
	if err := summary.Snapshot(context.Background(), Options{RepoPath: ".", Sort: "name", PageSize: 10}, filepath.Join(t.TempDir(), "summary.txt")); !errors.Is(err, formatErr) {
		t.Fatalf("expected snapshot to return formatter error, got %v", err)
	}
}
