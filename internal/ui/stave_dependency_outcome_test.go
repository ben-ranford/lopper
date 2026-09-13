package ui

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/stave/action"
	"github.com/ben-ranford/stave/event"
)

func TestStaveActionDependencyNamesResolveToCanonicalDetail(t *testing.T) {
	for _, tc := range []struct {
		name, language, dependency, selected string
		codemod                              bool
	}{
		{name: "open bare current language", language: "js", dependency: "lodash", selected: "js:lodash"},
		{name: "open explicit language", language: "js", dependency: "go:lodash", selected: "go:lodash"},
		{name: "open all languages follows legacy order", language: "all", dependency: "lodash", selected: "go:lodash"},
		{name: "codemod bare current language", language: "js", dependency: "lodash", selected: "js:lodash", codemod: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checkStaveDependencyOutcome(t, tc.language, tc.dependency, tc.selected, tc.codemod)
		})
	}
}

func checkStaveDependencyOutcome(t *testing.T, language, dependency, selected string, codemod bool) {
	t.Helper()
	runner := &staveBaselineRunner{}
	summary := NewSummary(io.Discard, strings.NewReader(""), &stubAnalyzer{}, report.NewFormatter())
	summary.Actions = runner
	opts := summary.applyDefaults(Options{Width: 80, Language: language})
	view := mapSummaryReportView(report.Report{Dependencies: []report.DependencyReport{{Language: "go", Name: "lodash"}, {Language: "js", Name: "lodash"}}})
	state := buildSummaryState(opts)
	program, err := newLopperStaveProgram(summary, &opts, &view, &state)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := program.NewSession(context.Background(), staveSessionOptions(opts, false))
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Session.Close()
	id := action.ID(staveActionOpen)
	args := map[string]any{"dependency": dependency}
	if codemod {
		id = action.ID(staveActionApplyCodemod)
		args["confirm"] = true
		args["allowDirty"] = false
	}
	if _, err := completeLopperAction(context.Background(), t, prepared, id, args, staveTestActionCall{sessionID: "names", callID: "resolve", confirm: codemod}); err != nil {
		t.Fatal(err)
	}
	snap, err := prepared.Session.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	got := snap.Model.interaction
	if got.error != "" || got.status == "" || got.focusPane != "detail" || got.summary.selectedDependency != selected {
		t.Fatalf("action did not open canonical detail: %+v", got)
	}
	if codemod {
		if snap.Model.view.Dependencies[0].CodemodApply != nil || snap.Model.view.Dependencies[1].CodemodApply == nil {
			t.Fatalf("codemod result merged into wrong language: %+v", snap.Model.view.Dependencies)
		}
		if view.Dependencies[1].CodemodApply != nil {
			t.Fatal("prior summary mutated")
		}
	}
}

func TestStaveMalformedPartialFailurePreservesReport(t *testing.T) {
	for _, failure := range []any{"", "   ", true} {
		view := summaryReportView{Dependencies: []summaryDependencyView{{Language: "js", Name: "lodash"}}}
		model := staveSummaryModel{view: &view, interaction: staveSummaryInteraction{pendingCallID: "partial", pendingActionID: staveActionApplyCodemod}}
		reduceStaveEffectResult(&model, event.EffectResultPayload{CallID: "partial", Status: "done", Value: map[string]any{
			"version": staveActionResultVersion, "action": staveActionApplyCodemod,
			"value": map[string]any{"dependency": "js:lodash", "applied": true, "failure": failure,
				"report": report.Report{Dependencies: []report.DependencyReport{{Language: "js", Name: "lodash", Codemod: &report.CodemodReport{Apply: &report.CodemodApplyReport{AppliedFiles: 1}}}}}},
		}})
		if model.interaction.error != "invalid action outcome: codemod failure invalid" || model.view.Dependencies[0].CodemodApply != nil {
			t.Fatalf("malformed failure %v mutated report or lacked validation error: %+v", failure, model.interaction)
		}
	}
}

func TestStaveCodemodMissingLanguageTargetPreservesOtherLanguage(t *testing.T) {
	view := summaryReportView{Dependencies: []summaryDependencyView{{Language: "go", Name: "lodash"}}}
	model := staveSummaryModel{opts: &Options{Language: "js"}, view: &view, interaction: staveSummaryInteraction{pendingCallID: "missing", pendingActionID: staveActionApplyCodemod}}
	reduceStaveEffectResult(&model, event.EffectResultPayload{CallID: "missing", Status: "done", Value: map[string]any{
		"version": staveActionResultVersion, "action": staveActionApplyCodemod,
		"value": map[string]any{"dependency": "lodash", "applied": true,
			"report": report.Report{Dependencies: []report.DependencyReport{{Language: "js", Name: "lodash", Codemod: &report.CodemodReport{Apply: &report.CodemodApplyReport{AppliedFiles: 1}}}}}},
	}})
	if model.view.Dependencies[0].CodemodApply != nil || model.interaction.error != "No data for dependency lodash" || model.interaction.status != "" {
		t.Fatalf("missing JS target changed Go result or lost diagnostic: %+v, %+v", model.view.Dependencies[0], model.interaction)
	}
}
