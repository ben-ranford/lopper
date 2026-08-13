package ui

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/ben-ranford/lopper/internal/analysis"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/stave"
	"github.com/ben-ranford/stave/action"
	"github.com/ben-ranford/stave/capability"
	"github.com/ben-ranford/stave/event"
	"github.com/ben-ranford/stave/semantic"
	"github.com/ben-ranford/stave/theme"
)

type terminalStubAnalyzer struct {
	report report.Report
	err    error
}

func (s *terminalStubAnalyzer) Analyse(context.Context, analysis.Request) (report.Report, error) {
	return s.report, s.err
}

func terminalCoverageShared() (*staveSummaryShared, *summaryReportView, *summaryState) {
	view := &summaryReportView{Dependencies: []summaryDependencyView{{Language: "go", Name: "alpha", UsedPercent: 12.5, EstimatedUnusedBytes: 3}}}
	state := &summaryState{page: 1, pageSize: 10, sortMode: sortByWaste}
	analyzer := &terminalStubAnalyzer{report: report.Report{
		SchemaVersion: report.SchemaVersion,
		Dependencies:  []report.DependencyReport{{Language: "go", Name: "alpha", UsedPercent: 12.5, EstimatedUnusedBytes: 3}},
	}}
	summary := NewSummary(io.Discard, nil, analyzer, report.NewFormatter())
	return &staveSummaryShared{summary: summary}, view, state
}

