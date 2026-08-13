package ui

import (
	"context"
	"errors"
	"io"
	"math"
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/stave"
	"github.com/ben-ranford/stave/action"
	"github.com/ben-ranford/stave/event"
	"github.com/ben-ranford/stave/theme"
	"github.com/creack/pty"
)

func reduceCoverageEffect(t *testing.T, actionID string, value any, status, errText string) staveSummaryModel {
	t.Helper()
	m := staveSummaryModel{view: &summaryReportView{Dependencies: []summaryDependencyView{{Language: "go", Name: "alpha"}}}, interaction: staveSummaryInteraction{pendingCallID: "call-1", pendingActionID: actionID}}
	n, _, err := reduceStaveSummary(stave.ReduceContext{}, m, event.Event{Kind: event.EffectResult, Payload: event.EffectResultPayload{CallID: "call-1", Status: status, Error: errText, Value: value}})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func validCoverageEnvelope(actionID string, value any) map[string]any {
	return map[string]any{"version": "lopper.action-result/v1", "action": actionID, "value": value}
}

func TestStaveReducerRejectsMalformedEffectResultEnvelopes(t *testing.T) {
	cases := []struct {
		name  string
		value any
		want  string
	}{
		{"wrong call", validCoverageEnvelope(staveActionRefresh, map[string]any{"refreshed": true, "report": report.Report{}}), ""},
		{"invalid scalar", "not-an-envelope", "invalid action outcome:"},
		{"wrong version", map[string]any{"version": "other", "action": staveActionRefresh, "value": map[string]any{}}, "version or action mismatch"},
		{"missing value", map[string]any{"version": "lopper.action-result/v1", "action": staveActionRefresh}, "missing value"},
		{"value not object", validCoverageEnvelope(staveActionRefresh, "bad"), "value must be an object"},
		{"refresh incomplete", validCoverageEnvelope(staveActionRefresh, map[string]any{"refreshed": false, "report": report.Report{}}), "refresh payload incomplete"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := reduceCoverageEffect(t, staveActionRefresh, tc.value, "completed", "")
			if tc.name == "wrong call" {
				// A call ID mismatch is intentionally ignored, preserving the pending action.
				m = staveSummaryModel{interaction: staveSummaryInteraction{pendingCallID: "other"}}
				var err error
				m, _, err = reduceStaveSummary(stave.ReduceContext{}, m, event.Event{Kind: event.EffectResult, Payload: event.EffectResultPayload{CallID: "call-1", Value: tc.value}})
				if err != nil {
					t.Fatal(err)
				}
				if m.interaction.pendingCallID != "other" {
					t.Fatal("stale effect result mutated pending state")
				}
				return
			}
			if tc.want != "" && !strings.Contains(m.interaction.error, tc.want) {
				t.Fatalf("error = %q, want substring %q", m.interaction.error, tc.want)
			}
		})
	}
}

func TestStaveReducerHandlesEffectErrorsAndDependencyFocus(t *testing.T) {
	m := reduceCoverageEffect(t, staveActionRefresh, nil, "error", "backend failed")
	if m.interaction.error != "backend failed" {
		t.Fatalf("error = %q", m.interaction.error)
	}

	opened := reduceCoverageEffect(t, staveActionOpen, validCoverageEnvelope(staveActionOpen, map[string]any{"dependency": "go:alpha"}), "completed", "")
	if opened.interaction.focusPane != "detail" || opened.interaction.summary.selectedDependency != "go:alpha" {
		t.Fatalf("valid open did not focus detail: %+v", opened.interaction)
	}
	missing := reduceCoverageEffect(t, staveActionOpen, validCoverageEnvelope(staveActionOpen, map[string]any{"dependency": "go:missing"}), "completed", "")
	if missing.interaction.error != "No data for dependency go:missing" || missing.interaction.focusPane != "summary" {
		t.Fatalf("missing dependency was not rejected: %+v", missing.interaction)
	}

	noView := staveSummaryModel{interaction: staveSummaryInteraction{pendingCallID: "call-1", pendingActionID: staveActionOpen}}
	noView, _, err := reduceStaveSummary(stave.ReduceContext{}, noView, event.Event{Kind: event.EffectResult, Payload: event.EffectResultPayload{CallID: "call-1", Value: validCoverageEnvelope(staveActionOpen, map[string]any{"dependency": "go:x"})}})
	if err != nil {
		t.Fatal(err)
	}
	if noView.interaction.error == "" {
		t.Fatal("missing view did not produce a user-facing error")
	}
}

