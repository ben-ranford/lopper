package ui

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/ben-ranford/lopper/internal/featureflags"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/stave"
	"github.com/ben-ranford/stave/action"
	"github.com/ben-ranford/stave/event"
	"github.com/ben-ranford/stave/layout"
)

func coverageFeatures(t *testing.T) featureflags.Set {
	t.Helper()
	s, err := featureflags.DefaultRegistry().Resolve(featureflags.ResolveOptions{Channel: featureflags.ChannelDev, Enable: []string{staveTUIFeature}})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func coverageEvent(t *testing.T, kind event.Kind, payload any) event.Event {
	t.Helper()
	ev, err := event.New(kind, payload)
	if err != nil {
		t.Fatal(err)
	}
	return ev
}

func coverageRawEvent(kind event.Kind, payload any) event.Event {
	return event.Event{Kind: kind, Payload: payload}
}

func coverageModel(shared *staveSummaryShared, views ...*summaryReportView) staveSummaryModel {
	var view *summaryReportView
	if len(views) > 0 {
		view = views[0]
	}
	return newStaveSummaryModel(view, nil, summaryState{page: 2, pageSize: 2, sortMode: sortByWaste})
}

func TestStaveModelHashAndCloneAreValueDeterministic(t *testing.T) {
	m := coverageModel(nil)
	a, err := hashStaveSummaryModel(m)
	if err != nil {
		t.Fatal(err)
	}
	b, err := hashStaveSummaryModel(m)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatal("equal models have different hashes")
	}
	clone, err := cloneStaveSummaryModel(m)
	if err != nil {
		t.Fatal(err)
	}
	if clone != m {
		t.Fatalf("clone changed model: %#v", clone)
	}
}

func TestReduceStaveSummaryHandlesPayloadsAndEffects(t *testing.T) {
	view := &summaryReportView{Dependencies: []summaryDependencyView{{Language: "go", Name: "a"}}}
	shared := &staveSummaryShared{}
	m := coverageModel(shared, view)
	for _, tc := range []struct {
		name  string
		ev    event.Event
		check func(staveSummaryModel) bool
	}{
		{"bad resize", coverageRawEvent(event.Resize, "bad"), func(g staveSummaryModel) bool { return g.interaction.viewport.Width == 80 }},
		{"resize clamps", coverageRawEvent(event.Resize, event.ResizePayload{Width: 0, Height: -1}), func(g staveSummaryModel) bool { return g.interaction.viewport == (layout.Size{Width: 1, Height: 1}) }},
		{"draft text", coverageEvent(t, event.Text, event.TextPayload{Text: "filter a"}), func(g staveSummaryModel) bool { return g.interaction.filterBuffer == "filter a" }},
		{"effect success", coverageEvent(t, event.EffectResult, event.EffectResultPayload{CallID: "c", Status: "done", Value: map[string]any{"version": "lopper.action-result/v1", "action": "test.action", "value": map[string]any{}}}), func(g staveSummaryModel) bool { return g.interaction.status != "" && g.interaction.error == "" }},
		{"effect error", coverageEvent(t, event.EffectResult, event.EffectResultPayload{CallID: "c", Status: "error", Error: "backend"}), func(g staveSummaryModel) bool { return g.interaction.error == "backend" }},
		{"bad action", coverageRawEvent(event.ActionInvoked, "bad"), func(g staveSummaryModel) bool { return g.interaction.error == "backend" }},
		{"shutdown", coverageEvent(t, event.Shutdown, nil), func(g staveSummaryModel) bool { return g.interaction.quit }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var err error
			if tc.name == "effect success" || tc.name == "effect error" {
				// Each completion consumes the pending call; announce it again for
				// the next independent effect assertion.
				m, _, err = reduceStaveSummary(stave.ReduceContext{}, m, coverageEvent(t, event.ActionInvoked, event.ActionInvokedPayload{CallID: "c", ActionID: "test.action"}))
				if err != nil {
					t.Fatal(err)
				}
			}
			m, _, err = reduceStaveSummary(stave.ReduceContext{}, m, tc.ev)
			if err != nil {
				t.Fatal(err)
			}
			if !tc.check(m) {
				t.Fatalf("unexpected state: %+v", m.interaction)
			}
		})
	}
}

