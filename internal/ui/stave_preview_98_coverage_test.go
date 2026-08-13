package ui

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/stave"
	"github.com/ben-ranford/stave/event"
	"github.com/ben-ranford/stave/layout"
)

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()

	original := os.Stdout
	readPipe, writePipe, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdout pipe: %v", err)
	}
	os.Stdout = writePipe
	t.Cleanup(func() {
		os.Stdout = original
		if err := readPipe.Close(); err != nil {
			t.Logf("close read pipe: %v", err)
		}
	})

	fn()

	if err := writePipe.Close(); err != nil {
		t.Fatalf("close stdout pipe: %v", err)
	}
	data, err := io.ReadAll(readPipe)
	if err != nil {
		t.Fatalf("read captured stdout: %v", err)
	}
	return string(data)
}

func newPreparedStaveSessionWithCloneHook(t *testing.T, rep report.Report, clone func(staveSummaryModel) (staveSummaryModel, error)) (*Summary, *StavePreview, *stave.Prepared[staveSummaryModel], summaryReportView, summaryState) {
	t.Helper()

	summary := NewSummary(io.Discard, strings.NewReader(""), &stubAnalyzer{report: rep}, report.NewFormatter())
	preview := NewStavePreview(summary).(*StavePreview)
	opts := Options{RepoPath: ".", UseStavePreview: true, Features: previewFeatures(t), Width: 80}
	view := mapSummaryReportView(rep)
	state := buildSummaryState(opts)
	program, err := newLopperStaveProgram(summary, &opts, &view, &state)
	if err != nil {
		t.Fatal(err)
	}
	if clone != nil {
		program.ModelPolicy.Clone = clone
	}
	prepared, err := program.NewSession(context.Background(), staveSessionOptions(opts, false))
	if err != nil {
		t.Fatal(err)
	}
	return summary, preview, prepared, view, state
}

func TestStavePreviewSnapshotStdoutFallbackAndFileError(t *testing.T) {
	rep := report.Report{
		Dependencies: []report.DependencyReport{
			{Language: "go", Name: "alpha", UsedPercent: 50, EstimatedUnusedBytes: 10},
		},
	}

	t.Run("stdout fallback when Out is nil", func(t *testing.T) {
		summary := NewSummary(nil, strings.NewReader(""), &stubAnalyzer{report: rep}, report.NewFormatter())
		preview := NewStavePreview(summary)

		got := captureStdout(t, func() {
			if err := preview.Snapshot(context.Background(), Options{RepoPath: ".", UseStavePreview: true, Features: previewFeatures(t), Width: 80}, "-"); err != nil {
				t.Fatalf("snapshot to stdout: %v", err)
			}
		})
		if !strings.Contains(got, "Stave preview") {
			t.Fatalf("stdout fallback missing preview output: %q", got)
		}
	})

	t.Run("file write failure is returned", func(t *testing.T) {
		summary := NewSummary(io.Discard, strings.NewReader(""), &stubAnalyzer{report: rep}, report.NewFormatter())
		preview := NewStavePreview(summary)
		dir := t.TempDir()
		path := filepath.Join(dir, "blocked")
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatalf("create blocked output dir: %v", err)
		}
		if err := preview.Snapshot(context.Background(), Options{RepoPath: ".", UseStavePreview: true, Features: previewFeatures(t), Width: 80}, path); err == nil {
			t.Fatal("expected snapshot write failure")
		}
	})
}