func TestStaveReducerRejectsInvalidCommandOutcome(t *testing.T) {
	m := reduceCoverageEffect(t, "lopper.summary.sort.v1", validCoverageEnvelope("lopper.summary.sort.v1", map[string]any{"command": "sort bogus"}), "completed", "")
	if m.interaction.status != "" || !strings.Contains(m.interaction.error, "unknown command") {
		t.Fatalf("invalid command outcome was reported as success: %+v", m.interaction)
	}
}

func TestStaveOutcomeValidationAndNormalization(t *testing.T) {
	cases := []struct {
		action string
		value  map[string]any
		want   string
	}{
		{staveActionOpen, map[string]any{}, "dependency missing"},
		{staveActionRefresh, map[string]any{"refreshed": true}, "refresh payload incomplete"},
		{staveActionApplyCodemod, map[string]any{"dependency": "go:x", "applied": false, "report": report.Report{}}, ""},
		{staveActionSaveBaseline, map[string]any{"ok": false}, "save payload incomplete"},
		{staveActionCompareBaseline, map[string]any{"ok": true}, "compare payload incomplete"},
		{"lopper.summary.filter.v1", map[string]any{}, "command missing"},
	}
	for _, tc := range cases {
		if got := validateStaveOutcome(tc.action, tc.value); got != tc.want {
			t.Errorf("validate(%s) = %q, want %q", tc.action, got, tc.want)
		}
	}
	for _, value := range []any{func() {}, make(chan int), []int{1, 2}} {
		if _, err := normalizeOutcomeMap(value); err == nil && value != nil {
			t.Errorf("normalize(%T) unexpectedly succeeded", value)
		}
	}
	if _, err := normalizeOutcomeMap(map[string]any{"version": "lopper.action-result/v1", "action": staveActionQuit, "value": map[string]any{}, "unexpected": true}); err == nil {
		t.Fatal("unknown outcome field was accepted")
	}
	if _, err := decodeStaveReportOutcome(math.NaN()); err == nil {
		t.Fatal("non-JSON report outcome was accepted")
	}
}

func TestStaveModelCloneAndHashPreserveIndependentSnapshots(t *testing.T) {
	color := true
	m := newStaveSummaryModel(&summaryReportView{Dependencies: []summaryDependencyView{{Language: "go", Name: "alpha"}}}, &Options{Color: &color}, summaryState{})
	clone, err := cloneStaveSummaryModel(m)
	if err != nil {
		t.Fatal(err)
	}
	clone.view.Dependencies[0].Name = "changed"
	*clone.opts.Color = false
	if m.view.Dependencies[0].Name == clone.view.Dependencies[0].Name || *m.opts.Color == *clone.opts.Color {
		t.Fatal("clone shares mutable snapshots")
	}
	if a, err := hashStaveSummaryModel(m); err != nil || a == ([32]byte{}) {
		t.Fatalf("hash = %x, err=%v", a, err)
	}
	if _, err := cloneStaveSummaryModel(staveSummaryModel{cloneErr: errors.New("clone failure")}); err != nil {
		t.Fatal(err)
	}
}

