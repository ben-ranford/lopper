package ui

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/stave/layout"
	"github.com/creack/pty"
)

type cancelOnFirstRead struct {
	reader io.Reader
	cancel func()
	done   bool
}

func (r *cancelOnFirstRead) Read(p []byte) (int, error) {
	if !r.done {
		r.done = true
		if r.cancel != nil {
			r.cancel()
		}
	}
	return r.reader.Read(p)
}

type failOnMarkerWriter struct {
	marker  string
	failOn  int
	err     error
	seen    int
	writes  []string
	wrapped io.Writer
}

func (w *failOnMarkerWriter) Write(p []byte) (int, error) {
	text := string(p)
	w.writes = append(w.writes, text)
	if strings.Contains(text, w.marker) {
		w.seen++
		if w.seen == w.failOn {
			return 0, w.err
		}
	}
	if w.wrapped != nil {
		return w.wrapped.Write(p)
	}
	return len(p), nil
}

func TestStaveStartRenderCoverageStartBranches(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("COLORTERM", "truecolor")
	t.Setenv("NO_COLOR", "")

	opts := Options{RepoPath: ".", UseStavePreview: true, Features: previewFeatures(t), Width: 80}
	rep := report.Report{
		SchemaVersion: report.SchemaVersion,
		Dependencies: []report.DependencyReport{
			{Language: "go", Name: "alpha", UsedPercent: 50, EstimatedUnusedBytes: 10},
		},
	}

	t.Run("new session sanitizes invalid UTF-8 without panicking", func(t *testing.T) {
		checkStaveStartNewSessionSanitizesInvalidUtf8WithoutPanicking(t, opts)
	})

	t.Run("full screen path runs when tty capabilities are available", func(t *testing.T) {
		checkStaveStartFullScreenPathRunsWhenTtyCapabilitiesAreAvailable(t, opts, rep)
	})

	t.Run("handled action invocation errors render as session feedback", func(t *testing.T) {
		checkStaveStartHandledActionInvocationErrorsRenderAsSessionFeedback(t, opts, rep)
	})

	t.Run("handled action event publication respects cancellation", func(t *testing.T) {
		checkStaveStartHandledActionEventPublicationRespectsCancellation(t, opts, rep)
	})

	t.Run("handled eof commands return final frame write failures", func(t *testing.T) {
		checkStaveStartHandledEofCommandsReturnFinalFrameWriteFailures(t, opts, rep)
	})

	t.Run("invalid command final-frame write errors bubble through Start", func(t *testing.T) {
		checkStaveStartInvalidCommandFinalFrameWriteErrorsBubbleThroughStart(t, opts, rep)
	})

	t.Run("legacy eof commands return final render cancellation", func(t *testing.T) {
		checkStaveStartLegacyEofCommandsReturnFinalRenderCancellation(t, opts, rep)
	})

	t.Run("legacy eof commands return final frame write failures", func(t *testing.T) {
		checkStaveStartLegacyEofCommandsReturnFinalFrameWriteFailures(t, opts, rep)
	})

	t.Run("legacy text events respect cancellation", func(t *testing.T) {
		checkStaveStartLegacyTextEventsRespectCancellation(t, opts, rep)
	})
}

func checkStaveStartNewSessionSanitizesInvalidUtf8WithoutPanicking(t *testing.T, opts Options) {
	t.Helper()
	bad := report.Report{
		SchemaVersion: report.SchemaVersion,
		Dependencies: []report.DependencyReport{
			{Language: "go", Name: string([]byte{0xff}), UsedPercent: 50, EstimatedUnusedBytes: 10},
		},
	}
	var out strings.Builder
	summary := NewSummary(&out, strings.NewReader(""), &stubAnalyzer{report: bad}, report.NewFormatter())
	if err := NewStavePreview(summary).Start(context.Background(), opts); err != nil {
		t.Fatalf("sanitized initial Stave session failed: %v", err)
	}
	if !utf8.ValidString(out.String()) || strings.Contains(out.String(), string([]byte{0xff})) {
		t.Fatalf("invalid UTF-8 escaped the Stave boundary: %q", out.String())
	}
}