func TestStavePreviewStartLineModeBranchesCoverage(t *testing.T) {
	rep := report.Report{
		Dependencies: []report.DependencyReport{
			{Language: "go", Name: "alpha", UsedPercent: 50, EstimatedUnusedBytes: 10},
		},
	}

	t.Run("nil Out falls back to stdout", func(t *testing.T) {
		summary := NewSummary(nil, strings.NewReader("q\n"), &stubAnalyzer{report: rep}, report.NewFormatter())
		preview := NewStavePreview(summary)

		got := captureStdout(t, func() {
			if err := preview.Start(context.Background(), Options{RepoPath: ".", UseStavePreview: true, Features: previewFeatures(t), Width: 80}); err != nil {
				t.Fatalf("start with nil Out: %v", err)
			}
		})
		if !strings.Contains(got, "Stave preview") {
			t.Fatalf("stdout fallback missing preview output: %q", got)
		}
	})

	t.Run("empty eof exits after first render", func(t *testing.T) {
		var out strings.Builder
		summary := NewSummary(&out, strings.NewReader(""), &stubAnalyzer{report: rep}, report.NewFormatter())
		preview := NewStavePreview(summary)
		if err := preview.Start(context.Background(), Options{RepoPath: ".", UseStavePreview: true, Features: previewFeatures(t), Width: 80}); err != nil {
			t.Fatalf("start on empty eof: %v", err)
		}
		if !strings.Contains(out.String(), "Stave preview") {
			t.Fatalf("empty eof did not render preview: %q", out.String())
		}
	})

	t.Run("unterminated handled command renders final frame", func(t *testing.T) {
		var out strings.Builder
		summary := NewSummary(&out, strings.NewReader("refresh"), &stubAnalyzer{report: rep}, report.NewFormatter())
		preview := NewStavePreview(summary)
		if err := preview.Start(context.Background(), Options{RepoPath: ".", UseStavePreview: true, Features: previewFeatures(t), Width: 80}); err != nil {
			t.Fatalf("start on unterminated handled command: %v", err)
		}
		if strings.Count(out.String(), "Stave preview") < 2 {
			t.Fatalf("handled eof command did not render a final frame after refresh: %q", out.String())
		}
	})

	t.Run("unterminated unknown command renders final frame", func(t *testing.T) {
		var out strings.Builder
		summary := NewSummary(&out, strings.NewReader("not-a-command"), &stubAnalyzer{report: rep}, report.NewFormatter())
		preview := NewStavePreview(summary)
		if err := preview.Start(context.Background(), Options{RepoPath: ".", UseStavePreview: true, Features: previewFeatures(t), Width: 80}); err != nil {
			t.Fatalf("start on unterminated unknown command: %v", err)
		}
		if !strings.Contains(out.String(), "unknown command") || !strings.Contains(out.String(), "Stave preview") {
			t.Fatalf("unknown eof command did not render final frame: %q", out.String())
		}
	})

	t.Run("text event branch is exercised for non-command input", func(t *testing.T) {
		var out strings.Builder
		summary := NewSummary(&out, strings.NewReader("bogus\nq\n"), &stubAnalyzer{report: rep}, report.NewFormatter())
		preview := NewStavePreview(summary)
		if err := preview.Start(context.Background(), Options{RepoPath: ".", UseStavePreview: true, Features: previewFeatures(t), Width: 80}); err != nil {
			t.Fatalf("start on text event input: %v", err)
		}
		if !strings.Contains(out.String(), "unknown command") {
			t.Fatalf("non-command input did not flow through the text-event branch: %q", out.String())
		}
	})
}

func TestStavePreviewSendLopperEventBranches(t *testing.T) {
	rep := report.Report{
		Dependencies: []report.DependencyReport{
			{Language: "go", Name: "alpha", UsedPercent: 50, EstimatedUnusedBytes: 10},
		},
	}

	t.Run("snapshot errors are returned before send", func(t *testing.T) {
		failSnapshot := false
		_, _, prepared, _, _ := newPreparedStaveSessionWithCloneHook(t, rep, func(m staveSummaryModel) (staveSummaryModel, error) {
			if failSnapshot {
				return staveSummaryModel{}, errors.New("snapshot clone failed")
			}
			return m, nil
		})
		defer prepared.Session.Close()

		failSnapshot = true
		ev, err := event.New(event.Text, event.TextPayload{Text: "refresh", Committed: true})
		if err != nil {
			t.Fatal(err)
		}
		if err := sendLopperEvent(context.Background(), prepared, ev); err == nil || !strings.Contains(err.Error(), "snapshot clone failed") {
			t.Fatalf("expected snapshot clone failure, got %v", err)
		}
	})

	t.Run("send errors are returned", func(t *testing.T) {
		_, _, prepared, _, _ := newPreparedStaveSession(t, rep)
		defer prepared.Session.Close()

		ev := event.Event{SchemaVersion: event.SchemaVersion, Kind: event.Text, Payload: func() {}}
		if err := sendLopperEvent(context.Background(), prepared, ev); err == nil {
			t.Fatal("expected invalid event payload to fail send")
		}
	})

	t.Run("wait cancellation is returned", func(t *testing.T) {
		_, _, prepared, _, _ := newPreparedStaveSession(t, rep)
		defer prepared.Session.Close()

		ev, err := event.New(event.Text, event.TextPayload{Text: "refresh", Committed: true})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := sendLopperEvent(ctx, prepared, ev); !errors.Is(err, context.Canceled) {
			t.Fatalf("expected wait cancellation, got %v", err)
		}
	})
}