func TestStavePreviewProgramAndTerminalDimensionGuards(t *testing.T) {
	if _, err := newLopperStaveProgram(NewSummary(io.Discard, strings.NewReader(""), nil, nil), nil, nil, nil); err == nil {
		t.Fatal("nil options accepted")
	}
	opts := &Options{Width: 80}
	if _, err := newLopperStaveProgram(NewSummary(io.Discard, strings.NewReader(""), nil, nil), opts, nil, nil); err == nil {
		t.Fatal("nil report view accepted")
	}
	if width, height, ok := staveTerminalDimensions(io.Discard); ok || width != 0 || height != 0 {
		t.Fatalf("non-file dimensions = %d x %d, ok=%v", width, height, ok)
	}
	devNull, err := os.OpenFile("/dev/null", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("open /dev/null: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := devNull.Close(); closeErr != nil && !errors.Is(closeErr, os.ErrClosed) {
			t.Errorf("close /dev/null: %v", closeErr)
		}
	})
	if _, _, ok := staveTerminalDimensions(devNull); ok {
		t.Fatal("non-terminal file reported usable dimensions")
	}
}

func TestStaveReducerAppliesRefreshAndClearsStaleSelection(t *testing.T) {
	model := staveSummaryModel{
		view:        &summaryReportView{Dependencies: []summaryDependencyView{{Language: "go", Name: "alpha"}}},
		interaction: staveSummaryInteraction{pendingCallID: "call-1", pendingActionID: staveActionRefresh, summary: summaryState{selectedDependency: "go:gone", page: 99, pageSize: 1}},
	}
	value := validCoverageEnvelope(staveActionRefresh, map[string]any{"refreshed": true, "report": report.Report{SchemaVersion: report.SchemaVersion, Dependencies: []report.DependencyReport{{Language: "go", Name: "alpha"}}}})
	model, _, err := reduceStaveSummary(stave.ReduceContext{}, model, event.Event{Kind: event.EffectResult, Payload: event.EffectResultPayload{CallID: "call-1", Value: value}})
	if err != nil {
		t.Fatal(err)
	}
	if model.interaction.summary.selectedDependency != "" || model.interaction.focusPane != "summary" {
		t.Fatalf("stale selection survived refresh: %+v", model.interaction)
	}
}

func TestSummaryActionReportedErrorRetainsCause(t *testing.T) {
	cause := errors.New("backend failed")
	reported := &summaryActionReportedError{err: cause}
	if reported.Error() != cause.Error() || !errors.Is(reported, cause) || !errors.Is(reported.Unwrap(), cause) {
		t.Fatalf("reported error lost cause: %v", reported)
	}
}

func TestStaveTerminalUpdateCompletionAndInputBranches(t *testing.T) {
	var sent []event.Event
	b := &staveTerminal{ctx: context.Background(), prepared: struct{}{}, sendEvent: func(_ context.Context, _ any, ev event.Event) error { sent = append(sent, ev); return nil }}
	b.snapshot = func(context.Context, any) (staveTerminalSnapshot, error) {
		return staveTerminalSnapshot{model: staveSummaryModel{interaction: staveSummaryInteraction{commandMode: true, filterBuffer: "filter go"}}}, nil
	}
	m := &staveTerminalModel{bridge: b}
	if _, cmd := m.Update(tea.PasteMsg{Content: "bad"}); cmd != nil || len(sent) == 0 {
		t.Fatal("paste diagnostic was not handled")
	}
	b.inflight = true
	if _, cmd := m.Update(staveTextCompletion{err: errors.New("text failed")}); cmd == nil {
		t.Fatal("text completion error did not quit")
	}
	b = &staveTerminal{prepared: struct{}{}, sendEvent: func(context.Context, any, event.Event) error { return nil }}
	m = &staveTerminalModel{bridge: b}
	if _, cmd := m.Update(tea.WindowSizeMsg{Width: 40, Height: 12}); cmd != nil || b.width != 40 || b.height != 12 {
		t.Fatalf("resize update failed: %+v cmd=%v", b, cmd)
	}
	b.sendEvent = func(context.Context, any, event.Event) error { return errors.New("resize failed") }
	if _, cmd := m.Update(tea.WindowSizeMsg{Width: 20, Height: 8}); cmd == nil || !b.quit {
		t.Fatal("resize error did not terminate")
	}
}

