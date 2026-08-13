package ui

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/ben-ranford/stave/action"
	"github.com/ben-ranford/stave/event"
)

func TestStaveTerminalSafeTextRemovesTerminalControls(t *testing.T) {
	got := terminalSafeText("failure\x1b]0;secret\a\x1b[31mred\x1b[0m")
	for _, control := range []byte{0x1b, 0x07, 0x9b, 0x9d} {
		if bytes.Contains([]byte(got), []byte{control}) {
			t.Fatalf("safe terminal text contains control byte 0x%x: %q", control, got)
		}
	}
	if !strings.Contains(got, "failure") || !strings.Contains(got, "red") {
		t.Fatalf("safe terminal text lost diagnostic content: %q", got)
	}
}

func TestStaveTerminalKeyTextRejectsControlAndEscapeInput(t *testing.T) {
	valid, _, ok := staveKeyText(tea.KeyPressMsg{Text: "f", Code: 'f'})
	if !ok || valid != "f" {
		t.Fatalf("expected printable key, got %q (ok=%v)", valid, ok)
	}
	for _, text := range []string{"\x1b", "\x00", "\x7f"} {
		_, _, ok := staveKeyText(tea.KeyPressMsg{Text: text})
		if ok {
			t.Fatalf("control input %q was accepted", text)
		}
	}
}

func TestStaveTerminalModelQuitSetsQuitAndReturnsTeaQuit(t *testing.T) {
	bridge := &staveTerminal{}
	model := &staveTerminalModel{bridge: bridge}
	_, cmd := model.Update(tea.QuitMsg{})
	if !bridge.quit {
		t.Fatal("quit message did not mark terminal model as quitting")
	}
	if cmd == nil {
		t.Fatal("quit message did not return a tea quit command")
	}
}

func TestStaveTerminalResizeSendsCanonicalEvent(t *testing.T) {
	var got string
	bridge := &staveTerminal{
		ctx:      context.Background(),
		prepared: struct{}{},
		sendEvent: func(_ context.Context, _ any, ev event.Event) error {
			got = string(ev.Kind)
			return nil
		},
	}
	if err := bridge.resize(40, 12); err != nil {
		t.Fatalf("resize: %v", err)
	}
	if got != string(event.Resize) {
		t.Fatalf("resize emitted %q, want %q", got, event.Resize)
	}
}

func TestStaveTerminalSessionSnapshotUsesInjectedReader(t *testing.T) {
	want := staveTerminalSnapshot{model: staveSummaryModel{interaction: staveSummaryInteraction{status: "ok"}}}
	bridge := &staveTerminal{ctx: context.Background(), snapshot: func(context.Context, any) (staveTerminalSnapshot, error) { return want, nil }}
	got, err := bridge.sessionSnapshot()
	if err != nil {
		t.Fatalf("session snapshot: %v", err)
	}
	if got.model.interaction.status != "ok" {
		t.Fatalf("snapshot status = %q, want ok", got.model.interaction.status)
	}
}

func TestStaveTerminalDispatchRejectsMissingSession(t *testing.T) {
	bridge := &staveTerminal{}
	if cmd := bridge.beginCommand("refresh"); cmd == nil {
		t.Fatal("command without session returned nil")
	}
}

func TestStaveTerminalFailureIsRetainedAndQuits(t *testing.T) {
	bridge := &staveTerminal{}
	want := errors.New("bad terminal")
	bridge.fail(want)
	bridge.fail(errors.New("later"))
	if !errors.Is(bridge.err, want) || !bridge.quit {
		t.Fatalf("failure state = err %v quit %v", bridge.err, bridge.quit)
	}
}

func TestStaveTerminalKeyAndTextForwardEvents(t *testing.T) {
	var kinds []event.Kind
	bridge := &staveTerminal{ctx: context.Background(), prepared: struct{}{}, sendEvent: func(_ context.Context, _ any, ev event.Event) error { kinds = append(kinds, ev.Kind); return nil }}
	if err := bridge.key(tea.KeyPressMsg{Text: "x", Code: 'x'}); err != nil {
		t.Fatalf("key: %v", err)
	}
	if err := bridge.text("filter charm"); err != nil {
		t.Fatalf("text: %v", err)
	}
	if len(kinds) != 2 || kinds[0] != event.Key || kinds[1] != event.Text {
		t.Fatalf("forwarded event kinds = %v", kinds)
	}
}

func TestStaveTerminalDispatchActionInvokesAndPublishes(t *testing.T) {
	var published event.Kind
	opts := &Options{}
	bridge := &staveTerminal{ctx: context.Background(), prepared: struct{}{}, snapshot: func(context.Context, any) (staveTerminalSnapshot, error) {
		return staveTerminalSnapshot{model: staveSummaryModel{opts: opts}}, nil
	}, sendEvent: func(_ context.Context, _ any, ev event.Event) error { published = ev.Kind; return nil }}
	cmd := bridge.beginAction(action.ID("refresh"), map[string]any{}, false)
	if cmd == nil {
		t.Fatal("begin action returned nil")
	}
	if published != event.ActionInvoked {
		t.Fatalf("action invocation published=%q", published)
	}
}

func TestSelectedDependencyForRowUsesFilteredPipeline(t *testing.T) {
	view := &summaryReportView{Dependencies: []summaryDependencyView{{Language: "go", Name: "alpha"}}}
	model := staveSummaryModel{view: view, interaction: staveSummaryInteraction{summary: summaryState{page: 1, pageSize: 10}, selectedRow: 0}}
	if got := selectedDependencyForRow(model); got != "go:alpha" {
		t.Fatalf("selected dependency = %q", got)
	}
}
