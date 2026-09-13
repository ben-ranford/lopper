package ui

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/stave"
	"github.com/ben-ranford/stave/event"
	"github.com/ben-ranford/stave/layout"
	"github.com/ben-ranford/stave/semantic"
)

func TestStaveFullScreenRequiresResolvedCursorCapabilities(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("COLORTERM", "truecolor")
	t.Setenv("NO_COLOR", "")
	if caps := staveSessionOptions(Options{Width: 80}, true).RuntimeDetected; !supportsStaveFullScreen(caps) {
		t.Fatalf("cursor-capable TTY did not select full-screen mode: %+v", caps)
	}

	t.Setenv("TERM", "dumb")
	if caps := staveSessionOptions(Options{Width: 80}, true).RuntimeDetected; supportsStaveFullScreen(caps) {
		t.Fatalf("TERM=dumb selected full-screen mode: %+v", caps)
	}

	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "1")
	if caps := staveSessionOptions(Options{Width: 80}, true).RuntimeDetected; supportsStaveFullScreen(caps) {
		t.Fatalf("NO_COLOR selected full-screen mode: %+v", caps)
	}

	t.Setenv("NO_COLOR", "")
	if caps := staveSessionOptions(Options{Width: 80}, false).RuntimeDetected; supportsStaveFullScreen(caps) {
		t.Fatalf("non-TTY selected full-screen mode: %+v", caps)
	}
}