func TestStaveTerminalUpdateActionCompletionStatuses(t *testing.T) {
	var sent []event.Event
	b := &staveTerminal{prepared: struct{}{}, sendEvent: func(context.Context, any, event.Event) error { return nil }}
	b.sendEvent = func(_ context.Context, _ any, ev event.Event) error { sent = append(sent, ev); return nil }
	m := &staveTerminalModel{bridge: b}
	_, cmd := m.Update(staveActionCompletion{callID: "c1", actionID: action.ID(staveActionRefresh), result: staveActionResult{Outcome: &staveActionOutcome{Value: map[string]any{"ok": true}}}})
	if cmd != nil || len(sent) != 1 || sent[0].Kind != event.EffectResult {
		t.Fatalf("completion event not published: %#v", sent)
	}
	sent = nil
	_, cmd = m.Update(staveActionCompletion{callID: "c2", actionID: action.ID(staveActionRefresh), result: staveActionResult{Error: &staveActionError{Message: "action failed"}}})
	if cmd != nil || len(sent) != 1 {
		t.Fatal("action error completion not published")
	}
	if _, cmd = m.Update(staveActionCompletion{callID: "c3", actionID: action.ID(staveActionQuit), result: staveActionResult{Outcome: &staveActionOutcome{Value: map[string]any{}}}}); cmd == nil || !b.quit {
		t.Fatal("successful quit completion did not quit")
	}
	// A command error is sanitized and still delivered as an EffectResult.
	b = &staveTerminal{prepared: struct{}{}, sendEvent: func(context.Context, any, event.Event) error { return nil }}
	var failed event.Event
	b.sendEvent = func(_ context.Context, _ any, ev event.Event) error { failed = ev; return nil }
	m = &staveTerminalModel{bridge: b}
	if _, cmd = m.Update(staveActionCompletion{callID: "c4", actionID: action.ID(staveActionRefresh), err: errors.New("bad\x1b[31m")}); cmd != nil || failed.Kind != event.EffectResult {
		t.Fatal("completion command error was not published")
	}
	// A publish failure terminates the adapter rather than silently dropping it.
	b = &staveTerminal{prepared: struct{}{}, sendEvent: func(context.Context, any, event.Event) error { return errors.New("publish failed") }}
	m = &staveTerminalModel{bridge: b}
	if _, cmd = m.Update(staveActionCompletion{callID: "c5", actionID: action.ID(staveActionRefresh)}); cmd == nil || !b.quit {
		t.Fatal("publish failure did not terminate")
	}
}

func TestStaveTerminalCancelAndReportErrorBranches(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var events []event.Event
	b := &staveTerminal{ctx: ctx, prepared: struct{}{}, currentCallID: "call", inflight: true, actionCancel: cancel, sendEvent: func(_ context.Context, _ any, ev event.Event) error { events = append(events, ev); return nil }}
	b.cancelInflight()
	if b.inflight || b.currentCallID != "" || b.actionCancel != nil || len(events) != 1 || events[0].Kind != event.EffectResult {
		t.Fatalf("inflight cancellation state = %+v events=%#v", b, events)
	}
	b.reportError(errors.New("bad\x1b[31m input"))
	if len(events) != 2 || events[1].Kind != event.Diagnostic {
		t.Fatalf("diagnostic was not published: %#v", events)
	}
	b.sendEvent = func(context.Context, any, event.Event) error { return errors.New("diagnostic failed") }
	b.reportError(errors.New("fallback"))
	if b.err == nil {
		t.Fatal("diagnostic send failure was not retained")
	}
}

