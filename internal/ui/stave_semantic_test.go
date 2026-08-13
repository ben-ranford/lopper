package ui

import (
	"strings"
	"testing"

	"github.com/ben-ranford/stave"
	"github.com/ben-ranford/stave/event"
	"github.com/ben-ranford/stave/semantic"
)

func TestStaveSemanticTreeRolesLabelsAndActions(t *testing.T) {
	view := previewView([]string{"a warning"}, previewDep("go", "alpha", 50, 10))
	tree, err := staveTree(view, view.Dependencies, view.Dependencies, summaryState{page: 1, pageSize: 10}, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	root := tree.Root()
	if root.Role() != semantic.Role("application") || root.Name() != "Lopper" {
		t.Fatalf("root semantics: role=%q name=%q", root.Role(), root.Name())
	}
	rootActions := map[string]bool{}
	for _, a := range root.Actions() {
		rootActions[string(a.ID)] = true
	}
	for _, want := range []string{staveActionQuit, staveActionRefresh, staveActionSaveBaseline, staveActionCompareBaseline} {
		if !rootActions[want] {
			t.Fatalf("root action %q missing", want)
		}
	}
	var row, alert bool
	for _, child := range root.Children() {
		switch child.Role() {
		case semantic.Role("row"):
			row = true
			if child.Name() != "alpha" || !strings.Contains(child.Description(), "go") {
				t.Fatalf("row label/content drift: %q %q", child.Name(), child.Description())
			}
			ids := map[string]bool{}
			for _, a := range child.Actions() {
				ids[string(a.ID)] = true
			}
			for _, want := range []string{staveActionOpen, staveActionApplyCodemod} {
				if !ids[want] {
					t.Fatalf("row action %q missing", want)
				}
			}
		case semantic.Role("alert"):
			alert = true
		}
	}
	if !row || !alert {
		t.Fatalf("expected row and alert roles: row=%t alert=%t", row, alert)
	}
}

func TestStaveSemanticInteractionStateIsNotColorOnly(t *testing.T) {
	model := newStaveSummaryModel(&summaryReportView{}, nil, summaryState{page: 1, pageSize: 10})
	for _, payload := range []event.KeyPayload{{Key: "tab"}, {Key: "rune", Rune: '/'}, {Key: "down"}} {
		ev, err := event.New(event.Key, payload)
		if err != nil {
			t.Fatal(err)
		}
		model, _, err = reduceStaveSummary(stave.ReduceContext{}, model, ev)
		if err != nil {
			t.Fatal(err)
		}
	}
	reduceStaveKey(&model, event.KeyPayload{Key: "?"})
	if model.interaction.focusPane != "summary" || !model.interaction.help || !model.interaction.commandMode {
		t.Fatalf("interaction semantics drifted: %+v", model.interaction)
	}
	if model.interaction.summary.showHelp != model.interaction.help {
		t.Fatal("help state was represented only by styling")
	}
}

func TestStaveSemanticStatusAndErrorAreTextual(t *testing.T) {
	model := newStaveSummaryModel(&summaryReportView{}, nil, summaryState{page: 1, pageSize: 10})
	bad, err := event.New(event.Text, event.TextPayload{Text: "unknown command", Committed: true})
	if err != nil {
		t.Fatal(err)
	}
	model, _, err = reduceStaveSummary(stave.ReduceContext{}, model, bad)
	if err != nil {
		t.Fatal(err)
	}
	if model.interaction.error == "" || !strings.Contains(model.interaction.error, "unknown command") {
		t.Fatalf("error lost semantic text: %q", model.interaction.error)
	}
	invoked, err := event.New(event.ActionInvoked, event.ActionInvokedPayload{CallID: "c1", ActionID: "test.action"})
	if err != nil {
		t.Fatal(err)
	}
	model, _, err = reduceStaveSummary(stave.ReduceContext{}, model, invoked)
	if err != nil {
		t.Fatal(err)
	}
	ok, err := event.New(event.EffectResult, event.EffectResultPayload{CallID: "c1", Status: "ok"})
	if err != nil {
		t.Fatal(err)
	}
	model, _, err = reduceStaveSummary(stave.ReduceContext{}, model, ok)
	if err != nil || model.interaction.status != "ok" {
		t.Fatalf("status semantics drifted: %+v %v", model.interaction, err)
	}
}
