package ui

import (
	"context"
	"errors"
	"testing"

	"github.com/ben-ranford/stave/event"
)

func TestStaveCoverageTailCountingContextContract(t *testing.T) {
	ctx := &countingContext{cancelAfter: 2}
	if deadline, ok := ctx.Deadline(); ok || !deadline.IsZero() {
		t.Fatalf("counting context reported a deadline: %v, %v", deadline, ok)
	}
	if ctx.Done() != nil {
		t.Fatal("counting context exposed a done channel")
	}
	if ctx.Value("missing") != nil {
		t.Fatal("counting context returned a value for an unknown key")
	}
	if err := ctx.Err(); err != nil {
		t.Fatalf("first Err() unexpectedly canceled: %v", err)
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatal("second Err() did not report cancellation")
	}
	if ctx.calls != 2 {
		t.Fatalf("Err() call count = %d, want 2", ctx.calls)
	}
}

func TestStaveCoverageTailParityParsingRejectsInvalidValues(t *testing.T) {
	if got := parseParityInt("not-an-int"); got != 0 {
		t.Fatalf("invalid int parsed as %d", got)
	}
	if got := parseParityInt64("not-an-int64"); got != 0 {
		t.Fatalf("invalid int64 parsed as %d", got)
	}
	if got := parseParityFloat("not-a-float"); got != 0 {
		t.Fatalf("invalid float parsed as %f", got)
	}
}

func TestStaveCoverageTailShutdownPublishesSendErrorAndContext(t *testing.T) {
	sendErr := errors.New("shutdown send failed")
	terminal := &staveTerminal{
		ctx:      context.Background(),
		prepared: struct{}{},
		sendEvent: func(_ context.Context, _ any, ev event.Event) error {
			if ev.Kind != event.Shutdown {
				t.Fatalf("shutdown sent unexpected event %q", ev.Kind)
			}
			return sendErr
		},
	}
	terminal.shutdown()
	if !terminal.shutdownSent {
		t.Fatal("shutdown did not mark the event as sent")
	}
	if !errors.Is(terminal.err, sendErr) {
		t.Fatalf("shutdown error = %v, want %v", terminal.err, sendErr)
	}
	terminal.shutdown()
	if !errors.Is(terminal.err, sendErr) {
		t.Fatalf("repeated shutdown changed error: %v", terminal.err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	seenContext := false
	terminal = &staveTerminal{
		ctx:      canceled,
		prepared: struct{}{},
		sendEvent: func(ctx context.Context, _ any, ev event.Event) error {
			if ev.Kind != event.Shutdown {
				t.Fatalf("canceled shutdown sent unexpected event %q", ev.Kind)
			}
			seenContext = errors.Is(ctx.Err(), context.Canceled)
			return ctx.Err()
		},
	}
	terminal.shutdown()
	if !seenContext || !errors.Is(terminal.err, context.Canceled) {
		t.Fatalf("canceled shutdown context/error not preserved: seen=%v err=%v", seenContext, terminal.err)
	}
}
