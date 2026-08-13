package ui

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/stave"
	"github.com/ben-ranford/stave/action"
	"github.com/ben-ranford/stave/event"
	"github.com/ben-ranford/stave/layout"
)

type coverageProgramRunner struct {
	applyCalls  int
	applyReq    CodemodApplyRequest
	applyReport report.Report
	applyErr    error

	saveCalls  int
	saveReq    BaselineSaveRequest
	saveReport report.Report
	savePath   string
	saveErr    error
}

func (r *coverageProgramRunner) ApplyCodemod(_ context.Context, req CodemodApplyRequest) (report.Report, error) {
	r.applyCalls++
	r.applyReq = req
	return r.applyReport, r.applyErr
}

func (r *coverageProgramRunner) SaveBaseline(_ context.Context, req BaselineSaveRequest) (report.Report, string, error) {
	r.saveCalls++
	r.saveReq = req
	if r.saveErr != nil {
		return report.Report{}, "", r.saveErr
	}
	if r.savePath == "" {
		path, err := report.SaveSnapshot(req.BaselineStorePath, "coverage-baseline", r.saveReport, time.Unix(1, 0).UTC())
		if err != nil {
			return report.Report{}, "", err
		}
		r.savePath = path
		if _, err := os.Stat(r.savePath); err != nil {
			return report.Report{}, "", err
		}
	}
	return r.saveReport, r.savePath, nil
}

func mustDecodeObjectSchema(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("decode schema: %v", err)
	}
	return schema
}

func requireSchemaProperty(t *testing.T, schema map[string]any, key string) map[string]any {
	t.Helper()
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema missing properties: %#v", schema)
	}
	prop, ok := props[key].(map[string]any)
	if !ok {
		t.Fatalf("schema missing property %q: %#v", key, schema)
	}
	return prop
}

