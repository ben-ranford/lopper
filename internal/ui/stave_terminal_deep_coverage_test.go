package ui

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/ben-ranford/stave"
	"github.com/ben-ranford/stave/action"
	"github.com/ben-ranford/stave/capability"
	"github.com/ben-ranford/stave/event"
	"github.com/ben-ranford/stave/theme"
)

func deepCoveragePreparedSession(t *testing.T) *stave.Prepared[staveSummaryModel] {
	t.Helper()
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
	t.Cleanup(func() {
		prepared.Session.Close()
	})
	return prepared
}

func validTerminalSnapshot(t *testing.T) staveTerminalSnapshot {
	t.Helper()
	return staveTerminalSnapshot{
		model: staveSummaryModel{interaction: staveSummaryInteraction{summary: summaryState{page: 1, pageSize: 10}}},
		tree:  structTreeForCoverage(t),
		caps:  capability.Manifest{},
		theme: theme.Resolved{},
	}
}

func TestStaveTerminalDeepCoverageUpdateBranches(t *testing.T) {
	t.Run("command mode enter dispatches command", func(t *testing.T) {
		b := &staveTerminal{
			ctx:      context.Background(),
			prepared: struct{}{},
			sendEvent: func(context.Context, any, event.Event) error {
				return nil
			},
			snapshot: func(context.Context, any) (staveTerminalSnapshot, error) {
				return staveTerminalSnapshot{
					model: staveSummaryModel{interaction: staveSummaryInteraction{commandMode: true, filterBuffer: "refresh"}},
				}, nil
			},
		}
		_, cmd := (&staveTerminalModel{bridge: b}).Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		if cmd == nil {
			t.Fatal("enter did not schedule async command")
		}
	})

	t.Run("selection enter dispatches dependency aware open", func(t *testing.T) {
		b := &staveTerminal{
			ctx:      context.Background(),
			prepared: struct{}{},
			sendEvent: func(context.Context, any, event.Event) error {
				return nil
			},
			snapshot: func(context.Context, any) (staveTerminalSnapshot, error) {
				return staveTerminalSnapshot{
					model: staveSummaryModel{
						view: &summaryReportView{Dependencies: []summaryDependencyView{{Language: "go", Name: "alpha"}}},
						interaction: staveSummaryInteraction{
							selectedRow: 0,
							summary:     summaryState{},
						},
					},
				}, nil
			},
		}
		_, cmd := (&staveTerminalModel{bridge: b}).Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		if cmd == nil {
			t.Fatal("enter did not schedule async open command")
		}
	})

	t.Run("refresh key dispatches refresh action", func(t *testing.T) {
		b := &staveTerminal{
			ctx:      context.Background(),
			prepared: struct{}{},
			sendEvent: func(context.Context, any, event.Event) error {
				return nil
			},
			snapshot: func(context.Context, any) (staveTerminalSnapshot, error) {
				return staveTerminalSnapshot{model: staveSummaryModel{opts: &Options{}}}, nil
			},
		}
		_, cmd := (&staveTerminalModel{bridge: b}).Update(tea.KeyPressMsg{Text: "r", Code: 'r'})
		if cmd == nil {
			t.Fatal("refresh key did not schedule async command")
		}
	})

	t.Run("post-key quit short-circuits", func(t *testing.T) {
		b := &staveTerminal{
			ctx:      context.Background(),
			prepared: struct{}{},
			sendEvent: func(context.Context, any, event.Event) error {
				return nil
			},
			snapshot: func(context.Context, any) (staveTerminalSnapshot, error) {
				return staveTerminalSnapshot{
					model: staveSummaryModel{interaction: staveSummaryInteraction{quit: true}},
				}, nil
			},
		}
		_, cmd := (&staveTerminalModel{bridge: b}).Update(tea.KeyPressMsg{Text: "x", Code: 'x'})
		if !b.quit {
			t.Fatal("quit snapshot did not mark bridge as quit")
		}
		if cmd == nil {
			t.Fatal("quit snapshot did not return tea.Quit")
		}
	})

	t.Run("paste failure is reported", func(t *testing.T) {
		var reported []event.Kind
		b := &staveTerminal{
			ctx:      context.Background(),
			prepared: struct{}{},
			sendEvent: func(_ context.Context, _ any, ev event.Event) error {
				reported = append(reported, ev.Kind)
				if ev.Kind == event.EffectResult {
					return errors.New("transport refused effect result")
				}
				return nil
			},
			snapshot: func(context.Context, any) (staveTerminalSnapshot, error) {
				return staveTerminalSnapshot{model: staveSummaryModel{opts: &Options{}}}, nil
			},
		}
		_, cmd := (&staveTerminalModel{bridge: b}).Update(tea.PasteMsg{Content: "plain text"})
		if cmd != nil {
			t.Fatal("paste failure should not schedule an extra command")
		}
		if len(reported) != 1 || reported[0] != event.Diagnostic {
			t.Fatalf("reported kinds = %v", reported)
		}
	})
}