func checkStaveStartFullScreenPathRunsWhenTtyCapabilitiesAreAvailable(t *testing.T, opts Options, rep report.Report) {
	t.Helper()
	terminalInput, terminalOutput, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	cleanupStaveTestCloser(t, "terminal input", terminalInput)
	cleanupStaveTestCloser(t, "terminal output", terminalOutput)
	if err := pty.Setsize(terminalOutput, &pty.Winsize{Rows: 24, Cols: 100}); err != nil {
		t.Fatal(err)
	}
	if !supportsStaveInteractiveTerminal(terminalOutput, terminalOutput) {
		t.Fatal("pseudo-terminal did not satisfy interactive TTY contract")
	}
	actualOpts := opts
	if width, _, ok := staveTerminalDimensions(terminalOutput); ok {
		actualOpts.Width = width
	}
	caps := staveSessionOptions(actualOpts, true).RuntimeDetected
	if !supportsStaveFullScreen(caps) {
		t.Fatalf("full-screen capabilities unavailable: %+v", caps)
	}
	ctx, cancel := context.WithTimeout(context.Background(), staveSignalSubprocessBound)
	defer cancel()
	summary := NewSummary(terminalOutput, terminalOutput, &stubAnalyzer{report: rep}, report.NewFormatter())
	done := make(chan error, 1)
	go func() { done <- NewStavePreview(summary).Start(ctx, actualOpts) }()
	capture := newSignalPTYCapture(terminalInput)
	waitSignalOutput(t, capture, done, func(output string) bool { return strings.Contains(output, "\x1b[?1049h") })
	if _, err := terminalInput.WriteString("q"); err != nil {
		t.Fatal(err)
	}
	if err := waitSignalProcess(done); err != nil {
		t.Fatalf("full-screen start failed: %v", err)
	}
	if err := terminalOutput.Close(); err != nil {
		t.Fatal(err)
	}
	if err := terminalInput.Close(); err != nil {
		t.Fatal(err)
	}
	waitSignalCapture(t, capture)
}

func checkStaveStartHandledActionInvocationErrorsRenderAsSessionFeedback(t *testing.T, opts Options, rep report.Report) {
	t.Helper()
	var out strings.Builder
	summary := NewSummary(&out, strings.NewReader("apply-codemod go:alpha\n"), &stubAnalyzer{report: rep}, report.NewFormatter())
	if err := NewStavePreview(summary).Start(context.Background(), opts); err != nil {
		t.Fatalf("handled Stave confirmation error escaped the session: %v", err)
	}
	if !strings.Contains(out.String(), "CONFIRMATION_REQUIRED") {
		t.Fatalf("confirmation error was not visible in the final frame: %q", out.String())
	}
}

func checkStaveStartHandledActionEventPublicationRespectsCancellation(t *testing.T, opts Options, rep report.Report) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	summary := NewSummary(io.Discard, &cancelOnFirstRead{reader: strings.NewReader("refresh"), cancel: cancel}, &stubAnalyzer{report: rep}, report.NewFormatter())
	err := NewStavePreview(summary).Start(ctx, opts)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected handled action event cancellation, got %v", err)
	}
}

func checkStaveStartHandledEofCommandsReturnFinalFrameWriteFailures(t *testing.T, opts Options, rep report.Report) {
	t.Helper()
	writeErr := errors.New("final frame write failed")
	writer := &failOnMarkerWriter{marker: "Stave preview", failOn: 2, err: writeErr}
	summary := NewSummary(writer, strings.NewReader("refresh"), &stubAnalyzer{report: rep}, report.NewFormatter())
	err := NewStavePreview(summary).Start(context.Background(), opts)
	if !errors.Is(err, writeErr) {
		t.Fatalf("expected final handled-frame write error, got %v", err)
	}
}

func checkStaveStartInvalidCommandFinalFrameWriteErrorsBubbleThroughStart(t *testing.T, opts Options, rep report.Report) {
	t.Helper()
	writeErr := errors.New("detail write failed")
	writer := &failOnMarkerWriter{marker: "Error:", failOn: 1, err: writeErr}
	summary := NewSummary(writer, strings.NewReader("apply-codemod --bad\n"), &stubAnalyzer{report: rep}, report.NewFormatter())
	err := NewStavePreview(summary).Start(context.Background(), opts)
	if !errors.Is(err, writeErr) {
		t.Fatalf("expected invalid-command frame write failure, got %v", err)
	}
}