func TestStaveTerminalUpdateCancelsInflightQuitAndCtrlKeys(t *testing.T) {
	for _, msg := range []tea.Msg{tea.KeyPressMsg{Text: "q"}, tea.KeyPressMsg{Mod: tea.ModCtrl, Text: "c"}, tea.KeyPressMsg{Mod: tea.ModCtrl, Text: "d"}} {
		ctx, cancel := context.WithCancel(context.Background())
		var shutdowns int
		b := &staveTerminal{ctx: ctx, prepared: struct{}{}, inflight: true, currentCallID: "call", actionCancel: cancel, sendEvent: func(context.Context, any, event.Event) error {
			shutdowns++
			return nil
		}, snapshot: func(context.Context, any) (staveTerminalSnapshot, error) { return staveTerminalSnapshot{}, nil }}
		m := &staveTerminalModel{bridge: b}
		if _, cmd := m.Update(msg); cmd == nil || !b.quit || b.inflight || shutdowns < 2 {
			t.Fatalf("cancel key %T left bridge active: %+v shutdowns=%d", msg, b, shutdowns)
		}
	}
	// Key release events are intentionally ignored and must not mutate state.
	b := &staveTerminal{prepared: struct{}{}, width: 7, height: 3}
	m := &staveTerminalModel{bridge: b}
	if _, cmd := m.Update(tea.KeyReleaseMsg(tea.Key{Text: "r"})); cmd != nil || b.width != 7 || b.height != 3 {
		t.Fatal("key release mutated terminal state")
	}
}

func TestRenderStaveSessionFrameSuccessAndRendererFailure(t *testing.T) {
	shared, view, state := terminalCoverageShared()
	program, err := newLopperStaveProgram(shared.summary, &Options{Width: 80}, view, state)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := program.NewSession(context.Background(), staveSessionOptions(Options{Width: 80}, false))
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Session.Close()
	frame, err := renderStaveSessionFrame(context.Background(), prepared, staveSessionOptions(Options{Width: 80}, false))
	if err != nil || !strings.Contains(frame, "Stave preview") {
		t.Fatalf("rendered frame = %q, err=%v", frame, err)
	}
	prepared.Theme = theme.Resolved{}
	if _, err := renderStaveSessionFrame(context.Background(), prepared, staveSessionOptions(Options{Width: 80}, false)); err == nil {
		t.Fatal("invalid theme renderer error was swallowed")
	}
	prepared.Session.Close()
	if _, err := renderStaveSessionFrame(context.Background(), prepared, staveSessionOptions(Options{Width: 80}, false)); err == nil {
		t.Fatal("closed session snapshot error was swallowed")
	}
}

func TestStavePreviewStartProcessesFinalHandledAndTextCommands(t *testing.T) {
	data := report.Report{SchemaVersion: report.SchemaVersion, Dependencies: []report.DependencyReport{{Language: "go", Name: "alpha", UsedPercent: 25, EstimatedUnusedBytes: 8}}}
	for _, input := range []string{"filter go", "unhandled text"} {
		var out strings.Builder
		summary := NewSummary(&out, strings.NewReader(input), &stubAnalyzer{report: data}, report.NewFormatter())
		preview := NewStavePreview(summary)
		opts := Options{UseStavePreview: true, Features: previewFeatures(t), Width: 80, PageSize: 10}
		if err := preview.Start(context.Background(), opts); err != nil {
			t.Fatalf("final command %q failed: %v", input, err)
		}
		if !strings.Contains(out.String(), "Stave preview") {
			t.Fatalf("final command %q did not render a final frame", input)
		}
	}
}

func TestStavePreviewStartRejectsNonJSONReportSnapshot(t *testing.T) {
	data := report.Report{
		SchemaVersion: report.SchemaVersion,
		Dependencies:  []report.DependencyReport{{Language: "go", Name: "nan", UsedPercent: math.NaN()}},
	}
	summary := NewSummary(io.Discard, strings.NewReader(""), &stubAnalyzer{report: data}, report.NewFormatter())
	err := NewStavePreview(summary).Start(context.Background(), Options{UseStavePreview: true, Features: previewFeatures(t), Width: 80})
	if err == nil || !strings.Contains(err.Error(), "not JSON-safe") {
		t.Fatalf("non-JSON report error = %v", err)
	}
}

