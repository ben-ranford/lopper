package ui

import (
	"github.com/ben-ranford/lopper/internal/report"
	"testing"
)

func TestStaveSummaryCloneIsolatesReportOptionsAndBridge(t *testing.T) {
	color := true
	view := summaryReportView{Warnings: []string{"warning"}, Dependencies: []summaryDependencyView{{Language: "go", Name: "alpha", TopUsedSymbols: []report.SymbolUsage{{Name: "x"}}}}}
	opts := Options{RepoPath: ".", Color: &color}
	state := summaryState{page: 2, pageSize: 5}
	model := newStaveSummaryModel(&view, &opts, state)
	clone, err := cloneStaveSummaryModel(model)
	if err != nil {
		t.Fatal(err)
	}
	clone.view.Warnings[0] = "changed"
	clone.view.Dependencies[0].TopUsedSymbols[0].Name = "changed"
	*clone.opts.Color = false
	clone.interaction.summary.page = 9
	if view.Warnings[0] != "warning" || view.Dependencies[0].TopUsedSymbols[0].Name != "x" || !*opts.Color || state.page != 2 {
		t.Fatal("clone mutated source snapshot")
	}
}
