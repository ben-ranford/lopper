package ui

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/ben-ranford/lopper/internal/analysis"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/stave"
	"github.com/ben-ranford/stave/action"
	"github.com/ben-ranford/stave/event"
	staveinput "github.com/ben-ranford/stave/input"
)

type gatedStaveAnalyzer struct {
	started chan struct{}
	release chan struct{}
	report  report.Report
}

func (a *gatedStaveAnalyzer) Analyse(ctx context.Context, _ analysis.Request) (report.Report, error) {
	close(a.started)
	select {
	case <-a.release:
		return a.report, nil
	case <-ctx.Done():
		return report.Report{}, ctx.Err()
	}
}

func TestStaveActionRunsOffLoopAfterPendingEvent(t *testing.T) {
	analyzer := &gatedStaveAnalyzer{
		started: make(chan struct{}),
		release: make(chan struct{}),
		report:  report.Report{SchemaVersion: report.SchemaVersion, Dependencies: []report.DependencyReport{{Language: "go", Name: "after"}}},
	}
	summary := NewSummary(io.Discard, strings.NewReader(""), analyzer, report.NewFormatter())
	opts := summary.applyDefaults(Options{Width: 80})
	view := summaryReportView{Dependencies: []summaryDependencyView{{Language: "go", Name: "before"}}}
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

	const callID = "async-contract-1"
	args := preparedLopperActionArgs(t, prepared, action.ID(staveActionRefresh), map[string]any{})
	if err := sendLopperEvent(context.Background(), prepared, mustStaveActionEvent(t, event.ActionInvoked, event.ActionInvokedPayload{CallID: callID, ActionID: staveActionRefresh, Arguments: args})); err != nil {
		t.Fatal(err)
	}
	execution := startLopperAction(context.Background(), prepared, action.ID(staveActionRefresh), args, "async-contract", false, callID)
	select {
	case <-analyzer.started:
	case <-time.After(time.Second):
		t.Fatal("refresh action did not start asynchronously")
	}
	select {
	case <-execution:
		t.Fatal("refresh action completed before the analyzer was released")
	default:
	}
	snapshot, err := prepared.Session.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Model.interaction.pendingCallID != callID {
		t.Fatalf("pending call = %q, want %q", snapshot.Model.interaction.pendingCallID, callID)
	}

	close(analyzer.release)
	completed := <-execution
	if completed.err != nil || completed.result.Outcome == nil {
		t.Fatalf("async completion = %+v, %v", completed.result, completed.err)
	}
	if err := sendLopperEvent(context.Background(), prepared, mustStaveActionEvent(t, event.EffectResult, event.EffectResultPayload{CallID: callID, Status: "completed", Value: completed.result.Outcome.Value})); err != nil {
		t.Fatal(err)
	}
}

func TestStaveRefreshReanalyzesAndPreservesInteractionState(t *testing.T) {
	analyzer := &stubAnalyzer{report: report.Report{SchemaVersion: report.SchemaVersion, Dependencies: []report.DependencyReport{{Language: "go", Name: "before"}}}}
	summary := NewSummary(io.Discard, strings.NewReader(""), analyzer, report.NewFormatter())
	opts := summary.applyDefaults(Options{Width: 80})
	view := summaryReportView{Dependencies: []summaryDependencyView{{Language: "go", Name: "before"}}}
	state := summaryState{filter: "go", sortMode: sortByName, page: 9, pageSize: 1}
	program, err := newLopperStaveProgram(summary, &opts, &view, &state)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := program.NewSession(context.Background(), staveSessionOptions(opts, false))
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Session.Close()
	analyzer.report = report.Report{SchemaVersion: report.SchemaVersion, Dependencies: []report.DependencyReport{{Language: "go", Name: "after"}}}
	if _, err := completeLopperAction(context.Background(), t, prepared, action.ID(staveActionRefresh), map[string]any{}, "lifecycle", "refresh", false); err != nil {
		t.Fatal(err)
	}
	snap, err := prepared.Session.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if snap.Model.view == nil || len(snap.Model.view.Dependencies) != 1 || snap.Model.view.Dependencies[0].Name != "after" {
		t.Fatalf("refresh did not replace session view: %+v", snap.Model.view)
	}
	if got := snap.Model.interaction.summary; got.filter != "go" || got.sortMode != sortByName || got.page != 1 || got.pageSize != 1 {
		t.Fatalf("refresh changed interaction state unexpectedly: %+v", got)
	}
}

