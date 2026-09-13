package ui

import (
	"context"
	"errors"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/ben-ranford/stave"
	"github.com/ben-ranford/stave/event"
)

func TestStaveTerminalInflightQuitKeys(t *testing.T) {
	for _, tc := range []struct {
		name                           string
		key                            tea.KeyPressMsg
		editing, wantQuit, wantEditing bool
	}{
		{name: "q", key: tea.KeyPressMsg{Code: 'q', Text: "q"}, wantQuit: true},
		{name: "escape", key: tea.KeyPressMsg{Code: tea.KeyEscape}, wantQuit: true},
		{name: "ctrl c", key: tea.KeyPressMsg{Code: 'c', Text: "c", Mod: tea.ModCtrl}, wantQuit: true},
		{name: "ctrl d", key: tea.KeyPressMsg{Code: 'd', Text: "d", Mod: tea.ModCtrl}, wantQuit: true},
		{name: "escape ends editing", key: tea.KeyPressMsg{Code: tea.KeyEscape}, editing: true},
		{name: "q continues editing", key: tea.KeyPressMsg{Code: 'q', Text: "q"}, editing: true, wantEditing: true},
	} {
		t.Run(tc.name, func(t *testing.T) { checkStaveInflightQuitKey(t, tc.key, tc.editing, tc.wantQuit, tc.wantEditing) })
	}
}

func checkStaveInflightQuitKey(t *testing.T, key tea.KeyPressMsg, editing, wantQuit, wantEditing bool) {
	t.Helper()
	actionCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	state := staveSummaryModel{interaction: staveSummaryInteraction{commandMode: editing, filterBuffer: "filter", pendingCallID: "running", pendingActionID: staveActionRefresh}}
	var events []event.Event
	bridge := &staveTerminal{prepared: struct{}{}, inflight: true, currentCallID: "running", actionCancel: cancel}
	bridge.snapshot = func(context.Context, any) (staveTerminalSnapshot, error) {
		return staveTerminalSnapshot{model: state}, nil
	}
	bridge.sendEvent = func(_ context.Context, _ any, ev event.Event) error {
		events = append(events, ev)
		var err error
		state, _, err = reduceStaveSummary(stave.ReduceContext{}, state, ev)
		return err
	}
	model := &staveTerminalModel{bridge: bridge}
	_, cmd := model.Update(key)
	if wantQuit {
		if cmd == nil || !bridge.quit || bridge.inflight || !errors.Is(actionCtx.Err(), context.Canceled) {
			t.Fatalf("inflight key failed to cancel and quit: cmd=%v quit=%v inflight=%v error=%v", cmd != nil, bridge.quit, bridge.inflight, actionCtx.Err())
		}
		checkStaveQuitCancellationEvents(t, events)
		return
	}
	if cmd != nil || bridge.quit || !bridge.inflight || actionCtx.Err() != nil || state.interaction.quit || state.interaction.commandMode != wantEditing {
		t.Fatalf("editing key interrupted action or retained wrong mode: %+v", state.interaction)
	}
}

func checkStaveQuitCancellationEvents(t *testing.T, events []event.Event) {
	t.Helper()
	if len(events) != 2 || events[0].Kind != event.EffectResult || events[1].Kind != event.Shutdown {
		t.Fatalf("quit cleanup event order: %+v", events)
	}
	payload, ok := events[0].Payload.(event.EffectResultPayload)
	if !ok || payload.CallID != "running" || payload.Status != "cancelled" || payload.Error == "" {
		t.Fatalf("quit lost correlated uncertain outcome: %+v", payload)
	}
}
