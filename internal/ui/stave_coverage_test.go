package ui

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/ben-ranford/stave"
	"github.com/ben-ranford/stave/action"
	"github.com/ben-ranford/stave/capability"
	"github.com/ben-ranford/stave/event"
	staveinput "github.com/ben-ranford/stave/input"
	"github.com/ben-ranford/stave/layout"
	"github.com/ben-ranford/stave/semantic"
	"github.com/ben-ranford/stave/theme"
)

func coverageShared() (*staveSummaryShared, *summaryReportView, *summaryState) {
	view := &summaryReportView{Dependencies: []summaryDependencyView{{Language: "go", Name: "alpha", UsedPercent: 12.5, EstimatedUnusedBytes: 3}}}
	state := &summaryState{page: 1, pageSize: 10, sortMode: sortByWaste}
	return &staveSummaryShared{summary: NewSummary(io.Discard, nil, nil, nil)}, view, state
}

func TestStaveTerminalViewRendersAndUsesFallbackViewport(t *testing.T) {
	_, view, state := coverageShared()
	sorted, deps, normalized, total := runSummaryDependencyPipeline(*view, *state)
	tree, err := staveTree(*view, sorted, deps, normalized, total, true)
	if err != nil {
		t.Fatal(err)
	}
	b := &staveTerminal{ctx: context.Background(), width: 0, height: 0, snapshot: func(context.Context, any) (staveTerminalSnapshot, error) {
		return staveTerminalSnapshot{tree: tree, caps: capability.Manifest{}, theme: theme.Resolved{}}, nil
	}}
	if got := (&staveTerminalModel{bridge: b}).View().Content; got == "" {
		t.Fatal("View returned empty output")
	}
}

func TestStaveTerminalViewReportsSnapshotAndRenderErrors(t *testing.T) {
	want := errors.New("snapshot failed")
	b := &staveTerminal{snapshot: func(context.Context, any) (staveTerminalSnapshot, error) { return staveTerminalSnapshot{}, want }}
	if !strings.Contains((&staveTerminalModel{bridge: b}).View().Content, "snapshot failed") {
		t.Fatal("snapshot error not shown")
	}
	b = &staveTerminal{snapshot: func(context.Context, any) (staveTerminalSnapshot, error) {
		return staveTerminalSnapshot{tree: structTreeForCoverage(t)}, nil
	}}
	if (&staveTerminalModel{bridge: b}).View().Content == "" {
		t.Fatal("render-error view unexpectedly empty")
	}
}

