package ui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/ben-ranford/lopper/internal/featureflags"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/workspace"
	"github.com/ben-ranford/stave/action"
	"github.com/ben-ranford/stave/capability"
	"github.com/ben-ranford/stave/event"
	"github.com/ben-ranford/stave/replay"
	"github.com/ben-ranford/stave/semantic"
	"github.com/ben-ranford/stave/state"
)

func previewView(warnings []string, deps ...summaryDependencyView) summaryReportView {
	return summaryReportView{Warnings: warnings, Dependencies: deps}
}

func previewDep(language, name string, used float64, waste int64) summaryDependencyView {
	return summaryDependencyView{Language: language, Name: name, UsedPercent: used, EstimatedUnusedBytes: waste}
}

func previewFeatures(t *testing.T) featureflags.Set {
	t.Helper()
	set, err := featureflags.DefaultRegistry().Resolve(featureflags.ResolveOptions{Channel: featureflags.ChannelDev, Enable: []string{staveTUIFeature}})
	if err != nil {
		t.Fatal(err)
	}
	return set
}

func boolPtr(v bool) *bool { return &v }

func actualParity(t *testing.T, view summaryReportView, state summaryState, ascii bool) ParityFrame {
	t.Helper()
	sorted, paged, state, totalPages := runSummaryDependencyPipeline(view, state)
	tree, err := staveTree(view, sorted, paged, state, totalPages, ascii)
	if err != nil {
		t.Fatal(err)
	}
	display := buildSummaryDisplayView(view, sorted, paged)
	want := LegacyParityProjection(display, state, totalPages, ParityCapabilities{Width: 80, ASCII: ascii, Interactive: true})
	got, err := StaveParityProjection(tree, ParityCapabilities{Width: 80, ASCII: ascii})
	if err != nil {
		t.Fatal(err)
	}
	returnFrame := got
	report := CompareParity(want, got)
	known := map[string]bool{"capabilities.interactive": true}
	if !report.EqualWithKnownGaps(known) {
		t.Fatalf("parity report does not match the documented contract: %+v", report)
	}
	return returnFrame
}

func TestStaveParityActualASCIIEqualsLegacy(t *testing.T) {
	actualParity(t, previewView([]string{"warn; keep order"}, previewDep("go", "alpha", 50.125, 10), previewDep("js", "beta", 0.000000123, 99)), summaryState{page: 1, pageSize: 10, sortMode: sortByWaste}, true)
}

func TestStaveParityActualUnicodeEqualsLegacyAndPreservesUnicode(t *testing.T) {
	frame := actualParity(t, previewView([]string{"warn; keep order"}, previewDep("go", "café", 50.125, 10), previewDep("js", "βeta", 0.000000123, 99)), summaryState{page: 1, pageSize: 10, sortMode: sortByName}, false)
	if frame.Rows[0].Name != "café" || frame.Rows[1].Name != "βeta" {
		t.Fatalf("Unicode was not preserved: %+v", frame.Rows)
	}
}

func TestStaveParityWarningsRoundTripInOrderAndSemicolons(t *testing.T) {
	warnings := []string{"first; keep; semicolons", "second warning", "third; warning"}
	frame := actualParity(t, previewView(warnings, previewDep("go", "a", 1, 2)), summaryState{page: 1, pageSize: 10, sortMode: sortByWaste}, false)
	if !reflect.DeepEqual(frame.Warnings, warnings) {
		t.Fatalf("warnings changed: %#v", frame.Warnings)
	}
}

func TestStaveParityUsesProductionFilterSortAndPagePipeline(t *testing.T) {
	deps := []summaryDependencyView{
		previewDep("go", "zeta", 1, 100),
		previewDep("js", "ignored", 1, 200),
		previewDep("go", "alpha", 1, 50),
	}
	view := previewView(nil, deps...)
	frame := actualParity(t, view, summaryState{filter: "go", sortMode: sortByName, page: 2, pageSize: 1}, false)
	if frame.Page != 2 || frame.TotalPages != 2 || frame.PageSize != 1 {
		t.Fatalf("production paging was not projected: %+v", frame)
	}
	if len(frame.Rows) != 1 || frame.Rows[0].Identity != "go:zeta" {
		t.Fatalf("production filter/sort/page result changed: %+v", frame.Rows)
	}
}

