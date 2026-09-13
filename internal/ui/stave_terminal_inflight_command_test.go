package ui

import (
	"context"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/stave"
	"github.com/ben-ranford/stave/action"
	"github.com/ben-ranford/stave/event"
)

func TestStaveTerminalInflightCommandEnterRetainsEditorUntilCompletion(t *testing.T) {
	for _, key := range []tea.KeyPressMsg{
		{Code: tea.KeyEnter},
		{Code: tea.KeyKpEnter},
	} {
		t.Run(key.String(), func(t *testing.T) { checkInflightCommandEnter(t, key) })
	}
}

func checkInflightCommandEnter(t *testing.T, key tea.KeyPressMsg) {
	t.Helper()
	state := staveSummaryModel{opts: &Options{}, interaction: staveSummaryInteraction{
		commandMode:     true,
		filterBuffer:    "refresh",
		pendingCallID:   "running",
		pendingActionID: staveActionRefresh,
	}}
	var events []event.Event
	bridge := &staveTerminal{prepared: struct{}{}, inflight: true, currentCallID: "running"}
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
	if cmd != nil || len(events) != 0 || !bridge.inflight || bridge.callCounter != 0 || !state.interaction.commandMode || state.interaction.filterBuffer != "refresh" {
		t.Fatalf("inflight enter changed editor or dispatched command: cmd=%v events=%v state=%+v bridge=%+v", cmd != nil, events, state.interaction, bridge)
	}

	completion := staveActionCompletion{
		callID:   "running",
		actionID: action.ID(staveActionRefresh),
		result: staveActionResult{Outcome: &staveActionOutcome{Value: validCoverageEnvelope(
			staveActionRefresh,
			map[string]any{"refreshed": true, "report": report.Report{}},
		)}},
	}
	_, cmd = model.Update(completion)
	if cmd != nil || bridge.inflight || !state.interaction.commandMode || state.interaction.filterBuffer != "refresh" {
		t.Fatalf("completion did not retain editor while ending action: cmd=%v state=%+v bridge=%+v", cmd != nil, state.interaction, bridge)
	}
	feedback, err := staveFeedbackNode(state.interaction, 80, false)
	if err != nil || feedback.Name() != "Command" || feedback.Description() != "refresh" {
		t.Fatalf("completed action lost command feedback: node=%v error=%v", feedback, err)
	}

	_, cmd = model.Update(key)
	if cmd == nil || !bridge.inflight || bridge.callCounter != 1 || state.interaction.commandMode {
		t.Fatalf("idle enter did not dispatch retained command once: cmd=%v state=%+v bridge=%+v", cmd != nil, state.interaction, bridge)
	}
	t.Cleanup(bridge.actionCancel)
	assertSingleRetainedCommandInvocation(t, events)
}

func assertSingleRetainedCommandInvocation(t *testing.T, events []event.Event) {
	t.Helper()
	invocations := 0
	for _, ev := range events {
		if ev.Kind == event.ActionInvoked {
			invocations++
		}
	}
	if invocations != 1 {
		t.Fatalf("retained command invocations = %d, events=%+v", invocations, events)
	}
}
