package ui

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/stave/action"
	"github.com/ben-ranford/stave/event"
	"github.com/ben-ranford/stave/semantic"
)

type deepCoverageActionRunner struct {
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

func (r *deepCoverageActionRunner) ApplyCodemod(_ context.Context, req CodemodApplyRequest) (report.Report, error) {
	r.applyCalls++
	r.applyReq = req
	return r.applyReport, r.applyErr
}

func (r *deepCoverageActionRunner) SaveBaseline(_ context.Context, req BaselineSaveRequest) (report.Report, string, error) {
	r.saveCalls++
	r.saveReq = req
	if r.saveErr != nil {
		return report.Report{}, "", r.saveErr
	}
	if req.BaselineStorePath != "" {
		key := strings.TrimSpace(req.BaselineKey)
		if key == "" {
			key = "label:" + strings.TrimSpace(req.BaselineLabel)
		}
		path, err := report.SaveSnapshot(req.BaselineStorePath, key, r.saveReport, time.Unix(1, 0).UTC())
		if err != nil {
			return report.Report{}, "", err
		}
		r.savePath = path
		return r.saveReport, path, nil
	}
	return r.saveReport, "", nil
}

func TestActionRegistryAndProgramHandlerBranches(t *testing.T) {
	store := t.TempDir()
	baseline := report.Report{
		SchemaVersion: report.SchemaVersion,
		Dependencies: []report.DependencyReport{
			{Language: "go", Name: "alpha", UsedExportsCount: 1, TotalExportsCount: 2, UsedPercent: 50, EstimatedUnusedBytes: 1},
		},
	}
	current := report.Report{
		SchemaVersion: report.SchemaVersion,
		Dependencies: []report.DependencyReport{
			{Language: "go", Name: "alpha", UsedExportsCount: 2, TotalExportsCount: 4, UsedPercent: 50, EstimatedUnusedBytes: 2},
			{Language: "go", Name: "beta", UsedExportsCount: 3, TotalExportsCount: 5, UsedPercent: 60, EstimatedUnusedBytes: 3},
		},
	}
	baseline.Dependencies[0].Codemod = &report.CodemodReport{Apply: &report.CodemodApplyReport{AppliedFiles: 1, AppliedPatches: 1}}
	runner := &deepCoverageActionRunner{applyReport: baseline, saveReport: baseline}
	summary := NewSummary(io.Discard, strings.NewReader(""), &stubAnalyzer{report: current}, report.NewFormatter())
	summary.Actions = runner

	opts := summary.applyDefaults(Options{RepoPath: ".", BaselineStorePath: store, Width: 96})
	view := mapSummaryReportView(current)
	program, err := newLopperStaveProgram(summary, &opts, &view, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := program.Initial.interaction.viewport.Width; got != 96 {
		t.Fatalf("initial viewport width = %d", got)
	}
	if got := program.Initial.interaction.summary; got.page != 1 || got.pageSize != 10 || got.sortMode != sortByWaste {
		t.Fatalf("default initial state drifted: %+v", got)
	}

	prepared, err := program.NewSession(context.Background(), staveSessionOptions(opts, false))
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Session.Close()

	got, ok := prepared.Actions.Definition(action.ID(staveActionOpen))
	if !ok || got.ID != action.ID(staveActionOpen) {
		t.Fatalf("open definition not registered correctly: ok=%v def=%+v", ok, got)
	}

	if _, err := completeLopperAction(context.Background(), t, prepared, action.ID(staveActionOpen), map[string]any{"dependency": "go:alpha"}, "lopper-preview", "open", false); err != nil {
		t.Fatalf("open success path failed: %v", err)
	}
	snap, err := prepared.Session.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if got := snap.Model.interaction.summary.selectedDependency; got != "go:alpha" {
		t.Fatalf("open did not update the session model: %q", got)
	}

	if err := invokeLopperAction(context.Background(), prepared, action.ID(staveActionSaveBaseline), preparedLopperActionArgs(t, prepared, action.ID(staveActionSaveBaseline), map[string]any{"label": "nightly", "store": store}), "lopper-preview", false); err != nil {
		t.Fatalf("save baseline failed: %v", err)
	}
	if runner.saveCalls != 1 || runner.saveReq.BaselineStorePath != store || runner.saveReq.BaselineLabel != "nightly" {
		t.Fatalf("save baseline request not preserved: %+v", runner.saveReq)
	}
	if runner.savePath == "" {
		t.Fatal("save baseline did not write a snapshot path")
	}

	compareView := summaryReportView{}
	compareProgram, err := newLopperStaveProgram(summary, &opts, &compareView, nil)
	if err != nil {
		t.Fatal(err)
	}
	comparePrepared, err := compareProgram.NewSession(context.Background(), staveSessionOptions(opts, false))
	if err != nil {
		t.Fatal(err)
	}
	defer comparePrepared.Session.Close()
	if _, err := completeLopperAction(context.Background(), t, comparePrepared, action.ID(staveActionCompareBaseline), map[string]any{"file": runner.savePath}, "lopper-preview", "compare", false); err != nil {
		t.Fatalf("compare baseline failed: %v", err)
	}

	if err := invokeLopperAction(context.Background(), prepared, action.ID(staveActionApplyCodemod), map[string]any{"dependency": "go:alpha", "confirm": false, "allowDirty": false}, "lopper-preview", false); err == nil || !strings.Contains(err.Error(), "CONFIRMATION_REQUIRED") {
		t.Fatalf("codemod confirmation guard missing: %v", err)
	}
	if runner.applyCalls != 0 {
		t.Fatalf("unconfirmed codemod should not have run: %+v", runner.applyReq)
	}

	if _, err := completeLopperAction(context.Background(), t, prepared, action.ID(staveActionApplyCodemod), map[string]any{"dependency": "go:alpha", "confirm": true, "allowDirty": true}, "lopper-preview", "codemod", true); err != nil {
		t.Fatalf("confirmed codemod failed: %v", err)
	}
	if runner.applyCalls != 1 || runner.applyReq.Dependency != "alpha" || !runner.applyReq.AllowDirty || runner.applyReq.Language != "go" {
		t.Fatalf("confirmed codemod request not recorded: %+v", runner.applyReq)
	}

	nilSummaryProgram, err := newLopperStaveProgram(nil, &opts, &view, &summaryState{page: 7, pageSize: 1, sortMode: sortByWaste})
	if err != nil {
		t.Fatal(err)
	}
	nilPrepared, err := nilSummaryProgram.NewSession(context.Background(), staveSessionOptions(opts, false))
	if err != nil {
		t.Fatal(err)
	}
	defer nilPrepared.Session.Close()
	if err := invokeLopperAction(context.Background(), nilPrepared, action.ID(staveActionRefresh), preparedLopperActionArgs(t, nilPrepared, action.ID(staveActionRefresh), map[string]any{}), "lopper-preview", false); err == nil || !strings.Contains(err.Error(), "refresh action is unavailable") {
		t.Fatalf("refresh guard did not trigger: %v", err)
	}
	if err := invokeLopperAction(context.Background(), nilPrepared, action.ID(staveActionOpen), map[string]any{"dependency": "go:alpha"}, "lopper-preview", false); err == nil || !strings.Contains(err.Error(), "invalid open action input") {
		t.Fatalf("open guard did not trigger: %v", err)
	}

	if err := invokeLopperAction(context.Background(), prepared, action.ID("missing"), map[string]any{}, "lopper-preview", false); err == nil || !strings.Contains(err.Error(), "action missing is not registered") {
		t.Fatalf("missing action branch failed: %v", err)
	}
}

func TestActionRegistryRegistrationAndModelParityBranches(t *testing.T) {
	reg := action.NewRegistry()
	def := action.Definition{
		ID:           action.ID("registry.test"),
		Version:      "1",
		Title:        "Registry test",
		InputSchema:  action.Schema{ID: "registry.test.input", JSON: json.RawMessage(`{"type":"object","additionalProperties":false}`)},
		OutputSchema: action.Schema{ID: "registry.test.output", JSON: json.RawMessage(`{"type":"object","additionalProperties":true}`)},
		Safety:       action.ReadOnly,
		Idempotency:  action.Idempotent,
	}
	if err := reg.Register(def, func(context.Context, action.Call, any) (any, error) { return map[string]any{"ok": true}, nil }); err != nil {
		t.Fatalf("valid registry registration failed: %v", err)
	}
	if got, ok := reg.Definition(def.ID); !ok || got.ID != def.ID {
		t.Fatalf("registered action not discoverable: ok=%v def=%+v", ok, got)
	}
	if err := reg.Register(def, nil); err == nil || !strings.Contains(err.Error(), "nil handler") {
		t.Fatalf("nil handler registration did not fail: %v", err)
	}
	if err := reg.Register(def, func(context.Context, action.Call, any) (any, error) { return nil, nil }); err == nil || !strings.Contains(err.Error(), "duplicate action") {
		t.Fatalf("duplicate registration did not fail: %v", err)
	}

	view := &summaryReportView{Dependencies: []summaryDependencyView{{Language: "go", Name: "alpha", UsedPercent: 12.5, EstimatedUnusedBytes: 3}}}
	model := newStaveSummaryModel(view, nil, summaryState{page: 3, pageSize: 2, sortMode: sortByName})
	clone, err := cloneStaveSummaryModel(model)
	if err != nil {
		t.Fatal(err)
	}
	if clone.interaction != model.interaction {
		t.Fatalf("clone did not preserve value semantics: %#v != %#v", clone, model)
	}
	clone.interaction.summary.page = 9
	if model.interaction.summary.page != 3 {
		t.Fatalf("clone mutation leaked back into original: %#v", model.interaction.summary)
	}
	hashA, err := hashStaveSummaryModel(model)
	if err != nil {
		t.Fatal(err)
	}
	hashB, err := hashStaveSummaryModel(model)
	if err != nil {
		t.Fatal(err)
	}
	if hashA != hashB {
		t.Fatal("equal models produced different hashes")
	}
	var zeroHash [32]byte
	if hashA == zeroHash {
		t.Fatal("hash should not collapse to the zero value")
	}

	model.interaction.commandMode = true
	model.interaction.filterBuffer = "ab"
	reduceStaveKey(&model, event.KeyPayload{Key: "rune", Rune: 'x'})
	if model.interaction.filterBuffer != "abx" {
		t.Fatalf("rune input not appended in command mode: %q", model.interaction.filterBuffer)
	}
	reduceStaveKey(&model, event.KeyPayload{Key: "space"})
	if model.interaction.filterBuffer != "abx " {
		t.Fatalf("space input not appended in command mode: %q", model.interaction.filterBuffer)
	}
	reduceStaveKey(&model, event.KeyPayload{Key: "backspace"})
	if model.interaction.filterBuffer != "abx" {
		t.Fatalf("backspace did not remove the last rune: %q", model.interaction.filterBuffer)
	}
	model.interaction.filterBuffer = ""
	reduceStaveKey(&model, event.KeyPayload{Key: "delete"})
	if model.interaction.filterBuffer != "" {
		t.Fatalf("delete on an empty buffer should be a no-op: %q", model.interaction.filterBuffer)
	}
	reduceStaveKey(&model, event.KeyPayload{Key: "d", Modifiers: []string{"ctrl"}})
	if !model.interaction.quit {
		t.Fatal("ctrl+d did not quit")
	}

	model.interaction.quit = false
	model.interaction.commandMode = true
	model.interaction.filterBuffer = "noop"
	reduceStaveKey(&model, event.KeyPayload{Key: "enter"})
	if model.interaction.commandMode || model.interaction.filterBuffer != "noop" {
		t.Fatalf("non-filter enter should just exit command mode: %+v", model.interaction)
	}
	model.interaction.commandMode = true
	model.interaction.filterBuffer = "filter go"
	reduceStaveKey(&model, event.KeyPayload{Key: "escape"})
	if model.interaction.commandMode || model.interaction.filterBuffer != "" || model.interaction.status != "cancelled" {
		t.Fatalf("escape in command mode did not cancel edit: %+v", model.interaction)
	}
	reduceStaveKey(&model, event.KeyPayload{Key: "escape"})
	if !model.interaction.quit {
		t.Fatal("escape outside command mode did not quit")
	}

	model.interaction.quit = false
	model.interaction.help = false
	model.interaction.focusPane = "summary"
	model.interaction.summary.showHelp = false
	reduceStaveKey(&model, event.KeyPayload{Key: "?"})
	if !model.interaction.help || !model.interaction.summary.showHelp {
		t.Fatalf("help toggle did not sync the summary state: %+v", model.interaction)
	}
	reduceStaveKey(&model, event.KeyPayload{Key: "tab"})
	if model.interaction.focusPane != "summary" {
		t.Fatalf("tab without selected detail should remain summary: %q", model.interaction.focusPane)
	}
	reduceStaveKey(&model, event.KeyPayload{Key: "tab"})
	if model.interaction.focusPane != "summary" {
		t.Fatalf("tab should remain summary without detail: %q", model.interaction.focusPane)
	}
	reduceStaveKey(&model, event.KeyPayload{Key: "r"})
	if model.interaction.status != "refresh" {
		t.Fatalf("r did not set refresh status: %q", model.interaction.status)
	}

	model.interaction.summary.page = 3
	reduceStaveKey(&model, event.KeyPayload{Key: "left"})
	if model.interaction.summary.page != 2 {
		t.Fatalf("left did not decrement page: %d", model.interaction.summary.page)
	}
	reduceStaveKey(&model, event.KeyPayload{Key: "prev"})
	if model.interaction.summary.page != 1 {
		t.Fatalf("prev did not decrement page: %d", model.interaction.summary.page)
	}
	reduceStaveKey(&model, event.KeyPayload{Key: "p"})
	if model.interaction.summary.page != 1 {
		t.Fatalf("page should not go below one: %d", model.interaction.summary.page)
	}
	model.view = &summaryReportView{Dependencies: []summaryDependencyView{{Language: "go", Name: "alpha"}, {Language: "go", Name: "beta"}}}
	model.interaction.summary.page = 1
	model.interaction.summary.pageSize = 1
	reduceStaveKey(&model, event.KeyPayload{Key: "right"})
	if model.interaction.summary.page != 2 {
		t.Fatalf("right did not increment page: %d", model.interaction.summary.page)
	}
	reduceStaveKey(&model, event.KeyPayload{Key: "next"})
	if model.interaction.summary.page != 2 {
		t.Fatalf("next should clamp to the last page: %d", model.interaction.summary.page)
	}
	reduceStaveKey(&model, event.KeyPayload{Key: "n"})
	if model.interaction.summary.page != 2 {
		t.Fatalf("n should keep the clamped page: %d", model.interaction.summary.page)
	}

	model.interaction.selectedRow = 5
	model.view = &summaryReportView{}
	clampStaveSelection(&model)
	if model.interaction.selectedRow != 0 {
		t.Fatalf("empty dependency set should reset selection: %d", model.interaction.selectedRow)
	}
	model.view = &summaryReportView{Dependencies: []summaryDependencyView{{Language: "go", Name: "alpha"}, {Language: "go", Name: "beta"}}}
	model.interaction.selectedRow = 99
	clampStaveSelection(&model)
	if model.interaction.selectedRow != 0 {
		t.Fatalf("selection was not clamped to visible page row: %d", model.interaction.selectedRow)
	}

}

func TestStaveParityProjectionComparisonAndKnownGapBranches(t *testing.T) {
	tree := parityCoverageTree(t)
	frame, err := StaveParityProjection(tree, ParityCapabilities{Width: 88, ASCII: false, Color: true, Interactive: false})
	if err != nil {
		t.Fatal(err)
	}
	if frame.Page != 2 || frame.TotalPages != 7 || frame.PageSize != 11 {
		t.Fatalf("root content was not projected correctly: %+v", frame)
	}
	if len(frame.Rows) != 1 || frame.Rows[0].Identity != "go:alpha" || frame.Rows[0].Used != 12.5 || frame.Rows[0].Waste != 3 {
		t.Fatalf("row projection drifted: %+v", frame.Rows)
	}
	if len(frame.Warnings) != 1 || frame.Warnings[0] != "warning line" {
		t.Fatalf("warning projection drifted: %+v", frame.Warnings)
	}
	if len(frame.Actions) != 3 || frame.Actions[0].Name != "nested.open" || frame.Actions[1].Name != "open" || frame.Actions[2].Name != "refresh" {
		t.Fatalf("action discovery did not recurse and sort: %+v", frame.Actions)
	}

	want := ParityFrame{
		Rows: []ParityRow{
			{Identity: "go:alpha", Language: "go", Name: "alpha", Waste: 3, Used: 12.5},
		},
		Warnings:     []string{"warning line"},
		Page:         2,
		TotalPages:   7,
		PageSize:     11,
		Capabilities: ParityCapabilities{Width: 88, ASCII: false, Color: true, Interactive: true},
		Actions: []ParityAction{
			{Name: "missing.action", Supported: true},
			{Name: "custom.action", Supported: true},
			{Name: "default.action", Supported: true},
			{Name: "preview-only.action", Supported: false},
		},
	}
	got := ParityFrame{
		Rows: []ParityRow{
			{Identity: "js:beta", Language: "js", Name: "beta", Waste: 9, Used: 99.9},
			{Identity: "go:extra", Language: "go", Name: "extra", Waste: 1, Used: 1},
		},
		Warnings:     []string{"other warning"},
		Page:         3,
		TotalPages:   8,
		PageSize:     13,
		Capabilities: ParityCapabilities{Width: 80, ASCII: true, Color: false, Interactive: false},
		Actions: []ParityAction{
			{Name: "custom.action", Supported: false, GapReason: "custom gap"},
			{Name: "default.action", Supported: false},
			{Name: "preview-only.action", Supported: true},
			{Name: "unexpected.action", Supported: true},
		},
	}
	report := CompareParity(want, got)
	wantViolations := map[string]bool{
		"page": true, "total_pages": true, "page_size": true,
		"capabilities.width": true, "capabilities.ascii": true, "capabilities.color": true,
		"rows.length": true, "rows[0].identity": true, "rows[0].language": true, "rows[0].name": true, "rows[0].waste": true, "rows[0].used": true,
		"warnings": true,
	}
	for path := range wantViolations {
		if !hasDiffPath(report.Violations, path) {
			t.Fatalf("missing violation path %q in %+v", path, report.Violations)
		}
	}
	wantGaps := map[string]bool{
		"capabilities.interactive":    true,
		"actions.missing.action":      true,
		"actions.custom.action":       true,
		"actions.default.action":      true,
		"actions.preview-only.action": true,
		"actions.unexpected.action":   true,
	}
	for path := range wantGaps {
		if !hasDiffPath(report.CapabilityGaps, path) {
			t.Fatalf("missing capability gap %q in %+v", path, report.CapabilityGaps)
		}
	}
	if report := CompareParity(want, want); len(report.Violations) != 0 || len(report.CapabilityGaps) != 0 {
		t.Fatalf("equal frames should not differ: %+v", report)
	}

	violationsReport := ParityReport{Violations: []ParityDiff{{Path: "page"}}}
	if violationsReport.EqualWithKnownGaps(map[string]bool{"page": true}) {
		t.Fatal("violations should fail known-gap comparison")
	}
	unknownGapReport := ParityReport{CapabilityGaps: []ParityDiff{{Path: "actions.unknown"}}}
	if unknownGapReport.EqualWithKnownGaps(map[string]bool{"actions.other": true}) {
		t.Fatal("unknown capability gaps should fail")
	}
	duplicateGapReport := ParityReport{CapabilityGaps: []ParityDiff{{Path: "actions.duplicate"}, {Path: "actions.duplicate"}}}
	if duplicateGapReport.EqualWithKnownGaps(map[string]bool{"actions.duplicate": true}) {
		t.Fatal("duplicate capability gaps should fail")
	}
	missingGapReport := ParityReport{CapabilityGaps: []ParityDiff{{Path: "actions.missing"}}}
	if missingGapReport.EqualWithKnownGaps(map[string]bool{"actions.missing": true, "actions.extra": true}) {
		t.Fatal("missing known gaps should fail")
	}
	knownGapsReport := ParityReport{CapabilityGaps: []ParityDiff{{Path: "actions.missing"}, {Path: "actions.default"}}}
	if !knownGapsReport.EqualWithKnownGaps(map[string]bool{"actions.missing": true, "actions.default": true}) {
		t.Fatal("expected known gaps to be accepted")
	}
}

func parityCoverageTree(t *testing.T) semantic.Tree {
	t.Helper()
	nested, err := semantic.NewNode(semantic.NodeSpec{
		Key:        &semantic.NodeKey{AppNamespace: "test", View: "summary", Kind: "dependency", Entity: "go/nested", Slot: "main"},
		Generation: 1,
		Role:       "row",
		Name:       "nested",
		Value:      semantic.Value{Text: "go • 1% used • 2 bytes waste", HasValue: true},
		Actions:    []semantic.ActionRef{{ID: "nested.open"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	row, err := semantic.NewNode(semantic.NodeSpec{
		Key:        &semantic.NodeKey{AppNamespace: "test", View: "summary", Kind: "dependency", Entity: "go/alpha", Slot: "main"},
		Generation: 1,
		Role:       "row",
		Name:       "alpha",
		Value:      semantic.Value{Text: "go • 12.5% used • 3 bytes waste", HasValue: true},
		Children:   []semantic.Node{nested},
		Actions:    []semantic.ActionRef{{ID: "open"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	alert, err := semantic.NewNode(semantic.NodeSpec{
		Key:        &semantic.NodeKey{AppNamespace: "test", View: "summary", Kind: "message", Entity: "warning", Slot: "main"},
		Generation: 1,
		Role:       "alert",
		Name:       "Warning",
		Value:      semantic.Value{Text: "warning line", HasValue: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	text, err := semantic.NewNode(semantic.NodeSpec{
		Key:        &semantic.NodeKey{AppNamespace: "test", View: "summary", Kind: "message", Entity: "info", Slot: "main"},
		Generation: 1,
		Role:       "text",
		Name:       "Info",
		Value:      semantic.Value{Text: "ignored text", HasValue: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	badRow, err := semantic.NewNode(semantic.NodeSpec{
		Key:        &semantic.NodeKey{AppNamespace: "test", View: "summary", Kind: "dependency", Entity: "go/bad", Slot: "main"},
		Generation: 1,
		Role:       "row",
		Name:       "bad",
		Value:      semantic.Value{Text: "go • malformed", HasValue: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	root, err := semantic.NewNode(semantic.NodeSpec{
		Key:        &semantic.NodeKey{AppNamespace: "test", View: "summary", Kind: "application", Entity: "root", Slot: "main"},
		Generation: 1,
		Role:       "application",
		Name:       "Root",
		Value:      semantic.Value{Text: "page 2/7 • 2 dependencies • 11 page size • Stave preview", HasValue: true},
		Children:   []semantic.Node{row, alert, text, badRow},
		Actions:    []semantic.ActionRef{{ID: "refresh"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	tree, err := semantic.NewTree(1, root)
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

func hasDiffPath(diffs []ParityDiff, path string) bool {
	for _, diff := range diffs {
		if diff.Path == path {
			return true
		}
	}
	return false
}