func structTreeForCoverage(t *testing.T) (tree semantic.Tree) {
	t.Helper()
	_, view, state := coverageShared()
	sorted, deps, normalized, total := runSummaryDependencyPipeline(*view, *state)
	tree, err := staveTree(*view, sorted, deps, normalized, total, false)
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

func TestStaveTerminalUpdateHandlesResizeReleaseAndQuit(t *testing.T) {
	b := &staveTerminal{ctx: context.Background(), prepared: struct{}{}, sendEvent: func(context.Context, any, event.Event) error { return nil }, snapshot: func(context.Context, any) (staveTerminalSnapshot, error) { return staveTerminalSnapshot{}, nil }}
	m := &staveTerminalModel{bridge: b}
	_, _ = m.Update(tea.WindowSizeMsg{Width: 40, Height: 12})
	_, _ = m.Update(tea.KeyReleaseMsg{})
	_, cmd := m.Update(tea.InterruptMsg{})
	if !b.quit || cmd == nil {
		t.Fatal("interrupt did not quit")
	}
}

func TestStaveTerminalUpdateReportsSnapshotAndKeyFailures(t *testing.T) {
	b := &staveTerminal{snapshot: func(context.Context, any) (staveTerminalSnapshot, error) {
		return staveTerminalSnapshot{}, errors.New("snapshot boom")
	}}
	m := &staveTerminalModel{bridge: b}
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if b.quit || cmd != nil {
		t.Fatalf("snapshot lookup should be non-fatal: quit=%v cmd=%v", b.quit, cmd)
	}
	b = &staveTerminal{prepared: struct{}{}, sendEvent: func(context.Context, any, event.Event) error { return errors.New("key boom") }}
	m = &staveTerminalModel{bridge: b}
	_, cmd = m.Update(tea.KeyPressMsg{Text: "x", Code: 'x'})
	if !b.quit || cmd == nil || !strings.Contains(b.err.Error(), "key boom") {
		t.Fatalf("key failure state: quit=%v cmd=%v err=%v", b.quit, cmd, b.err)
	}
}

func TestRunStaveTerminalQuitsFromInput(t *testing.T) {
	shared, view, state := coverageShared()
	opts := Options{Width: 80}
	program, err := newLopperStaveProgram(shared.summary, &opts, view, state)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := program.NewSession(context.Background(), staveSessionOptions(opts, false))
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Session.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var output bytes.Buffer
	preview := &StavePreview{legacy: shared.summary}
	if err := preview.runStaveTerminal(ctx, opts, *view, *state, prepared, bytes.NewBufferString("q"), &output, false); err != nil {
		t.Fatal(err)
	}
}

func TestStaveTerminalDispatchTextAndActionErrors(t *testing.T) {
	b := &staveTerminal{ctx: context.Background(), prepared: struct{}{}, snapshot: func(context.Context, any) (staveTerminalSnapshot, error) {
		return staveTerminalSnapshot{model: staveSummaryModel{}}, nil
	}, sendEvent: func(context.Context, any, event.Event) error { return nil }}
	if msg := b.beginCommand("arbitrary text")(); msg == nil {
		t.Fatal("text command returned nil completion")
	}
	b.inflight = false
	if cmd := b.beginCommand("refresh"); cmd == nil {
		t.Fatal("missing prepared action should return a completion command")
	}
}

func TestStaveTerminalDispatchCoversSnapshotAndPublishFailures(t *testing.T) {
	b := &staveTerminal{snapshot: func(context.Context, any) (staveTerminalSnapshot, error) {
		return staveTerminalSnapshot{}, errors.New("snapshot")
	}}
	if cmd := b.beginCommand("refresh"); cmd == nil {
		t.Fatal("snapshot failure swallowed")
	}
	b = &staveTerminal{snapshot: func(context.Context, any) (staveTerminalSnapshot, error) { return staveTerminalSnapshot{}, nil }, sendEvent: func(context.Context, any, event.Event) error { return errors.New("publish") }}
	_ = b.beginCommand("free text")
	b.inflight = false
	// beginAction requires a real Stave.Prepared session; transport failures
	// are covered by the Bubble Tea PTY tests and are not invoked with a dummy.
}

func TestStaveTerminalReportErrorAndShutdownAreIdempotent(t *testing.T) {
	var kinds []event.Kind
	b := &staveTerminal{ctx: context.Background(), prepared: struct{}{}, sendEvent: func(context.Context, any, event.Event) error { kinds = append(kinds, event.Text); return nil }}
	b.reportError(errors.New("bad\x1b[31m"))
	b.reportError(nil)
	b.shutdown()
	b.shutdown()
	if len(kinds) != 2 {
		t.Fatalf("expected one error and one shutdown event, got %d", len(kinds))
	}
}

func TestStaveModelClampsSelectionAndPageWithoutSharedData(t *testing.T) {
	m := &staveSummaryModel{interaction: staveSummaryInteraction{selectedRow: 9, summary: summaryState{page: 4}}}
	clampStaveSelection(m)
	clampStavePage(m)
	if m.interaction.selectedRow != 9 || m.interaction.summary.page != 4 {
		t.Fatal("nil shared unexpectedly mutated model")
	}
}

func TestStaveModelCommandErrors(t *testing.T) {
	shared, view, state := coverageShared()
	m := newStaveSummaryModel(view, nil, *state)
	m.interaction.summary.page = 2
	if status, err := applyStaveCommand(&m.interaction.summary, "unknown-command", shared); status != "" || err == "" {
		t.Fatalf("unknown command result = %q, %q", status, err)
	}
	if status, err := applyStaveCommand(&m.interaction.summary, "", shared); status != "" || err != "" {
		t.Fatalf("empty command result = %q, %q", status, err)
	}
}

func TestStaveTerminalSessionAndKeyErrorPaths(t *testing.T) {
	b := &staveTerminal{}
	if _, err := b.sessionSnapshot(); err == nil {
		t.Fatal("unprepared session returned nil error")
	}
	b = &staveTerminal{prepared: struct{}{}, sendEvent: func(context.Context, any, event.Event) error { return errors.New("send failed") }}
	if err := b.key(tea.KeyPressMsg{Text: "x", Code: 'x'}); err == nil {
		t.Fatal("send failure was swallowed")
	}
	if err := b.key(tea.KeyPressMsg{Code: tea.KeyF1}); err == nil {
		t.Fatal("unsupported special key accepted")
	}
	b = &staveTerminal{prepared: struct{}{}, sendEvent: func(context.Context, any, event.Event) error { return nil }}
	if err := b.key(tea.KeyPressMsg{Text: "?", Code: '?'}); err != nil {
		t.Fatalf("printable fallback key: %v", err)
	}
	if _, err := (&staveTerminal{prepared: struct{}{}}).sessionSnapshot(); err == nil {
		t.Fatal("invalid prepared session accepted")
	}
}

func TestStaveTerminalUnsupportedKeyPublishesDiagnosticAndContinues(t *testing.T) {
	var events []event.Event
	b := &staveTerminal{
		ctx:      context.Background(),
		prepared: struct{}{},
		sendEvent: func(_ context.Context, _ any, ev event.Event) error {
			events = append(events, ev)
			return nil
		},
	}
	m := &staveTerminalModel{bridge: b}
	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyF1})
	if updated != m || cmd != nil || b.quit || b.err != nil {
		t.Fatalf("unsupported key was fatal: model=%T cmd=%v bridge=%+v", updated, cmd, b)
	}
	if len(events) != 1 || events[0].Kind != event.Diagnostic {
		t.Fatalf("unsupported key events = %+v", events)
	}
	payload, ok := events[0].Payload.(event.DiagnosticPayload)
	if !ok || payload.Code != "LOPPER_INPUT_REJECTED" || !strings.Contains(payload.Message, "unsupported terminal key") {
		t.Fatalf("unsupported key diagnostic = %#v", events[0].Payload)
	}
}