func TestStavePreviewStartUsesLineTerminalDimensions(t *testing.T) {
	t.Setenv("TERM", "dumb")
	t.Setenv("NO_COLOR", "1")
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Skipf("open pty: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := tty.Close(); closeErr != nil && !errors.Is(closeErr, os.ErrClosed) {
			t.Errorf("close tty: %v", closeErr)
		}
		if closeErr := ptmx.Close(); closeErr != nil && !errors.Is(closeErr, os.ErrClosed) {
			t.Errorf("close pty: %v", closeErr)
		}
	})
	if err := pty.Setsize(ptmx, &pty.Winsize{Cols: 40, Rows: 12}); err != nil {
		t.Fatal(err)
	}
	data := report.Report{SchemaVersion: report.SchemaVersion, Dependencies: []report.DependencyReport{{Language: "go", Name: "alpha", UsedPercent: 25}}}
	summary := NewSummary(tty, strings.NewReader("q\n"), &stubAnalyzer{report: data}, report.NewFormatter())
	if err := NewStavePreview(summary).Start(context.Background(), Options{UseStavePreview: true, Features: previewFeatures(t), Width: 80}); err != nil {
		t.Fatal(err)
	}
	if width, height, ok := staveTerminalDimensions(tty); !ok || width != 40 || height != 12 {
		t.Fatalf("terminal dimensions = %dx%d ok=%v", width, height, ok)
	}
}

func TestStaveModelRareErrorAndDiagnosticBranches(t *testing.T) {
	invalidView := summaryReportView{Dependencies: []summaryDependencyView{{Name: "bad", UsedPercent: math.NaN()}}}
	if _, err := cloneStaveSummaryModel(staveSummaryModel{view: &invalidView}); err == nil {
		t.Fatal("clone accepted a non-JSON report")
	}
	if _, err := hashStaveSummaryModel(staveSummaryModel{view: &invalidView}); err == nil {
		t.Fatal("hash accepted a non-JSON report")
	}

	model := staveSummaryModel{interaction: staveSummaryInteraction{status: "old"}}
	model, _, err := reduceStaveSummary(stave.ReduceContext{}, model, event.Event{Kind: event.Diagnostic, Payload: event.DiagnosticPayload{Message: "input rejected"}})
	if err != nil || model.interaction.status != "" || model.interaction.error != "input rejected" {
		t.Fatalf("diagnostic reduction = %+v, err=%v", model.interaction, err)
	}

	badReport := validCoverageEnvelope(staveActionRefresh, map[string]any{"refreshed": true, "report": map[string]any{"warnings": 7}})
	model = reduceCoverageEffect(t, staveActionRefresh, badReport, "completed", "")
	if !strings.Contains(model.interaction.error, "report decode failed") {
		t.Fatalf("invalid report error = %q", model.interaction.error)
	}
	if got := validateStaveOutcome(staveActionApplyCodemod, map[string]any{"dependency": "go:x", "applied": false}); got != "codemod payload incomplete" {
		t.Fatalf("codemod validation = %q", got)
	}
	if got := staveActionStatus(staveActionOpen, nil); got != "Opened" {
		t.Fatalf("open fallback status = %q", got)
	}
	model = staveSummaryModel{interaction: staveSummaryInteraction{focusPane: "detail", summary: summaryState{selectedDependency: "go:x"}}}
	reduceStaveKey(&model, event.KeyPayload{Key: "tab"})
	if model.interaction.focusPane != "summary" {
		t.Fatalf("detail tab focus = %q", model.interaction.focusPane)
	}
}

