package ui

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/stave"
	"github.com/ben-ranford/stave/event"
	"github.com/ben-ranford/stave/layout"
	"github.com/ben-ranford/stave/session"
)

type errReader struct {
	err error
}

func (r *errReader) Read([]byte) (int, error) {
	return 0, r.err
}

type countingContext struct {
	calls       int
	cancelAfter int
}

func (c *countingContext) Deadline() (time.Time, bool) {
	return time.Time{}, false
}

func (c *countingContext) Err() error {
	c.calls++
	if c.cancelAfter > 0 && c.calls >= c.cancelAfter {
		return context.Canceled
	}
	return nil
}

func (c *countingContext) Done() <-chan struct{} {
	return nil
}

func (c *countingContext) Value(key any) any {
	return nil
}

func newStavePreviewFixture(t *testing.T, rep report.Report) (*Summary, *StavePreview) {
	t.Helper()
	summary := NewSummary(io.Discard, strings.NewReader(""), &stubAnalyzer{report: rep}, report.NewFormatter())
	preview := NewStavePreview(summary).(*StavePreview)
	return summary, preview
}

func newPreparedStaveSession(t *testing.T, rep report.Report) (*Summary, *StavePreview, *stave.Prepared[staveSummaryModel], summaryReportView, summaryState) {
	t.Helper()
	summary, preview := newStavePreviewFixture(t, rep)
	opts := Options{RepoPath: ".", UseStavePreview: true, Features: previewFeatures(t), Width: 80}
	view := mapSummaryReportView(rep)
	state := buildSummaryState(opts)
	program, err := newLopperStaveProgram(summary, &opts, &view, &state)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := program.NewSession(context.Background(), staveSessionOptions(opts, false))
	if err != nil {
		t.Fatal(err)
	}
	return summary, preview, prepared, view, state
}

func TestStavePreviewSnapshotDelegatesAndWritesExpectedFiles(t *testing.T) {
	rep := report.Report{
		Dependencies: []report.DependencyReport{
			{Language: "go", Name: "alpha", UsedPercent: 50, EstimatedUnusedBytes: 10},
		},
	}
	_, preview := newStavePreviewFixture(t, rep)
	legacy := preview.legacy
	opts := Options{RepoPath: ".", PageSize: 10}

	wantPath := filepath.Join(t.TempDir(), "legacy.txt")
	gotPath := filepath.Join(t.TempDir(), "delegated.txt")
	if err := legacy.Snapshot(context.Background(), opts, wantPath); err != nil {
		t.Fatalf("legacy snapshot: %v", err)
	}
	if err := preview.Snapshot(context.Background(), Options{RepoPath: ".", PageSize: 10, UseStavePreview: false}, gotPath); err != nil {
		t.Fatalf("delegated snapshot: %v", err)
	}

	wantBytes, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatalf("read legacy snapshot: %v", err)
	}
	gotBytes, err := os.ReadFile(gotPath)
	if err != nil {
		t.Fatalf("read delegated snapshot: %v", err)
	}
	if string(gotBytes) != string(wantBytes) {
		t.Fatalf("delegated snapshot diverged\nwant:\n%s\ngot:\n%s", string(wantBytes), string(gotBytes))
	}

}

func TestStavePreviewStartLineModeBranches(t *testing.T) {
	rep := report.Report{
		Dependencies: []report.DependencyReport{
			{Language: "go", Name: "alpha", UsedPercent: 50, EstimatedUnusedBytes: 10},
		},
	}
	_, _ = newStavePreviewFixture(t, rep)

	t.Run("delegates when feature disabled", func(t *testing.T) {
		var out strings.Builder
		summary := NewSummary(&out, strings.NewReader("q\n"), &stubAnalyzer{report: rep}, report.NewFormatter())
		if err := NewStavePreview(summary).Start(context.Background(), Options{UseStavePreview: true, Width: 80}); err != nil {
			t.Fatalf("legacy start: %v", err)
		}
		if !strings.Contains(out.String(), "Lopper TUI (summary)") {
			t.Fatalf("legacy route was not used: %q", out.String())
		}
	})

	t.Run("handles unterminated command at eof", func(t *testing.T) {
		var out strings.Builder
		summary := NewSummary(&out, strings.NewReader("refresh"), &stubAnalyzer{report: rep}, report.NewFormatter())
		opts := Options{UseStavePreview: true, Features: previewFeatures(t), Width: 80}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := NewStavePreview(summary).Start(ctx, opts); err != nil {
			t.Fatalf("line-mode start: %v", err)
		}
		if !strings.Contains(out.String(), "Stave preview") || !strings.Contains(out.String(), "alpha") {
			t.Fatalf("line-mode output missing preview content: %q", out.String())
		}
	})

	t.Run("propagates reader errors", func(t *testing.T) {
		readErr := errors.New("input stream failed")
		summary := NewSummary(&strings.Builder{}, &errReader{err: readErr}, &stubAnalyzer{report: rep}, report.NewFormatter())
		opts := Options{UseStavePreview: true, Features: previewFeatures(t), Width: 80}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := NewStavePreview(summary).Start(ctx, opts); !errors.Is(err, readErr) {
			t.Fatalf("reader error was not returned: %v", err)
		}
	})

	t.Run("propagates writer failures", func(t *testing.T) {
		writeErr := errors.New("write failed")
		summary := NewSummary(&failAfterWriter{failAt: 0, err: writeErr}, strings.NewReader("q\n"), &stubAnalyzer{report: rep}, report.NewFormatter())
		opts := Options{UseStavePreview: true, Features: previewFeatures(t), Width: 80}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := NewStavePreview(summary).Start(ctx, opts); !errors.Is(err, writeErr) {
			t.Fatalf("writer error was not returned: %v", err)
		}
	})
}

