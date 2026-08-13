package ui

import (
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/stave/capability"
	"github.com/ben-ranford/stave/layout"
	"github.com/ben-ranford/stave/semantic"
)

func TestStaveTreeHelperCoverageSnapshotModes(t *testing.T) {
	t.Run("snapshot tree includes help warning and summary focus", func(t *testing.T) {
		view := previewView([]string{"warn\x1bing"}, previewDep("go", "alpha", 50, 10))
		state := summaryState{page: 2, pageSize: 1}

		tree, err := staveTreeForInteraction(view, view.Dependencies, view.Dependencies, state, 3, true, staveSummaryInteraction{
			summary: state,
			help:    true,
		})
		if err != nil {
			t.Fatalf("snapshot tree: %v", err)
		}

		root := tree.Root()
		children := root.Children()
		if got := len(children); got != 4 {
			t.Fatalf("snapshot child count = %d, want 4", got)
		}
		if children[0].Name() != "Lopper" {
			t.Fatalf("snapshot status name = %q", children[0].Name())
		}
		if want := "page 2/3 | 1 dependencies | 1 page size | Stave preview filter none | focus summary"; children[0].Description() != want {
			t.Fatalf("snapshot status = %q, want %q", children[0].Description(), want)
		}
		if children[1].Name() != "alpha" || !strings.Contains(children[1].Description(), "go | 50% used | 10 bytes waste") {
			t.Fatalf("snapshot dependency drifted: %q / %q", children[1].Name(), children[1].Description())
		}
		if children[2].Name() != "Warning" || children[2].Description() != "warn\\x1bing" {
			t.Fatalf("snapshot warning = %q / %q", children[2].Name(), children[2].Description())
		}
		if children[3].Name() != "Help" || !strings.Contains(children[3].Description(), "Commands: / filter | : command") {
			t.Fatalf("snapshot help drifted: %q / %q", children[3].Name(), children[3].Description())
		}
		if root.Description() != "page 2/3 | 1 dependencies | 1 page size | Stave preview" {
			t.Fatalf("snapshot root description = %q", root.Description())
		}
	})

	t.Run("snapshot tree folds filter text for ascii output", func(t *testing.T) {
		view := previewView(nil, previewDep("go", "alpha", 50, 10))
		state := summaryState{page: 1, pageSize: 10, filter: "café\x1b"}

		tree, err := staveTreeForInteraction(view, view.Dependencies, view.Dependencies, state, 1, true, staveSummaryInteraction{
			summary: state,
		})
		if err != nil {
			t.Fatalf("snapshot filter tree: %v", err)
		}

		status := tree.Root().Children()[0]
		if !strings.Contains(status.Description(), "filter caf?\\x1b") {
			t.Fatalf("ascii filter was not sanitized/folded: %q", status.Description())
		}
		if strings.Contains(status.Description(), "focus summary") {
			t.Fatalf("filtered snapshot should not report empty-filter focus text: %q", status.Description())
		}
	})
}

