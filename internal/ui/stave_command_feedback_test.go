package ui

import (
	"testing"

	"github.com/ben-ranford/stave"
	"github.com/ben-ranford/stave/event"
)

func TestStaveCommandEntryReplacesStaleErrorWithVisibleInput(t *testing.T) {
	for _, tc := range []struct {
		key    string
		prompt string
	}{{":", ""}, {"/", "filter "}} {
		t.Run(tc.key, func(t *testing.T) {
			model := newStaveSummaryModel(&summaryReportView{}, nil, summaryState{page: 1, pageSize: 10})
			model = reduceStaveSelectionText(t, model, "unknown command")
			if model.interaction.error == "" {
				t.Fatal("invalid command did not produce an error")
			}
			model.interaction.status = "prior update"
			model = reduceStaveCommandFeedbackRune(t, model, []rune(tc.key)[0])
			assertStaveCommandFeedback(t, model, tc.prompt)
			model = reduceStaveCommandFeedbackRune(t, model, 'a')
			assertStaveCommandFeedback(t, model, tc.prompt+"a")
		})
	}
}

func assertStaveCommandFeedback(t *testing.T, model staveSummaryModel, prompt string) {
	t.Helper()
	if !model.interaction.commandMode || model.interaction.error != "" || model.interaction.status != "prior update" {
		t.Fatalf("command editor state = %+v", model.interaction)
	}
	node, err := staveFeedbackNode(model.interaction, 80, false)
	if err != nil {
		t.Fatal(err)
	}
	if node.Name() != "Command" || node.Description() != prompt {
		t.Fatalf("visible feedback = %q / %q, want Command / %q", node.Name(), node.Description(), prompt)
	}
}

func reduceStaveCommandFeedbackRune(t *testing.T, model staveSummaryModel, key rune) staveSummaryModel {
	t.Helper()
	next, _, err := reduceStaveSummary(stave.ReduceContext{}, model, mustStaveSelectionEvent(t, event.Key, event.KeyPayload{Key: "rune", Rune: key}))
	if err != nil {
		t.Fatal(err)
	}
	return next
}
