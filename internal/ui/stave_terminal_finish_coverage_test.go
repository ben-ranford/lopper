package ui

import (
	"context"
	"errors"
	"fmt"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/ben-ranford/stave/event"
)

func TestFinishStaveTerminalRunSignalCleanupAndResultPriority(t *testing.T) {
	t.Run("signal cancels action and publishes correlated outcome then shutdown", checkSignalCancelsActionAndPublishesCorrelatedOutcomeThenShutdown)

	t.Run("cleanup send failure wins", checkCleanupSendFailureWins)

	t.Run("shutdown send failure is retained after cancellation event", checkShutdownSendFailureIsRetainedAfterCancellationEvent)

	t.Run("terminal dispatch cancellation is normalized before cleanup", checkTerminalDispatchCancellationIsNormalizedBeforeCleanup)

	t.Run("cancellation during terminal dispatch is normalized before cleanup", checkCancellationDuringTerminalDispatchIsNormalizedBeforeCleanup)

	t.Run("cleanup failure and parent cause are retained", checkCleanupFailureAndParentCauseAreRetained)

	t.Run("wrapped and joined cancellation errors are retained", checkWrappedAndJoinedCancellationErrorsAreRetained)

	t.Run("bridge error retains parent cancellation", checkBridgeErrorRetainsParentCancellation)

	t.Run("parent cause and program errors are preserved", checkParentCauseAndProgramErrorsArePreserved)
}

func checkSignalCancelsActionAndPublishesCorrelatedOutcomeThenShutdown(t *testing.T) {
	t.Helper()
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
}

func checkCleanupSendFailureWins(t *testing.T) {
	t.Helper()
	runCtx, cancelRun := context.WithCancel(context.Background())
	cancelRun()
	want := errors.New("cleanup send failed")
	bridge := &staveTerminal{currentCallID: "call-error"}
	got := finishStaveTerminalRun(context.Background(), runCtx, bridge, struct{}{}, func(context.Context, any, event.Event) error { return want }, nil)
	if !errors.Is(got, want) || !errors.Is(bridge.err, want) {
		t.Fatalf("cleanup failure = %v bridge=%v", got, bridge.err)
	}
}

func checkShutdownSendFailureIsRetainedAfterCancellationEvent(t *testing.T) {
	t.Helper()
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
}

func checkTerminalDispatchCancellationIsNormalizedBeforeCleanup(t *testing.T) {
	t.Helper()
	for _, message := range []tea.Msg{tea.WindowSizeMsg{Width: 100, Height: 30}, tea.QuitMsg{}} {
		t.Run(fmt.Sprintf("%T", message), func(t *testing.T) {
			runCtx, cancelRun := context.WithCancel(context.Background())
			cancelRun()
			var events []event.Event
			sendEvent := terminalCancellationDispatchSender(runCtx, &events)
			bridge := &staveTerminal{ctx: runCtx, prepared: struct{}{}, sendEvent: sendEvent}
			if _, command := (&staveTerminalModel{bridge: bridge}).Update(message); command == nil || !errors.Is(bridge.err, context.Canceled) {
				t.Fatalf("post-cancellation %T bridge state = %+v", message, bridge)
			}
			if err := finishStaveTerminalRun(context.Background(), runCtx, bridge, struct{}{}, sendEvent, nil); err != nil {
				t.Fatalf("post-cancellation %T result = %v", message, err)
			}
			assertTerminalCancellationCleanup(t, events)
		})
	}
}

func checkCancellationDuringTerminalDispatchIsNormalizedBeforeCleanup(t *testing.T) {
	t.Helper()
	runCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()
	var events []event.Event
	sendEvent := func(ctx context.Context, _ any, ev event.Event) error {
		if ev.Kind == event.Resize {
			cancelRun()
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		events = append(events, ev)
		return nil
	}
	bridge := &staveTerminal{ctx: runCtx, prepared: struct{}{}, sendEvent: sendEvent}
	if _, command := (&staveTerminalModel{bridge: bridge}).Update(tea.WindowSizeMsg{Width: 100, Height: 30}); command == nil || !errors.Is(bridge.err, context.Canceled) {
		t.Fatalf("cancellation-during-dispatch bridge state = %+v", bridge)
	}
	if err := finishStaveTerminalRun(context.Background(), runCtx, bridge, struct{}{}, sendEvent, nil); err != nil {
		t.Fatalf("cancellation-during-dispatch result = %v", err)
	}
	assertTerminalCancellationCleanup(t, events)
}

func terminalCancellationDispatchSender(runCtx context.Context, events *[]event.Event) func(context.Context, any, event.Event) error {
	return func(ctx context.Context, _ any, ev event.Event) error {
		if ctx == runCtx {
			return ctx.Err()
		}
		*events = append(*events, ev)
		return nil
	}
}

func assertTerminalCancellationCleanup(t *testing.T, events []event.Event) {
	t.Helper()
	if len(events) != 1 || events[0].Kind != event.Shutdown {
		t.Fatalf("cancellation cleanup events = %#v", events)
	}
}

func checkCleanupFailureAndParentCauseAreRetained(t *testing.T) {
	t.Helper()
	parent, cancelParent := context.WithCancelCause(context.Background())
	parentCause := errors.New("parent cancelled")
	cancelParent(parentCause)
	cleanupErr := errors.New("cleanup failed")
	bridge := &staveTerminal{err: context.Canceled, currentCallID: "call-cleanup"}
	got := finishStaveTerminalRun(parent, parent, bridge, struct{}{}, func(context.Context, any, event.Event) error { return cleanupErr }, nil)
	if !errors.Is(got, parentCause) || !errors.Is(got, cleanupErr) {
		t.Fatalf("cleanup and parent cancellation = %v", got)
	}
}

func checkWrappedAndJoinedCancellationErrorsAreRetained(t *testing.T) {
	t.Helper()
	realErr := errors.New("real failure")
	for _, bridgeErr := range []error{
		fmt.Errorf("wrapped cancellation: %w", context.Canceled),
		errors.Join(context.Canceled, realErr),
	} {
		runCtx, cancelRun := context.WithCancel(context.Background())
		cancelRun()
		bridge := &staveTerminal{err: bridgeErr}
		got := finishStaveTerminalRun(context.Background(), runCtx, bridge, struct{}{}, func(context.Context, any, event.Event) error { return nil }, nil)
		if !errors.Is(got, bridgeErr) || got.Error() != bridgeErr.Error() || !errors.Is(got, context.Canceled) {
			t.Fatalf("wrapped or joined cancellation = %v, want %v", got, bridgeErr)
		}
		if errors.Is(bridgeErr, realErr) && !errors.Is(got, realErr) {
			t.Fatalf("joined real failure was lost: %v", got)
		}
	}
}

func checkBridgeErrorRetainsParentCancellation(t *testing.T) {
	t.Helper()
	parent, cancelParent := context.WithCancelCause(context.Background())
	parentCause := errors.New("parent cancelled")
	cancelParent(parentCause)
	bridgeErr := errors.New("bridge failed")
	got := finishStaveTerminalRun(parent, parent, &staveTerminal{err: bridgeErr}, struct{}{}, func(context.Context, any, event.Event) error { return nil }, nil)
	if !errors.Is(got, bridgeErr) || !errors.Is(got, parentCause) {
		t.Fatalf("bridge and parent cancellation = %v", got)
	}
}

func checkParentCauseAndProgramErrorsArePreserved(t *testing.T) {
	t.Helper()
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
}