func TestStaveInteractiveSmallViewportsKeepFeedbackAndHelpVisible(t *testing.T) {
	view := staveUXFixture()
	state := summaryState{page: 1, pageSize: 10}

	tree, err := staveTreeForInteraction(view, view.Dependencies, view.Dependencies, state, 1, true, staveSummaryInteraction{
		summary:   state,
		focusPane: "summary",
		viewport:  layout.Size{Width: 20, Height: 8},
		error:     "unknown command: bogus",
	})
	if err != nil {
		t.Fatal(err)
	}
	if tree.Root().ChildCount() > 8 {
		t.Fatalf("20x8 tree exceeded height: %d rows", tree.Root().ChildCount())
	}
	if !staveTreeHasText(tree.Root().Children(), "Error", "unknown command") {
		t.Fatalf("20x8 tree hid the error: %#v", tree.Root().Children())
	}

	helpTree, err := staveTreeForInteraction(view, view.Dependencies, view.Dependencies, state, 1, true, staveSummaryInteraction{
		summary:   state,
		focusPane: "summary",
		viewport:  layout.Size{Width: 20, Height: 8},
		help:      true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if helpTree.Root().ChildCount() != 8 {
		t.Fatalf("20x8 help used %d rows, want 8", helpTree.Root().ChildCount())
	}
	for _, want := range []string{"Nav", "Open", "Find", "Order", "Base", "Code", "Exit"} {
		if !staveTreeHasName(helpTree.Root().Children(), want) {
			t.Fatalf("20x8 help omitted %q: %#v", want, helpTree.Root().Children())
		}
	}

	wideHelp, err := staveTreeForInteraction(view, view.Dependencies, view.Dependencies, state, 1, true, staveSummaryInteraction{
		summary:   state,
		focusPane: "summary",
		viewport:  layout.Size{Width: 40, Height: 12},
		help:      true,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"p/n page", ": command", "sort name|waste", "save-baseline", "compare-baseline", "--confirm", "r refresh", "q quit"} {
		if !staveTreeHasText(wideHelp.Root().Children(), "", text) {
			t.Fatalf("40x12 help omitted %q: %#v", text, wideHelp.Root().Children())
		}
	}
}

func TestStaveInteractiveDetailIsStackedFocusedAndHeightBounded(t *testing.T) {
	view := staveUXFixture()
	state := summaryState{page: 1, pageSize: 10, selectedDependency: "go:alpha"}
	tree, err := staveTreeForInteraction(view, view.Dependencies, view.Dependencies, state, 1, true, staveSummaryInteraction{
		summary:   state,
		focusPane: "detail",
		viewport:  layout.Size{Width: 20, Height: 8},
	})
	if err != nil {
		t.Fatal(err)
	}
	children := tree.Root().Children()
	if len(children) > 8 {
		t.Fatalf("detail tree exceeded 20x8 viewport: %d rows", len(children))
	}
	for _, want := range []string{"> Detail", "Exports", "Waste", "Removal", "Warn"} {
		if !staveTreeHasName(children, want) {
			t.Fatalf("focused detail omitted %q: %#v", want, children)
		}
	}
	warnings := 0
	for _, child := range children {
		if child.Name() == "Warn" {
			warnings++
		}
	}
	if warnings != 1 {
		t.Fatalf("warnings were not compressed to one row: %#v", children)
	}
}

func TestStaveDetailFocusRequiresAnOpenedDependency(t *testing.T) {
	view := staveUXFixture()
	state := summaryState{page: 1, pageSize: 10}
	model := newStaveSummaryModel(&view, nil, state)

	reduceStaveKey(&model, event.KeyPayload{Key: "tab"})
	if model.interaction.focusPane != "summary" || !strings.Contains(model.interaction.status, "Open a dependency") {
		t.Fatalf("Tab focused absent detail: %+v", model.interaction)
	}

	model.interaction.status = ""
	model.interaction.summary.selectedDependency = "go:alpha"
	model.interaction.selectedRow = 1
	reduceStaveKey(&model, event.KeyPayload{Key: "tab"})
	if model.interaction.focusPane != "detail" {
		t.Fatalf("Tab did not focus opened detail: %+v", model.interaction)
	}
	reduceStaveKey(&model, event.KeyPayload{Key: "down"})
	if model.interaction.selectedRow != 1 {
		t.Fatalf("detail-focused navigation moved summary selection: %+v", model.interaction)
	}

	model = newStaveSummaryModel(&view, nil, summaryState{page: 1, pageSize: 10})
	ev, err := event.New(event.ActionInvoked, event.ActionInvokedPayload{CallID: "ux-open", ActionID: staveActionOpen, Arguments: map[string]any{"dependency": "go:alpha"}})
	if err != nil {
		t.Fatal(err)
	}
	model, _, err = reduceStaveSummary(stave.ReduceContext{}, model, ev)
	if err != nil {
		t.Fatal(err)
	}
	result, err := event.New(event.EffectResult, event.EffectResultPayload{CallID: "ux-open", Status: "done", Value: map[string]any{"version": "lopper.action-result/v1", "action": staveActionOpen, "value": map[string]any{"dependency": "go:alpha"}}})
	if err != nil {
		t.Fatal(err)
	}
	model, _, err = reduceStaveSummary(stave.ReduceContext{}, model, result)
	if err != nil {
		t.Fatal(err)
	}
	if model.interaction.focusPane != "detail" || model.interaction.summary.selectedDependency != "go:alpha" {
		t.Fatalf("open action did not reveal and focus detail: %+v", model.interaction)
	}
}

func TestStaveOpenMissingDependencyReportsNoData(t *testing.T) {
	view := staveUXFixture()
	model := newStaveSummaryModel(&view, nil, summaryState{page: 1, pageSize: 10})
	model, _, err := reduceStaveSummary(stave.ReduceContext{}, model, mustStaveActionEvent(t, event.ActionInvoked, event.ActionInvokedPayload{CallID: "missing-open", ActionID: staveActionOpen, Arguments: map[string]any{"dependency": "go:missing"}}))
	if err != nil {
		t.Fatal(err)
	}
	model, _, err = reduceStaveSummary(stave.ReduceContext{}, model, mustStaveActionEvent(t, event.EffectResult, event.EffectResultPayload{CallID: "missing-open", Status: "done", Value: map[string]any{"version": "lopper.action-result/v1", "action": staveActionOpen, "value": map[string]any{"dependency": "go:missing"}}}))
	if err != nil {
		t.Fatal(err)
	}
	if model.interaction.error != "No data for dependency go:missing" || model.interaction.status != "" {
		t.Fatalf("missing open feedback is misleading: %+v", model.interaction)
	}
	if model.interaction.summary.selectedDependency != "" || model.interaction.focusPane != "summary" {
		t.Fatalf("missing dependency was opened: %+v", model.interaction)
	}
}

func TestStaveInteractiveStatusHasStableSpacing(t *testing.T) {
	state := summaryState{page: 1, pageSize: 10}
	view := staveUXFixture()
	tree, err := staveTreeForInteraction(view, view.Dependencies, view.Dependencies, state, 1, true, staveSummaryInteraction{
		summary:   state,
		focusPane: "summary",
		viewport:  layout.Size{Width: 100, Height: 30},
	})
	if err != nil {
		t.Fatal(err)
	}
	header, _ := tree.Root().Child(0)
	if strings.Contains(header.Value().Text, "|  ") || strings.Contains(header.Value().Text, "  |") {
		t.Fatalf("status contains doubled separator spacing: %q", header.Value().Text)
	}
}

func TestStaveLineModeHelpVisibleAtNarrowWidthWithoutCSI(t *testing.T) {
	t.Setenv("TERM", "dumb")
	t.Setenv("COLORTERM", "")
	t.Setenv("NO_COLOR", "1")
	var output strings.Builder
	analyzer := &stubAnalyzer{report: report.Report{Dependencies: []report.DependencyReport{{Language: "go", Name: "alpha", UsedPercent: 50}}}}
	summary := NewSummary(&output, strings.NewReader("?\nq\n"), analyzer, report.NewFormatter())
	err := NewStavePreview(summary).Start(context.Background(), Options{
		UseStavePreview: true,
		Features:        previewFeatures(t),
		Width:           20,
		PageSize:        1,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := output.String()
	if !strings.Contains(got, "Nav:") || !strings.Contains(got, "Open:") || !strings.Contains(got, "Find:") {
		t.Fatalf("line-compatible help was not rendered after ?: %q", got)
	}
	for _, control := range []byte{0x1b, 0x9b, 0x9d} {
		if bytes.Contains([]byte(got), []byte{control}) {
			t.Fatalf("line-compatible help emitted terminal control byte 0x%x: %q", control, got)
		}
	}
	for _, line := range strings.Split(strings.TrimSuffix(got, "\n"), "\n") {
		if len([]rune(line)) > 20 {
			t.Fatalf("line-compatible 20-column output overflowed: %q", line)
		}
	}
}

func TestStaveHelpOverlayPreservesAndTemporarilyCoversDetailFocus(t *testing.T) {
	view := staveUXFixture()
	state := summaryState{page: 1, pageSize: 10, selectedDependency: "go:alpha"}
	interaction := staveSummaryInteraction{
		summary:   state,
		focusPane: "detail",
		viewport:  layout.Size{Width: 40, Height: 12},
		help:      true,
	}
	helpTree, err := staveTreeForInteraction(view, view.Dependencies, view.Dependencies, state, 1, true, interaction)
	if err != nil {
		t.Fatal(err)
	}
	if staveTreeHasName(helpTree.Root().Children(), "> Detail") {
		t.Fatalf("help overlay leaked focused detail content: %#v", helpTree.Root().Children())
	}
	if !staveTreeHasName(helpTree.Root().Children(), "Move") || interaction.focusPane != "detail" {
		t.Fatalf("help overlay lost its map or detail focus state: %#v", helpTree.Root().Children())
	}

	interaction.help = false
	detailTree, err := staveTreeForInteraction(view, view.Dependencies, view.Dependencies, state, 1, true, interaction)
	if err != nil {
		t.Fatal(err)
	}
	if !staveTreeHasName(detailTree.Root().Children(), "> Detail") {
		t.Fatalf("closing help did not reveal focused detail: %#v", detailTree.Root().Children())
	}
}

func staveUXFixture() summaryReportView {
	return summaryReportView{
		Warnings: []string{"generated files skipped", "build constraints skipped"},
		Dependencies: []summaryDependencyView{
			{Language: "go", Name: "alpha", UsedExportsCount: 1, TotalExportsCount: 4, UsedPercent: 25, EstimatedUnusedBytes: 40},
			{Language: "go", Name: "beta", UsedExportsCount: 2, TotalExportsCount: 4, UsedPercent: 50, EstimatedUnusedBytes: 20},
			{Language: "go", Name: "gamma", UsedExportsCount: 3, TotalExportsCount: 4, UsedPercent: 75, EstimatedUnusedBytes: 10},
			{Language: "go", Name: "delta", UsedExportsCount: 4, TotalExportsCount: 4, UsedPercent: 100},
			{Language: "go", Name: "epsilon", UsedExportsCount: 4, TotalExportsCount: 4, UsedPercent: 100},
		},
	}
}

func staveTreeHasName(children []semantic.Node, name string) bool {
	for _, child := range children {
		if child.Name() == name {
			return true
		}
	}
	return false
}

func staveTreeHasText(children []semantic.Node, name, contains string) bool {
	for _, child := range children {
		if (name == "" || child.Name() == name) && strings.Contains(child.Value().Text, contains) {
			return true
		}
	}
	return false
}
