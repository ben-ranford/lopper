package ui

import (
	"context"
	"errors"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/ben-ranford/stave/event"
)

func TestFinishStaveTerminalRunSignalCleanupAndResultPriority(t *testing.T) {
	t.Run("signal cancels action and publishes correlated outcome then shutdown", func(t *testing.T) {
		runCtx, cancelRun := context.WithCancel(context.Background())
		cancelRun()
		cancelledAction := false
		var events []event.Event
		bridge := &staveTerminal{currentCallID: "call-9", actionCancel: func() { cancelledAction = true }}
		sendEvent := func(_ context.Context, _ any, ev event.Event) error {
			events = append(events, ev)
			return nil
		}
		err := finishStaveTerminalRun(context.Background(), runCtx, bridge, struct{}{}, sendEvent, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !cancelledAction || bridge.actionCancel != nil {
			t.Fatalf("action cancellation was not consumed: %+v", bridge)
		}
		if len(events) != 2 || events[0].Kind != event.EffectResult || events[1].Kind != event.Shutdown {
			t.Fatalf("signal cleanup events = %+v", events)
		}
		payload, ok := events[0].Payload.(event.EffectResultPayload)
		if !ok || payload.CallID != "call-9" || payload.Status != "cancelled" {
			t.Fatalf("signal cancellation payload = %#v", events[0].Payload)
		}
	})

	t.Run("cleanup send failure wins", func(t *testing.T) {
		runCtx, cancelRun := context.WithCancel(context.Background())
		cancelRun()
		want := errors.New("cleanup send failed")
		bridge := &staveTerminal{currentCallID: "call-error"}
		got := finishStaveTerminalRun(context.Background(), runCtx, bridge, struct{}{}, func(context.Context, any, event.Event) error { return want }, nil)
		if !errors.Is(got, want) || !errors.Is(bridge.err, want) {
			t.Fatalf("cleanup failure = %v bridge=%v", got, bridge.err)
		}
	})

	t.Run("shutdown send failure is retained after cancellation event", func(t *testing.T) {
		runCtx, cancelRun := context.WithCancel(context.Background())
		cancelRun()
		want := errors.New("shutdown send failed")
		calls := 0
		bridge := &staveTerminal{currentCallID: "call-shutdown"}
		sendEvent := func(context.Context, any, event.Event) error {
			calls++
			if calls == 2 {
				return want
			}
			return nil
		}
		got := finishStaveTerminalRun(context.Background(), runCtx, bridge, struct{}{}, sendEvent, nil)
		if !errors.Is(got, want) || calls != 2 {
			t.Fatalf("shutdown failure = %v calls=%d", got, calls)
		}
	})

	t.Run("bridge error precedes parent cancellation", func(t *testing.T) {
		parent, cancelParent := context.WithCancelCause(context.Background())
		parentCause := errors.New("parent cancelled")
		cancelParent(parentCause)
		bridgeErr := errors.New("bridge failed")
		got := finishStaveTerminalRun(parent, parent, &staveTerminal{err: bridgeErr}, struct{}{}, func(context.Context, any, event.Event) error { return nil }, nil)
		if !errors.Is(got, bridgeErr) {
			t.Fatalf("bridge failure priority = %v", got)
		}
	})

	t.Run("parent cause and program errors are preserved", func(t *testing.T) {
		parent, cancelParent := context.WithCancelCause(context.Background())
		parentCause := errors.New("parent cancelled")
		cancelParent(parentCause)
		if got := finishStaveTerminalRun(parent, parent, &staveTerminal{}, struct{}{}, func(context.Context, any, event.Event) error { return nil }, nil); !errors.Is(got, parentCause) {
			t.Fatalf("parent cause = %v", got)
		}
		programErr := errors.New("program failed")
		if got := finishStaveTerminalRun(context.Background(), context.Background(), &staveTerminal{}, struct{}{}, func(context.Context, any, event.Event) error { return nil }, programErr); !errors.Is(got, programErr) {
			t.Fatalf("program error = %v", got)
		}
		if got := finishStaveTerminalRun(context.Background(), context.Background(), &staveTerminal{}, struct{}{}, func(context.Context, any, event.Event) error { return nil }, tea.ErrProgramKilled); got != nil {
			t.Fatalf("program-killed error = %v", got)
		}
	})
}