func TestStaveParityDiscoversActionsRecursively(t *testing.T) {
	child, err := semantic.NewNode(semantic.NodeSpec{Key: &semantic.NodeKey{AppNamespace: "test", View: "v", Kind: "row", Entity: "child", Slot: "main"}, Generation: 1, Role: "row", Name: "Child", Description: "go | 1% used | 2 bytes waste", Actions: []semantic.ActionRef{{ID: "open"}}})
	if err != nil {
		t.Fatal(err)
	}
	root, err := semantic.NewNode(semantic.NodeSpec{Key: &semantic.NodeKey{AppNamespace: "test", View: "v", Kind: "application", Entity: "root", Slot: "main"}, Generation: 1, Role: "application", Name: "Root", Description: "page 1/1 | 1 dependencies | 10 page size", Children: []semantic.Node{child}, Actions: []semantic.ActionRef{{ID: "refresh"}}})
	if err != nil {
		t.Fatal(err)
	}
	tree, err := semantic.NewTree(1, root)
	if err != nil {
		t.Fatal(err)
	}
	frame, err := StaveParityProjection(tree, ParityCapabilities{})
	if err != nil {
		t.Fatal(err)
	}
	want := []ParityAction{{Name: "open", Supported: true}, {Name: "refresh", Supported: true}}
	if !reflect.DeepEqual(frame.Actions, want) {
		t.Fatalf("recursive actions changed: got %+v want %+v", frame.Actions, want)
	}
}

func TestCompareParityDetectsEverySemanticField(t *testing.T) {
	base := ParityFrame{Rows: []ParityRow{{Identity: "go:a", Language: "go", Name: "a", Used: 1.25, Waste: 2}}, Warnings: []string{"w;1"}, Page: 1, TotalPages: 2, PageSize: 10, Capabilities: ParityCapabilities{Width: 80, ASCII: false, Color: true}}
	cases := []struct {
		name   string
		mutate func(*ParityFrame)
	}{
		{"row order", func(f *ParityFrame) {
			f.Rows = append(f.Rows, ParityRow{Identity: "go:b"})
			f.Rows[0], f.Rows[1] = f.Rows[1], f.Rows[0]
		}},
		{"identity", func(f *ParityFrame) { f.Rows[0].Identity = "go:x" }},
		{"language", func(f *ParityFrame) { f.Rows[0].Language = "js" }},
		{"name", func(f *ParityFrame) { f.Rows[0].Name = "x" }},
		{"used", func(f *ParityFrame) { f.Rows[0].Used = 9 }},
		{"waste", func(f *ParityFrame) { f.Rows[0].Waste = 9 }},
		{"page", func(f *ParityFrame) { f.Page = 2 }},
		{"total pages", func(f *ParityFrame) { f.TotalPages = 3 }},
		{"page size", func(f *ParityFrame) { f.PageSize = 20 }},
		{"warnings", func(f *ParityFrame) { f.Warnings = []string{"other"} }},
		{"width", func(f *ParityFrame) { f.Capabilities.Width = 30 }},
		{"ascii", func(f *ParityFrame) { f.Capabilities.ASCII = true }},
		{"color", func(f *ParityFrame) { f.Capabilities.Color = false }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := base
			got.Rows = append([]ParityRow(nil), base.Rows...)
			got.Warnings = append([]string(nil), base.Warnings...)
			tc.mutate(&got)
			if len(CompareParity(base, got).Violations) == 0 {
				t.Fatal("mutation was not detected")
			}
		})
	}
	got := base
	got.Capabilities.Interactive = true
	if report := CompareParity(base, got); len(report.Violations) != 0 || len(report.CapabilityGaps) == 0 {
		t.Fatalf("interactive mismatch should be a capability gap: %+v", report)
	}
}

