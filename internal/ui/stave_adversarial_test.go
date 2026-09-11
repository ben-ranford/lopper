package ui

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/stave"
	"github.com/ben-ranford/stave/event"
)

func TestStaveColonUnknownCommandReportsErrorWithoutMutatingState(t *testing.T) {
	state := summaryState{page: 2, pageSize: 10, filter: "keep", sortMode: sortByWaste}
	model := newStaveSummaryModel(&summaryReportView{Dependencies: []summaryDependencyView{{Name: "keep"}}}, nil, state)
	var err error
	model, _, err = reduceStaveSummary(stave.ReduceContext{}, model, mustAdversarialEvent(t, event.Key, event.KeyPayload{Key: "rune", Rune: ':'}))
	if err != nil {
		t.Fatal(err)
	}
	model, _, err = reduceStaveSummary(stave.ReduceContext{}, model, textEvent(t, "definitely-not-a-command", true))
	if err != nil {
		t.Fatal(err)
	}

	if model.interaction.error == "" || !strings.Contains(model.interaction.error, "unknown command") {
		t.Fatalf("unknown colon command did not produce visible error: %q", model.interaction.error)
	}
	if model.interaction.summary != state {
		t.Fatalf("unknown command mutated summary state: got %#v want %#v", model.interaction.summary, state)
	}
	if model.interaction.commandMode {
		t.Fatal("committed unknown command left command mode active")
	}
}

func TestStaveOversizedCommandIsRejectedWithinBound(t *testing.T) {
	input := "unknown " + strings.Repeat("x", 2<<20)
	done := make(chan error, 1)
	go func() {
		_, err := event.New(event.Text, event.TextPayload{Text: input, Committed: true})
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("oversized command was accepted")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("oversized command validation was not bounded")
	}
}

func TestStaveSequentialSessionsStartWithFreshInteractionState(t *testing.T) {
	summary := NewSummary(io.Discard, strings.NewReader(""), &stubAnalyzer{}, report.NewFormatter())
	opts := summary.applyDefaults(Options{UseStavePreview: true, Features: previewFeatures(t), Width: 80})
	view := summaryReportView{Dependencies: []summaryDependencyView{{Language: "go", Name: "alpha"}}}
	for i := 0; i < 2; i++ {
		initial := buildSummaryState(opts)
		program, err := newLopperStaveProgram(summary, &opts, &view, &initial)
		if err != nil {
			t.Fatal(err)
		}
		prepared, err := program.NewSession(context.Background(), staveSessionOptions(opts, false))
		if err != nil {
			t.Fatal(err)
		}
		snap, err := prepared.Session.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		if snap.Model.interaction.quit || snap.Model.interaction.commandMode || snap.Model.interaction.filterBuffer != "" || snap.Model.interaction.pendingConfirm != "" {
			t.Fatalf("session %d inherited transient state: %+v", i, snap.Model.interaction)
		}
		prepared.Session.Close()
	}
}

func TestStaveEffectErrorClearsSuccessAndRetainsBackendMessage(t *testing.T) {
	model := newStaveSummaryModel(&summaryReportView{}, nil, summaryState{})
	model.interaction.status = "Applied successfully"
	var err error
	model, _, err = reduceStaveSummary(stave.ReduceContext{}, model, mustAdversarialEvent(t, event.ActionInvoked, event.ActionInvokedPayload{CallID: "backend", ActionID: staveActionApplyCodemod}))
	if err != nil {
		t.Fatal(err)
	}
	model, _, err = reduceStaveSummary(stave.ReduceContext{}, model, mustAdversarialEvent(t, event.EffectResult, event.EffectResultPayload{CallID: "backend", Status: "error", Error: "successfully started, but backend failed"}))
	if err != nil {
		t.Fatal(err)
	}
	if model.interaction.error != "successfully started, but backend failed" {
		t.Fatalf("backend error was not retained: %q", model.interaction.error)
	}
	if model.interaction.status != "Pending "+staveActionApplyCodemod {
		t.Fatalf("error unexpectedly rewrote pending status: %q", model.interaction.status)
	}
}

