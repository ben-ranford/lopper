package ui

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ben-ranford/lopper/internal/analysis"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/stave"
	"github.com/ben-ranford/stave/event"
	"github.com/ben-ranford/stave/session"
	"github.com/ben-ranford/stave/state"
	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"
)

type parentCancellationRefreshAnalyzer struct {
	calls       atomic.Int32
	started     chan struct{}
	cancelled   chan struct{}
	release     chan struct{}
	finished    chan struct{}
	releaseOnce sync.Once
	report      report.Report
}

func TestStaveTerminalParentCancellationPublishesIndeterminateOutcome(t *testing.T) {
	parent, cancelParent := context.WithCancelCause(context.Background())
	sessionCtx, stopSession := context.WithCancel(context.WithoutCancel(parent))
	defer stopSession()
	prepared := parentCancellationPreparedSession(sessionCtx, t)
	defer prepared.Session.Close()

	const callID = "parent-cancel"
	pending, err := event.New(event.ActionInvoked, event.ActionInvokedPayload{CallID: callID, ActionID: staveActionOpen, Arguments: map[string]any{"dependency": "go:alpha"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := sendLopperEvent(context.Background(), prepared, pending); err != nil {
		t.Fatal(err)
	}

	backendCtx, cancelBackend := context.WithCancel(context.Background())
	defer cancelBackend()
	var events []event.Event
	bridge := &staveTerminal{currentCallID: callID, actionCancel: cancelBackend}
	sendEvent := func(ctx context.Context, raw any, ev event.Event) error {
		events = append(events, ev)
		return sendLopperEvent(ctx, raw.(*stave.Prepared[staveSummaryModel]), ev)
	}
	runCtx, stopRun := context.WithCancel(parent)
	defer stopRun()
	parentCause := errors.New("parent cancellation")
	cancelParent(parentCause)

	if err := finishStaveTerminalRun(parent, runCtx, bridge, prepared, sendEvent, nil); !errors.Is(err, parentCause) {
		t.Fatalf("parent cancellation result = %v, want %v", err, parentCause)
	}
	if backendCtx.Err() == nil {
		t.Fatal("parent cancellation did not cancel the in-flight backend context")
	}
	if len(events) != 2 || events[0].Kind != event.EffectResult || events[1].Kind != event.Shutdown {
		t.Fatalf("parent cancellation events = %#v", events)
	}
	cancelled, ok := events[0].Payload.(event.EffectResultPayload)
	if !ok || cancelled.CallID != callID || cancelled.Status != "cancelled" || !strings.Contains(cancelled.Error, "final action outcome unknown") {
		t.Fatalf("parent cancellation outcome = %#v", events[0].Payload)
	}
	// Stave publishes the shutdown reducer state before beginClose closes its
	// event queue. A sequence advance acknowledges publication, not closure.
	closureCtx, cancelClosure := context.WithTimeout(context.Background(), time.Second)
	defer cancelClosure()
	if err := prepared.Session.Wait(closureCtx, func(state.State[staveSummaryModel]) bool {
		return prepared.Session.Lifecycle() == session.LifecycleClosed
	}); err != nil {
		t.Fatalf("shutdown closure not observed: %v; lifecycle=%s; events=%#v", err, prepared.Session.Lifecycle(), events)
	}
	snapshot, err := prepared.Session.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Model.interaction.quit || snapshot.Model.interaction.pendingCallID != "" || !strings.Contains(snapshot.Model.interaction.error, "final action outcome unknown") {
		t.Fatalf("parent cancellation session state = %+v", snapshot.Model.interaction)
	}
	late, err := event.New(event.EffectResult, event.EffectResultPayload{CallID: callID, Status: "completed"})
	if err != nil {
		t.Fatal(err)
	}
	if err := sendLopperEvent(context.Background(), prepared, late); !errors.Is(err, session.ErrSessionClosed) {
		t.Fatalf("late completion error = %v, want closed session", err)
	}
	afterLate, err := prepared.Session.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if afterLate.Sequence != snapshot.Sequence || afterLate.Model.interaction.error != snapshot.Model.interaction.error {
		t.Fatalf("late completion changed cancelled outcome: before=%+v; after=%+v", snapshot, afterLate)
	}
}

func TestStavePreviewParentCancellationRestoresTerminalAndReturnsCause(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("COLORTERM", "truecolor")
	t.Setenv("NO_COLOR", "")
	t.Setenv("CI", "")
	master, terminal, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	cleanupStaveTestCloser(t, "parent-cancellation PTY master", master)
	cleanupStaveTestCloser(t, "parent-cancellation PTY terminal", terminal)
	if !supportsStaveInteractiveTerminal(terminal, terminal) {
		t.Skip("pseudo-terminal status unavailable")
	}
	if err := pty.Setsize(terminal, &pty.Winsize{Rows: 30, Cols: 100}); err != nil {
		t.Fatal(err)
	}

	analyzer := newParentCancellationRefreshAnalyzer()
	t.Cleanup(func() { releaseParentCancellationRefresh(t, analyzer, false) })
	parent, cancelParent := context.WithCancelCause(context.Background())
	returned := make(chan error, 1)
	exited := make(chan struct{})
	t.Cleanup(func() {
		cancelParent(nil)
		select {
		case <-exited:
		case <-time.After(staveSignalSubprocessBound):
			t.Error("preview did not stop before PTY cleanup")
		}
	})
	summary := NewSummary(terminal, terminal, analyzer, report.NewFormatter())
	go func() {
		defer close(exited)
		returned <- NewStavePreview(summary).Start(parent, Options{RepoPath: ".", UseStavePreview: true, Features: previewFeatures(t), Width: 100})
	}()
	capture := newSignalPTYCapture(master)
	waitSignalOutput(t, capture, returned, func(output string) bool {
		return strings.Contains(output, "Status: Stave preview") && strings.Contains(output, "\x1b[?1049h")
	})
	if _, err := master.Write([]byte(":refresh")); err != nil {
		t.Fatalf("start refresh action: %v", err)
	}
	// Wait for command acceptance before submitting it. Rendering the initial
	// frame does not prove that the PTY input queue has been processed.
	waitSignalOutput(t, capture, returned, func(output string) bool {
		return strings.Contains(ansi.Strip(output), "Command: refresh")
	})
	if _, err := master.Write([]byte("\r")); err != nil {
		t.Fatalf("submit refresh action: %v", err)
	}
	waitForParentCancellationRefresh(t, analyzer.started, "startup", staveSignalSubprocessBound, capture)

	parentCause := errors.New("parent cancellation")
	cancelParent(parentCause)
	waitForParentCancellationRefresh(t, analyzer.cancelled, "cancellation", time.Second, capture)
	if err := waitForParentCancellationReturn(returned); !errors.Is(err, parentCause) {
		t.Fatalf("start parent cancellation = %v, want cause %v", err, parentCause)
	}
	releaseParentCancellationRefresh(t, analyzer, true)

	if err := terminal.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
		t.Fatalf("close parent-cancellation terminal: %v", err)
	}
	waitSignalCapture(t, capture)
	assertStaveParentCancellationRestored(t, capture.String())
}

func newParentCancellationRefreshAnalyzer() *parentCancellationRefreshAnalyzer {
	return &parentCancellationRefreshAnalyzer{
		started:   make(chan struct{}),
		cancelled: make(chan struct{}),
		release:   make(chan struct{}),
		finished:  make(chan struct{}),
		report: report.Report{SchemaVersion: report.SchemaVersion, Dependencies: []report.DependencyReport{{
			Language: "go",
			Name:     "parent-cancellation-fixture",
		}}},
	}
}

func (a *parentCancellationRefreshAnalyzer) Analyse(ctx context.Context, _ analysis.Request) (report.Report, error) {
	switch a.calls.Add(1) {
	case 1:
		return a.report, nil
	case 2:
		close(a.started)
		<-ctx.Done()
		close(a.cancelled)
		<-a.release
		close(a.finished)
		return report.Report{}, ctx.Err()
	default:
		return report.Report{}, errors.New("unexpected analysis call")
	}
}

func waitForParentCancellationRefresh(t *testing.T, signal <-chan struct{}, phase string, bound time.Duration, capture *signalPTYCapture) {
	t.Helper()
	// Preview return and backend cancellation are independent acknowledgements.
	// Do not consume the return value while waiting for the backend signal.
	select {
	case <-signal:
	case <-time.After(bound):
		t.Fatalf("refresh %s was not observed within %s; output=%q", phase, bound, capture.String())
	}
}

func waitForParentCancellationReturn(returned <-chan error) error {
	select {
	case err := <-returned:
		return err
	case <-time.After(time.Second):
		return errors.New("preview did not return within one second of parent cancellation")
	}
}

func releaseParentCancellationRefresh(t *testing.T, analyzer *parentCancellationRefreshAnalyzer, requireFinished bool) {
	t.Helper()
	analyzer.releaseOnce.Do(func() { close(analyzer.release) })
	if !requireFinished && analyzer.calls.Load() < 2 {
		return
	}
	select {
	case <-analyzer.finished:
	case <-time.After(time.Second):
		if requireFinished {
			t.Fatal("refresh backend did not finish after release")
		}
	}
}

func assertStaveParentCancellationRestored(t *testing.T, output string) {
	t.Helper()
	if got := strings.Count(output, "\x1b[?1049h"); got != 1 {
		t.Fatalf("parent cancellation alternate-screen enter count = %d, want 1; output=%q", got, output)
	}
	if got := strings.Count(output, "\x1b[?1049l"); got != 1 {
		t.Fatalf("parent cancellation alternate-screen leave count = %d, want 1; output=%q", got, output)
	}
	if got := strings.Count(output, "\x1b[?25h"); got != 1 {
		t.Fatalf("parent cancellation cursor-show count = %d, want 1; output=%q", got, output)
	}
	restore := strings.LastIndex(output, "\x1b[?1049l")
	tail := output[restore+len("\x1b[?1049l"):]
	if strings.Contains(tail, "Status: Stave preview") || strings.Contains(tail, "parent-cancellation-fixture") || strings.Contains(tail, "\x1b[H") || strings.Contains(tail, "\x1b[2J") {
		t.Fatalf("parent cancellation repainted after terminal restoration: %q", tail)
	}
}

func parentCancellationPreparedSession(ctx context.Context, t *testing.T) *stave.Prepared[staveSummaryModel] {
	t.Helper()
	reportData := report.Report{SchemaVersion: report.SchemaVersion, Dependencies: []report.DependencyReport{{Language: "go", Name: "alpha"}}}
	summary := NewSummary(io.Discard, strings.NewReader(""), &stubAnalyzer{report: reportData}, report.NewFormatter())
	opts := summary.applyDefaults(Options{UseStavePreview: true, Features: previewFeatures(t), Width: 80})
	view := mapSummaryReportView(reportData)
	state := buildSummaryState(opts)
	program, err := newLopperStaveProgram(summary, &opts, &view, &state)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := program.NewSession(ctx, staveSessionOptions(opts, true))
	if err != nil {
		t.Fatal(err)
	}
	return prepared
}