func terminalTreeForCoverage(t *testing.T) (tree semantic.Tree) {
	t.Helper()
	_, view, state := terminalCoverageShared()
	sorted, deps, normalized, total := runSummaryDependencyPipeline(*view, *state)
	tree, err := staveTree(*view, sorted, deps, normalized, total, false)
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

func TestStaveTerminalUpdateCoversDispatchQuitResizeAndKeyBranches(t *testing.T) {
	t.Run("dispatch error from command mode update", func(t *testing.T) {
		b := &staveTerminal{
			ctx:      context.Background(),
			prepared: struct{}{},
			snapshot: func(context.Context, any) (staveTerminalSnapshot, error) {
				return staveTerminalSnapshot{
					model: staveSummaryModel{
						interaction: staveSummaryInteraction{
							commandMode:  true,
							filterBuffer: "refresh",
							summary:      summaryState{},
						},
					},
				}, nil
			},
		}
		_, cmd := (&staveTerminalModel{bridge: b}).Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		if cmd == nil {
			t.Fatal("command-mode action did not schedule async command")
		}
		if b.quit {
			t.Fatal("dispatch error should not quit the session")
		}
		// Completion errors are surfaced by the returned tea.Msg after the
		// command runs; Update itself only schedules the asynchronous action.
	})

	t.Run("selected action dispatch error and update quit", func(t *testing.T) {
		_, _, state := terminalCoverageShared()
		var sends int
		b := &staveTerminal{
			ctx:      context.Background(),
			prepared: struct{}{},
			snapshot: func(context.Context, any) (staveTerminalSnapshot, error) {
				return staveTerminalSnapshot{
					model: staveSummaryModel{
						interaction: staveSummaryInteraction{
							summary:     *state,
							selectedRow: 0,
						},
					},
				}, nil
			},
			sendEvent: func(context.Context, any, event.Event) error {
				sends++
				if sends == 1 {
					return nil
				}
				return errors.New("send failed")
			},
		}
		_, cmd := (&staveTerminalModel{bridge: b}).Update(tea.KeyPressMsg{Text: "r", Code: 'r'})
		if cmd == nil {
			t.Fatal("selected action did not schedule async command")
		}

		b = &staveTerminal{
			ctx:      context.Background(),
			prepared: struct{}{},
			snapshot: func(context.Context, any) (staveTerminalSnapshot, error) {
				return staveTerminalSnapshot{
					model: staveSummaryModel{
						interaction: staveSummaryInteraction{
							commandMode:  true,
							filterBuffer: "quit",
							summary:      summaryState{},
						},
					},
				}, nil
			},
		}
		_, cmd = (&staveTerminalModel{bridge: b}).Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		if cmd == nil {
			t.Fatalf("quit action did not schedule async command: quit=%v cmd=%v", b.quit, cmd)
		}
	})

	t.Run("resize and key fallback failures", func(t *testing.T) {
		b := &staveTerminal{prepared: struct{}{}, sendEvent: func(context.Context, any, event.Event) error { return nil }}
		if err := b.resize(-1, -1); err == nil {
			t.Fatal("invalid resize dimensions were accepted")
		}

		b = &staveTerminal{prepared: struct{}{}, sendEvent: func(context.Context, any, event.Event) error { return nil }}
		if err := b.paste(":a b"); err == nil {
			t.Fatal("paste key parse failure was swallowed")
		}
		if err := b.key(tea.KeyPressMsg{Text: "a\n"}); err == nil {
			t.Fatal("control rune key text was accepted")
		}
		if err := b.key(tea.KeyPressMsg{Text: "a "}); err == nil {
			t.Fatal("space rune inside multi-rune key text was accepted")
		}

		b.sendEvent = func(context.Context, any, event.Event) error { return errors.New("send failed") }
		if err := b.key(tea.KeyPressMsg{Text: "ab"}); err == nil {
			t.Fatal("multi-rune key send failure was swallowed")
		}
	})
}

func TestStaveTerminalTextPasteSnapshotAndDispatchActionBranches(t *testing.T) {
	t.Run("text event and paste parse/send failures", func(t *testing.T) {
		b := &staveTerminal{ctx: context.Background(), prepared: struct{}{}, sendEvent: func(context.Context, any, event.Event) error { return nil }}
		if err := b.text("bad\x1b"); err == nil {
			t.Fatal("control-text event was accepted")
		}
		b.sendEvent = func(context.Context, any, event.Event) error { return errors.New("send failed") }
		if err := b.text("hello"); err == nil {
			t.Fatal("text send failure was swallowed")
		}

		b.sendEvent = func(context.Context, any, event.Event) error { return nil }
		b.snapshot = func(context.Context, any) (staveTerminalSnapshot, error) {
			return staveTerminalSnapshot{model: staveSummaryModel{interaction: staveSummaryInteraction{commandMode: true}}}, nil
		}
		if err := b.paste(":a b"); err == nil {
			t.Fatal("paste key parse failure was swallowed")
		}
		b.sendEvent = func(context.Context, any, event.Event) error { return errors.New("send failed") }
		if err := b.paste(":ab"); err == nil {
			t.Fatal("paste send failure was swallowed")
		}

		b.snapshot = func(context.Context, any) (staveTerminalSnapshot, error) {
			return staveTerminalSnapshot{}, errors.New("snapshot failed")
		}
		if err := b.paste(":ok"); err == nil {
			t.Fatal("paste snapshot failure was swallowed")
		}
	})

	t.Run("dispatch and dispatchAction error branches", func(t *testing.T) {
		b := &staveTerminal{
			ctx:      context.Background(),
			prepared: struct{}{},
			snapshot: func(context.Context, any) (staveTerminalSnapshot, error) {
				return staveTerminalSnapshot{
					model: staveSummaryModel{
						interaction: staveSummaryInteraction{
							summary: summaryState{},
						},
					},
				}, nil
			},
			sendEvent: func(context.Context, any, event.Event) error { return nil },
		}
		if cmd := b.beginCommand("bad\x1b"); cmd == nil {
			t.Fatal("invalid text dispatch was accepted")
		}
		b.inflight = false
		if cmd := b.beginAction(action.ID(""), nil, false); cmd == nil {
			t.Fatal("empty action id was accepted")
		}
		b.sendEvent = func(context.Context, any, event.Event) error { return errors.New("send failed") }
		if cmd := b.beginAction(action.ID("refresh"), nil, false); cmd == nil {
			t.Fatal("dispatchAction send failure was swallowed")
		}
	})

	t.Run("session snapshot branches", func(t *testing.T) {
		if _, err := (&staveTerminal{prepared: &stave.Prepared[staveSummaryModel]{}}).sessionSnapshot(); err == nil {
			t.Fatal("invalid prepared session was accepted")
		}
		if _, err := (&staveTerminal{snapshot: func(context.Context, any) (staveTerminalSnapshot, error) {
			return staveTerminalSnapshot{}, errors.New("snapshot failed")
		}}).sessionSnapshot(); err == nil {
			t.Fatal("snapshot callback failure was swallowed")
		}
	})
}

func TestStaveTerminalViewAndRunBranches(t *testing.T) {
	t.Run("view error and renderer error branches", func(t *testing.T) {
		if got := (&staveTerminalModel{bridge: &staveTerminal{err: errors.New("unsafe \x1b[31m error")}}).View().Content; !strings.Contains(got, "unsafe") || strings.Contains(got, "\x1b") {
			t.Fatalf("sanitized error frame = %q", got)
		}

		b := &staveTerminal{
			snapshot: func(context.Context, any) (staveTerminalSnapshot, error) {
				return staveTerminalSnapshot{tree: terminalTreeForCoverage(t), caps: capability.Manifest{}, theme: theme.Resolved{}}, nil
			},
		}
		if got := (&staveTerminalModel{bridge: b}).View().Content; got == "" || !strings.Contains(got, "render theme is invalid") {
			t.Fatalf("renderer failure was not surfaced: %q", got)
		}
	})

	t.Run("run honours cancellation and action input", func(t *testing.T) {
		shared, view, state := terminalCoverageShared()
		program, err := newLopperStaveProgram(shared.summary, &Options{Width: 80}, view, state)
		if err != nil {
			t.Fatal(err)
		}
		prepared, err := program.NewSession(context.Background(), staveSessionOptions(Options{Width: 80}, false))
		if err != nil {
			t.Fatal(err)
		}
		defer prepared.Session.Close()

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var out bytes.Buffer
		preview := &StavePreview{legacy: shared.summary}
		if err := preview.runStaveTerminal(ctx, Options{Width: 80}, *view, *state, prepared, bytes.NewBufferString("q"), &out, false); err != nil {
			t.Fatalf("run with quit input failed: %v", err)
		}

		cancelCtx, cancelRun := context.WithCancel(context.Background())
		cancelRun()
		if err := preview.runStaveTerminal(cancelCtx, Options{Width: 80}, *view, *state, prepared, strings.NewReader(""), io.Discard, false); err == nil {
			t.Fatal("cancelled run unexpectedly succeeded")
		}
	})
}