func checkStaveStartLegacyEofCommandsReturnFinalRenderCancellation(t *testing.T, opts Options, rep report.Report) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	var out strings.Builder
	summary := NewSummary(&out, &cancelOnFirstRead{reader: strings.NewReader("bogus"), cancel: cancel}, &stubAnalyzer{report: rep}, report.NewFormatter())
	err := NewStavePreview(summary).Start(ctx, opts)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected legacy eof render cancellation, got %v", err)
	}
}

func checkStaveStartLegacyEofCommandsReturnFinalFrameWriteFailures(t *testing.T, opts Options, rep report.Report) {
	t.Helper()
	writeErr := errors.New("legacy final frame write failed")
	writer := &failOnMarkerWriter{marker: "Stave preview", failOn: 2, err: writeErr}
	summary := NewSummary(writer, strings.NewReader("bogus"), &stubAnalyzer{report: rep}, report.NewFormatter())
	err := NewStavePreview(summary).Start(context.Background(), opts)
	if !errors.Is(err, writeErr) {
		t.Fatalf("expected legacy final-frame write error, got %v", err)
	}
}

func checkStaveStartLegacyTextEventsRespectCancellation(t *testing.T, opts Options, rep report.Report) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	var out strings.Builder
	summary := NewSummary(&out, &cancelOnFirstRead{reader: strings.NewReader("bogus\n"), cancel: cancel}, &stubAnalyzer{report: rep}, report.NewFormatter())
	err := NewStavePreview(summary).Start(ctx, opts)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected legacy text-event cancellation, got %v", err)
	}
}

func TestStaveStartRenderCoverageTreeAndHelpers(t *testing.T) {
	deps := []summaryDependencyView{previewDep("go", "alpha", 50, 10), previewDep("js", "beta", 25, 5)}
	validView := previewView([]string{"warn"}, deps...)

	t.Run("interactive tree defaults zero viewport dimensions", func(t *testing.T) {
		checkStaveTreeInteractiveTreeDefaultsZeroViewportDimensions(t, validView)
	})

	t.Run("help nodes are truncated to viewport height", func(t *testing.T) {
		checkStaveTreeHelpNodesAreTruncatedToViewportHeight(t, validView)
	})

	t.Run("detail nodes fail for invalid raw dependency identity", func(t *testing.T) {
		checkStaveTreeDetailNodesFailForInvalidRawDependencyIdentity(t)
	})

	t.Run("interactive detail pane fails when selected dependency identity is invalid", func(t *testing.T) {
		checkStaveTreeInteractiveDetailPaneFailsWhenSelectedDependencyIdentityIsInvalid(t)
	})

	t.Run("interactive rows fail when dependency identity is invalid", func(t *testing.T) {
		checkStaveTreeInteractiveRowsFailWhenDependencyIdentityIsInvalid(t)
	})

	t.Run("interactive layout keeps one row and truncates overflowing children", func(t *testing.T) {
		checkStaveTreeInteractiveLayoutKeepsOneRowAndTruncatesOverflowingChildren(t, validView)
	})

	t.Run("application tree rejects invalid root descriptions", func(t *testing.T) {
		checkStaveTreeApplicationTreeRejectsInvalidRootDescriptions(t)
	})

	t.Run("visible row helpers cover empty and full-budget cases", func(t *testing.T) {
		checkStaveTreeVisibleRowHelpersCoverEmptyAndFullBudgetCases(t)
	})
}

func checkStaveTreeInteractiveTreeDefaultsZeroViewportDimensions(t *testing.T, validView summaryReportView) {
	t.Helper()
	tree, err := staveInteractiveTree(validView, validView.Dependencies, validView.Dependencies, summaryState{page: 1, pageSize: 10}, 1, true, staveSummaryInteraction{summary: summaryState{page: 1, pageSize: 10}})
	if err != nil {
		t.Fatalf("interactive tree with default viewport failed: %v", err)
	}
	if tree.Root().ChildCount() == 0 {
		t.Fatal("interactive tree lost all content")
	}
}