func TestReduceStaveKeyEditingNavigationAndFocus(t *testing.T) {
	deps := []summaryDependencyView{{Language: "go", Name: "a"}, {Language: "go", Name: "b"}}
	m := coverageModel(&staveSummaryShared{}, &summaryReportView{Dependencies: deps})
	keys := []event.KeyPayload{{Key: "rune", Rune: '/'}, {Key: "rune", Rune: 'é'}, {Key: "backspace"}, {Key: "enter"}, {Key: "rune", Rune: ':'}, {Key: "rune", Rune: 'x'}, {Key: "escape"}, {Key: "tab"}, {Key: "tab"}, {Key: "down"}, {Key: "up"}, {Key: "right"}, {Key: "left"}, {Key: "rune", Rune: '?'}, {Key: "rune", Rune: '?'}}
	for _, p := range keys {
		var err error
		m, _, err = reduceStaveSummary(stave.ReduceContext{}, m, coverageEvent(t, event.Key, p))
		if err != nil {
			t.Fatalf("reduce key: %v", err)
		}
	}
	if m.interaction.summary.page != 1 || m.interaction.selectedRow != 0 || m.interaction.focusPane != "summary" || m.interaction.commandMode || m.interaction.filterBuffer != "" {
		t.Fatalf("editing/navigation state: %+v", m.interaction)
	}
	for _, p := range []event.KeyPayload{{Key: "down"}, {Key: "down"}, {Key: "delete"}} {
		var err error
		m, _, err = reduceStaveSummary(stave.ReduceContext{}, m, coverageEvent(t, event.Key, p))
		if err != nil {
			t.Fatalf("reduce selection key: %v", err)
		}
	}
	if m.interaction.selectedRow != 1 {
		t.Fatalf("selection did not clamp: %d", m.interaction.selectedRow)
	}
	for _, p := range []event.KeyPayload{{Key: "rune", Rune: 'c', Modifiers: []string{"ctrl"}}, {Key: "escape"}} {
		var err error
		m, _, err = reduceStaveSummary(stave.ReduceContext{}, m, coverageEvent(t, event.Key, p))
		if err != nil {
			t.Fatalf("reduce quit key: %v", err)
		}
	}
	if !m.interaction.quit {
		t.Fatal("ctrl-c did not quit")
	}
}

func TestStaveActionStatusCanonicalForms(t *testing.T) {
	cases := []struct {
		id   string
		args any
		want string
	}{
		{staveActionRefresh, nil, "Refreshed"}, {staveActionOpen, map[string]any{"dependency": "go:a"}, "Opened go:a"}, {staveActionOpen, "bad", "Opened"},
		{"lopper.summary.sort.v1", map[string]any{"value": "name"}, "Sorted by name"}, {"lopper.summary.filter.v1", map[string]any{"value": "go"}, "Filtered go"}, {"lopper.summary.page.v1", map[string]any{"value": 2}, "Page 2"},
		{staveActionSaveBaseline, nil, "Baseline saved"}, {staveActionCompareBaseline, nil, "Baseline compared"}, {staveActionApplyCodemod, nil, "Codemod applied"}, {"other", nil, "Action complete"},
	}
	for _, tc := range cases {
		if got := staveActionStatus(tc.id, tc.args); got != tc.want {
			t.Errorf("%s: got %q want %q", tc.id, got, tc.want)
		}
	}
}

func TestApplyStaveCommandBlankUnknownAndClamps(t *testing.T) {
	s := summaryState{page: 4, pageSize: 1}
	shared := &staveSummaryShared{}
	if status, errText := applyStaveCommand(&s, "  ", shared); status != "" || errText != "" {
		t.Fatalf("blank command: %q %q", status, errText)
	}
	if _, errText := applyStaveCommand(&s, "bogus value", shared); !strings.Contains(errText, "unknown command") {
		t.Fatalf("unknown command: %q", errText)
	}
	if status, errText := applyStaveCommand(&s, "page 1", shared); status != "ok" || errText != "" || s.page != 1 {
		t.Fatalf("valid command: %+v %q %q", s, status, errText)
	}
}