func TestCompareParityReportsMissingAndUnexpectedSupportedActions(t *testing.T) {
	want := ParityFrame{Actions: []ParityAction{{Name: "quit", Supported: true}, {Name: "open", Supported: true}}}
	got := ParityFrame{Actions: []ParityAction{{Name: "refresh", Supported: true}}}
	report := CompareParity(want, got)
	if len(report.CapabilityGaps) != 3 {
		t.Fatalf("expected missing quit/open and unexpected refresh gaps, got %+v", report.CapabilityGaps)
	}
}

func TestStaveTreeUsesRawKeysAndFilteredTotal(t *testing.T) {
	view := previewView(nil, previewDep("go", "a\x1b", 1, 2), previewDep("go", "a\\x1b", 2, 3))
	tree, err := staveTree(view, view.Dependencies, view.Dependencies, summaryState{page: 1, pageSize: 10}, 1, true)
	if err != nil {
		t.Fatal(err)
	}
	children := tree.Root().Children()
	if children[1].ID() == children[2].ID() {
		t.Fatalf("raw logical keys collided: %q", children[1].ID())
	}
	if !strings.Contains(tree.Root().Description(), "2 dependencies") {
		t.Fatalf("filtered total missing: %q", tree.Root().Description())
	}
	if !tree.Root().Flags().Visible || !children[0].Flags().Visible || !children[1].Flags().Visible || !children[2].Flags().Visible {
		t.Fatal("renderable Stave nodes must be explicitly visible")
	}
}

func TestStavePreviewDefaultsToTenRowsAndReportsFilteredTotal(t *testing.T) {
	deps := make([]summaryDependencyView, 12)
	for i := range deps {
		deps[i] = previewDep("go", fmt.Sprintf("dep-%02d", i), 1, int64(i))
	}
	defaults := NewSummary(nil, nil, &stubAnalyzer{}, report.NewFormatter()).applyDefaults(Options{})
	sorted, paged, state, pages := runSummaryDependencyPipeline(previewView(nil, deps...), buildSummaryState(defaults))
	if state.pageSize != 10 || len(paged) != 10 || pages != 2 || len(sorted) != 12 {
		t.Fatalf("unexpected pagination: sorted=%d paged=%d state=%+v pages=%d", len(sorted), len(paged), state, pages)
	}
	tree, err := staveTree(previewView(nil, deps...), sorted, paged, state, pages, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tree.Root().Description(), "12 dependencies") {
		t.Fatalf("filtered total absent: %q", tree.Root().Description())
	}
}