func TestStaveTerminalAdditionalAsyncGuards(t *testing.T) {
	snapshotErr := errors.New("snapshot failed")
	b := &staveTerminal{prepared: struct{}{}, snapshot: func(context.Context, any) (staveTerminalSnapshot, error) {
		return staveTerminalSnapshot{}, snapshotErr
	}}
	if msg := b.beginCommand("bogus")(); !errors.Is(msg.(staveActionCompletion).err, snapshotErr) {
		t.Fatalf("command snapshot error = %#v", msg)
	}
	if msg := b.beginAction(action.ID(staveActionRefresh), map[string]any{}, false)(); !errors.Is(msg.(staveTextCompletion).err, snapshotErr) {
		t.Fatalf("action snapshot error = %#v", msg)
	}

	b = &staveTerminal{prepared: struct{}{}, snapshot: func(context.Context, any) (staveTerminalSnapshot, error) {
		return staveTerminalSnapshot{model: staveSummaryModel{}}, nil
	}}
	if msg := b.beginAction(action.ID(staveActionRefresh), map[string]any{}, false)(); msg.(staveTextCompletion).err == nil {
		t.Fatal("refresh accepted missing model options")
	}

	b = &staveTerminal{prepared: struct{}{}, snapshot: func(context.Context, any) (staveTerminalSnapshot, error) {
		return staveTerminalSnapshot{model: staveSummaryModel{opts: &Options{}}}, nil
	}, sendEvent: func(context.Context, any, event.Event) error { return errors.New("send failed") }}
	if msg := b.beginAction(action.ID(staveActionRefresh), map[string]any{}, false)(); msg.(staveActionCompletion).err == nil || b.inflight {
		t.Fatalf("action publication failure = %#v inflight=%v", msg, b.inflight)
	}

	var rejected event.Event
	b = &staveTerminal{prepared: struct{}{}, snapshot: func(context.Context, any) (staveTerminalSnapshot, error) {
		return staveTerminalSnapshot{model: staveSummaryModel{}}, nil
	}, sendEvent: func(_ context.Context, _ any, ev event.Event) error { rejected = ev; return nil }}
	if msg := b.beginCommand(string([]byte{0xff}))(); msg.(staveTextCompletion).err != nil {
		t.Fatalf("invalid command diagnostic failed: %v", msg)
	}
	payload, ok := rejected.Payload.(event.DiagnosticPayload)
	if rejected.Kind != event.Diagnostic || !ok || payload.Code != "LOPPER_INPUT_REJECTED" {
		t.Fatalf("invalid command event = %#v", rejected)
	}
	b.inflight = true
	if cmd := b.beginAction(action.ID(staveActionOpen), map[string]any{"dependency": "go:x"}, false); cmd != nil {
		t.Fatal("second action was scheduled while one was in flight")
	}
	b.inflight = false
	if msg := b.beginAction(action.ID(string([]byte{0xff})), map[string]any{}, false)(); msg.(staveActionCompletion).err == nil {
		t.Fatal("invalid action identity event was accepted")
	}
	b = &staveTerminal{prepared: struct{}{}, sendEvent: func(context.Context, any, event.Event) error { return nil }}
	m := &staveTerminalModel{bridge: b}
	if _, cmd := m.Update(staveActionCompletion{callID: string([]byte{0xff}), actionID: action.ID(staveActionRefresh)}); cmd == nil || !b.quit {
		t.Fatal("invalid completion event did not terminate the adapter")
	}

	b = &staveTerminal{prepared: struct{}{}, snapshot: func(context.Context, any) (staveTerminalSnapshot, error) {
		return staveTerminalSnapshot{model: staveSummaryModel{}}, nil
	}, sendEvent: func(context.Context, any, event.Event) error { return nil }}
	b.inflight = true
	m = &staveTerminalModel{bridge: b}
	if _, cmd := m.Update(tea.KeyPressMsg{Text: "x"}); cmd != nil || !b.inflight {
		t.Fatal("ordinary key escaped the in-flight guard")
	}
	b.inflight = false
	b.snapshot = func(context.Context, any) (staveTerminalSnapshot, error) {
		return staveTerminalSnapshot{model: staveSummaryModel{interaction: staveSummaryInteraction{commandMode: true}}}, nil
	}
	if _, cmd := m.Update(tea.KeyPressMsg{Text: "r"}); cmd != nil {
		t.Fatal("command-mode r incorrectly scheduled refresh")
	}

	b = &staveTerminal{}
	want := errors.New("not ready")
	b.reportError(want)
	if !errors.Is(b.err, want) {
		t.Fatalf("not-ready diagnostic error = %v", b.err)
	}

	b = &staveTerminal{prepared: struct{}{}, currentCallID: "call", sendEvent: func(context.Context, any, event.Event) error { return errors.New("cancel publish failed") }}
	b.cancelInflight()
	if b.err == nil || b.currentCallID != "" {
		t.Fatalf("cancel publication state = %+v", b)
	}
}

