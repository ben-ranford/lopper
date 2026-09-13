package ui

import (
	"context"
	"testing"

	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/stave"
	"github.com/ben-ranford/stave/event"
)

func TestStaveEditorRemainsVisibleAfterAsynchronousActionFailure(t *testing.T) {
	for _, entry := range []rune{':', '/'} {
		t.Run(string(entry), func(t *testing.T) { checkStaveAsyncFailureFeedback(t, entry) })
	}
}

func checkStaveAsyncFailureFeedback(t *testing.T, entry rune) {
	t.Helper()
	_, _, prepared, _, _ := newPreparedStaveSession(t, report.Report{})
	defer prepared.Session.Close()
	sendAsyncFeedbackEvent(t, prepared, event.ActionInvoked, event.ActionInvokedPayload{CallID: "running", ActionID: staveActionRefresh})
	sendAsyncFeedbackEvent(t, prepared, event.Key, event.KeyPayload{Key: "rune", Rune: entry})
	sendAsyncFeedbackEvent(t, prepared, event.Key, event.KeyPayload{Key: "rune", Rune: 'a'})
	sendAsyncFeedbackEvent(t, prepared, event.EffectResult, event.EffectResultPayload{CallID: "running", Status: "error", Error: "backend failed"})
	prompt := "a"
	if entry == '/' {
		prompt = "filter a"
	}
	assertAsyncFailureFeedback(t, prepared, "Command", prompt)
	sendAsyncFeedbackEvent(t, prepared, event.Key, event.KeyPayload{Key: "rune", Rune: 'b'})
	assertAsyncFailureFeedback(t, prepared, "Command", prompt+"b")
	sendAsyncFeedbackEvent(t, prepared, event.Key, event.KeyPayload{Key: "escape"})
	assertAsyncFailureFeedback(t, prepared, "Error", "backend failed")
}

func sendAsyncFeedbackEvent(t *testing.T, prepared *stave.Prepared[staveSummaryModel], kind event.Kind, payload any) {
	t.Helper()
	if err := sendLopperEvent(context.Background(), prepared, mustStaveActionEvent(t, kind, payload)); err != nil {
		t.Fatal(err)
	}
}

func assertAsyncFailureFeedback(t *testing.T, prepared *stave.Prepared[staveSummaryModel], name, text string) {
	t.Helper()
	snapshot, err := prepared.Session.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	interaction := snapshot.Model.interaction
	if interaction.error != "backend failed" || interaction.pendingCallID != "" || interaction.commandMode != (name == "Command") {
		t.Fatalf("action error or editor state was lost: %+v", interaction)
	}
	node, err := staveFeedbackNode(interaction, 80, false)
	if err != nil || node.Name() != name || node.Description() != text {
		t.Fatalf("feedback = %v, error=%v; want %s: %s", node, err, name, text)
	}
}