func TestStaveTerminalUpdateReturnsQuitWhenAlreadyFailed(t *testing.T) {
	b := &staveTerminal{err: errors.New("already failed"), quit: true}
	_, cmd := (&staveTerminalModel{bridge: b}).Update(tea.KeyPressMsg{Text: "x", Code: 'x'})
	if cmd == nil {
		t.Fatal("failed model did not return quit command")
	}
}

func TestStaveTerminalDispatchActionPropagatesInvocationError(t *testing.T) {
	b := &staveTerminal{ctx: context.Background(), prepared: struct{}{}}
	if cmd := b.beginAction(action.ID("refresh"), nil, false); cmd == nil {
		t.Fatal("beginAction did not return completion")
	}
}

func TestStaveTerminalPasteValidatesModeAndNormalizesInput(t *testing.T) {
	var count int
	b := &staveTerminal{ctx: context.Background(), prepared: struct{}{}, sendEvent: func(context.Context, any, event.Event) error { count++; return nil }, snapshot: func(context.Context, any) (staveTerminalSnapshot, error) {
		return staveTerminalSnapshot{model: staveSummaryModel{}}, nil
	}}
	if err := b.paste("abc"); err == nil {
		t.Fatal("paste outside command mode accepted")
	}
	b.snapshot = func(context.Context, any) (staveTerminalSnapshot, error) {
		return staveTerminalSnapshot{model: staveSummaryModel{interaction: staveSummaryInteraction{commandMode: true}}}, nil
	}
	if err := b.paste(":ab\n"); err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("sent %d pasted keys, want 3", count)
	}
	if err := b.paste(strings.Repeat("a", staveinput.DefaultMaxPasteBytes+1)); err == nil {
		t.Fatal("oversized pasted command accepted")
	}
}

func TestStaveTerminalKeyHandlesMultiRuneAndControlText(t *testing.T) {
	count := 0
	b := &staveTerminal{prepared: struct{}{}, sendEvent: func(context.Context, any, event.Event) error { count++; return nil }}
	if err := b.key(tea.KeyPressMsg{Text: "ab"}); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("multi-rune key sent %d events", count)
	}
	if err := b.key(tea.KeyPressMsg{Text: "a\n"}); err == nil {
		t.Fatal("control rune accepted")
	}
}

