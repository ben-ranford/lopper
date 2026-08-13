package ui

import (
	"context"
	"errors"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/ben-ranford/stave/event"

	"github.com/ben-ranford/lopper/internal/report"
)

func TestStaveTerminalTextAndKeyFallbackBranches(t *testing.T) {
	count := 0
	b := &staveTerminal{ctx: context.Background(), prepared: struct{}{}, sendEvent: func(context.Context, any, event.Event) error { count++; return nil }}
	if err := b.text("hello"); err != nil || count != 1 {
		t.Fatalf("text event: %v count=%d", err, count)
	}
	if err := b.key(tea.KeyPressMsg{Text: "!", Code: '!'}); err != nil {
		t.Fatal(err)
	}
	if err := b.key(tea.KeyPressMsg{Code: tea.KeyF2}); err == nil {
		t.Fatal("unsupported key accepted")
	}
}

func TestStaveSnapshotPropagatesOutputAndAnnouncementErrors(t *testing.T) {
	rep := report.Report{Dependencies: []report.DependencyReport{{Language: "go", Name: "alpha"}}}
	s := NewSummary(&staveCoverageErrWriter{}, nil, &stubAnalyzer{report: rep}, report.NewFormatter())
	p := NewStavePreview(s).(*StavePreview)
	opts := Options{UseStavePreview: true, Features: previewFeatures(t), Width: 80}
	if err := p.Snapshot(context.Background(), opts, "-"); err == nil {
		t.Fatal("snapshot output error swallowed")
	}
	path := t.TempDir() + "/snapshot.txt"
	s.Out = nil
	if err := p.Snapshot(context.Background(), opts, path); err != nil {
		t.Fatal(err)
	}
}

func TestDetailAndSummaryReportWritersPropagateEveryWriteFailure(t *testing.T) {
	errWant := errors.New("writer failed")
	apply := &report.CodemodApplyReport{AppliedFiles: 1, AppliedPatches: 2, BackupPath: "/tmp/backup", Results: []report.CodemodApplyResult{{Status: "applied", File: "x.go", PatchCount: 1, Message: "ok"}}}
	for failAt := 0; failAt < 12; failAt++ {
		if err := printDetailCodemodApply(&failAfterWriter{failAt: failAt, err: errWant}, apply); err == nil {
			if failAt < 6 {
				t.Fatalf("printDetailCodemodApply swallowed failure at write %d", failAt)
			}
		}
	}
	for failAt := 0; failAt < 12; failAt++ {
		if err := writeCodemodApplyReport(&failAfterWriter{failAt: failAt, err: errWant}, "go:dep", apply); err == nil {
			if failAt < 7 {
				t.Fatalf("writeCodemodApplyReport swallowed failure at write %d", failAt)
			}
		}
	}
	comparison := &report.BaselineComparison{Dependencies: []report.DependencyDelta{{}}, Regressions: []report.DependencyDelta{{}}, Progressions: []report.DependencyDelta{{}}, Added: []report.DependencyDelta{{}}, Removed: []report.DependencyDelta{{}}}
	for failAt := 0; failAt < 5; failAt++ {
		if err := writeBaselineCompareResult(&failAfterWriter{failAt: failAt, err: errWant}, "nightly", comparison); err == nil {
			if failAt < 3 {
				t.Fatalf("writeBaselineCompareResult swallowed failure at write %d", failAt)
			}
		}
	}
}