func TestReadStaveLineInputAndSendLopperEventBranches(t *testing.T) {
	t.Run("complete line and eof", func(t *testing.T) {
		line, eof, err := readStaveLineInput(bufio.NewReader(strings.NewReader("refresh\n")))
		if err != nil || eof || line != "refresh" {
			t.Fatalf("complete line mismatch: line=%q eof=%v err=%v", line, eof, err)
		}
		line, eof, err = readStaveLineInput(bufio.NewReader(strings.NewReader("refresh")))
		if err != nil || !eof || line != "refresh" {
			t.Fatalf("unterminated line mismatch: line=%q eof=%v err=%v", line, eof, err)
		}
	})

	t.Run("reader error", func(t *testing.T) {
		want := errors.New("read boom")
		line, eof, err := readStaveLineInput(bufio.NewReader(&errReader{err: want}))
		if !errors.Is(err, want) || eof || line != "" {
			t.Fatalf("reader error mismatch: line=%q eof=%v err=%v", line, eof, err)
		}
	})

	t.Run("send lopper event snapshot send and wait", func(t *testing.T) {
		_, _, prepared, _, _ := newPreparedStaveSession(t, report.Report{
			Dependencies: []report.DependencyReport{
				{Language: "go", Name: "alpha", UsedPercent: 50, EstimatedUnusedBytes: 10},
			},
		})
		defer prepared.Session.Close()

		okCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		ev, err := event.New(event.Text, event.TextPayload{Text: "refresh", Committed: true})
		if err != nil {
			t.Fatal(err)
		}
		if err := sendLopperEvent(okCtx, prepared, ev); err != nil {
			t.Fatalf("send success: %v", err)
		}

	})

	t.Run("send failure and wait failure", func(t *testing.T) {
		_, _, prepared, _, _ := newPreparedStaveSession(t, report.Report{
			Dependencies: []report.DependencyReport{
				{Language: "go", Name: "alpha", UsedPercent: 50, EstimatedUnusedBytes: 10},
			},
		})
		ev, err := event.New(event.Text, event.TextPayload{Text: "refresh", Committed: true})
		if err != nil {
			t.Fatal(err)
		}
		prepared.Session.Close()
		if err := sendLopperEvent(context.Background(), prepared, ev); !errors.Is(err, session.ErrSessionClosed) {
			t.Fatalf("send failure was not returned: %v", err)
		}

		_, _, prepared, _, _ = newPreparedStaveSession(t, report.Report{
			Dependencies: []report.DependencyReport{
				{Language: "go", Name: "alpha", UsedPercent: 50, EstimatedUnusedBytes: 10},
			},
		})
		defer prepared.Session.Close()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := sendLopperEvent(ctx, prepared, ev); !errors.Is(err, context.Canceled) {
			t.Fatalf("wait cancellation was not returned: %v", err)
		}
	})
}

