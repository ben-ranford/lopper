package ui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/stave"
	"github.com/ben-ranford/stave/action"
	"github.com/ben-ranford/stave/capability"
)

type staveNoResultRunner struct {
	applyErr error
}

func (r *staveNoResultRunner) ApplyCodemod(context.Context, CodemodApplyRequest) (report.Report, error) {
	return report.Report{SchemaVersion: report.SchemaVersion}, r.applyErr
}

func (r *staveNoResultRunner) SaveBaseline(context.Context, BaselineSaveRequest) (report.Report, string, error) {
	return report.Report{}, "", errors.New("save failed")
}

func TestStaveProgramRejectsInvalidSnapshotsAndViewModels(t *testing.T) {
	opts := Options{Width: 80}
	if _, err := newLopperStaveProgram(nil, nil, &summaryReportView{}, nil); err == nil || !strings.Contains(err.Error(), "options snapshot") {
		t.Fatalf("nil options error = %v", err)
	}
	if _, err := newLopperStaveProgram(nil, &opts, nil, nil); err == nil || !strings.Contains(err.Error(), "report snapshot") {
		t.Fatalf("nil report error = %v", err)
	}
	invalid := summaryReportView{Dependencies: []summaryDependencyView{{Name: "bad", UsedPercent: math.NaN()}}}
	if _, err := newLopperStaveProgram(nil, &opts, &invalid, nil); err == nil || !strings.Contains(err.Error(), "not JSON-safe") {
		t.Fatalf("invalid report error = %v", err)
	}

	view := summaryReportView{Dependencies: []summaryDependencyView{{Language: "go", Name: "alpha"}}}
	program, err := newLopperStaveProgram(NewSummary(io.Discard, strings.NewReader(""), &stubAnalyzer{}, report.NewFormatter()), &opts, &view, nil)
	if err != nil {
		t.Fatal(err)
	}
	missingView := program.Initial
	missingView.view = nil
	if _, err := program.View(stave.ViewContext{}, missingView); err == nil || !strings.Contains(err.Error(), "report snapshot") {
		t.Fatalf("missing view error = %v", err)
	}
	missingOptions := program.Initial
	missingOptions.opts = nil
	if _, err := program.View(stave.ViewContext{}, missingOptions); err == nil || !strings.Contains(err.Error(), "options snapshot") {
		t.Fatalf("missing options error = %v", err)
	}
	if _, err := program.View(stave.ViewContext{Capabilities: capability.Manifest{Unicode: capability.UnicodeNone}}, program.Initial); err != nil {
		t.Fatalf("ASCII capability view failed: %v", err)
	}
}

func TestStaveProgramActionGuardOutcomes(t *testing.T) {
	runner := &staveNoResultRunner{}
	summary := NewSummary(io.Discard, strings.NewReader(""), &stubAnalyzer{}, report.NewFormatter())
	summary.Actions = runner
	opts := summary.applyDefaults(Options{Width: 80})
	view := summaryReportView{Dependencies: []summaryDependencyView{{Language: "go", Name: "alpha"}}}
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

	if err := invokeLopperAction(context.Background(), prepared, action.ID(staveActionApplyCodemod), map[string]any{"dependency": "go:alpha", "confirm": true, "allowDirty": false}, "guards", true); err == nil || !strings.Contains(err.Error(), "no safe codemod") {
		t.Fatalf("missing codemod result error = %v", err)
	}
	runner.applyErr = errors.New("apply failed")
	if err := invokeLopperAction(context.Background(), prepared, action.ID(staveActionApplyCodemod), map[string]any{"dependency": "go:alpha", "confirm": true, "allowDirty": false}, "guards-2", true); err == nil || !strings.Contains(err.Error(), "apply failed") {
		t.Fatalf("codemod backend error = %v", err)
	}
	if err := invokeLopperAction(context.Background(), prepared, action.ID(staveActionSaveBaseline), preparedLopperActionArgs(t, prepared, action.ID(staveActionSaveBaseline), map[string]any{"label": "nightly", "store": t.TempDir()}), "guards", false); err == nil || !strings.Contains(err.Error(), "save failed") {
		t.Fatalf("baseline save backend error = %v", err)
	}
	if err := invokeLopperAction(context.Background(), prepared, action.ID("lopper.summary.sort.v1"), map[string]any{"value": "invalid"}, "guards", false); err == nil || !strings.Contains(err.Error(), "invalid Sort value") {
		t.Fatalf("invalid sort error = %v", err)
	}
	if err := invokeLopperAction(context.Background(), prepared, action.ID("lopper.summary.page.v1"), map[string]any{"value": "zero"}, "guards", false); err == nil || !strings.Contains(err.Error(), "invalid Page value") {
		t.Fatalf("invalid page error = %v", err)
	}
}

