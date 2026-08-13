package ui

import (
	"context"
	"io"
	"testing"

	"github.com/ben-ranford/stave"
	"github.com/ben-ranford/stave/action"
	"github.com/ben-ranford/stave/event"
	staveinput "github.com/ben-ranford/stave/input"
	"github.com/ben-ranford/stave/layout"
)

func TestStaveSummaryReducerSequenceIsDeterministic(t *testing.T) {
	base := staveSummaryModel{interaction: staveSummaryInteraction{summary: summaryState{page: 1, pageSize: 10, sortMode: sortByWaste}, focusPane: "summary", viewport: layout.Size{Width: 80, Height: 24}}}
	events := []event.Event{
		staveinput.KeyEvent(mustStaveKey(t, "down")),
		staveinput.KeyEvent(mustStaveKey(t, "/")),
		{Kind: event.Text, Payload: event.TextPayload{Text: "filter go", Committed: true}},
		staveinput.KeyEvent(mustStaveKey(t, "right")),
		{Kind: event.Resize, Payload: event.ResizePayload{Width: 120, Height: 40}},
		staveinput.KeyEvent(mustStaveKey(t, "?")),
	}
	a, b := base, base
	for _, ev := range events {
		var err error
		a, _, err = reduceStaveSummary(stave.ReduceContext{}, a, ev)
		if err != nil {
			t.Fatalf("reduce a: %v", err)
		}
		b, _, err = reduceStaveSummary(stave.ReduceContext{}, b, ev)
		if err != nil {
			t.Fatalf("reduce b: %v", err)
		}
	}
	if a.interaction != b.interaction {
		t.Fatalf("same event sequence produced different state: %#v != %#v", a.interaction, b.interaction)
	}
	if a.interaction.summary.filter != "go" || a.interaction.summary.page != 2 || !a.interaction.help {
		t.Fatalf("unexpected reduced state: %#v", a.interaction)
	}
}

func mustStaveKey(t *testing.T, raw string) staveinput.KeyChord {
	t.Helper()
	key, err := staveinput.ParseKey(raw)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func TestStaveSummaryReducerShutdownAndCanonicalKeys(t *testing.T) {
	m := staveSummaryModel{}
	for _, raw := range []string{"q", "Q", "ctrl+c", "escape"} {
		chord := mustStaveKey(t, raw)
		n, _, err := reduceStaveSummary(stave.ReduceContext{}, m, staveinput.KeyEvent(chord))
		if err != nil || !n.interaction.quit {
			t.Fatalf("key %q did not quit: %#v, %v", raw, n.interaction, err)
		}
	}
}

func TestStaveSortActionUpdatesSharedState(t *testing.T) {
	view := &summaryReportView{Dependencies: []summaryDependencyView{{Name: "zeta", Language: "go"}, {Name: "alpha", Language: "go"}}}
	state := &summaryState{page: 1, pageSize: 10, sortMode: sortByWaste}
	program, err := newLopperStaveProgram(NewSummary(io.Discard, nil, nil, nil), &Options{}, view, state)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := program.NewSession(context.Background(), staveSessionOptions(Options{Width: 80}, false))
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Session.Close()
	if _, err := completeLopperAction(context.Background(), t, prepared, action.ID("lopper.summary.sort.v1"), map[string]any{"value": "name"}, "lopper-preview", "sort", false); err != nil {
		t.Fatal(err)
	}
	snap, err := prepared.Session.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if snap.Model.interaction.summary.sortMode != sortByName {
		t.Fatalf("sort mode = %q", snap.Model.interaction.summary.sortMode)
	}
	if snap.Model.interaction.status != "Sorted by name" {
		t.Fatalf("sort status = %q", snap.Model.interaction.status)
	}
}