func TestStavePreviewSnapshotFileAndDisabledLegacy(t *testing.T) {
	data := report.Report{Dependencies: []report.DependencyReport{{Language: "go", Name: "alpha", UsedPercent: 50, EstimatedUnusedBytes: 10}}}
	var out strings.Builder
	summary := NewSummary(&out, strings.NewReader(""), &stubAnalyzer{report: data}, report.NewFormatter())
	preview := NewStavePreview(summary)
	features := previewFeatures(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "preview.txt")
	if err := preview.Snapshot(context.Background(), Options{Features: features, UseStavePreview: true}, path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("snapshot mode %o", info.Mode().Perm())
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	if !strings.Contains(string(contents), "Stave preview") {
		t.Fatalf("Stave snapshot missing marker: %q", contents)
	}
	out.Reset()
	if err := preview.Snapshot(context.Background(), Options{Features: features, UseStavePreview: false}, "-"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "Stave preview") || !strings.Contains(out.String(), "Lopper TUI (summary)") {
		t.Fatalf("disabled snapshot did not use legacy: %q", out.String())
	}
}

func TestStavePreviewRequiresResolvedFeatureAndExplicitOptIn(t *testing.T) {
	data := report.Report{Dependencies: []report.DependencyReport{{Language: "go", Name: "alpha", UsedPercent: 50, EstimatedUnusedBytes: 10}}}
	var out strings.Builder
	summary := NewSummary(&out, strings.NewReader("q\n"), &stubAnalyzer{report: data}, report.NewFormatter())
	preview := NewStavePreview(summary)
	if err := preview.Start(context.Background(), Options{UseStavePreview: true}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "Stave preview") || !strings.Contains(out.String(), "Lopper TUI (summary)") {
		t.Fatalf("boolean bypassed the resolved feature set: %q", out.String())
	}
}

type failingWriter struct{}

func (*failingWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestStavePreviewStartAndSnapshotDashPropagateWriterError(t *testing.T) {
	data := report.Report{Dependencies: []report.DependencyReport{{Language: "go", Name: "a", UsedPercent: 1, EstimatedUnusedBytes: 2}}}
	summary := NewSummary(&failingWriter{}, strings.NewReader(""), &stubAnalyzer{report: data}, report.NewFormatter())
	preview := NewStavePreview(summary)
	if err := preview.Start(context.Background(), Options{UseStavePreview: true, Features: previewFeatures(t)}); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("Start error: %v", err)
	}
	if err := preview.Snapshot(context.Background(), Options{UseStavePreview: true, Features: previewFeatures(t)}, "-"); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("Snapshot error: %v", err)
	}
}

func TestStavePreviewDeterministicAndSanitized(t *testing.T) {
	data := report.Report{Warnings: []string{"bad\x1b[31m\nwarning"}, Dependencies: []report.DependencyReport{{Language: "go", Name: "café\n\x1b", UsedPercent: 50.125, EstimatedUnusedBytes: 10}}}
	summary := NewSummary(&strings.Builder{}, strings.NewReader("q\n"), &stubAnalyzer{report: data}, report.NewFormatter())
	preview := NewStavePreview(summary)
	var outputs []string
	for i := 0; i < 3; i++ {
		var out strings.Builder
		summary.Out = &out
		summary.In = strings.NewReader("q\n")
		if err := preview.Start(context.Background(), Options{UseStavePreview: true, Features: previewFeatures(t), ASCII: true, Width: 30}); err != nil {
			t.Fatal(err)
		}
		outputs = append(outputs, out.String())
	}
	if outputs[0] != outputs[1] || outputs[1] != outputs[2] {
		t.Fatal("repeated renders differ")
	}
	for _, r := range outputs[0] {
		if r > 127 && r != '\n' {
			t.Fatalf("ASCII output contains non-ASCII rune %U", r)
		}
		if unicode.IsControl(r) && r != '\n' {
			t.Fatalf("control rune leaked: %U", r)
		}
	}
}

