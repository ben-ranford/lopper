package ui

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/stave"
	"github.com/ben-ranford/stave/action"
	"github.com/ben-ranford/stave/event"
	"github.com/ben-ranford/stave/semantic"
)

type staveBaselineRunner struct {
	baseline report.Report
	path     string
	saveReq  BaselineSaveRequest
	applyErr error
	apply    *report.CodemodApplyReport
	applyN   int
}

func (r *staveBaselineRunner) ApplyCodemod(context.Context, CodemodApplyRequest) (report.Report, error) {
	r.applyN++
	apply := r.apply
	if apply == nil {
		apply = &report.CodemodApplyReport{AppliedFiles: 1, AppliedPatches: 1}
	}
	return report.Report{SchemaVersion: report.SchemaVersion, Dependencies: []report.DependencyReport{{Language: "js", Name: "lodash", UsedExportsCount: 1, TotalExportsCount: 2, UsedPercent: 50, Codemod: &report.CodemodReport{Apply: apply}}}}, r.applyErr
}
func (r *staveBaselineRunner) SaveBaseline(_ context.Context, req BaselineSaveRequest) (report.Report, string, error) {
	r.saveReq = req
	path, err := report.SaveSnapshot(req.BaselineStorePath, "label:nightly", r.baseline, time.Unix(1, 0).UTC())
	r.path = path
	return r.baseline, path, err
}

func TestStaveActionsSaveBaselineThenCompareUseTempStoreAndRefreshModel(t *testing.T) {
	store := t.TempDir()
	baseline := report.Report{SchemaVersion: report.SchemaVersion, Dependencies: []report.DependencyReport{{Language: "go", Name: "alpha", UsedExportsCount: 1, TotalExportsCount: 10, UsedPercent: 10}}}
	current := report.Report{SchemaVersion: report.SchemaVersion, Dependencies: []report.DependencyReport{{Language: "go", Name: "alpha", UsedExportsCount: 3, TotalExportsCount: 10, UsedPercent: 30}}}
	runner := &staveBaselineRunner{baseline: baseline}
	summary := NewSummary(io.Discard, strings.NewReader(""), &stubAnalyzer{report: current}, report.NewFormatter())
	summary.Actions = runner
	opts := summary.applyDefaults(Options{RepoPath: ".", BaselineStorePath: store, Width: 80})
	view := mapSummaryReportView(current)
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
	if _, err := completeLopperAction(context.Background(), t, prepared, staveActionSaveBaseline, map[string]any{"label": "nightly", "store": store}, "e2e", "save", false); err != nil {
		t.Fatal(err)
	}
	if runner.saveReq.BaselineStorePath != store || runner.path == "" {
		t.Fatalf("baseline save request/path not honest: req=%+v path=%q", runner.saveReq, runner.path)
	}
	if _, err := os.Stat(runner.path); err != nil {
		t.Fatalf("saved baseline file missing: %v", err)
	}
	if _, err := completeLopperAction(context.Background(), t, prepared, staveActionCompareBaseline, map[string]any{"file": runner.path}, "e2e", "compare", false); err != nil {
		t.Fatal(err)
	}
	snap, err := prepared.Session.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if snap.Model.view == nil || snap.Model.view.BaselineComparison == nil {
		t.Fatal("compare action did not refresh the session report")
	}
	if snap.Model.view.BaselineComparison.BaselineKey == "" {
		t.Fatalf("compare did not preserve baseline key: %#v", snap.Model.view.BaselineComparison)
	}
}

