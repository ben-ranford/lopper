package ui

import (
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/stave/event"
)

func TestStaveReportReplacementNormalizesSelection(t *testing.T) {
	for _, id := range []string{staveActionRefresh, staveActionSaveBaseline, staveActionCompareBaseline} {
		t.Run(id, func(t *testing.T) {
			checkStaveReportSelection(t, id, "go:removed", true)
			checkStaveReportSelection(t, id, "go:kept", false)
		})
	}
}

func TestStaveBaselineOutcomeRejectsMalformedComponentsAtomically(t *testing.T) {
	for _, actionID := range []string{staveActionSaveBaseline, staveActionCompareBaseline} {
		t.Run(actionID+" malformed report", func(t *testing.T) {
			model := baselineOutcomeModel(actionID)
			value := map[string]any{
				"ok":      true,
				"report":  map[string]any{"warnings": 7},
				"options": map[string]any{"baselinePath": "new.json", "baselineStorePath": "new-store", "baselineKey": "new-key"},
			}
			reduceStaveEffectResult(&model, event.EffectResultPayload{CallID: "baseline", Status: "done", Value: validCoverageEnvelope(actionID, value)})
			assertBaselineOutcomeUnchanged(t, model, "report decode failed")
		})

		for _, options := range []any{"bad", map[string]any{"baselinePath": 7, "baselineStorePath": "new-store", "baselineKey": "new-key"}} {
			model := baselineOutcomeModel(actionID)
			value := map[string]any{
				"ok":      true,
				"report":  report.Report{Dependencies: []report.DependencyReport{{Language: "go", Name: "new"}}},
				"options": options,
			}
			reduceStaveEffectResult(&model, event.EffectResultPayload{CallID: "baseline", Status: "done", Value: validCoverageEnvelope(actionID, value)})
			assertBaselineOutcomeUnchanged(t, model, "options invalid")
		}

		t.Run(actionID+" valid outcome", func(t *testing.T) {
			model := baselineOutcomeModel(actionID)
			value := map[string]any{
				"ok":      true,
				"report":  report.Report{Dependencies: []report.DependencyReport{{Language: "go", Name: "new"}}},
				"options": map[string]any{"baselinePath": "", "baselineStorePath": "new-store"},
			}
			reduceStaveEffectResult(&model, event.EffectResultPayload{CallID: "baseline", Status: "done", Value: validCoverageEnvelope(actionID, value)})
			if model.interaction.error != "" || model.interaction.status != staveActionStatus(actionID, value) || model.view.Dependencies[0].Name != "new" || model.opts.BaselinePath != "" || model.opts.BaselineStorePath != "new-store" || model.opts.BaselineKey != "old-key" {
				t.Fatalf("valid baseline outcome was not applied: %+v", model)
			}
		})
	}
}

func baselineOutcomeModel(actionID string) staveSummaryModel {
	view := summaryReportView{Dependencies: []summaryDependencyView{{Language: "go", Name: "old"}}}
	opts := Options{BaselinePath: "old.json", BaselineStorePath: "old-store", BaselineKey: "old-key"}
	return staveSummaryModel{view: &view, opts: &opts, interaction: staveSummaryInteraction{pendingCallID: "baseline", pendingActionID: actionID}}
}

func assertBaselineOutcomeUnchanged(t *testing.T, model staveSummaryModel, wantError string) {
	t.Helper()
	if model.view == nil || len(model.view.Dependencies) != 1 || model.view.Dependencies[0].Name != "old" {
		t.Fatalf("malformed outcome changed report view: %#v", model.view)
	}
	if model.opts == nil || model.opts.BaselinePath != "old.json" || model.opts.BaselineStorePath != "old-store" || model.opts.BaselineKey != "old-key" {
		t.Fatalf("malformed outcome changed baseline options: %#v", model.opts)
	}
	if !strings.Contains(model.interaction.error, wantError) {
		t.Fatalf("outcome error = %q, want %q", model.interaction.error, wantError)
	}
	if model.interaction.status != "" {
		t.Fatalf("malformed outcome retained success status: %q", model.interaction.status)
	}
}

func checkStaveReportSelection(t *testing.T, id, selected string, missing bool) {
	t.Helper()
	view := summaryReportView{Dependencies: []summaryDependencyView{{Language: "go", Name: "kept"}, {Language: "go", Name: "other"}, {Language: "go", Name: "removed"}}}
	model := staveSummaryModel{view: &view, interaction: staveSummaryInteraction{summary: summaryState{page: 1, pageSize: 10, sortMode: sortByName, selectedDependency: selected}, selectedRow: 2, focusPane: "detail", pendingCallID: "replace", pendingActionID: id}}
	value := map[string]any{"ok": true, "refreshed": true, "options": map[string]any{"baselinePath": "", "baselineStorePath": "", "baselineKey": ""}, "report": report.Report{Dependencies: []report.DependencyReport{{Language: "go", Name: "kept"}}}}
	reduceStaveEffectResult(&model, event.EffectResultPayload{CallID: "replace", Status: "done", Value: map[string]any{"version": staveActionResultVersion, "action": id, "value": value}})
	if model.interaction.error != "" || model.interaction.selectedRow != 0 || len(model.view.Dependencies) != 1 {
		t.Fatalf("replacement did not normalize selected row: %+v", model.interaction)
	}
	if missing {
		if model.interaction.summary.selectedDependency != "" || model.interaction.focusPane != "summary" {
			t.Fatalf("missing detail selection retained: %+v", model.interaction)
		}
	} else if model.interaction.summary.selectedDependency != selected || model.interaction.focusPane != "detail" {
		t.Fatalf("surviving detail lost: %+v", model.interaction)
	}
}
