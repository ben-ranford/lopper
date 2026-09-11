package ui

import (
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
