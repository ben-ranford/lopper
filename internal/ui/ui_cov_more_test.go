package ui

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/report"
)

func TestUIAdditionalOutputBranches(t *testing.T) {
	var out bytes.Buffer
	if err := printRemovalCandidate(&out, nil); err != nil {
		t.Fatalf("print nil removal candidate: %v", err)
	}
	if !strings.Contains(out.String(), noneLabel) {
		t.Fatalf("expected nil removal candidate to render %q, got %q", noneLabel, out.String())
	}

	writeErr := errors.New("write failed")
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
	if err := summary.renderSummaryOutput(rep, summaryState{sortMode: sortByWaste, page: 1, pageSize: 10}); err != nil {
		t.Fatalf("render summary output: %v", err)
	}
	if !strings.Contains(out.String(), "Lopper TUI (summary)") {
		t.Fatalf("expected rendered summary frame, got %q", out.String())
	}
}

func TestSummaryUnknownCommandWriteError(t *testing.T) {
	writeErr := errors.New("write failed")
	summary := NewSummary(&failAfterWriter{failAt: 0, err: writeErr}, strings.NewReader(""), &stubAnalyzer{report: report.Report{}}, report.NewFormatter())
	_, err := summary.handleSummaryInput(context.Background(), Options{RepoPath: "."}, &summaryState{}, "noop")
	if !errors.Is(err, writeErr) {
		t.Fatalf("expected unknown-command write failure, got %v", err)
	}
}

func TestUIDetailAdditionalBranches(t *testing.T) {
	t.Run("render list empty placeholder", func(t *testing.T) {
		var out bytes.Buffer
		if err := renderList[string](&out, "Empty", nil, func(_ io.Writer, _ string) error { return nil }); err != nil {
			t.Fatalf("render empty list: %v", err)
		}
		text := out.String()
		if !strings.Contains(text, "Empty (0)") || !strings.Contains(text, noneLabel) {
			t.Fatalf("expected empty list placeholder output, got %q", text)
		}
	})

	t.Run("print codemod nil placeholder", func(t *testing.T) {
		var out bytes.Buffer
		if err := printCodemod(&out, nil); err != nil {
			t.Fatalf("print nil codemod: %v", err)
		}
		if !strings.Contains(out.String(), noneLabel) {
			t.Fatalf("expected nil codemod to render %q, got %q", noneLabel, out.String())
		}
	})

	t.Run("reachability signals without rationale", func(t *testing.T) {
		var out bytes.Buffer
		if err := printReachabilitySignals(&out, []report.ReachabilitySignal{{Code: "runtime-overlap", Score: 100, Weight: 0.2, Contribution: 20}}); err != nil {
			t.Fatalf("print reachability signals: %v", err)
		}
		text := out.String()
		if !strings.Contains(text, "runtime-overlap: score=100.0 weight=0.200 contribution=20.0") {
			t.Fatalf("expected signal output, got %q", text)
		}
		if strings.Contains(text, "rationale:") {
			t.Fatalf("expected blank rationale to be omitted, got %q", text)
		}
	})

	t.Run("reachability signals empty", func(t *testing.T) {
		var out bytes.Buffer
		if err := printReachabilitySignals(&out, nil); err != nil {
			t.Fatalf("print empty reachability signals: %v", err)
		}
		if out.Len() != 0 {
			t.Fatalf("expected no output for empty signals, got %q", out.String())
		}
	})

	t.Run("removal candidate without rationale", func(t *testing.T) {
		var out bytes.Buffer
		if err := printRemovalCandidate(&out, &report.RemovalCandidate{Score: 80, Usage: 70, Impact: 60, Confidence: 90}); err != nil {
			t.Fatalf("print removal candidate without rationale: %v", err)
		}
		text := out.String()
		if strings.Contains(text, "rationale:") {
			t.Fatalf("expected rationale section to be omitted, got %q", text)
		}
		if !strings.Contains(text, "confidence: 90.0") {
			t.Fatalf("expected removal candidate details, got %q", text)
		}
	})
}

func TestUIDetailAdditionalWriteErrorBranches(t *testing.T) {
	writeErr := errors.New("write failed")

	if err := writeLines(&failAfterWriter{failAt: 0, err: writeErr}, []string{"line"}); !errors.Is(err, writeErr) {
		t.Fatalf("expected writeLines error, got %v", err)
	}

	if err := printReachabilitySignals(&failAfterWriter{failAt: 0, err: writeErr}, []report.ReachabilitySignal{{Code: "runtime-overlap"}}); !errors.Is(err, writeErr) {
		t.Fatalf("expected signals header write failure, got %v", err)
	}

	if err := printReachabilitySignals(&failAfterWriter{failAt: 1, err: writeErr}, []report.ReachabilitySignal{{Code: "runtime-overlap"}}); !errors.Is(err, writeErr) {
		t.Fatalf("expected signals row write failure, got %v", err)
	}

	if err := printReachabilitySignals(&failAfterWriter{failAt: 2, err: writeErr}, []report.ReachabilitySignal{{Code: "runtime-overlap", Rationale: "because"}}); !errors.Is(err, writeErr) {
		t.Fatalf("expected signals rationale write failure, got %v", err)
	}

	if err := printReachabilityConfidence(&failAfterWriter{failAt: 1, err: writeErr}, &report.ReachabilityConfidence{Model: "reachability-v2", Score: 72.5}); !errors.Is(err, writeErr) {
		t.Fatalf("expected confidence writeLines failure, got %v", err)
	}

	if err := printReachabilityConfidence(&failAfterWriter{failAt: 3, err: writeErr}, &report.ReachabilityConfidence{
		Model:   "reachability-v2",
		Score:   72.5,
		Signals: []report.ReachabilitySignal{{Code: "runtime-overlap"}},
	}); !errors.Is(err, writeErr) {
		t.Fatalf("expected confidence signal write failure, got %v", err)
	}
}