func TestStaveTreeHelperCoverageInteractiveModes(t *testing.T) {
	t.Run("help mode shows feedback first on narrow viewports", func(t *testing.T) {
		state := summaryState{page: 1, pageSize: 10}

		tree, err := staveTreeForInteraction(summaryReportView{}, nil, nil, state, 1, true, staveSummaryInteraction{
			summary:   state,
			focusPane: "summary",
			help:      true,
			error:     "boom",
			viewport:  layout.Size{Width: 20, Height: 3},
		})
		if err != nil {
			t.Fatalf("interactive help tree: %v", err)
		}

		children := tree.Root().Children()
		if got := len(children); got != 3 {
			t.Fatalf("help viewport child count = %d, want 3", got)
		}
		if children[0].Name() != "Error" || children[0].Style().Role != "status.failure" {
			t.Fatalf("help feedback drifted: %q / %q", children[0].Name(), children[0].Style().Role)
		}
		if children[1].Name() != "Nav" || children[1].Description() != "j/k p/n" {
			t.Fatalf("narrow help navigation line drifted: %q / %q", children[1].Name(), children[1].Description())
		}
		if children[2].Name() != "Open" || children[2].Description() != "Enter Tab" {
			t.Fatalf("narrow help open line drifted: %q / %q", children[2].Name(), children[2].Description())
		}
	})

	t.Run("invalid detail focus falls back to summary selection and narrow warnings", func(t *testing.T) {
		view := previewView([]string{"watch this"}, previewDep("go", "alpha", 50, 10), previewDep("js", "beta", 25, 5))
		state := summaryState{page: 1, pageSize: 10, selectedDependency: "missing:dep"}

		tree, err := staveTreeForInteraction(view, view.Dependencies, view.Dependencies, state, 1, true, staveSummaryInteraction{
			summary:     state,
			focusPane:   "detail",
			selectedRow: -4,
			viewport:    layout.Size{Width: 20, Height: 4},
		})
		if err != nil {
			t.Fatalf("interactive fallback tree: %v", err)
		}

		children := tree.Root().Children()
		if got := len(children); got != 4 {
			t.Fatalf("fallback child count = %d, want 4", got)
		}
		if children[2].Name() != "> alpha" || children[2].Style().Role != "domain.primary" {
			t.Fatalf("summary focus did not clamp/select first dependency: %q / %q", children[2].Name(), children[2].Style().Role)
		}
		if children[3].Name() != "Warn" || children[3].Description() != "1 | watch this" {
			t.Fatalf("narrow warning summary drifted: %q / %q", children[3].Name(), children[3].Description())
		}
	})

	t.Run("detail focus shows command feedback and truncates to viewport height", func(t *testing.T) {
		dep := summaryDependencyView{
			Language:             "go",
			Name:                 "alpha",
			UsedExportsCount:     1,
			TotalExportsCount:    2,
			UsedPercent:          50,
			EstimatedUnusedBytes: 10,
		}
		view := summaryReportView{Dependencies: []summaryDependencyView{dep}}
		state := summaryState{page: 3, pageSize: 1, filter: "go", selectedDependency: "go:alpha"}

		tree, err := staveTreeForInteraction(view, view.Dependencies, view.Dependencies, state, 4, false, staveSummaryInteraction{
			summary:      state,
			focusPane:    "detail",
			commandMode:  true,
			filterBuffer: "size 5",
			selectedRow:  99,
			viewport:     layout.Size{Width: 80, Height: 4},
		})
		if err != nil {
			t.Fatalf("interactive detail tree: %v", err)
		}

		children := tree.Root().Children()
		if got := len(children); got != 4 {
			t.Fatalf("detail viewport child count = %d, want 4", got)
		}
		if children[1].Name() != "Command" || children[1].Description() != "size 5" {
			t.Fatalf("detail feedback drifted: %q / %q", children[1].Name(), children[1].Description())
		}
		if children[2].Name() != "alpha" {
			t.Fatalf("detail pane should not visually select summary row: %q", children[2].Name())
		}
		if children[3].Name() != "> Detail" || children[3].Description() != "go:alpha" {
			t.Fatalf("detail pane contract drifted: %q / %q", children[3].Name(), children[3].Description())
		}
	})
}

func TestStaveTreeHelperCoverageFeedbackAndHelpModes(t *testing.T) {
	t.Run("feedback priority prefers confirm over status", func(t *testing.T) {
		interaction := staveSummaryInteraction{
			pendingConfirm: "apply codemod?",
			status:         "ready",
		}
		node, err := staveFeedbackNode(interaction, 80, false)
		if err != nil {
			t.Fatalf("confirm feedback node: %v", err)
		}
		if node.Name() != "Confirm" || node.Description() != "apply codemod?" || node.Style().Role != "status.advisory" {
			t.Fatalf("confirm feedback drifted: %q / %q / %q", node.Name(), node.Description(), node.Style().Role)
		}
	})

	t.Run("feedback exposes command and status contracts", func(t *testing.T) {
		commandNode, err := staveFeedbackNode(staveSummaryInteraction{commandMode: true, filterBuffer: "filter caf\x1b"}, 40, true)
		if err != nil {
			t.Fatalf("command feedback node: %v", err)
		}
		if commandNode.Name() != "Command" || commandNode.Description() != "filter caf\\x1b" || commandNode.Style().Role != "domain.primary" {
			t.Fatalf("command feedback drifted: %q / %q / %q", commandNode.Name(), commandNode.Description(), commandNode.Style().Role)
		}

		statusNode, err := staveFeedbackNode(staveSummaryInteraction{status: "ready"}, 40, true)
		if err != nil {
			t.Fatalf("status feedback node: %v", err)
		}
		if statusNode.Name() != "Update" || statusNode.Description() != "ready" || statusNode.Style().Role != "status.success" {
			t.Fatalf("status feedback drifted: %q / %q / %q", statusNode.Name(), statusNode.Description(), statusNode.Style().Role)
		}
	})

	t.Run("help layouts switch by width", func(t *testing.T) {
		cases := []struct {
			name       string
			width      int
			wantFirst  string
			wantLast   string
			wantCount  int
			wantPhrase string
		}{
			{name: "compact", width: 20, wantFirst: "Nav", wantLast: "Exit", wantCount: 7, wantPhrase: "j/k p/n"},
			{name: "medium", width: 40, wantFirst: "Move", wantLast: "Exit", wantCount: 8, wantPhrase: "j/k select | p/n page"},
			{name: "wide", width: 80, wantFirst: "Navigate", wantLast: "Session", wantCount: 5, wantPhrase: "arrows/j/k select | left/right or p/n page | Enter open | Tab pane"},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				nodes, err := staveHelpNodes(tc.width, true)
				if err != nil {
					t.Fatalf("help nodes: %v", err)
				}
				if got := len(nodes); got != tc.wantCount {
					t.Fatalf("help node count = %d, want %d", got, tc.wantCount)
				}
				if nodes[0].Name() != tc.wantFirst || nodes[len(nodes)-1].Name() != tc.wantLast {
					t.Fatalf("help names drifted: first=%q last=%q", nodes[0].Name(), nodes[len(nodes)-1].Name())
				}
				if nodes[0].Description() != tc.wantPhrase {
					t.Fatalf("help first line drifted: %q", nodes[0].Description())
				}
			})
		}
	})
}