func TestStaveTerminalIgnoresCompletionAfterSignalCancellation(t *testing.T) {
	runCtx, cancel := context.WithCancel(context.Background())
	cancel()
	b := &staveTerminal{
		ctx:           runCtx,
		inflight:      true,
		currentCallID: "signal-call",
		actionCancel:  func() {},
	}
	m := &staveTerminalModel{bridge: b}
	updated, cmd := m.Update(staveActionCompletion{
		callID:   "signal-call",
		actionID: action.ID(staveActionRefresh),
		result:   staveActionResult{Outcome: &staveActionOutcome{Value: map[string]any{"ignored": true}}},
	})
	if updated != m || cmd != nil {
		t.Fatalf("canceled completion returned model=%T cmd=%v", updated, cmd)
	}
	if !b.inflight || b.currentCallID != "signal-call" || b.actionCancel == nil {
		t.Fatalf("signal completion stole cleanup ownership: %+v", b)
	}
}

func TestStaveSummaryActionWriterAndLegacySuppressionBranches(t *testing.T) {
	if err := reportSummaryActionFailure(&failingWriter{}, "failed", errors.New("cause")); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("failure writer error = %v", err)
	}

	apply := &report.CodemodApplyReport{AppliedFiles: 1, AppliedPatches: 1}
	runner := &stubSummaryActionRunner{applyReport: report.Report{Dependencies: []report.DependencyReport{{Language: "go", Name: "alpha", Codemod: &report.CodemodReport{Apply: apply}}}}}
	summary := NewSummary(&failingWriter{}, strings.NewReader(""), &stubAnalyzer{}, report.NewFormatter())
	summary.Actions = runner
	err := summary.runSummaryCodemodApply(context.Background(), &Options{Language: "go"}, &summaryReportView{}, summaryAction{dependency: "go:alpha", confirm: true})
	if !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("codemod report writer error = %v", err)
	}

	runner.applyReport = report.Report{}
	err = summary.runSummaryCodemodApply(context.Background(), &Options{Language: "go"}, &summaryReportView{}, summaryAction{dependency: "go:alpha", confirm: true})
	if !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("no-result writer error = %v", err)
	}

	var out strings.Builder
	runner.applyErr = errors.New("backend failed")
	summary = NewSummary(&out, strings.NewReader(""), &stubAnalyzer{}, report.NewFormatter())
	summary.Actions = runner
	handled, err := summary.handleSummaryActionInput(context.Background(), &Options{Language: "go"}, &summaryReportView{}, &summaryState{}, "apply-codemod go:alpha --confirm")
	if !handled || err != nil || !strings.Contains(out.String(), "Codemod apply failed") {
		t.Fatalf("legacy reported failure = handled=%v err=%v out=%q", handled, err, out.String())
	}

	if _, err := applySummaryBaselineIfNeeded(report.Report{}, Options{RepoPath: t.TempDir(), BaselineStorePath: t.TempDir()}); err == nil {
		t.Fatal("baseline store without resolvable key was accepted")
	}

	got := findCodemodApplyReport(report.Report{Dependencies: []report.DependencyReport{{Name: "skip"}, {Language: "go", Name: "alpha", Codemod: &report.CodemodReport{Apply: apply}}}}, "go:alpha")
	if got != apply {
		t.Fatal("codemod lookup did not skip dependencies without apply data")
	}
}