func TestStaveTerminalDeepCoverageKeyPasteDispatchBranches(t *testing.T) {
	t.Run("key guard and success", func(t *testing.T) {
		if err := (&staveTerminal{}).key(tea.KeyPressMsg{Text: "x", Code: 'x'}); err != nil {
			t.Fatalf("nil bridge key guard returned error: %v", err)
		}
		var kinds []event.Kind
		b := &staveTerminal{
			ctx:      context.Background(),
			prepared: struct{}{},
			sendEvent: func(_ context.Context, _ any, ev event.Event) error {
				kinds = append(kinds, ev.Kind)
				return nil
			},
		}
		if err := b.key(tea.KeyPressMsg{Text: "x", Code: 'x'}); err != nil {
			t.Fatalf("key send: %v", err)
		}
		if len(kinds) != 1 || kinds[0] != event.Key {
			t.Fatalf("key event kinds = %v", kinds)
		}
	})

	t.Run("multi rune and unsupported special key", func(t *testing.T) {
		var count int
		b := &staveTerminal{
			ctx:      context.Background(),
			prepared: struct{}{},
			sendEvent: func(_ context.Context, _ any, ev event.Event) error {
				count++
				if ev.Kind != event.Key {
					t.Fatalf("unexpected event kind: %v", ev.Kind)
				}
				return nil
			},
		}
		if err := b.key(tea.KeyPressMsg{Text: "ab"}); err != nil {
			t.Fatalf("multi-rune key: %v", err)
		}
		if count != 2 {
			t.Fatalf("multi-rune key sent %d events", count)
		}
		if err := b.key(tea.KeyPressMsg{Code: tea.KeyF1}); err == nil {
			t.Fatal("unsupported special key accepted")
		}
	})

	t.Run("paste guards, success and failure", func(t *testing.T) {
		if err := (&staveTerminal{}).paste("abc"); err != nil {
			t.Fatalf("nil bridge paste guard returned error: %v", err)
		}
		terminal := &staveTerminal{
			ctx:      context.Background(),
			prepared: struct{}{},
			sendEvent: func(context.Context, any, event.Event) error {
				return nil
			},
			snapshot: func(context.Context, any) (staveTerminalSnapshot, error) {
				return staveTerminalSnapshot{}, errors.New("snapshot boom")
			},
		}
		if err := terminal.paste("abc"); err == nil || !strings.Contains(err.Error(), "snapshot boom") {
			t.Fatalf("snapshot failure not surfaced: %v", err)
		}

		var count int
		commandMode := &staveTerminal{
			ctx:      context.Background(),
			prepared: struct{}{},
			sendEvent: func(context.Context, any, event.Event) error {
				count++
				return nil
			},
			snapshot: func(context.Context, any) (staveTerminalSnapshot, error) {
				return staveTerminalSnapshot{model: staveSummaryModel{interaction: staveSummaryInteraction{commandMode: true}}}, nil
			},
		}
		if err := commandMode.paste(":ab\n"); err != nil {
			t.Fatalf("command paste: %v", err)
		}
		if count != 3 {
			t.Fatalf("pasted runes sent %d key events", count)
		}

		sendErr := errors.New("send failed")
		b := &staveTerminal{
			ctx:      context.Background(),
			prepared: struct{}{},
			sendEvent: func(context.Context, any, event.Event) error {
				return sendErr
			},
			snapshot: func(context.Context, any) (staveTerminalSnapshot, error) {
				return staveTerminalSnapshot{model: staveSummaryModel{interaction: staveSummaryInteraction{commandMode: true}}}, nil
			},
		}
		if err := b.paste("x"); !errors.Is(err, sendErr) {
			t.Fatalf("paste send error = %v", err)
		}
	})

	t.Run("dispatch text, validation and action branches", func(t *testing.T) {
		var published []event.Kind
		snapshotModel := staveSummaryModel{opts: &Options{}}
		b := &staveTerminal{
			ctx:      context.Background(),
			prepared: struct{}{},
			sendEvent: func(_ context.Context, _ any, ev event.Event) error {
				published = append(published, ev.Kind)
				return nil
			},
			snapshot: func(context.Context, any) (staveTerminalSnapshot, error) {
				return staveTerminalSnapshot{model: snapshotModel}, nil
			},
		}
		if cmd := b.beginCommand("plain text"); cmd == nil {
			t.Fatal("plain text command returned nil")
		} else {
			_ = cmd()
		}
		if len(published) != 1 || published[0] != event.Diagnostic {
			t.Fatalf("text dispatch kinds = %v", published)
		}
		b.inflight = false
		if cmd := b.beginCommand("plain text\x1b"); cmd == nil {
			t.Fatal("control text command was not represented")
		}

		b.inflight = false
		published = published[:0]
		if cmd := b.beginCommand("refresh"); cmd == nil {
			t.Fatal("refresh command returned nil")
		}
		if len(published) != 1 || published[0] != event.ActionInvoked {
			t.Fatalf("refresh publish kinds = %v", published)
		}
		b.inflight = false

	})
}

