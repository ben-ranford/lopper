package ui

import (
	"testing"

	"github.com/ben-ranford/stave"
	"github.com/ben-ranford/stave/event"
)

func TestStaveSelectionTracksVisibleSliceChanges(t *testing.T) {
	view := &summaryReportView{Dependencies: []summaryDependencyView{
		{Language: "go", Name: "first"},
		{Language: "go", Name: "second"},
		{Language: "go", Name: "third"},
	}}
	model := newStaveSummaryModel(view, nil, summaryState{page: 1, pageSize: 2, sortMode: sortByName})
	model.interaction.selectedRow = 1

	model = reduceStaveSelectionKey(t, model, "right")
	if model.interaction.selectedRow != 0 {
		t.Fatalf("last-page selected row = %d, want 0", model.interaction.selectedRow)
	}
	if got := selectedDependencyForRow(model); got != "go:third" {
		t.Fatalf("last-page selected dependency = %q, want go:third", got)
	}

	model.interaction.selectedRow = 2
	model = reduceStaveSelectionText(t, model, "filter second")
	if model.interaction.selectedRow != 0 {
		t.Fatalf("filtered selected row = %d, want 0", model.interaction.selectedRow)
	}
	if got := selectedDependencyForRow(model); got != "go:second" {
		t.Fatalf("filtered selected dependency = %q, want go:second", got)
	}
}

func TestStaveSelectionTracksCommandOutcomePageSizeChange(t *testing.T) {
	view := &summaryReportView{Dependencies: []summaryDependencyView{
		{Language: "go", Name: "first"},
		{Language: "go", Name: "second"},
		{Language: "go", Name: "third"},
	}}
	model := newStaveSummaryModel(view, nil, summaryState{page: 1, pageSize: 3, sortMode: sortByName})
	model.interaction.selectedRow = 2
	model.interaction.pendingCallID = "size-change"
	model.interaction.pendingActionID = "lopper.summary.size.v1"

	result := mustStaveSelectionEvent(t, event.EffectResult, event.EffectResultPayload{
		CallID: "size-change",
		Status: "done",
		Value: map[string]any{
			"version": "lopper.action-result/v1",
			"action":  "lopper.summary.size.v1",
			"value":   map[string]any{"command": "size 1"},
		},
	})
	var err error
	model, _, err = reduceStaveSummary(stave.ReduceContext{}, model, result)
	if err != nil {
		t.Fatal(err)
	}
	if model.interaction.selectedRow != 0 {
		t.Fatalf("resized selected row = %d, want 0", model.interaction.selectedRow)
	}
	if got := selectedDependencyForRow(model); got != "go:first" {
		t.Fatalf("resized selected dependency = %q, want go:first", got)
	}
}

func reduceStaveSelectionKey(t *testing.T, model staveSummaryModel, key string) staveSummaryModel {
	t.Helper()
	next, _, err := reduceStaveSummary(stave.ReduceContext{}, model, mustStaveSelectionEvent(t, event.Key, event.KeyPayload{Key: key}))
	if err != nil {
		t.Fatal(err)
	}
	return next
}

func reduceStaveSelectionText(t *testing.T, model staveSummaryModel, text string) staveSummaryModel {
	t.Helper()
	next, _, err := reduceStaveSummary(stave.ReduceContext{}, model, mustStaveSelectionEvent(t, event.Text, event.TextPayload{Text: text, Committed: true}))
	if err != nil {
		t.Fatal(err)
	}
	return next
}

func mustStaveSelectionEvent(t *testing.T, kind event.Kind, payload any) event.Event {
	t.Helper()
	event, err := event.New(kind, payload)
	if err != nil {
		t.Fatal(err)
	}
	return event
}