func TestStaveLineModeEOFIsCleanAndProcessesFinalCommand(t *testing.T) {
	features := previewFeatures(t)
	for _, tc := range []struct{ name, input string }{{"empty", ""}, {"unterminated", "filter go"}} {
		t.Run(tc.name, func(t *testing.T) {
			out := &strings.Builder{}
			summary := NewSummary(out, strings.NewReader(tc.input), &stubAnalyzer{report: report.Report{SchemaVersion: report.SchemaVersion, Dependencies: []report.DependencyReport{{Language: "go", Name: "alpha"}}}}, report.NewFormatter())
			err := NewStavePreview(summary).Start(context.Background(), Options{UseStavePreview: true, Features: features, Width: 80})
			if err != nil {
				t.Fatalf("EOF should be clean: %v", err)
			}
			if tc.name == "unterminated" && !strings.Contains(out.String(), "alpha") {
				t.Fatalf("final command did not produce rendered output: %q", out.String())
			}
		})
	}
}

func TestStaveTerminalQuitViewDoesNotRepaint(t *testing.T) {
	b := &staveTerminal{quit: true}
	v := (&staveTerminalModel{bridge: b}).View()
	if v.Content != "" || v.AltScreen {
		t.Fatalf("quit view repainted: content=%q alt=%v", v.Content, v.AltScreen)
	}
}

func TestStaveCommandModeAcceptsCanonicalSpaceKey(t *testing.T) {
	model := newStaveSummaryModel(&summaryReportView{}, nil, summaryState{})
	var err error
	for _, r := range ":sort name" {
		var chord staveinput.KeyChord
		if r == ' ' {
			chord = staveinput.KeyChord{Code: staveinput.KeySpace}
		} else {
			chord, err = staveinput.ParseKey(string(r))
			if err != nil {
				t.Fatal(err)
			}
		}
		ev := staveinput.KeyEvent(chord)
		model, _, err = reduceStaveSummary(stave.ReduceContext{}, model, ev)
		if err != nil {
			t.Fatal(err)
		}
	}
	if model.interaction.filterBuffer != "sort name" {
		t.Fatalf("command buffer = %q", model.interaction.filterBuffer)
	}
	status, commandErr := applyStaveCommand(&model.interaction.summary, model.interaction.filterBuffer, nil)
	if commandErr != "" || status != "ok" || model.interaction.summary.sortMode != sortByName {
		t.Fatalf("sort command failed: status=%q error=%q state=%+v", status, commandErr, model.interaction.summary)
	}
}

func TestStaveRefreshClearsStaleSelectionAndClampsRow(t *testing.T) {
	state := summaryState{page: 9, pageSize: 1, selectedDependency: "go:removed"}
	model := newStaveSummaryModel(&summaryReportView{Dependencies: []summaryDependencyView{{Language: "go", Name: "new"}}}, nil, state)
	model.interaction.selectedRow = 8
	ev, err := event.New(event.ActionInvoked, event.ActionInvokedPayload{CallID: "refresh", ActionID: staveActionRefresh})
	if err != nil {
		t.Fatal(err)
	}
	model, _, err = reduceStaveSummary(stave.ReduceContext{}, model, ev)
	if err != nil {
		t.Fatal(err)
	}
	if model.interaction.selectedRow != 8 || model.interaction.summary.page != 9 || model.interaction.summary.selectedDependency != "go:removed" {
		t.Fatalf("pending refresh mutated interaction state: row=%d state=%+v", model.interaction.selectedRow, model.interaction.summary)
	}
}