func TestStaveTreeHelperCoverageInvalidNodeErrors(t *testing.T) {
	invalidName := string([]byte{0xff})

	t.Run("snapshot row key validation returns an error", func(t *testing.T) {
		view := summaryReportView{Dependencies: []summaryDependencyView{{Language: "go", Name: invalidName, UsedPercent: 50, EstimatedUnusedBytes: 10}}}
		state := summaryState{page: 1, pageSize: 10}
		if _, err := staveTreeForInteraction(view, view.Dependencies, view.Dependencies, state, 1, true, staveSummaryInteraction{summary: state}); err == nil {
			t.Fatal("expected snapshot tree to reject invalid dependency identity")
		}
	})

	t.Run("interactive detail and summary rows reject invalid identities", func(t *testing.T) {
		view := summaryReportView{Dependencies: []summaryDependencyView{{Language: "go", Name: invalidName, UsedExportsCount: 1, TotalExportsCount: 2, UsedPercent: 50, EstimatedUnusedBytes: 10}}}
		state := summaryState{page: 1, pageSize: 10, selectedDependency: "go:" + invalidName}

		if _, err := staveTreeForInteraction(view, view.Dependencies, view.Dependencies, state, 1, true, staveSummaryInteraction{
			summary:   state,
			focusPane: "detail",
			viewport:  layout.Size{Width: 80, Height: 10},
		}); err == nil {
			t.Fatal("expected detail tree to reject invalid dependency identity")
		}

		state.selectedDependency = ""
		if _, err := staveTreeForInteraction(view, view.Dependencies, view.Dependencies, state, 1, true, staveSummaryInteraction{
			summary:   state,
			focusPane: "summary",
			viewport:  layout.Size{Width: 80, Height: 10},
		}); err == nil {
			t.Fatal("expected interactive row tree to reject invalid dependency identity")
		}
	})

	t.Run("detail nodes and application roots reject invalid utf8", func(t *testing.T) {
		dep := summaryDependencyView{
			Language:             "go",
			Name:                 invalidName,
			UsedExportsCount:     1,
			TotalExportsCount:    2,
			UsedPercent:          50,
			EstimatedUnusedBytes: 10,
			RemovalCandidate:     &report.RemovalCandidate{Score: 42.5},
		}
		if _, err := staveDetailNodes(dep, false, true); err == nil {
			t.Fatal("expected detail nodes to reject invalid dependency identity")
		}

		if _, err := staveApplicationTree(nil, invalidName); err == nil {
			t.Fatal("expected application tree to reject invalid root description")
		}
	})
}