func TestStaveRendererUsesASCIIContentWhenTerminalCannotRenderUnicode(t *testing.T) {
	t.Setenv("TERM", "dumb")
	t.Setenv("COLORTERM", "")
	renderer, err := newStaveRenderer(Options{Width: 80}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !renderer.ASCII {
		t.Fatal("non-Unicode capability did not select ASCII semantic content")
	}
	tree, err := staveTree(previewView(nil, previewDep("go", "alpha", 1, 2)), []summaryDependencyView{previewDep("go", "alpha", 1, 2)}, []summaryDependencyView{previewDep("go", "alpha", 1, 2)}, summaryState{page: 1, pageSize: 10}, 1, renderer.ASCII)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(tree.Root().Description(), "•") || strings.Contains(tree.Root().Description(), `\u2022`) {
		t.Fatalf("ASCII semantic content retained a Unicode separator: %q", tree.Root().Description())
	}
}

func TestStavePreviewErrorsRemain(t *testing.T) {
	want := errors.New("analysis failed")
	summary := NewSummary(&strings.Builder{}, strings.NewReader(""), &stubAnalyzer{err: want}, report.NewFormatter())
	if err := NewStavePreview(summary).Start(context.Background(), Options{UseStavePreview: true, Features: previewFeatures(t)}); !errors.Is(err, want) {
		t.Fatalf("analysis error: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := NewStavePreview(summary).Start(ctx, Options{UseStavePreview: true, Features: previewFeatures(t)}); !errors.Is(err, context.Canceled) {
		t.Fatalf("context error: %v", err)
	}
}

func TestStavePreviewOptInDefaultRoute(t *testing.T) {
	data := report.Report{Warnings: []string{"check generated files"}, Dependencies: []report.DependencyReport{{Language: "go", Name: "alpha", UsedPercent: 50, EstimatedUnusedBytes: 10}}}
	var out strings.Builder
	summary := NewSummary(&out, strings.NewReader("q\n"), &stubAnalyzer{report: data}, report.NewFormatter())
	if err := NewStavePreview(summary).Start(context.Background(), Options{PageSize: 10}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Lopper TUI (summary)") {
		t.Fatal("default route did not stay legacy")
	}
}

func TestNewStaveRendererDegradesWidthWithoutExplicitASCII(t *testing.T) {
	renderer, err := newStaveRenderer(Options{Width: 30, Color: boolPtr(false)}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !renderer.ASCII {
		t.Fatal("width below 40 must enable ASCII degradation")
	}
}

func TestNewStaveRendererNegotiatesTerminalColourProfiles(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("CI", "")
	for _, tc := range []struct {
		name, term, colorTerm string
		want                  capability.ColorLevel
	}{
		{name: "truecolor", term: "xterm-256color", colorTerm: "truecolor", want: capability.ColorTrueColor},
		{name: "ansi256", term: "screen-256color", want: capability.ColorANSI256},
		{name: "ansi16", term: "vt100", want: capability.ColorANSI16},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TERM", tc.term)
			t.Setenv("COLORTERM", tc.colorTerm)
			renderer, err := newStaveRenderer(Options{Width: 80}, true)
			if err != nil {
				t.Fatal(err)
			}
			if renderer.Caps.Color != tc.want {
				t.Fatalf("color=%s want=%s", renderer.Caps.Color, tc.want)
			}
		})
	}
}

func TestStavePreviewRenderContainsRowsAndHonorsNarrowWidth(t *testing.T) {
	data := report.Report{Dependencies: []report.DependencyReport{{Language: "js-ts", Name: "lodash", UsedPercent: 50, EstimatedUnusedBytes: 10}}}
	summary := NewSummary(&strings.Builder{}, strings.NewReader(""), &stubAnalyzer{report: data}, report.NewFormatter())
	preview := NewStavePreview(summary).(*StavePreview)
	output, err := preview.render(context.Background(), Options{UseStavePreview: true, Features: previewFeatures(t), Width: 30, ASCII: true, Color: boolPtr(false)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "lodash") {
		t.Fatalf("rendered row missing: %q", output)
	}
	for _, line := range strings.Split(strings.TrimSuffix(output, "\n"), "\n") {
		if len([]rune(line)) > 30 {
			t.Fatalf("narrow output line is %d cells: %q", len([]rune(line)), line)
		}
	}
}

func TestStavePreviewPreservesCommandAndConsequentialActionGrammar(t *testing.T) {
	reportData := report.Report{SchemaVersion: report.SchemaVersion, Dependencies: []report.DependencyReport{{Language: "js-ts", Name: "lodash", UsedExportsCount: 1, TotalExportsCount: 2, UsedPercent: 50, EstimatedUnusedBytes: 10}}}
	baselineStore := t.TempDir()
	baselineReport := report.Report{SchemaVersion: report.SchemaVersion, Dependencies: []report.DependencyReport{{Language: "js-ts", Name: "lodash", UsedExportsCount: 1, TotalExportsCount: 2, UsedPercent: 50, EstimatedUnusedBytes: 10}}}
	baselinePath, err := report.SaveSnapshot(baselineStore, "label:nightly", baselineReport, time.Unix(1, 0).UTC())
	if err != nil {
		t.Fatalf("create baseline fixture: %v", err)
	}
	commit, err := workspace.CurrentCommitSHA(".")
	if err != nil {
		t.Fatalf("resolve commit fixture key: %v", err)
	}
	if _, err := report.SaveSnapshot(baselineStore, "commit:"+commit, baselineReport, time.Unix(1, 0).UTC()); err != nil {
		t.Fatalf("create commit baseline fixture: %v", err)
	}
	actions := &stubSummaryActionRunner{
		applyReport: report.Report{Dependencies: []report.DependencyReport{{Language: "js-ts", Name: "lodash", UsedExportsCount: 1, TotalExportsCount: 2, UsedPercent: 50, Codemod: &report.CodemodReport{Apply: &report.CodemodApplyReport{AppliedFiles: 1, AppliedPatches: 1}}}}},
		saveReport:  reportData,
		savePath:    baselinePath,
	}
	var out strings.Builder
	commands := []string{
		"filter lodash",
		"sort name",
		"page 1",
		"open js-ts:lodash",
		"apply-codemod --confirm --allow-dirty",
		"save-baseline nightly",
		"compare-baseline --key label:nightly --store " + baselineStore,
		"q",
	}
	input := strings.Join(commands, "\n") + "\n"
	summary := NewSummary(&out, strings.NewReader(input), &stubAnalyzer{report: reportData}, report.NewFormatter())
	summary.Actions = actions
	err = NewStavePreview(summary).Start(context.Background(), Options{RepoPath: ".", Language: "all", BaselineStorePath: baselineStore, UseStavePreview: true, Features: previewFeatures(t)})
	if err != nil {
		t.Fatal(err)
	}
	if actions.applyCalls != 1 || !actions.applyReq.AllowDirty || actions.applyReq.Dependency != "lodash" {
		t.Fatalf("codemod action drifted: calls=%d request=%+v", actions.applyCalls, actions.applyReq)
	}
	if actions.saveCalls != 1 || actions.saveReq.BaselineLabel != "nightly" {
		t.Fatalf("baseline save drifted: calls=%d request=%+v", actions.saveCalls, actions.saveReq)
	}
	for _, marker := range []string{"Stave preview", "Codemod applied", "Baseline saved", "Baseline compared"} {
		if !strings.Contains(out.String(), marker) {
			t.Fatalf("missing %q from Stave flow output: %q", marker, out.String())
		}
	}
}

func TestLopperStaveProgramRegistersTypedTreeActionsAndRejectsInvalidSchema(t *testing.T) {
	data := report.Report{Dependencies: []report.DependencyReport{{Language: "js-ts", Name: "lodash", UsedPercent: 50, EstimatedUnusedBytes: 10}}}
	summary := NewSummary(io.Discard, strings.NewReader(""), &stubAnalyzer{report: data}, report.NewFormatter())
	opts := summary.applyDefaults(Options{UseStavePreview: true, Features: previewFeatures(t), Width: 80})
	view := mapSummaryReportView(data)
	modelState := buildSummaryState(opts)
	program, err := newLopperStaveProgram(summary, &opts, &view, &modelState)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := program.NewSession(context.Background(), staveSessionOptions(opts, false))
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Session.Close()
	ids := map[string]bool{}
	for _, def := range prepared.Actions.Manifest() {
		ids[string(def.ID)] = true
	}
	for _, id := range []string{staveActionQuit, staveActionRefresh, staveActionOpen, staveActionApplyCodemod, staveActionSaveBaseline, staveActionCompareBaseline} {
		if !ids[id] {
			t.Fatalf("typed action %s missing from manifest", id)
		}
	}
	bad, err := json.Marshal(map[string]any{"dependency": "lodash", "confirm": true, "allowDirty": false, "unexpected": true})
	if err != nil {
		t.Fatalf("marshal invalid action: %v", err)
	}
	result := prepared.Actions.Invoke(context.Background(), action.Call{ActionID: action.ID(staveActionApplyCodemod), Arguments: bad, SessionID: "lopper-preview"})
	if result.Error == nil || result.Error.Code != action.InvalidArgument {
		t.Fatalf("unknown typed field was accepted: %+v", result)
	}
	def, ok := prepared.Actions.Definition(action.ID(staveActionApplyCodemod))
	if !ok {
		t.Fatal("codemod action definition missing")
	}
	args, err := json.Marshal(map[string]any{"dependency": "lodash", "confirm": true, "allowDirty": false})
	if err != nil {
		t.Fatalf("marshal action confirmation: %v", err)
	}
	confirmation, err := action.NewConfirmation("lopper-preview", def, semantic.Target{}, args, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err := prepared.Actions.IssueConfirmation(confirmation); err != nil {
		t.Fatal(err)
	}
	call := action.Call{ActionID: action.ID(staveActionApplyCodemod), Arguments: args, SessionID: "lopper-preview", Confirmation: &confirmation}
	if first := prepared.Actions.Invoke(context.Background(), call); first.Error == nil || !strings.Contains(first.Error.Message, "unavailable") {
		t.Fatalf("confirmed codemod result drifted: %+v", first.Error)
	}
	if replay := prepared.Actions.Invoke(context.Background(), call); replay.Error == nil || replay.Error.Code != action.ConfirmationInvalid {
		t.Fatalf("single-use confirmation replay was accepted: %+v", replay)
	}
}

func TestLopperStaveActionEventsProduceReplayableTranscript(t *testing.T) {
	summary := NewSummary(io.Discard, strings.NewReader(""), &stubAnalyzer{}, report.NewFormatter())
	opts := summary.applyDefaults(Options{UseStavePreview: true, Features: previewFeatures(t), Width: 80})
	view := summaryReportView{}
	modelState := buildSummaryState(opts)
	program, err := newLopperStaveProgram(summary, &opts, &view, &modelState)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := program.NewSession(context.Background(), staveSessionOptions(opts, false))
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Session.Close()
	ev, err := event.New(event.ActionInvoked, event.ActionInvokedPayload{CallID: "c1", ActionID: staveActionRefresh, Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := prepared.Session.Send(ev); err != nil {
		t.Fatal(err)
	}
	if err := prepared.Session.Wait(context.Background(), func(s state.State[staveSummaryModel]) bool { return s.Sequence >= 1 }); err != nil {
		t.Fatal(err)
	}
	refreshedReport := report.Report{SchemaVersion: report.SchemaVersion, Dependencies: []report.DependencyReport{{Language: "go", Name: "alpha", UsedExportsCount: 1, TotalExportsCount: 2, UsedPercent: 50}}}
	result := event.EffectResultPayload{CallID: "c1", Status: "done", Value: map[string]any{"version": "lopper.action-result/v1", "action": staveActionRefresh, "value": map[string]any{"refreshed": true, "report": refreshedReport}}}
	effect, err := event.New(event.EffectResult, result)
	if err != nil {
		t.Fatal(err)
	}
	clone, err := effect.Clone()
	if err != nil {
		t.Fatal(err)
	}
	if clone.Kind != effect.Kind || clone.Payload == nil {
		t.Fatalf("effect clone lost payload: %#v", clone)
	}
	if err := prepared.Session.Send(effect); err != nil {
		t.Fatal(err)
	}
	if err := prepared.Session.Wait(context.Background(), func(s state.State[staveSummaryModel]) bool { return s.Sequence >= 2 }); err != nil {
		t.Fatal(err)
	}
	transcript, err := prepared.Session.Transcript()
	if err != nil {
		t.Fatal(err)
	}
	if len(transcript.Records) != 2 || transcript.Records[0].Event.Kind != event.ActionInvoked || transcript.Records[1].Event.Kind != event.EffectResult {
		t.Fatalf("typed event missing from transcript: %+v", transcript)
	}
	checkpoint, err := prepared.Session.Checkpoint()
	if err != nil {
		t.Fatal(err)
	}
	if err := checkpoint.VerifyChecksum(); err != nil {
		t.Fatalf("checkpoint checksum: %v", err)
	}
	if checkpoint.Model == nil {
		t.Fatal("checkpoint omitted model")
	}
	modelJSON, err := json.Marshal(checkpoint.Model)
	if err != nil || len(modelJSON) == 0 {
		t.Fatalf("checkpoint model is not JSON: %v", err)
	}
	if !strings.Contains(string(modelJSON), "summaryShowHelp") || !strings.Contains(string(modelJSON), "dependencies") {
		t.Fatalf("checkpoint model omitted replayable UI state: %s", modelJSON)
	}
	if strings.Contains(string(modelJSON), "summary") && strings.Contains(string(modelJSON), "shared") {
		t.Fatalf("checkpoint model leaked shared services: %s", modelJSON)
	}
	newReplayApply := func() (replay.ApplyFunc, func()) {
		replayProgram, replayErr := newLopperStaveProgram(summary, &opts, &view, &modelState)
		if replayErr != nil {
			t.Fatal(replayErr)
		}
		replayPrepared, replayErr := replayProgram.NewSession(context.Background(), staveSessionOptions(opts, false))
		if replayErr != nil {
			t.Fatal(replayErr)
		}
		apply := func(ctx context.Context, prior state.Checkpoint, replayEvent event.Event) (state.Checkpoint, error) {
			if err := prior.VerifyChecksum(); err != nil {
				return state.Checkpoint{}, fmt.Errorf("restore checkpoint checksum: %w", err)
			}
			current, err := replayPrepared.Session.Checkpoint()
			if err != nil {
				return state.Checkpoint{}, err
			}
			if current.Checksum != prior.Checksum {
				return state.Checkpoint{}, fmt.Errorf("restore checkpoint mismatch: got %s want %s", current.Checksum, prior.Checksum)
			}
			if err := replayPrepared.Session.Send(replayEvent); err != nil {
				return state.Checkpoint{}, err
			}
			if err := replayPrepared.Session.Wait(ctx, func(s state.State[staveSummaryModel]) bool { return s.Sequence > prior.Sequence }); err != nil {
				return state.Checkpoint{}, err
			}
			return replayPrepared.Session.Checkpoint()
		}
		return apply, func() { replayPrepared.Session.Close() }
	}

	apply, closeReplay := newReplayApply()
	replayed, err := replay.Execute(context.Background(), transcript, apply)
	closeReplay()
	if err != nil {
		t.Fatalf("replay execute: %v", err)
	}
	if len(replayed.Records) != len(transcript.Records) || replayed.Records[len(replayed.Records)-1].Result.Hashes.Model != transcript.Records[len(transcript.Records)-1].Result.Hashes.Model {
		t.Fatalf("replay digest diverged")
	}

	mutated, err := transcript.Clone()
	if err != nil {
		t.Fatal(err)
	}
	mutatedReport := report.Report{SchemaVersion: report.SchemaVersion, Dependencies: []report.DependencyReport{{Language: "go", Name: "mutated", UsedExportsCount: 2, TotalExportsCount: 2, UsedPercent: 100}}}
	mutatedEffect, err := event.New(event.EffectResult, event.EffectResultPayload{CallID: "c1", Status: "done", Value: map[string]any{"version": "lopper.action-result/v1", "action": staveActionRefresh, "value": map[string]any{"refreshed": true, "report": mutatedReport}}})
	if err != nil {
		t.Fatal(err)
	}
	mutated.Records[1].Event = mutatedEffect.WithAccepted(mutated.Records[1].Event.Sequence, mutated.Records[1].Event.Revision)
	mutatedApply, closeMutatedReplay := newReplayApply()
	_, err = replay.Execute(context.Background(), mutated, mutatedApply)
	closeMutatedReplay()
	var divergence *replay.Divergence
	if !errors.As(err, &divergence) {
		t.Fatalf("mutated replay did not diverge: %v", err)
	}
}