func TestLopperStaveInputRecognizesCanonicalCommandsAndMalformed(t *testing.T) {
	state := summaryState{}
	for _, tc := range []struct {
		input, id        string
		handled, confirm bool
	}{
		{"q", staveActionQuit, true, false}, {"quit", staveActionQuit, true, false}, {"", staveActionRefresh, true, false}, {"refresh", staveActionRefresh, true, false}, {"open go:a", staveActionOpen, true, false},
		{"filter go", "lopper.summary.filter.v1", true, false}, {"sort name", "lopper.summary.sort.v1", true, false}, {"page 2", "lopper.summary.page.v1", true, false}, {"size 5", "lopper.summary.size.v1", true, false},
		{"apply-codemod --confirm", staveActionApplyCodemod, true, true}, {"save-baseline x", staveActionSaveBaseline, true, false}, {"compare-baseline x", staveActionCompareBaseline, true, false}, {"not-a-command", "", false, false},
	} {
		id, _, confirm, handled := lopperStaveInput(tc.input, state)
		if string(id) != tc.id || confirm != tc.confirm || handled != tc.handled {
			t.Errorf("%q => %q confirm=%v handled=%v", tc.input, id, confirm, handled)
		}
	}
}

func TestStaveTreeInteractionBranchesAndSafeDisplay(t *testing.T) {
	view := previewView([]string{"warn\x1b[31m"}, previewDep("go", "café", 12.5, 4))
	state := summaryState{page: 1, pageSize: 10, filter: "go", selectedDependency: "go:café"}
	for _, tc := range []struct {
		compact     bool
		ascii       bool
		interaction staveSummaryInteraction
	}{
		{true, true, staveSummaryInteraction{summary: state, focusPane: "detail", selectedRow: 4, status: "ok", error: "bad", pendingConfirm: "yes", commandMode: true, filterBuffer: "filter go", viewport: layout.Size{Width: 40, Height: 10}, help: true}},
		{false, false, staveSummaryInteraction{summary: state, selectedRow: -1, help: true}},
	} {
		tree, err := staveTreeForInteraction(view, view.Dependencies, view.Dependencies, state, 1, tc.ascii, tc.interaction)
		if err != nil {
			t.Fatal(err)
		}
		if tree.Root().Description() == "" {
			t.Fatal("tree has no root value")
		}
	}
	if got := safeDisplay("café\x1b", true); got != "caf?\\x1b" {
		t.Fatalf("safe display = %q", got)
	}
}