func TestStaveProgramConstructorViewAndRegistrySchemas(t *testing.T) {
	analyzer := &stubAnalyzer{
		report: report.Report{
			SchemaVersion: report.SchemaVersion,
			Dependencies: []report.DependencyReport{
				{Language: "go", Name: "alpha", UsedPercent: 12.5, EstimatedUnusedBytes: 3},
				{Language: "js", Name: "beta", UsedPercent: 4.5, EstimatedUnusedBytes: 1},
			},
		},
	}
	summary := NewSummary(io.Discard, strings.NewReader(""), analyzer, report.NewFormatter())
	opts := summary.applyDefaults(Options{RepoPath: ".", Width: 96})
	view := summaryReportView{Dependencies: []summaryDependencyView{
		{Language: "go", Name: "alpha", UsedPercent: 12.5, EstimatedUnusedBytes: 3},
		{Language: "js", Name: "beta", UsedPercent: 4.5, EstimatedUnusedBytes: 1},
	}}

	program, err := newLopperStaveProgram(summary, &opts, &view, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := program.Initial.interaction.viewport.Width; got != 96 {
		t.Fatalf("initial width = %d", got)
	}
	if got := program.Initial.interaction.summary; got.page != 1 || got.pageSize != 10 || got.sortMode != sortByWaste {
		t.Fatalf("default initial summary drifted: %+v", got)
	}

	hashA, err := program.ModelPolicy.Hash(program.Initial)
	if err != nil {
		t.Fatal(err)
	}
	hashB, err := program.ModelPolicy.Hash(program.Initial)
	if err != nil {
		t.Fatal(err)
	}
	if hashA != hashB {
		t.Fatal("equal models hashed differently")
	}
	var zeroHash [32]byte
	if hashA == zeroHash {
		t.Fatal("hash collapsed to zero value")
	}

	tree, err := program.View(stave.ViewContext{}, program.Initial)
	if err != nil {
		t.Fatal(err)
	}
	if tree.Root().Description() == "" {
		t.Fatal("view tree root was empty")
	}

	manifest := program.Actions.Manifest()
	if len(manifest) != 10 {
		t.Fatalf("manifest length = %d", len(manifest))
	}

	cases := map[action.ID]struct {
		title   string
		safety  action.Safety
		idem    action.Idempotency
		confirm bool
		input   string
	}{
		action.ID(staveActionQuit):            {title: "Quit", safety: action.ReadOnly, idem: action.Idempotent, input: `{"type":"object","additionalProperties":false}`},
		action.ID(staveActionRefresh):         {title: "Refresh", safety: action.ReadOnly, idem: action.Idempotent, input: `{"type":"object","additionalProperties":false,"properties":{"currentBaselinePath":{"type":"string"},"currentBaselineStore":{"type":"string"},"currentBaselineKey":{"type":"string"}},"required":["currentBaselinePath","currentBaselineStore","currentBaselineKey"]}`},
		action.ID(staveActionOpen):            {title: "Open dependency", safety: action.ReadOnly, idem: action.Idempotent, input: `{"type":"object","additionalProperties":false,"properties":{"dependency":{"type":"string","minLength":1}},"required":["dependency"]}`},
		action.ID(staveActionApplyCodemod):    {title: "Apply codemod", safety: action.Consequential, idem: action.NonIdempotent, confirm: true, input: `{"type":"object","additionalProperties":false,"properties":{"dependency":{"type":"string","minLength":1},"confirm":{"type":"boolean"},"allowDirty":{"type":"boolean"}},"required":["dependency","confirm","allowDirty"]}`},
		action.ID(staveActionSaveBaseline):    {title: "Save baseline", safety: action.Reversible, idem: action.Idempotent, input: `{"type":"object","additionalProperties":false,"properties":{"label":{"type":"string"},"key":{"type":"string"},"store":{"type":"string"},"file":{"type":"string"},"target":{"type":"string"},"currentBaselinePath":{"type":"string"},"currentBaselineStore":{"type":"string"},"currentBaselineKey":{"type":"string"}},"required":["currentBaselinePath","currentBaselineStore","currentBaselineKey"]}`},
		action.ID(staveActionCompareBaseline): {title: "Compare baseline", safety: action.Reversible, idem: action.Idempotent, input: `{"type":"object","additionalProperties":false,"properties":{"label":{"type":"string"},"key":{"type":"string"},"store":{"type":"string"},"file":{"type":"string"},"target":{"type":"string"},"currentBaselinePath":{"type":"string"},"currentBaselineStore":{"type":"string"},"currentBaselineKey":{"type":"string"}},"required":["currentBaselinePath","currentBaselineStore","currentBaselineKey"]}`},
		action.ID("lopper.summary.filter.v1"): {title: "Filter", safety: action.Reversible, idem: action.Idempotent, input: `{"type":"object","additionalProperties":false,"properties":{"value":{"type":"string","minLength":1}},"required":["value"]}`},
		action.ID("lopper.summary.sort.v1"):   {title: "Sort", safety: action.Reversible, idem: action.Idempotent, input: `{"type":"object","additionalProperties":false,"properties":{"value":{"type":"string","minLength":1}},"required":["value"]}`},
		action.ID("lopper.summary.page.v1"):   {title: "Page", safety: action.Reversible, idem: action.Idempotent, input: `{"type":"object","additionalProperties":false,"properties":{"value":{"type":"string","minLength":1}},"required":["value"]}`},
		action.ID("lopper.summary.size.v1"):   {title: "Page size", safety: action.Reversible, idem: action.Idempotent, input: `{"type":"object","additionalProperties":false,"properties":{"value":{"type":"string","minLength":1}},"required":["value"]}`},
	}
	for id, want := range cases {
		def, ok := program.Actions.Definition(id)
		if !ok {
			t.Fatalf("missing action definition for %s", id)
		}
		if def.Title != want.title || def.Safety != want.safety || def.Idempotency != want.idem {
			t.Fatalf("%s definition mismatch: %+v", id, def)
		}
		if def.OutputSchema.ID != "lopper.action.output" || len(def.OutputSchema.JSON) == 0 {
			t.Fatalf("%s output schema drifted: %+v", id, def.OutputSchema)
		}
		if want.confirm {
			if !def.Confirmation.Required || !def.Confirmation.SingleUse {
				t.Fatalf("%s confirmation policy drifted: %+v", id, def.Confirmation)
			}
		}
		if got := string(def.InputSchema.JSON); got != want.input {
			t.Fatalf("%s input schema drifted:\nwant %s\n got %s", id, want.input, got)
		}
		schema := mustDecodeObjectSchema(t, def.InputSchema.JSON)
		if schema["type"] != "object" || schema["additionalProperties"] != false {
			t.Fatalf("%s schema shape changed: %#v", id, schema)
		}
		if want.title == "Open dependency" {
			prop := requireSchemaProperty(t, schema, "dependency")
			if prop["type"] != "string" || prop["minLength"] != float64(1) {
				t.Fatalf("open schema dependency drifted: %#v", prop)
			}
		}
	}
}

func TestStaveProgramHandlersAndReducerBranches(t *testing.T) {
	ctx := context.Background()
	store := t.TempDir()
	refreshAnalyzer := &stubAnalyzer{
		report: report.Report{
			SchemaVersion: report.SchemaVersion,
			Dependencies: []report.DependencyReport{
				{Language: "go", Name: "alpha", UsedPercent: 50, EstimatedUnusedBytes: 10},
			},
		},
	}
	refreshSummary := NewSummary(io.Discard, strings.NewReader(""), refreshAnalyzer, report.NewFormatter())
	runner := &coverageProgramRunner{
		applyReport: report.Report{
			SchemaVersion: report.SchemaVersion,
			Dependencies: []report.DependencyReport{
				{
					Language: "go",
					Name:     "alpha",
					Codemod:  &report.CodemodReport{Apply: &report.CodemodApplyReport{AppliedFiles: 1, AppliedPatches: 2}},
				},
			},
		},
		saveReport: report.Report{
			SchemaVersion: report.SchemaVersion,
			Dependencies:  []report.DependencyReport{{Language: "go", Name: "alpha", UsedExportsCount: 1, TotalExportsCount: 2, UsedPercent: 50, EstimatedUnusedBytes: 10}},
		},
	}
	refreshSummary.Actions = runner
	opts := refreshSummary.applyDefaults(Options{RepoPath: ".", Width: 80})
	view := summaryReportView{Dependencies: []summaryDependencyView{
		{Language: "go", Name: "alpha", UsedPercent: 50, EstimatedUnusedBytes: 10},
	}}
	state := summaryState{page: 2, pageSize: 1, sortMode: sortByWaste, selectedDependency: "go:beta"}

	program, err := newLopperStaveProgram(refreshSummary, &opts, &view, &state)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := program.NewSession(ctx, staveSessionOptions(opts, false))
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Session.Close()

	if _, err := completeLopperAction(ctx, t, prepared, action.ID(staveActionRefresh), map[string]any{}, "coverage", "refresh", false); err != nil {
		t.Fatalf("refresh success failed: %v", err)
	}
	snap, err := prepared.Session.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if snap.Model.view == nil || len(snap.Model.view.Dependencies) != 1 || snap.Model.interaction.summary.page != 1 || snap.Model.interaction.summary.selectedDependency != "" {
		t.Fatalf("refresh did not replace session view or clamp stale interaction: view=%+v state=%+v", snap.Model.view, snap.Model.interaction.summary)
	}

	model := program.Initial
	ev, err := event.New(event.ActionInvoked, event.ActionInvokedPayload{CallID: "refresh", ActionID: staveActionRefresh, Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	model, _, err = reduceStaveSummary(stave.ReduceContext{}, model, ev)
	if err != nil {
		t.Fatal(err)
	}
	if model.interaction.status != "Pending lopper.summary.refresh.v1" || model.interaction.summary.selectedDependency != "go:beta" {
		t.Fatalf("refresh reducer mutated interaction before completion: model=%+v state=%+v", model.interaction, state)
	}

	if _, err := completeLopperAction(ctx, t, prepared, action.ID(staveActionOpen), map[string]any{"dependency": "go:alpha"}, "coverage", "open", false); err != nil {
		t.Fatalf("open success failed: %v", err)
	}
	snap, err = prepared.Session.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if snap.Model.interaction.summary.selectedDependency != "go:alpha" {
		t.Fatalf("open did not record selected dependency: %q", snap.Model.interaction.summary.selectedDependency)
	}

	nilSummaryProgram, err := newLopperStaveProgram(nil, &opts, &view, &state)
	if err != nil {
		t.Fatal(err)
	}
	nilPrepared, err := nilSummaryProgram.NewSession(ctx, staveSessionOptions(opts, false))
	if err != nil {
		t.Fatal(err)
	}
	defer nilPrepared.Session.Close()
	if err := invokeLopperAction(ctx, nilPrepared, action.ID(staveActionOpen), map[string]any{"dependency": "go:alpha"}, "coverage", false); err == nil || !strings.Contains(err.Error(), "invalid open action input") {
		t.Fatalf("open nil-summary guard missing: %v", err)
	}

	if err := invokeLopperAction(ctx, nilPrepared, action.ID(staveActionRefresh), preparedLopperActionArgs(t, nilPrepared, action.ID(staveActionRefresh), map[string]any{}), "coverage", false); err == nil || !strings.Contains(err.Error(), "refresh action is unavailable") {
		t.Fatalf("refresh nil-summary guard missing: %v", err)
	}

	if err := invokeLopperAction(ctx, prepared, action.ID(staveActionApplyCodemod), map[string]any{"dependency": "go:alpha", "confirm": false, "allowDirty": false}, "coverage", false); err == nil || !strings.Contains(err.Error(), "CONFIRMATION_REQUIRED") {
		t.Fatalf("unconfirmed codemod should have been rejected: %v", err)
	}
	if runner.applyCalls != 0 {
		t.Fatalf("unconfirmed codemod reached handler: %+v", runner.applyReq)
	}
	if err := invokeLopperAction(ctx, prepared, action.ID(staveActionApplyCodemod), map[string]any{"dependency": "go:alpha", "confirm": false, "allowDirty": true}, "coverage", true); err == nil || !strings.Contains(err.Error(), "requires --confirm") {
		t.Fatalf("codemod confirmation-sensitive branch missing: %v", err)
	}
	if runner.applyCalls != 0 {
		t.Fatalf("codemod confirmation-sensitive branch should not have applied: %+v", runner.applyReq)
	}
	if _, err := completeLopperAction(ctx, t, prepared, action.ID(staveActionApplyCodemod), map[string]any{"dependency": "go:alpha", "confirm": true, "allowDirty": true}, "coverage", "codemod", true); err != nil {
		t.Fatalf("confirmed codemod failed: %v", err)
	}
	if runner.applyCalls != 1 || runner.applyReq.Dependency != "alpha" || !runner.applyReq.AllowDirty {
		t.Fatalf("confirmed codemod request drifted: %+v", runner.applyReq)
	}

	if _, err := completeLopperAction(ctx, t, prepared, action.ID(staveActionSaveBaseline), map[string]any{"label": "nightly", "store": store}, "coverage", "save", false); err != nil {
		t.Fatalf("save baseline success failed: %v", err)
	}
	if runner.saveCalls != 1 || runner.saveReq.BaselineLabel != "nightly" || runner.saveReq.BaselineStorePath != store {
		t.Fatalf("save baseline request drifted: %+v", runner.saveReq)
	}
	if _, err := os.Stat(runner.savePath); err != nil {
		t.Fatalf("save baseline path not recorded: %q: %v", runner.savePath, err)
	}

	compareView := summaryReportView{}
	compareState := summaryState{page: 1, pageSize: 1}
	compareAnalyzer := &stubAnalyzer{
		report: report.Report{
			SchemaVersion: report.SchemaVersion,
			Dependencies:  []report.DependencyReport{{Language: "go", Name: "alpha", UsedExportsCount: 1, TotalExportsCount: 2, UsedPercent: 50, EstimatedUnusedBytes: 10}},
		},
	}
	compareSummary := NewSummary(io.Discard, strings.NewReader(""), compareAnalyzer, report.NewFormatter())
	compareSummary.Actions = runner
	compareOpts := compareSummary.applyDefaults(Options{RepoPath: ".", BaselineStorePath: store, Width: 80})
	compareProgram, err := newLopperStaveProgram(compareSummary, &compareOpts, &compareView, &compareState)
	if err != nil {
		t.Fatal(err)
	}
	comparePrepared, err := compareProgram.NewSession(ctx, staveSessionOptions(compareOpts, false))
	if err != nil {
		t.Fatal(err)
	}
	defer comparePrepared.Session.Close()
	if _, err := completeLopperAction(ctx, t, comparePrepared, action.ID(staveActionCompareBaseline), map[string]any{"file": runner.savePath}, "coverage", "compare", false); err != nil {
		t.Fatalf("compare baseline success failed: %v", err)
	}
	badCompareSummary := NewSummary(io.Discard, strings.NewReader(""), &stubAnalyzer{report: report.Report{SchemaVersion: report.SchemaVersion}}, report.NewFormatter())
	badCompareSummary.Actions = runner
	badCompareOut := &bytes.Buffer{}
	badCompareSummary.Out = badCompareOut
	badCompareOpts := badCompareSummary.applyDefaults(Options{RepoPath: t.TempDir(), BaselineStorePath: store, Width: 80})
	badCompareProgram, err := newLopperStaveProgram(badCompareSummary, &badCompareOpts, &summaryReportView{}, &summaryState{page: 1, pageSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	badComparePrepared, err := badCompareProgram.NewSession(ctx, staveSessionOptions(badCompareOpts, false))
	if err != nil {
		t.Fatal(err)
	}
	defer badComparePrepared.Session.Close()
	if err := invokeLopperAction(ctx, badComparePrepared, action.ID(staveActionCompareBaseline), preparedLopperActionArgs(t, badComparePrepared, action.ID(staveActionCompareBaseline), map[string]any{"file": filepath.Join(t.TempDir(), "missing.json")}), "coverage", false); err == nil || !strings.Contains(err.Error(), "file does not exist") {
		t.Fatalf("compare baseline missing-file failure was not surfaced: %v", err)
	}
	// Typed action callers receive the error directly; the registry no longer
	// writes legacy summary text as a side effect.
	if err := invokeLopperAction(ctx, badComparePrepared, action.ID(staveActionSaveBaseline), preparedLopperActionArgs(t, badComparePrepared, action.ID(staveActionSaveBaseline), map[string]any{}), "coverage", false); err == nil || !strings.Contains(err.Error(), "label or --key") {
		t.Fatalf("save baseline without label or key did not fail honestly: %v", err)
	}
	if badCompareOut.Len() != 0 {
		t.Fatalf("typed actions wrote directly to terminal: %q", badCompareOut.String())
	}

	if err := invokeLopperAction(ctx, prepared, action.ID("missing"), map[string]any{}, "coverage", false); err == nil || !strings.Contains(err.Error(), "is not registered") {
		t.Fatalf("missing action guard missing: %v", err)
	}
	if err := invokeLopperAction(ctx, prepared, action.ID(staveActionOpen), []any{}, "coverage", false); err == nil || !strings.Contains(err.Error(), "INVALID_ARGUMENT") {
		t.Fatalf("schema validation failure not surfaced: %v", err)
	}
	if err := invokeLopperAction(ctx, prepared, action.ID(staveActionOpen), math.NaN(), "coverage", false); err == nil || !strings.Contains(err.Error(), "unsupported value: NaN") {
		t.Fatalf("marshal failure not surfaced: %v", err)
	}
}

func TestStaveModelHashAndEventBranches(t *testing.T) {
	view := &summaryReportView{Dependencies: []summaryDependencyView{{Language: "go", Name: "alpha"}}}
	model := newStaveSummaryModel(view, nil, summaryState{page: 2, pageSize: 1, sortMode: sortByWaste, selectedDependency: "go:beta"})

	hashA, err := hashStaveSummaryModel(model)
	if err != nil {
		t.Fatal(err)
	}
	hashB, err := hashStaveSummaryModel(model)
	if err != nil {
		t.Fatal(err)
	}
	if hashA != hashB {
		t.Fatal("equal models hashed differently")
	}

	model.interaction.pendingConfirm = "confirm?"
	model, _, err = reduceStaveSummary(stave.ReduceContext{}, model, event.Event{Kind: event.ActionInvoked, Payload: event.ActionInvokedPayload{CallID: "c1", ActionID: "test.action"}})
	if err != nil {
		t.Fatal(err)
	}
	model, _, err = reduceStaveSummary(stave.ReduceContext{}, model, event.Event{Kind: event.EffectResult, Payload: event.EffectResultPayload{CallID: "c1", Status: "done", Value: map[string]any{"version": "lopper.action-result/v1", "action": "test.action", "value": map[string]any{}}}})
	if err != nil {
		t.Fatal(err)
	}
	if model.interaction.pendingConfirm != "" || model.interaction.status == "" || model.interaction.error != "" {
		t.Fatalf("effect success did not update state: %+v", model.interaction)
	}

	model.interaction.pendingConfirm = "confirm?"
	model, _, err = reduceStaveSummary(stave.ReduceContext{}, model, event.Event{Kind: event.ActionInvoked, Payload: event.ActionInvokedPayload{CallID: "c2", ActionID: "test.action"}})
	if err != nil {
		t.Fatal(err)
	}
	model, _, err = reduceStaveSummary(stave.ReduceContext{}, model, event.Event{Kind: event.EffectResult, Payload: event.EffectResultPayload{CallID: "c2", Status: "error", Error: "backend failed"}})
	if err != nil {
		t.Fatal(err)
	}
	if model.interaction.pendingConfirm != "" || model.interaction.error != "backend failed" {
		t.Fatalf("effect error did not preserve error: %+v", model.interaction)
	}

	model.interaction.commandMode = true
	model.interaction.filterBuffer = "filter beta"
	model, _, err = reduceStaveSummary(stave.ReduceContext{}, model, event.Event{Kind: event.Key, Payload: event.KeyPayload{Key: "enter"}})
	if err != nil {
		t.Fatal(err)
	}
	if model.interaction.commandMode || model.interaction.summary.filter != "" || model.interaction.summary.page != 2 {
		t.Fatalf("command enter mutated state: %+v", model.interaction)
	}

	model.interaction.commandMode = true
	model.interaction.filterBuffer = "noop"
	model, _, err = reduceStaveSummary(stave.ReduceContext{}, model, event.Event{Kind: event.Key, Payload: event.KeyPayload{Key: "enter"}})
	if err != nil {
		t.Fatal(err)
	}
	if model.interaction.commandMode || model.interaction.filterBuffer != "noop" {
		t.Fatalf("non-filter enter should just exit command mode: %+v", model.interaction)
	}

	model.interaction.commandMode = true
	model.interaction.filterBuffer = "filter beta"
	model, _, err = reduceStaveSummary(stave.ReduceContext{}, model, event.Event{Kind: event.Key, Payload: event.KeyPayload{Key: "escape"}})
	if err != nil {
		t.Fatal(err)
	}
	if model.interaction.commandMode || model.interaction.filterBuffer != "" || model.interaction.status != "cancelled" {
		t.Fatalf("escape in command mode did not cancel: %+v", model.interaction)
	}

	model.interaction.selectedRow = 9
	model.interaction.focusPane = "summary"
	model, _, err = reduceStaveSummary(stave.ReduceContext{}, model, event.Event{Kind: event.Key, Payload: event.KeyPayload{Key: "tab"}})
	if err != nil {
		t.Fatal(err)
	}
	if model.interaction.focusPane != "detail" {
		t.Fatalf("tab did not switch to detail pane: %q", model.interaction.focusPane)
	}

	model, _, err = reduceStaveSummary(stave.ReduceContext{}, model, event.Event{Kind: event.ActionInvoked, Payload: event.ActionInvokedPayload{CallID: "refresh", ActionID: staveActionRefresh, Arguments: map[string]any{}}})
	if err != nil {
		t.Fatal(err)
	}
	model, _, err = reduceStaveSummary(stave.ReduceContext{}, model, event.Event{Kind: event.EffectResult, Payload: event.EffectResultPayload{CallID: "refresh", Status: "done", Value: map[string]any{"version": "lopper.action-result/v1", "action": staveActionRefresh, "value": map[string]any{"refreshed": true, "report": map[string]any{"dependencies": []any{}}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if model.interaction.pendingCallID != "" || model.interaction.status != "Refreshed" {
		t.Fatalf("refresh action left pending/shared mutation: %+v", model.interaction)
	}

	prevStatus := model.interaction.status
	model, _, err = reduceStaveSummary(stave.ReduceContext{}, model, event.Event{Kind: event.ActionInvoked, Payload: "bad"})
	if err != nil {
		t.Fatal(err)
	}
	if model.interaction.error != "" || model.interaction.status != prevStatus {
		t.Fatalf("bad action payload should be ignored: %+v", model.interaction)
	}
}

func TestStaveModelHashIncludesEveryValueField(t *testing.T) {
	color := true
	base := staveSummaryModel{opts: &Options{RepoPath: ".", Language: "go", Filter: "f", Sort: "name", BaselinePath: "base", BaselineStorePath: "store", BaselineKey: "key", TopN: 2, PageSize: 3, Width: 80, ASCII: true, UseStavePreview: true, Color: &color}, view: &summaryReportView{Dependencies: []summaryDependencyView{{Language: "go", Name: "alpha"}}, Warnings: []string{"warn"}}, interaction: staveSummaryInteraction{summary: summaryState{filter: "f", sortMode: sortByName, page: 2, pageSize: 3, showHelp: true, selectedDependency: "go:x"}, selectedRow: 1, focusPane: "detail", commandMode: true, filterBuffer: "filter f", viewport: layout.Size{Width: 80, Height: 24}, help: true, status: "ok", error: "err", pendingConfirm: "confirm", pendingCallID: "call", pendingActionID: "action", quit: true}}
	h, err := hashStaveSummaryModel(base)
	if err != nil {
		t.Fatal(err)
	}
	mutate := []func(*staveSummaryModel){
		func(m *staveSummaryModel) { m.interaction.summary.filter += "x" },
		func(m *staveSummaryModel) { m.interaction.summary.sortMode = sortByWaste },
		func(m *staveSummaryModel) { m.interaction.summary.page++ },
		func(m *staveSummaryModel) { m.interaction.summary.pageSize++ },
		func(m *staveSummaryModel) { m.interaction.summary.showHelp = false },
		func(m *staveSummaryModel) { m.interaction.summary.selectedDependency += "x" },
		func(m *staveSummaryModel) { m.interaction.selectedRow++ },
		func(m *staveSummaryModel) { m.interaction.focusPane = "summary" },
		func(m *staveSummaryModel) { m.interaction.commandMode = false },
		func(m *staveSummaryModel) { m.interaction.filterBuffer += "x" },
		func(m *staveSummaryModel) { m.interaction.viewport.Width++ },
		func(m *staveSummaryModel) { m.interaction.viewport.Height++ },
		func(m *staveSummaryModel) { m.interaction.help = false },
		func(m *staveSummaryModel) { m.interaction.status += "x" },
		func(m *staveSummaryModel) { m.interaction.error += "x" },
		func(m *staveSummaryModel) { m.interaction.pendingConfirm += "x" },
		func(m *staveSummaryModel) { m.interaction.pendingCallID += "x" },
		func(m *staveSummaryModel) { m.interaction.pendingActionID += "x" },
		func(m *staveSummaryModel) { m.interaction.quit = false },
		func(m *staveSummaryModel) { m.opts.RepoPath += "x" },
		func(m *staveSummaryModel) { m.opts.TopN++ },
		func(m *staveSummaryModel) { *m.opts.Color = false },
		func(m *staveSummaryModel) { m.view.Dependencies[0].Name += "x" },
		func(m *staveSummaryModel) { m.view.Warnings[0] += "x" },
	}
	for i, change := range mutate {
		m := base
		change(&m)
		got, err := hashStaveSummaryModel(m)
		if err != nil {
			t.Fatal(err)
		}
		if got == h {
			t.Errorf("mutation %d did not change hash", i)
		}
	}
}