func TestStavePreviewRenderViewCancellationAndTreeDetailBranches(t *testing.T) {
	rep := report.Report{
		Dependencies: []report.DependencyReport{
			{
				Language:             "go",
				Name:                 "alpha",
				UsedPercent:          50,
				EstimatedUnusedBytes: 10,
				RemovalCandidate:     &report.RemovalCandidate{Score: 42.5},
			},
		},
		Warnings: []string{"warn"},
	}
	_, preview := newStavePreviewFixture(t, rep)

	t.Run("renderView returns render errors from the renderer", func(t *testing.T) {
		ctx := &countingContext{cancelAfter: 2}
		view := summaryReportView{Dependencies: []summaryDependencyView{{Language: "go", Name: "alpha", UsedPercent: 50, EstimatedUnusedBytes: 10}}}
		if _, err := preview.renderView(ctx, Options{RepoPath: ".", UseStavePreview: true, Features: previewFeatures(t), Width: 80}, view, summaryState{page: 1, pageSize: 10}); !errors.Is(err, context.Canceled) {
			t.Fatalf("expected render cancellation from render.Render, got %v", err)
		}
	})

	t.Run("compact tree includes removal candidate detail and footer metadata", func(t *testing.T) {
		view := summaryReportView{
			Warnings: []string{"warn"},
			Dependencies: []summaryDependencyView{
				{
					Language:             "go",
					Name:                 "alpha",
					UsedPercent:          50,
					EstimatedUnusedBytes: 10,
					RemovalCandidate:     &report.RemovalCandidate{Score: 42.5},
				},
			},
		}
		state := summaryState{page: 2, pageSize: 1, filter: "go", selectedDependency: "go:alpha"}
		interaction := staveSummaryInteraction{
			summary:        state,
			selectedRow:    0,
			focusPane:      "detail",
			commandMode:    true,
			filterBuffer:   "filter go",
			viewport:       layout.Size{Width: 40, Height: 10},
			help:           false,
			status:         "ready",
			error:          "backend failed",
			pendingConfirm: "confirm?",
		}
		tree, err := staveTreeForInteraction(view, view.Dependencies, view.Dependencies, state, 3, true, interaction)
		if err != nil {
			t.Fatalf("compact tree: %v", err)
		}
		root := tree.Root()
		if !strings.Contains(root.Description(), "Stave preview") {
			t.Fatalf("compact tree root = %q", root.Description())
		}
		foundDetail := false
		foundRemoval := false
		childSummaries := make([]string, 0, root.ChildCount())
		for i := 0; i < root.ChildCount(); i++ {
			child, ok := root.Child(i)
			if !ok {
				t.Fatalf("missing child %d", i)
			}
			childSummaries = append(childSummaries, child.Name()+": "+child.Description()+" | "+child.Value().Text)
			if strings.Contains(child.Name(), "Detail") && strings.Contains(child.Value().Text, "go:alpha") {
				foundDetail = true
			}
			if strings.Contains(child.Name(), "Removal") && strings.Contains(child.Value().Text, "42.5") {
				foundRemoval = true
			}
		}
		if !foundDetail {
			t.Fatalf("compact tree missing selected dependency detail: %#v", childSummaries)
		}
		if !foundRemoval {
			t.Fatalf("compact tree missing removal candidate score: %#v", childSummaries)
		}
	})
}