func checkStaveTreeHelpNodesAreTruncatedToViewportHeight(t *testing.T, validView summaryReportView) {
	t.Helper()
	tree, err := staveInteractiveTree(validView, validView.Dependencies, validView.Dependencies, summaryState{page: 1, pageSize: 10}, 1, true, staveSummaryInteraction{
		summary:   summaryState{page: 1, pageSize: 10},
		help:      true,
		viewport:  layout.Size{Width: 20, Height: 3},
		focusPane: "summary",
	})
	if err != nil {
		t.Fatalf("help truncation failed: %v", err)
	}
	if got := tree.Root().ChildCount(); got != 3 {
		t.Fatalf("help truncation kept %d children, want 3", got)
	}
}

func checkStaveTreeDetailNodesFailForInvalidRawDependencyIdentity(t *testing.T) {
	t.Helper()
	bad := summaryDependencyView{Language: "go", Name: string([]byte{0xff}), UsedPercent: 50, EstimatedUnusedBytes: 10}
	if _, err := staveDetailNodes(bad, true, true); err == nil {
		t.Fatal("expected invalid detail node identity to fail")
	}
}

func checkStaveTreeInteractiveDetailPaneFailsWhenSelectedDependencyIdentityIsInvalid(t *testing.T) {
	t.Helper()
	bad := summaryDependencyView{Language: "go", Name: string([]byte{0xff}), UsedPercent: 50, EstimatedUnusedBytes: 10}
	view := summaryReportView{Dependencies: []summaryDependencyView{bad}}
	state := summaryState{page: 1, pageSize: 10, selectedDependency: "go:" + bad.Name}
	_, err := staveInteractiveTree(view, view.Dependencies, view.Dependencies, state, 1, true, staveSummaryInteraction{
		summary:     state,
		focusPane:   "detail",
		selectedRow: 0,
		viewport:    layout.Size{Width: 40, Height: 10},
	})
	if err == nil {
		t.Fatal("expected invalid detail identity to fail interactive tree rendering")
	}
}

func checkStaveTreeInteractiveRowsFailWhenDependencyIdentityIsInvalid(t *testing.T) {
	t.Helper()
	bad := summaryDependencyView{Language: "go", Name: string([]byte{0xff}), UsedPercent: 50, EstimatedUnusedBytes: 10}
	view := summaryReportView{Dependencies: []summaryDependencyView{bad}}
	_, err := staveInteractiveTree(view, view.Dependencies, view.Dependencies, summaryState{page: 1, pageSize: 10}, 1, true, staveSummaryInteraction{
		summary:   summaryState{page: 1, pageSize: 10},
		focusPane: "summary",
		viewport:  layout.Size{Width: 40, Height: 10},
	})
	if err == nil {
		t.Fatal("expected invalid dependency identity to fail row rendering")
	}
}

func checkStaveTreeInteractiveLayoutKeepsOneRowAndTruncatesOverflowingChildren(t *testing.T, validView summaryReportView) {
	t.Helper()
	state := summaryState{page: 1, pageSize: 10}
	tree, err := staveInteractiveTree(validView, validView.Dependencies, validView.Dependencies, state, 1, true, staveSummaryInteraction{
		summary:   state,
		focusPane: "summary",
		viewport:  layout.Size{Width: 20, Height: 3},
	})
	if err != nil {
		t.Fatalf("interactive truncation failed: %v", err)
	}
	if got := tree.Root().ChildCount(); got != 3 {
		t.Fatalf("interactive truncation kept %d children, want 3", got)
	}
}

func checkStaveTreeApplicationTreeRejectsInvalidRootDescriptions(t *testing.T) {
	t.Helper()
	if _, err := staveApplicationTree(nil, string([]byte{0xff})); err == nil {
		t.Fatal("expected invalid application description to fail")
	}
}

func checkStaveTreeVisibleRowHelpersCoverEmptyAndFullBudgetCases(t *testing.T) {
	t.Helper()
	if start, end := staveVisibleRows(0, 0, 5); start != 0 || end != 0 {
		t.Fatalf("empty visible rows = (%d,%d)", start, end)
	}
	if start, end := staveVisibleRows(2, 1, 5); start != 0 || end != 2 {
		t.Fatalf("full-budget visible rows = (%d,%d)", start, end)
	}
	if start, end := staveVisibleRows(5, 0, 2); start != 0 || end != 2 {
		t.Fatalf("low-end clamped rows = (%d,%d)", start, end)
	}
	if start, end := staveVisibleRows(5, 5, 2); start != 3 || end != 5 {
		t.Fatalf("high-end clamped rows = (%d,%d)", start, end)
	}
}