func TestStaveCodemodConfirmationIsRequiredAndSingleUse(t *testing.T) {
	runner := &staveBaselineRunner{}
	summary := NewSummary(io.Discard, strings.NewReader(""), &stubAnalyzer{report: report.Report{SchemaVersion: report.SchemaVersion, Dependencies: []report.DependencyReport{{Language: "go", Name: "zeta"}, {Language: "go", Name: "alpha"}, {Language: "js", Name: "other"}}}}, report.NewFormatter())
	summary.Actions = runner
	opts := summary.applyDefaults(Options{Width: 80})
	view := summaryReportView{}
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
	args := map[string]any{"dependency": "lodash", "confirm": false, "allowDirty": false}
	if err := invokeLopperAction(context.Background(), prepared, staveActionApplyCodemod, args, "e2e", false); err == nil {
		t.Fatal("unconfirmed codemod was accepted")
	}
	if runner.applyN != 0 {
		t.Fatalf("unconfirmed codemod mutated runner: %d", runner.applyN)
	}
	def, ok := prepared.Actions.Definition(action.ID(staveActionApplyCodemod))
	if !ok {
		t.Fatal("codemod action definition missing")
	}
	raw, err := json.Marshal(map[string]any{"dependency": "lodash", "confirm": true, "allowDirty": false})
	if err != nil {
		t.Fatalf("marshal confirmation arguments: %v", err)
	}
	confirmation, err := action.NewConfirmation("e2e", def, semantic.Target{}, raw, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err := prepared.Actions.IssueConfirmation(confirmation); err != nil {
		t.Fatal(err)
	}
	call := action.Call{ActionID: action.ID(staveActionApplyCodemod), Arguments: raw, SessionID: "e2e", Confirmation: &confirmation}
	if result := prepared.Actions.Invoke(context.Background(), call); result.Error != nil {
		t.Fatalf("confirmed codemod rejected: %+v", result.Error)
	}
	if replay := prepared.Actions.Invoke(context.Background(), call); replay.Error == nil || replay.Error.Code != action.ConfirmationInvalid {
		t.Fatalf("confirmation replay accepted: %+v", replay)
	}
}

func TestStaveCodemodSkipOnlyOutcomeIsNotReportedAsApplied(t *testing.T) {
	runner := &staveBaselineRunner{apply: &report.CodemodApplyReport{SkippedFiles: 1, SkippedPatches: 2}}
	summary := NewSummary(io.Discard, strings.NewReader(""), &stubAnalyzer{}, report.NewFormatter())
	summary.Actions = runner
	opts := summary.applyDefaults(Options{Width: 80})
	view := summaryReportView{Dependencies: []summaryDependencyView{{Language: "js", Name: "lodash"}}}
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

	result, err := completeLopperAction(context.Background(), t, prepared, staveActionApplyCodemod, map[string]any{"dependency": "js:lodash", "confirm": true, "allowDirty": false}, "e2e", "skip-only", true)
	if err != nil {
		t.Fatal(err)
	}
	value := result.Outcome.Value
	payload, ok := value["value"].(map[string]any)
	if !ok || payload["applied"] != false {
		t.Fatalf("skip-only codemod claimed applied: %#v", value)
	}
	snap, err := prepared.Session.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if snap.Model.interaction.status != "No codemod changes" || snap.Model.interaction.error != "" {
		t.Fatalf("skip-only codemod feedback is misleading: %+v", snap.Model.interaction)
	}
}

func TestStaveTypedActionsUpdateSessionStateAndTree(t *testing.T) {
	deps := []summaryDependencyView{{Language: "go", Name: "zeta"}, {Language: "go", Name: "alpha"}, {Language: "js", Name: "other"}}
	summary := NewSummary(io.Discard, strings.NewReader(""), &stubAnalyzer{}, report.NewFormatter())
	opts := summary.applyDefaults(Options{Width: 80})
	view := summaryReportView{Dependencies: deps}
	state := summaryState{page: 1, pageSize: 10, sortMode: sortByWaste}
	program, err := newLopperStaveProgram(summary, &opts, &view, &state)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := program.NewSession(context.Background(), staveSessionOptions(opts, false))
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Session.Close()
	cases := []struct {
		id    string
		value any
	}{
		{"lopper.summary.filter.v1", map[string]any{"value": "a"}},
		{"lopper.summary.sort.v1", map[string]any{"value": "name"}},
		{"lopper.summary.size.v1", map[string]any{"value": "1"}},
		{"lopper.summary.page.v1", map[string]any{"value": "2"}},
		{staveActionOpen, map[string]any{"dependency": "go:alpha"}},
	}
	for i, tc := range cases {
		if _, err := completeLopperAction(context.Background(), t, prepared, action.ID(tc.id), tc.value, "e2e", "typed-"+string(rune('a'+i)), false); err != nil {
			t.Fatalf("%s: %v", tc.id, err)
		}
	}
	snap, err := prepared.Session.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	got := snap.Model.interaction.summary
	if got.filter != "a" || got.sortMode != sortByName || got.page != 2 || got.pageSize != 1 || got.selectedDependency != "go:alpha" {
		t.Fatalf("typed actions did not update model: %+v", got)
	}
	tree, err := program.View(stave.ViewContext{}, snap.Model)
	if err != nil {
		t.Fatal(err)
	}
	children := tree.Root().Children()
	foundZeta := false
	for _, child := range children {
		if strings.Contains(child.Name(), "zeta") {
			foundZeta = true
		}
	}
	if !foundZeta {
		t.Fatalf("tree did not reflect filtered/sorted/page state: children=%d", len(children))
	}
}

func mustStaveActionEvent(t *testing.T, kind event.Kind, payload any) event.Event {
	t.Helper()
	ev, err := event.New(kind, payload)
	if err != nil {
		t.Fatal(err)
	}
	return ev
}