func TestStaveTerminalCompletionMatchesPendingCallAndDoesNotReportSuccess(t *testing.T) {
	model := newStaveSummaryModel(&summaryReportView{}, nil, summaryState{})
	var err error
	model, _, err = reduceStaveSummary(stave.ReduceContext{}, model, mustAdversarialEvent(t, event.ActionInvoked, event.ActionInvokedPayload{CallID: "c1", ActionID: staveActionApplyCodemod}))
	if err != nil {
		t.Fatal(err)
	}
	if model.interaction.status != "Pending "+staveActionApplyCodemod || model.interaction.pendingCallID != "c1" {
		t.Fatalf("pending action was not recorded: %+v", model.interaction)
	}
	model, _, err = reduceStaveSummary(stave.ReduceContext{}, model, mustAdversarialEvent(t, event.EffectResult, event.EffectResultPayload{CallID: "c1", Status: "error", Error: "backend failed"}))
	if err != nil {
		t.Fatal(err)
	}
	if model.interaction.error != "backend failed" || model.interaction.pendingCallID != "" || model.interaction.status == "completed" {
		t.Fatalf("completion produced an invalid terminal state: %+v", model.interaction)
	}
}

func TestStaveHostileTextIsTerminalSafeAndValidUTF8(t *testing.T) {
	raw := "café\x00\x1b[31mred\x1b]0;title\a"
	safe := terminalSafeText(raw)
	for _, control := range []byte{0, 0x1b, 0x9b, 0x9d} {
		if strings.ContainsRune(safe, rune(control)) {
			t.Fatalf("hostile text retained terminal control 0x%x: %q", control, safe)
		}
	}
	if !utf8.ValidString(safe) || !strings.Contains(safe, "café") || !strings.Contains(safe, "red") {
		t.Fatalf("hostile text was not safely rendered: %q", safe)
	}
	// Invalid UTF-8 must not panic or reintroduce terminal controls.
	invalid := terminalSafeText(string([]byte{0xff, 0xfe}))
	for _, control := range []byte{0, 0x1b, 0x07, 0x9b, 0x9d} {
		if bytes.Contains([]byte(invalid), []byte{control}) {
			t.Fatalf("invalid UTF-8 introduced terminal control 0x%x: %q", control, invalid)
		}
	}
}

type cancellingActionRunner struct{}

func (*cancellingActionRunner) ApplyCodemod(ctx context.Context, _ CodemodApplyRequest) (report.Report, error) {
	<-ctx.Done()
	return report.Report{}, ctx.Err()
}
func (*cancellingActionRunner) SaveBaseline(context.Context, BaselineSaveRequest) (report.Report, string, error) {
	return report.Report{}, "", errors.New("unused")
}

func TestStaveHungActionCancelsWithContext(t *testing.T) {
	data := report.Report{Dependencies: []report.DependencyReport{{Language: "js-ts", Name: "lodash", Codemod: &report.CodemodReport{}}}}
	summary := NewSummary(io.Discard, strings.NewReader(""), &stubAnalyzer{}, report.NewFormatter())
	summary.Actions = &cancellingActionRunner{}
	opts := summary.applyDefaults(Options{UseStavePreview: true, Features: previewFeatures(t), Width: 80})
	view := mapSummaryReportView(data)
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
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err = invokeLopperAction(ctx, prepared, staveActionApplyCodemod, map[string]any{"dependency": "lodash", "confirm": true, "allowDirty": false}, "adversarial", true)
	if err != nil && !errors.Is(err, context.DeadlineExceeded) && !strings.Contains(err.Error(), "deadline") {
		t.Fatalf("action bridge returned unexpected error: %v", err)
	}
	if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("action context was not canceled: %v", ctx.Err())
	}
}

func textEvent(t *testing.T, text string, committed bool) event.Event {
	t.Helper()
	return mustAdversarialEvent(t, event.Text, event.TextPayload{Text: text, Committed: committed})
}

func mustAdversarialEvent(t *testing.T, kind event.Kind, payload any) event.Event {
	t.Helper()
	ev, err := event.New(kind, payload)
	if err != nil {
		t.Fatal(err)
	}
	return ev
}