func TestStaveProgramArgumentPreparationAndInvocationGuards(t *testing.T) {
	if _, err := staveActionOptions(Options{}, map[string]any{}); err == nil {
		t.Fatal("missing current options accepted")
	}
	if got, err := prepareLopperActionArgs(staveSummaryModel{}, action.ID(staveActionOpen), map[string]any{"dependency": "go:a"}); err != nil || got == nil {
		t.Fatalf("non-baseline arguments changed: %#v %v", got, err)
	}
	if _, err := prepareLopperActionArgs(staveSummaryModel{}, action.ID(staveActionRefresh), map[string]any{}); err == nil || !strings.Contains(err.Error(), "options snapshot") {
		t.Fatalf("nil model options error = %v", err)
	}
	if _, err := prepareLopperActionArgs(staveSummaryModel{opts: &Options{}}, action.ID(staveActionRefresh), "bad"); err == nil || !strings.Contains(err.Error(), "arguments must be an object") {
		t.Fatalf("scalar action arguments error = %v", err)
	}

	view := summaryReportView{}
	opts := Options{Width: 80}
	program, err := newLopperStaveProgram(NewSummary(io.Discard, strings.NewReader(""), &stubAnalyzer{}, report.NewFormatter()), &opts, &view, nil)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := program.NewSession(context.Background(), staveSessionOptions(opts, false))
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Session.Close()
	if _, err := invokeLopperActionResult(context.Background(), prepared, action.ID(staveActionQuit), map[string]any{}, "guards", false); err != nil {
		t.Fatalf("generated call ID invocation failed: %v", err)
	}
	if _, err := invokeLopperActionResult(context.Background(), prepared, action.ID(staveActionQuit), math.NaN(), "guards", false); err == nil {
		t.Fatal("non-JSON arguments accepted")
	}
}

func TestStaveProgramUnavailableServicesAndBaselineBuildErrors(t *testing.T) {
	ctx := context.Background()
	opts := Options{RepoPath: t.TempDir(), Width: 80}
	view := summaryReportView{Dependencies: []summaryDependencyView{{Language: "go", Name: "alpha"}}}
	program, err := newLopperStaveProgram(nil, &opts, &view, nil)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := program.NewSession(ctx, staveSessionOptions(opts, false))
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Session.Close()
	current := map[string]any{"currentBaselinePath": "", "currentBaselineStore": "", "currentBaselineKey": ""}
	cases := []struct {
		id      action.ID
		args    map[string]any
		confirm bool
	}{
		{action.ID(staveActionRefresh), current, false},
		{action.ID(staveActionOpen), map[string]any{"dependency": "go:alpha"}, false},
		{action.ID(staveActionApplyCodemod), map[string]any{"dependency": "go:alpha", "confirm": true, "allowDirty": false}, true},
		{action.ID(staveActionCompareBaseline), current, false},
	}
	for i, tc := range cases {
		if err := invokeLopperAction(ctx, prepared, tc.id, tc.args, fmt.Sprintf("unavailable-%d", i), tc.confirm); err == nil {
			t.Errorf("%s unexpectedly succeeded without Summary service", tc.id)
		}
	}

	summary := NewSummary(io.Discard, strings.NewReader(""), &stubAnalyzer{}, report.NewFormatter())
	program, err = newLopperStaveProgram(summary, &opts, &view, nil)
	if err != nil {
		t.Fatal(err)
	}
	withoutRunner, err := program.NewSession(ctx, staveSessionOptions(opts, false))
	if err != nil {
		t.Fatal(err)
	}
	defer withoutRunner.Session.Close()
	saveArgs := map[string]any{"label": "nightly", "currentBaselinePath": "", "currentBaselineStore": "", "currentBaselineKey": ""}
	if err := invokeLopperAction(ctx, withoutRunner, action.ID(staveActionSaveBaseline), saveArgs, "no-runner", false); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("nil baseline runner error = %v", err)
	}

	summary.Actions = &staveNoResultRunner{}
	program, err = newLopperStaveProgram(summary, &opts, &view, nil)
	if err != nil {
		t.Fatal(err)
	}
	withRunner, err := program.NewSession(ctx, staveSessionOptions(opts, false))
	if err != nil {
		t.Fatal(err)
	}
	defer withRunner.Session.Close()
	if err := invokeLopperAction(ctx, withRunner, action.ID(staveActionSaveBaseline), current, "save-build", false); err == nil || !strings.Contains(err.Error(), "unable to resolve git commit") {
		t.Fatalf("save request build error = %v", err)
	}
	if err := invokeLopperAction(ctx, withRunner, action.ID(staveActionCompareBaseline), current, "compare-build", false); err == nil || !strings.Contains(err.Error(), "baseline key or file") {
		t.Fatalf("compare options build error = %v", err)
	}

	analyzeErr := errors.New("compare analysis failed")
	summary = NewSummary(io.Discard, strings.NewReader(""), &stubAnalyzer{err: analyzeErr}, report.NewFormatter())
	program, err = newLopperStaveProgram(summary, &opts, &view, nil)
	if err != nil {
		t.Fatal(err)
	}
	failingCompare, err := program.NewSession(ctx, staveSessionOptions(opts, false))
	if err != nil {
		t.Fatal(err)
	}
	defer failingCompare.Session.Close()
	compareArgs := map[string]any{"key": "nightly", "currentBaselinePath": "", "currentBaselineStore": t.TempDir(), "currentBaselineKey": ""}
	if err := invokeLopperAction(ctx, failingCompare, action.ID(staveActionCompareBaseline), compareArgs, "compare-analyze", false); err == nil || !strings.Contains(err.Error(), analyzeErr.Error()) {
		t.Fatalf("compare analysis error = %v", err)
	}
}