func TestStaveRendererWidthEnvironmentAndColorOverrides(t *testing.T) {
	t.Setenv("LOPPER_TUI_WIDTH", "37")
	t.Setenv("TERM", "dumb")
	t.Setenv("NO_COLOR", "1")
	r, err := newStaveRenderer(Options{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if r.Caps.Width != 37 || !r.ASCII || !r.Caps.ColorDisabled {
		t.Fatalf("renderer negotiation: %+v ascii=%v", r.Caps, r.ASCII)
	}
	t.Setenv("LOPPER_TUI_WIDTH", "invalid")
	r, err = newStaveRenderer(Options{Width: 0, Color: boolPtr(true), ASCII: true}, false)
	if err != nil {
		t.Fatal(err)
	}
	if r.Caps.Width != 80 || r.ASCII != true {
		t.Fatalf("invalid width fallback: %+v", r)
	}
}

func TestStavePreviewSnapshotAndStartContextAndFeatureFallback(t *testing.T) {
	summary := NewSummary(io.Discard, strings.NewReader("q\n"), &stubAnalyzer{report: report.Report{}}, report.NewFormatter())
	p := NewStavePreview(summary)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := p.Snapshot(ctx, Options{UseStavePreview: true, Features: coverageFeatures(t)}, "-"); !errors.Is(err, context.Canceled) {
		t.Fatalf("snapshot context: %v", err)
	}
	if err := p.Snapshot(context.Background(), Options{UseStavePreview: true, Features: coverageFeatures(t)}, ""); err == nil {
		t.Fatal("empty snapshot path accepted")
	}
	if err := p.Start(context.Background(), Options{UseStavePreview: false}); err != nil {
		t.Fatal(err)
	}
}

func TestLopperSummaryActionsRejectInvalidInputs(t *testing.T) {
	summary := NewSummary(io.Discard, strings.NewReader(""), &stubAnalyzer{}, report.NewFormatter())
	view, state := summaryReportView{}, buildSummaryState(Options{})
	opts := Options{Width: 80}
	program, err := newLopperStaveProgram(summary, &opts, &view, &state)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := program.NewSession(context.Background(), staveSessionOptions(Options{}, false))
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Session.Close()
	for _, id := range []string{staveActionOpen, staveActionApplyCodemod, staveActionSaveBaseline, staveActionCompareBaseline, "lopper.summary.filter.v1", "lopper.summary.sort.v1", "lopper.summary.page.v1", "lopper.summary.size.v1"} {
		r := prepared.Actions.Invoke(context.Background(), action.Call{ActionID: action.ID(id), Arguments: json.RawMessage(`[]`), SessionID: "test"})
		if r.Error == nil {
			t.Errorf("%s accepted array input", id)
		}
	}
	if err := invokeLopperAction(context.Background(), prepared, action.ID("missing"), map[string]any{}, "test", false); err == nil {
		t.Fatal("missing action accepted")
	}
}

func TestStaveTerminalAdapterGuardsAndDispatchBranches(t *testing.T) {
	b := &staveTerminal{ctx: context.Background()}
	if b.text("x") != nil || b.key(tea.KeyPressMsg{Text: "x"}) != nil || b.resize(10, 10) != nil || b.sendAndWait(event.Event{}) != nil {
		t.Fatal("nil bridge guards failed")
	}
	if _, err := b.sessionSnapshot(); err == nil {
		t.Fatal("unprepared session snapshot accepted")
	}
	if cmd := b.beginCommand("x"); cmd == nil {
		t.Fatal("command without snapshot failed to report completion")
	}
	b.snapshot = func(context.Context, any) (staveTerminalSnapshot, error) {
		return staveTerminalSnapshot{model: staveSummaryModel{}}, nil
	}
	if cmd := b.beginCommand("plain text"); cmd == nil {
		t.Fatal("text command returned nil")
	}
	_ = b.beginCommand("refresh")
	_ = b.beginCommand("refresh")
}

func TestStaveTerminalModelViewAndUpdateErrorPaths(t *testing.T) {
	b := &staveTerminal{ctx: context.Background(), snapshot: func(context.Context, any) (staveTerminalSnapshot, error) {
		return staveTerminalSnapshot{}, errors.New("snap")
	}}
	m := &staveTerminalModel{bridge: b}
	if !strings.Contains(m.View().Content, "snap") {
		t.Fatal("snapshot error not rendered")
	}
	if m.Init() != nil {
		t.Fatal("Init should not schedule command")
	}
	_, cmd := m.Update(tea.InterruptMsg{})
	if cmd == nil || !b.quit {
		t.Fatal("interrupt did not quit")
	}
	b = &staveTerminal{ctx: context.Background(), prepared: struct{}{}, sendEvent: func(context.Context, any, event.Event) error { return errors.New("send") }}
	m = &staveTerminalModel{bridge: b}
	_, cmd = m.Update(tea.WindowSizeMsg{Width: 20, Height: 10})
	if cmd == nil || !b.quit {
		t.Fatal("resize error did not quit")
	}
}

func TestSelectedDependencyForRowBoundsAndDispatchAction(t *testing.T) {
	if got := selectedDependencyForRow(staveSummaryModel{}); got != "" {
		t.Fatal(got)
	}
	m := staveSummaryModel{view: &summaryReportView{Dependencies: []summaryDependencyView{{Language: "go", Name: "a"}}}, interaction: staveSummaryInteraction{selectedRow: 0}}
	if got := selectedDependencyForRow(m); got != "go:a" {
		t.Fatalf("selected dependency=%q", got)
	}
}