func TestStaveTreeHelperCoverageHelpers(t *testing.T) {
	if got := staveSeparator(true); got != " | " {
		t.Fatalf("ascii separator = %q", got)
	}
	if got := staveSeparator(false); got != " • " {
		t.Fatalf("unicode separator = %q", got)
	}

	if got := staveKeyHint(20); got != "? / : q" {
		t.Fatalf("narrow key hint = %q", got)
	}
	if got := staveKeyHint(40); got != "? help | / filter | : cmd | q" {
		t.Fatalf("medium key hint = %q", got)
	}
	if got := staveKeyHint(80); got != "? help | / filter | : commands | arrows navigate | q quit" {
		t.Fatalf("wide key hint = %q", got)
	}

	if !staveHasFeedback(staveSummaryInteraction{status: "ready"}) {
		t.Fatal("status feedback should be detected")
	}
	if !staveHasFeedback(staveSummaryInteraction{pendingConfirm: "confirm"}) {
		t.Fatal("pending confirm feedback should be detected")
	}
	if staveHasFeedback(staveSummaryInteraction{}) {
		t.Fatal("empty interaction should not report feedback")
	}

	view := summaryReportView{Dependencies: []summaryDependencyView{
		{Language: "go", Name: "alpha"},
		{Language: "js", Name: "beta"},
	}}
	if dep, ok := staveSelectedDetail(view, "js:beta"); !ok || dep.Name != "beta" {
		t.Fatalf("selected detail lookup failed: ok=%v dep=%+v", ok, dep)
	}
	if _, ok := staveSelectedDetail(view, "missing"); ok {
		t.Fatal("unexpected detail lookup match for missing dependency")
	}

	if got := clampStaveRow(-1, 2); got != 0 {
		t.Fatalf("clamp negative row = %d", got)
	}
	if got := clampStaveRow(9, 2); got != 1 {
		t.Fatalf("clamp overflow row = %d", got)
	}
	if got := clampStaveRow(1, 2); got != 1 {
		t.Fatalf("clamp in-range row = %d", got)
	}

	start, end := staveVisibleRows(0, 0, 2)
	if start != 0 || end != 0 {
		t.Fatalf("empty visible rows = %d,%d", start, end)
	}
	start, end = staveVisibleRows(2, 1, 5)
	if start != 0 || end != 2 {
		t.Fatalf("full-budget visible rows = %d,%d", start, end)
	}
	start, end = staveVisibleRows(10, 0, 3)
	if start != 0 || end != 3 {
		t.Fatalf("head-clamped visible rows = %d,%d", start, end)
	}
	start, end = staveVisibleRows(10, 9, 3)
	if start != 7 || end != 10 {
		t.Fatalf("tail-clamped visible rows = %d,%d", start, end)
	}

	if got := safeDisplay("café\x1b"+string([]byte{0xff}), true); got != "caf?\\x1b\\xff" {
		t.Fatalf("ascii safeDisplay = %q", got)
	}
	if got := safeDisplay("café\x1b"+string([]byte{0xff}), false); got != "café\\x1b\\xff" {
		t.Fatalf("unicode safeDisplay = %q", got)
	}
}

func TestStaveTreeHelperCoverageRendererModes(t *testing.T) {
	t.Run("env width and no color force ascii-safe renderer", func(t *testing.T) {
		t.Setenv("LOPPER_TUI_WIDTH", "37")
		t.Setenv("NO_COLOR", "1")
		t.Setenv("TERM", "xterm-256color")
		t.Setenv("COLORTERM", "")

		renderer, err := newStaveRenderer(Options{}, false)
		if err != nil {
			t.Fatalf("newStaveRenderer non-tty: %v", err)
		}
		if renderer.Caps.Width != 37 {
			t.Fatalf("renderer width = %d", renderer.Caps.Width)
		}
		if !renderer.ASCII || renderer.Caps.Unicode != capability.UnicodeASCII {
			t.Fatalf("renderer should be ascii-safe: ascii=%v unicode=%v", renderer.ASCII, renderer.Caps.Unicode)
		}
		if !renderer.Caps.ColorDisabled || renderer.Caps.Color != capability.ColorNone {
			t.Fatalf("renderer color contract drifted: disabled=%v color=%v", renderer.Caps.ColorDisabled, renderer.Caps.Color)
		}
		if renderer.Theme.ThemeID != "lopper-sap-ember-blight-loam" {
			t.Fatalf("renderer theme id = %q", renderer.Theme.ThemeID)
		}
	})

	t.Run("explicit color option keeps interactive renderer colored", func(t *testing.T) {
		t.Setenv("LOPPER_TUI_WIDTH", "19")
		t.Setenv("NO_COLOR", "")
		t.Setenv("TERM", "xterm-256color")
		t.Setenv("COLORTERM", "")

		renderer, err := newStaveRenderer(Options{Width: 80, Color: boolPtr(true)}, true)
		if err != nil {
			t.Fatalf("newStaveRenderer tty: %v", err)
		}
		if renderer.Caps.Width != 80 {
			t.Fatalf("explicit width should win over env width: %d", renderer.Caps.Width)
		}
		if renderer.Caps.ColorDisabled || renderer.Caps.Color == capability.ColorNone {
			t.Fatalf("renderer should preserve color when explicitly enabled: disabled=%v color=%v", renderer.Caps.ColorDisabled, renderer.Caps.Color)
		}
		if renderer.ASCII {
			t.Fatalf("wide tty renderer should keep unicode when available: %+v", renderer.Caps)
		}
	})
}

func TestStaveTreeHelperCoverageApplicationTreeRejectsDuplicateChildrenAtValidation(t *testing.T) {
	child, err := staveRecordNode("dependency", "go/alpha", "main", "row", "alpha", "go • 50% used • 10 bytes waste", "status.success", nil)
	if err != nil {
		t.Fatalf("child node: %v", err)
	}

	_, err = staveApplicationTree([]semantic.Node{child, child}, "Stave preview")
	if err == nil || !strings.Contains(err.Error(), "duplicate node id") {
		t.Fatalf("expected duplicate child id validation error, got %v", err)
	}
}