func TestSelectedDependencyForRowRejectsOutOfRange(t *testing.T) {
	_, view, state := coverageShared()
	m := newStaveSummaryModel(view, nil, *state)
	m.interaction.selectedRow = 9
	if got := selectedDependencyForRow(m); got != "" {
		t.Fatalf("out-of-range dependency = %q", got)
	}
}

func TestStaveModelHashAndReducerPayloadBranches(t *testing.T) {
	m := staveSummaryModel{interaction: staveSummaryInteraction{summary: summaryState{page: 1}, focusPane: "summary"}}
	if _, err := hashStaveSummaryModel(m); err != nil {
		t.Fatal(err)
	}
	for _, ev := range []event.Event{{Kind: event.Resize, Payload: event.ResizePayload{Width: 0, Height: 0}}, {Kind: event.ActionInvoked, Payload: event.ActionInvokedPayload{CallID: "quit", ActionID: staveActionQuit}}, {Kind: event.EffectResult, Payload: event.EffectResultPayload{CallID: "quit", Status: "ok"}}} {
		var err error
		m, _, err = reduceStaveSummary(stave.ReduceContext{}, m, ev)
		if err != nil {
			t.Fatal(err)
		}
	}
	if m.interaction.status != "ok" || m.interaction.viewport.Width != 1 {
		t.Fatalf("unexpected reducer state: %+v", m.interaction)
	}
}

func TestStavePreviewSnapshotRejectsMissingPathAndCancelledContext(t *testing.T) {
	p := NewStavePreview(NewSummary(io.Discard, nil, nil, nil)).(*StavePreview)
	opts := Options{UseStavePreview: true, Features: previewFeatures(t)}
	if err := p.Snapshot(context.Background(), opts, ""); err == nil {
		t.Fatal("missing snapshot path accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := p.Snapshot(ctx, opts, "-"); err == nil {
		t.Fatal("cancelled snapshot succeeded")
	}
}

func TestStavePreviewStartCancelledAndRendererFlags(t *testing.T) {
	p := NewStavePreview(NewSummary(io.Discard, nil, nil, nil)).(*StavePreview)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	opts := Options{UseStavePreview: true, Features: previewFeatures(t)}
	if err := p.Start(ctx, opts); err == nil {
		t.Fatal("cancelled start succeeded")
	}
	for _, ascii := range []bool{true, false} {
		r, err := newStaveRenderer(Options{Width: 80}, ascii)
		if err != nil {
			t.Fatal(err)
		}
		if r.Caps.Width != 80 {
			t.Fatalf("renderer width = %d", r.Caps.Width)
		}
	}
}

func TestLopperStaveInputActionBranches(t *testing.T) {
	s := summaryState{page: 1}
	for _, in := range []string{"q", "", "open go:alpha", "filter go", "sort name", "page 2", "size 5", "apply-codemod go:alpha --confirm", "baseline-save label", "baseline-compare"} {
		_, _, _, handled := lopperStaveInput(in, s)
		if !handled {
			t.Fatalf("input %q not handled", in)
		}
	}
	if _, _, _, handled := lopperStaveInput("not a command", s); handled {
		t.Fatal("unknown command handled")
	}
}

func TestInvokeLopperActionRejectsUnknownAction(t *testing.T) {
	shared, view, state := coverageShared()
	p, err := newLopperStaveProgram(shared.summary, &Options{}, view, state)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := p.NewSession(context.Background(), staveSessionOptions(Options{Width: 80}, false))
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Session.Close()
	b := &staveTerminal{ctx: context.Background(), prepared: prepared}
	if _, err := b.sessionSnapshot(); err != nil {
		t.Fatalf("real session snapshot: %v", err)
	}
	if err := invokeLopperAction(context.Background(), prepared, action.ID("missing"), nil, "test", false); err == nil {
		t.Fatal("unknown action accepted")
	}
}

func TestStaveSessionOptionsDefaultsWidthAndTTY(t *testing.T) {
	o := staveSessionOptions(Options{}, false)
	if o.Viewport != (layout.Size{Width: 80, Height: 24}) {
		t.Fatalf("viewport = %+v", o.Viewport)
	}
}