func TestStavePreviewRenderAndTreeBranches(t *testing.T) {
	rep := report.Report{
		Warnings: []string{"warn\x1b[31m"},
		Dependencies: []report.DependencyReport{
			{Language: "go", Name: "alpha", UsedPercent: 50, EstimatedUnusedBytes: 10},
			{Language: "js", Name: "beta", UsedPercent: 25, EstimatedUnusedBytes: 5},
		},
	}
	_, preview := newStavePreviewFixture(t, rep)

	t.Run("render returns analyzer errors", func(t *testing.T) {
		renderErr := errors.New("analysis failed")
		broken := NewSummary(io.Discard, strings.NewReader(""), &stubAnalyzer{err: renderErr}, report.NewFormatter())
		if _, err := NewStavePreview(broken).(*StavePreview).render(context.Background(), Options{UseStavePreview: true, Features: previewFeatures(t), Width: 80}); !errors.Is(err, renderErr) {
			t.Fatalf("render analyzer error was not returned: %v", err)
		}
	})

	t.Run("renderView returns tree failures", func(t *testing.T) {
		_, err := preview.renderView(context.Background(), Options{UseStavePreview: true, Features: previewFeatures(t), Width: 80}, summaryReportView{Dependencies: []summaryDependencyView{{Language: "go", Name: "\xff"}}}, summaryState{page: 1, pageSize: 10})
		if err == nil {
			t.Fatal("expected invalid tree to fail")
		}
	})

	t.Run("renderView returns post-render cancellation", func(t *testing.T) {
		opts := Options{UseStavePreview: true, Features: previewFeatures(t), Width: 80, ASCII: true, Color: boolPtr(false)}
		state := summaryState{page: 1, pageSize: 1}
		view := summaryReportView{Dependencies: []summaryDependencyView{{Language: "go", Name: "alpha", UsedPercent: 50, EstimatedUnusedBytes: 10}}}
		ctxCount := &countingContext{}
		if output, err := preview.renderView(ctxCount, opts, view, state); err != nil {
			t.Fatalf("renderView success: %v", err)
		} else if !strings.Contains(output, "alpha") || !strings.Contains(output, "Stave preview") {
			t.Fatalf("renderView output missing content: %q", output)
		}
		ctxCancel := &countingContext{cancelAfter: ctxCount.calls}
		if _, err := preview.renderView(ctxCancel, opts, view, state); !errors.Is(err, context.Canceled) {
			t.Fatalf("renderView cancellation was not returned: %v", err)
		}
	})

	t.Run("renderer profiles and ascii override", func(t *testing.T) {
		t.Setenv("NO_COLOR", "")
		t.Setenv("CI", "")
		for _, tc := range []struct {
			name, term, colorTerm string
			want                  string
		}{
			{name: "truecolor", term: "xterm-256color", colorTerm: "truecolor", want: "truecolor"},
			{name: "ansi256", term: "screen-256color", want: "ansi256"},
			{name: "ansi16", term: "vt100", want: "ansi16"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				t.Setenv("TERM", tc.term)
				t.Setenv("COLORTERM", tc.colorTerm)
				renderer, err := newStaveRenderer(Options{Width: 80}, true)
				if err != nil {
					t.Fatal(err)
				}
				if got := renderer.Caps.Color.String(); got != tc.want {
					t.Fatalf("color level = %s, want %s", got, tc.want)
				}
			})
		}
		renderer, err := newStaveRenderer(Options{Width: 80, ASCII: true}, false)
		if err != nil {
			t.Fatal(err)
		}
		if !renderer.ASCII {
			t.Fatal("explicit ASCII override was ignored")
		}
	})
}

func TestStaveTreeForInteractionBranches(t *testing.T) {
	view := summaryReportView{
		Warnings: []string{"warning one"},
		Dependencies: []summaryDependencyView{
			{Language: "go", Name: "alpha", UsedPercent: 12.5, EstimatedUnusedBytes: 4},
			{Language: "js", Name: "beta", UsedPercent: 99.9, EstimatedUnusedBytes: 8},
		},
	}
	state := summaryState{page: 2, pageSize: 1, filter: "go", selectedDependency: "go:alpha"}
	interaction := staveSummaryInteraction{
		summary:        state,
		selectedRow:    9,
		focusPane:      "summary",
		commandMode:    true,
		filterBuffer:   "filter go",
		viewport:       layout.Size{Width: 40, Height: 10},
		help:           true,
		status:         "ready",
		error:          "backend failed",
		pendingConfirm: "confirm?",
	}
	tree, err := staveTreeForInteraction(view, view.Dependencies, view.Dependencies, state, 3, true, interaction)
	if err != nil {
		t.Fatalf("compact tree: %v", err)
	}
	root := tree.Root()
	if root.Description() != "Stave preview" {
		t.Fatalf("compact tree root = %q", root.Description())
	}
	if root.ChildCount() < 4 {
		t.Fatalf("compact tree lost child nodes: %d", root.ChildCount())
	}
	var statusFound bool
	for i := 0; i < root.ChildCount(); i++ {
		n, _ := root.Child(i)
		if n.Name() == "Status" || n.Name() == "Error" {
			statusFound = true
		}
	}
	if !statusFound {
		t.Fatal("status/error node missing")
	}
	// Row ordering and labels are validated by semantic/golden tests; this test
	// only exercises compact interaction-node construction.
	if root.ChildCount() < 4 {
		t.Fatal("compact interaction nodes missing")
	}

	detailTree, err := staveTreeForInteraction(view, view.Dependencies, view.Dependencies, state, 3, true, staveSummaryInteraction{summary: state, focusPane: "detail", selectedRow: 0, viewport: layout.Size{Width: 40, Height: 10}})
	if err != nil {
		t.Fatalf("detail tree: %v", err)
	}
	if detailTree.Root().ChildCount() == 0 {
		t.Fatal("detail tree empty")
	}

	nonCompact, err := staveTreeForInteraction(view, view.Dependencies, view.Dependencies, summaryState{}, 1, false, staveSummaryInteraction{summary: summaryState{page: 1, pageSize: 10}, selectedRow: -1, help: false})
	if err != nil {
		t.Fatalf("non-compact tree: %v", err)
	}
	if nonCompact.Root().Name() != "Lopper" {
		t.Fatalf("unexpected non-compact root name: %q", nonCompact.Root().Name())
	}
	if nonCompact.Root().ChildCount() == 0 {
		t.Fatal("non-compact root empty")
	}

	if _, err := staveTreeForInteraction(summaryReportView{}, nil, []summaryDependencyView{{Language: "go", Name: "\xff"}}, summaryState{}, 1, false, staveSummaryInteraction{}); err == nil {
		t.Fatal("invalid dependency tree was accepted")
	}
}