func TestStaveTerminalDeepCoverageViewResizeSessionReportActionBranches(t *testing.T) {
	t.Run("cancel inflight publishes correlated cancellation outcome", func(t *testing.T) {
		var events []event.Event
		b := &staveTerminal{
			prepared: struct{}{}, currentCallID: "cancel-call-7", inflight: true,
			actionCancel: func() {},
			sendEvent:    func(_ context.Context, _ any, ev event.Event) error { events = append(events, ev); return nil },
		}
		b.cancelInflight()
		if b.inflight || b.currentCallID != "" || b.actionCancel != nil {
			t.Fatalf("cancel did not clear in-flight state: %+v", b)
		}
		if len(events) != 1 || events[0].Kind != event.EffectResult {
			t.Fatalf("cancellation events = %+v", events)
		}
		payload, ok := events[0].Payload.(event.EffectResultPayload)
		if !ok || payload.CallID != "cancel-call-7" || payload.Status != "cancelled" || !strings.Contains(payload.Error, "final action outcome unknown") {
			t.Fatalf("cancellation payload = %+v", events[0].Payload)
		}
	})

	t.Run("view error paths and fallbacks", func(t *testing.T) {
		if got := (&staveTerminalModel{bridge: &staveTerminal{quit: true}}).View(); got.Content != "" || got.AltScreen {
			t.Fatalf("quit view = %+v", got)
		}

		errView := (&staveTerminalModel{bridge: &staveTerminal{err: errors.New("boom")}}).View()
		if !strings.Contains(errView.Content, "boom") {
			t.Fatalf("error view = %q", errView.Content)
		}

		snapErr := errors.New("snapshot fail")
		if got := (&staveTerminalModel{bridge: &staveTerminal{snapshot: func(context.Context, any) (staveTerminalSnapshot, error) {
			return staveTerminalSnapshot{}, snapErr
		}}}).View(); !strings.Contains(got.Content, "snapshot fail") {
			t.Fatalf("snapshot error view = %q", got.Content)
		}

		renderCtx, cancel := context.WithCancel(context.Background())
		cancel()
		renderErrView := (&staveTerminalModel{bridge: &staveTerminal{
			ctx:      renderCtx,
			width:    0,
			height:   0,
			alt:      true,
			prepared: struct{}{},
			snapshot: func(context.Context, any) (staveTerminalSnapshot, error) {
				return validTerminalSnapshot(t), nil
			},
		}}).View()
		if !strings.Contains(renderErrView.Content, context.Canceled.Error()) {
			t.Fatalf("render error view = %q", renderErrView.Content)
		}

		renderPrepared := deepCoveragePreparedSession(t)
		renderBridge := &staveTerminal{
			ctx:      context.Background(),
			width:    0,
			height:   0,
			alt:      true,
			prepared: renderPrepared,
		}
		snap, err := renderBridge.sessionSnapshot()
		if err != nil {
			t.Fatalf("render snapshot: %v", err)
		}
		renderBridge.snapshot = func(context.Context, any) (staveTerminalSnapshot, error) {
			return snap, nil
		}
		renderView := (&staveTerminalModel{bridge: renderBridge}).View()
		if renderView.Content == "" {
			t.Fatal("render view was empty")
		}
		if !renderView.AltScreen {
			t.Fatal("alternate screen should be enabled when requested")
		}
	})

	t.Run("sessionSnapshot, resize and reportError", func(t *testing.T) {
		prepared := deepCoveragePreparedSession(t)
		b := &staveTerminal{ctx: context.Background(), prepared: prepared}
		snap, err := b.sessionSnapshot()
		if err != nil {
			t.Fatalf("real session snapshot: %v", err)
		}
		if snap.caps.Width != 80 {
			t.Fatalf("snapshot width = %d", snap.caps.Width)
		}
		if snap.model.interaction.summary.page != 1 {
			t.Fatalf("snapshot model page = %d", snap.model.interaction.summary.page)
		}

		if err := (&staveTerminal{}).resize(10, 10); err != nil {
			t.Fatalf("resize nil guard: %v", err)
		}

		var gotKinds []event.Kind
		b = &staveTerminal{
			ctx:      context.Background(),
			prepared: struct{}{},
			sendEvent: func(_ context.Context, _ any, ev event.Event) error {
				gotKinds = append(gotKinds, ev.Kind)
				return nil
			},
		}
		if err := b.resize(-1, 10); err == nil {
			t.Fatal("negative resize dimensions accepted")
		}
		if len(gotKinds) != 0 {
			t.Fatalf("resize emitted events after validation failure: %v", gotKinds)
		}
		b.sendEvent = nil
		if err := b.resize(40, 12); err != nil {
			t.Fatalf("resize with nil sender: %v", err)
		}

		sendErr := errors.New("report transport failed")
		b = &staveTerminal{
			ctx:      context.Background(),
			prepared: struct{}{},
			sendEvent: func(_ context.Context, _ any, ev event.Event) error {
				gotKinds = append(gotKinds, ev.Kind)
				if ev.Kind == event.EffectResult {
					return sendErr
				}
				return nil
			},
		}
		b.reportError(errors.New("bad\x1b[31m"))
		if b.err != nil && !strings.Contains(b.err.Error(), "bad") {
			t.Fatalf("reportError stored = %v", b.err)
		}
		if len(gotKinds) == 0 || gotKinds[0] != event.Diagnostic {
			t.Fatalf("reportError published = %v", gotKinds)
		}
	})

	t.Run("dispatchAction event validation and runStaveTerminal exits", func(t *testing.T) {
		var published []event.Kind
		b := &staveTerminal{
			ctx:      context.Background(),
			prepared: struct{}{},
			sendEvent: func(_ context.Context, _ any, ev event.Event) error {
				published = append(published, ev.Kind)
				return nil
			},
		}
		if cmd := b.beginAction(action.ID("bad\x1b"), map[string]any{"value": "x"}, false); cmd == nil {
			t.Fatal("dispatchAction accepted control characters")
		}
		if len(published) != 0 {
			t.Fatalf("dispatchAction published after validation failure: %v", published)
		}

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

		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		var output bytes.Buffer
		preview := &StavePreview{legacy: shared.summary}
		if err := preview.runStaveTerminal(ctx, opts, *view, *state, prepared, bytes.NewBuffer(nil), &output, false); err == nil {
			t.Fatal("canceled runStaveTerminal unexpectedly succeeded")
		}
	})
}

func TestStaveTerminalDeepCoverageRunStaveTerminalClosedSessionError(t *testing.T) {
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
	prepared.Session.Close()
	var output bytes.Buffer
	preview := &StavePreview{legacy: shared.summary}
	err = preview.runStaveTerminal(context.Background(), opts, *view, *state, prepared, strings.NewReader("x"), &output, false)
	if err == nil {
		t.Fatal("closed session run unexpectedly succeeded")
	}
	if !strings.Contains(err.Error(), "session") && !strings.Contains(err.Error(), "closed") {
		t.Fatalf("runStaveTerminal error = %v", err)
	}
}

func TestStaveTerminalDeepCoverageRunStaveTerminalNormalQuit(t *testing.T) {
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
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var output bytes.Buffer
	preview := &StavePreview{legacy: shared.summary}
	if err := preview.runStaveTerminal(ctx, opts, *view, *state, prepared, strings.NewReader("q"), &output, false); err != nil {
		t.Fatal(err)
	}
}
