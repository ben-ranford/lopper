package ui

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ben-ranford/lopper/internal/analysis"
	"github.com/ben-ranford/lopper/internal/report"
)

type stubbornLineRefreshAnalyzer struct {
	calls       atomic.Int32
	started     chan struct{}
	release     chan struct{}
	done        chan struct{}
	releaseOnce sync.Once
	report      report.Report
}

func (a *stubbornLineRefreshAnalyzer) Analyse(context.Context, analysis.Request) (report.Report, error) {
	if a.calls.Add(1) == 1 {
		return a.report, nil
	}
	close(a.started)
	<-a.release
	close(a.done)
	return a.report, nil
}

func TestStaveLineRefreshCancellationReturnsAndRecordsIndeterminateOutcome(t *testing.T) {
	analyzer := newStubbornLineRefreshAnalyzer()
	t.Cleanup(func() { releaseStubbornLineBackend(t, analyzer, false) })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	returned := make(chan error, 1)
	summary := NewSummary(io.Discard, strings.NewReader("refresh\n"), analyzer, report.NewFormatter())
	opts := Options{UseStavePreview: true, Features: previewFeatures(t), Width: 80}
	go func() {
		returned <- NewStavePreview(summary).Start(ctx, opts)
	}()

	waitForStubbornLineRefresh(t, analyzer.started, returned)
	cancel()
	if err := waitForStubbornLineReturn(returned); !errors.Is(err, context.Canceled) {
		t.Fatalf("Start cancellation = %v, want context cancellation", err)
	}
	releaseStubbornLineBackend(t, analyzer, true)
}

func TestStaveLineCancellationClearsPendingActionWithIndeterminateOutcome(t *testing.T) {
	analyzer := newStubbornLineRefreshAnalyzer()
	analyzer.calls.Store(1) // The direct line command below is the refresh analysis.
	t.Cleanup(func() { releaseStubbornLineBackend(t, analyzer, false) })
	summary := NewSummary(io.Discard, strings.NewReader(""), analyzer, report.NewFormatter())
	opts := summary.applyDefaults(Options{UseStavePreview: true, Features: previewFeatures(t), Width: 80})
	view := mapSummaryReportView(analyzer.report)
	state := buildSummaryState(opts)
	program, err := newLopperStaveProgram(summary, &opts, &view, &state)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := program.NewSession(context.Background(), staveSessionOptions(opts, false))
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Session.Close()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	returned := make(chan error, 1)
	line := staveLineSession{prepared: prepared, opts: staveSessionOptions(opts, false), writer: io.Discard}
	go func() {
		_, err := line.command(ctx, "refresh")
		returned <- err
	}()

	waitForStubbornLineRefresh(t, analyzer.started, returned)
	cancel()
	if err := waitForStubbornLineReturn(returned); !errors.Is(err, context.Canceled) {
		t.Fatalf("line command cancellation = %v, want context cancellation", err)
	}
	snapshot, err := prepared.Session.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Model.interaction.pendingCallID != "" || snapshot.Model.interaction.pendingActionID != "" {
		t.Fatalf("cancellation left pending action: %+v", snapshot.Model.interaction)
	}
	if !strings.Contains(snapshot.Model.interaction.error, "final action outcome unknown") {
		t.Fatalf("cancellation outcome = %+v", snapshot.Model.interaction)
	}
	releaseStubbornLineBackend(t, analyzer, true)
}

func TestStaveLineCompletionCancellationFallbackClearsPendingAction(t *testing.T) {
	reportData := report.Report{SchemaVersion: report.SchemaVersion, Dependencies: []report.DependencyReport{{Language: "go", Name: "alpha"}}}
	summary := NewSummary(io.Discard, strings.NewReader(""), &stubAnalyzer{report: reportData}, report.NewFormatter())
	opts := summary.applyDefaults(Options{UseStavePreview: true, Features: previewFeatures(t), Width: 80})
	view := mapSummaryReportView(reportData)
	state := buildSummaryState(opts)
	program, err := newLopperStaveProgram(summary, &opts, &view, &state)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := program.NewSession(context.Background(), staveSessionOptions(opts, false))
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Session.Close()

	// The invocation checks Err once, the completion branch checks it again,
	// and the completion send observes the third call as canceled.
	ctx := &countingContext{cancelAfter: 3}
	line := staveLineSession{prepared: prepared, opts: staveSessionOptions(opts, false), writer: io.Discard}
	if _, err := line.command(ctx, "open go:alpha"); !errors.Is(err, context.Canceled) {
		t.Fatalf("completion cancellation = %v, want context cancellation", err)
	}
	snapshot, err := prepared.Session.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Model.interaction.pendingCallID != "" || snapshot.Model.interaction.pendingActionID != "" {
		t.Fatalf("completion cancellation left pending action: %+v", snapshot.Model.interaction)
	}
	if !strings.Contains(snapshot.Model.interaction.error, "final action outcome unknown") {
		t.Fatalf("completion cancellation outcome = %+v", snapshot.Model.interaction)
	}
}

func newStubbornLineRefreshAnalyzer() *stubbornLineRefreshAnalyzer {
	return &stubbornLineRefreshAnalyzer{
		started: make(chan struct{}),
		release: make(chan struct{}),
		done:    make(chan struct{}),
		report: report.Report{SchemaVersion: report.SchemaVersion, Dependencies: []report.DependencyReport{{
			Language: "go",
			Name:     "blocked",
		}}},
	}
}

func waitForStubbornLineRefresh(t *testing.T, started <-chan struct{}, returned <-chan error) {
	t.Helper()
	select {
	case <-started:
	case err := <-returned:
		t.Fatalf("refresh returned before it blocked: %v", err)
	case <-time.After(time.Second):
		t.Fatal("refresh action did not start")
	}
}

func waitForStubbornLineReturn(returned <-chan error) error {
	select {
	case err := <-returned:
		return err
	case <-time.After(time.Second):
		return errors.New("line action did not return within one second of cancellation")
	}
}

func releaseStubbornLineBackend(t *testing.T, analyzer *stubbornLineRefreshAnalyzer, requireDone bool) {
	t.Helper()
	analyzer.releaseOnce.Do(func() { close(analyzer.release) })
	if !requireDone && analyzer.calls.Load() < 2 {
		return
	}
	select {
	case <-analyzer.done:
		return
	case <-time.After(time.Second):
		if requireDone {
			t.Fatal("refresh backend did not finish after release")
		}
	}
}
