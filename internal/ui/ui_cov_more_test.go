package ui

import (
	"bytes"
	"context"
	"errors"
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
