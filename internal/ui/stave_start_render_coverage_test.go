package ui

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/stave/layout"
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
	})

	t.Run("full screen path runs when tty capabilities are available", func(t *testing.T) {
		charDevice, err := os.OpenFile("/dev/null", os.O_RDWR, 0)
		if err != nil {
			t.Skipf("open tty-like device: %v", err)
		}
		t.Cleanup(func() {
			if err := charDevice.Close(); err != nil {
				t.Logf("close character device: %v", err)
			}
		})
		if !supportsScreenRefresh(charDevice) {
			t.Skip("character-device refresh detection unavailable")
		}
		if !supportsStaveFullScreen(staveSessionOptions(opts, true).RuntimeDetected) {
			t.Skip("full-screen Stave capabilities unavailable in this environment")
		}

		summary := NewSummary(charDevice, strings.NewReader("q\n"), &stubAnalyzer{report: rep}, report.NewFormatter())
		if err := NewStavePreview(summary).Start(context.Background(), opts); err != nil {
			t.Fatalf("full-screen start failed: %v", err)
		}
	})

	t.Run("handled action invocation errors render as session feedback", func(t *testing.T) {
		var out strings.Builder
		summary := NewSummary(&out, strings.NewReader("apply-codemod go:alpha\n"), &stubAnalyzer{report: rep}, report.NewFormatter())
		if err := NewStavePreview(summary).Start(context.Background(), opts); err != nil {
			t.Fatalf("handled Stave confirmation error escaped the session: %v", err)
		}
		if !strings.Contains(out.String(), "CONFIRMATION_REQUIRED") {
			t.Fatalf("confirmation error was not visible in the final frame: %q", out.String())
		}
	})

	t.Run("handled action event publication respects cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		summary := NewSummary(io.Discard, &cancelOnFirstRead{reader: strings.NewReader("refresh"), cancel: cancel}, &stubAnalyzer{report: rep}, report.NewFormatter())
		err := NewStavePreview(summary).Start(ctx, opts)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected handled action event cancellation, got %v", err)
		}
	})

	t.Run("handled eof commands return final frame write failures", func(t *testing.T) {
		writeErr := errors.New("final frame write failed")
		writer := &failOnMarkerWriter{marker: "Stave preview", failOn: 2, err: writeErr}
		summary := NewSummary(writer, strings.NewReader("refresh"), &stubAnalyzer{report: rep}, report.NewFormatter())
		err := NewStavePreview(summary).Start(context.Background(), opts)
		if !errors.Is(err, writeErr) {
			t.Fatalf("expected final handled-frame write error, got %v", err)
		}
	})

	t.Run("invalid command final-frame write errors bubble through Start", func(t *testing.T) {
		writeErr := errors.New("detail write failed")
		writer := &failOnMarkerWriter{marker: "Error:", failOn: 1, err: writeErr}
		summary := NewSummary(writer, strings.NewReader("apply-codemod --bad\n"), &stubAnalyzer{report: rep}, report.NewFormatter())
		err := NewStavePreview(summary).Start(context.Background(), opts)
		if !errors.Is(err, writeErr) {
			t.Fatalf("expected invalid-command frame write failure, got %v", err)
		}
	})

	t.Run("legacy eof commands return final render cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		var out strings.Builder
		summary := NewSummary(&out, &cancelOnFirstRead{reader: strings.NewReader("bogus"), cancel: cancel}, &stubAnalyzer{report: rep}, report.NewFormatter())
		err := NewStavePreview(summary).Start(ctx, opts)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected legacy eof render cancellation, got %v", err)
		}
	})

	t.Run("legacy eof commands return final frame write failures", func(t *testing.T) {
		writeErr := errors.New("legacy final frame write failed")
		writer := &failOnMarkerWriter{marker: "Stave preview", failOn: 2, err: writeErr}
		summary := NewSummary(writer, strings.NewReader("bogus"), &stubAnalyzer{report: rep}, report.NewFormatter())
		err := NewStavePreview(summary).Start(context.Background(), opts)
		if !errors.Is(err, writeErr) {
			t.Fatalf("expected legacy final-frame write error, got %v", err)
		}
	})

	t.Run("legacy text events respect cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		var out strings.Builder
		summary := NewSummary(&out, &cancelOnFirstRead{reader: strings.NewReader("bogus\n"), cancel: cancel}, &stubAnalyzer{report: rep}, report.NewFormatter())
		err := NewStavePreview(summary).Start(ctx, opts)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected legacy text-event cancellation, got %v", err)
		}
	})
}

func TestStaveStartRenderCoverageTreeAndHelpers(t *testing.T) {
	deps := []summaryDependencyView{previewDep("go", "alpha", 50, 10), previewDep("js", "beta", 25, 5)}
	validView := previewView([]string{"warn"}, deps...)

	t.Run("interactive tree defaults zero viewport dimensions", func(t *testing.T) {
		tree, err := staveInteractiveTree(validView, validView.Dependencies, validView.Dependencies, summaryState{page: 1, pageSize: 10}, 1, true, staveSummaryInteraction{summary: summaryState{page: 1, pageSize: 10}})
		if err != nil {
			t.Fatalf("interactive tree with default viewport failed: %v", err)
		}
		if tree.Root().ChildCount() == 0 {
			t.Fatal("interactive tree lost all content")
		}
	})

	t.Run("help nodes are truncated to viewport height", func(t *testing.T) {
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
	})

	t.Run("detail nodes fail for invalid raw dependency identity", func(t *testing.T) {
		bad := summaryDependencyView{Language: "go", Name: string([]byte{0xff}), UsedPercent: 50, EstimatedUnusedBytes: 10}
		if _, err := staveDetailNodes(bad, true, true); err == nil {
			t.Fatal("expected invalid detail node identity to fail")
		}
	})

	t.Run("interactive detail pane fails when selected dependency identity is invalid", func(t *testing.T) {
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
	})

	t.Run("interactive rows fail when dependency identity is invalid", func(t *testing.T) {
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
	})

	t.Run("interactive layout keeps one row and truncates overflowing children", func(t *testing.T) {
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
	})

	t.Run("application tree rejects invalid root descriptions", func(t *testing.T) {
		if _, err := staveApplicationTree(nil, string([]byte{0xff})); err == nil {
			t.Fatal("expected invalid application description to fail")
		}
	})

	t.Run("visible row helpers cover empty and full-budget cases", func(t *testing.T) {
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
	})
}
