package ui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/stave"
	"github.com/ben-ranford/stave/action"
	"github.com/ben-ranford/stave/event"
	staveinput "github.com/ben-ranford/stave/input"
	"github.com/ben-ranford/stave/semantic"
	stavestate "github.com/ben-ranford/stave/state"
)

type finalCoverageRefreshThenQuitReader struct {
	prepared *stave.Prepared[staveSummaryModel]
	stage    int
}

func (r *finalCoverageRefreshThenQuitReader) Read(p []byte) (int, error) {
	if r.stage == 0 {
		r.stage++
		return copy(p, "r"), nil
	}
	if r.stage == 1 {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := r.prepared.Session.Wait(ctx, func(s stavestate.State[staveSummaryModel]) bool {
			return s.Model.interaction.status == "Refreshed"
		}); err != nil {
			return 0, err
		}
		r.stage++
		return copy(p, "q"), nil
	}
	return 0, io.EOF
}

func finalCoveragePreparedSession(t *testing.T, out io.Writer, analyzer *stubAnalyzer) (*StavePreview, Options, *summaryReportView, *summaryState, *stave.Prepared[staveSummaryModel]) {
	t.Helper()

	if out == nil {
		out = io.Discard
	}
	if analyzer == nil {
		analyzer = &stubAnalyzer{report: report.Report{
			SchemaVersion: report.SchemaVersion,
			Dependencies: []report.DependencyReport{
				{Language: "go", Name: "alpha", UsedExportsCount: 2, TotalExportsCount: 10, UsedPercent: 25, EstimatedUnusedBytes: 8},
			},
		}}
	}

	summary := NewSummary(out, strings.NewReader(""), analyzer, report.NewFormatter())
	preview := NewStavePreview(summary).(*StavePreview)
	opts := summary.applyDefaults(Options{RepoPath: ".", Width: 80, PageSize: 10})
	opts.BaselineStorePath = t.TempDir()
	opts.BaselineKey = "nightly"
	baselinePath, err := report.SaveSnapshot(opts.BaselineStorePath, opts.BaselineKey, report.Report{SchemaVersion: report.SchemaVersion, Dependencies: []report.DependencyReport{{Language: "go", Name: "alpha", UsedExportsCount: 2, TotalExportsCount: 10, UsedPercent: 20}}}, time.Unix(1, 0).UTC())
	if err != nil {
		t.Fatalf("seed baseline: %v", err)
	}
	opts.BaselinePath = baselinePath
	view := &summaryReportView{Dependencies: []summaryDependencyView{{Language: "go", Name: "alpha", UsedPercent: 25, EstimatedUnusedBytes: 8}}}
	state := &summaryState{page: 1, pageSize: 10, sortMode: sortByWaste}

	program, err := newLopperStaveProgram(summary, &opts, view, state)
	if err != nil {
		t.Fatalf("newLopperStaveProgram: %v", err)
	}
	prepared, err := program.NewSession(context.Background(), staveSessionOptions(opts, false))
	if err != nil {
		t.Fatalf("program.NewSession: %v", err)
	}
	return preview, opts, view, state, prepared
}

func finalCoverageInvokeRegistry(t *testing.T, reg *action.Registry, id action.ID, args string) action.Result {
	t.Helper()

	result := reg.Invoke(context.Background(), action.Call{
		CallID:    "final-coverage",
		ActionID:  id,
		Arguments: json.RawMessage(args),
		SessionID: "final-coverage",
	})
	if result.Error == nil {
		t.Fatalf("registry invoke %s unexpectedly succeeded with args %s", id, args)
	}
	return result
}

func finalCoverageSnapshotFailurePrepared(t *testing.T) (*StavePreview, Options, *summaryReportView, *summaryState, *stave.Prepared[staveSummaryModel]) {
	t.Helper()

	shared, view, state := terminalCoverageShared()
	model := newStaveSummaryModel(view, nil, *state)
	cloneCalls := 0
	registry, err := lopperSummaryActions(shared)
	if err != nil {
		t.Fatalf("lopperSummaryActions: %v", err)
	}

	program := stave.Program[staveSummaryModel]{
		Initial: model,
		Reduce:  reduceStaveSummary,
		View: func(_ stave.ViewContext, _ staveSummaryModel) (semantic.Tree, error) {
			return terminalTreeForCoverage(t), nil
		},
		Actions: registry,
		Theme:   lopperTheme(),
		ModelPolicy: stavestate.ModelPolicy[staveSummaryModel]{
			Clone: func(m staveSummaryModel) (staveSummaryModel, error) {
				cloneCalls++
				if cloneCalls >= 4 {
					return staveSummaryModel{}, errors.New("clone boom")
				}
				return m, nil
			},
			Sanitize: func(m staveSummaryModel) (staveSummaryModel, error) { return m, nil },
			Hash:     hashStaveSummaryModel,
		},
	}

	prepared, err := program.NewSession(context.Background(), staveSessionOptions(Options{Width: 80}, false))
	if err != nil {
		t.Fatalf("program.NewSession: %v", err)
	}
	preview := NewStavePreview(shared.summary).(*StavePreview)
	opts := shared.summary.applyDefaults(Options{RepoPath: ".", Width: 80})
	return preview, opts, view, state, prepared
}

func TestStaveProgramFinalCoverageActionFailures(t *testing.T) {
	t.Run("refresh propagate analyzer error", func(t *testing.T) {
		_, _, _, _, prepared := finalCoveragePreparedSession(t, io.Discard, &stubAnalyzer{err: errors.New("refresh boom")})
		defer prepared.Session.Close()

		err := invokeLopperAction(context.Background(), prepared, action.ID(staveActionRefresh), preparedLopperActionArgs(t, prepared, action.ID(staveActionRefresh), map[string]any{}), "lopper-preview", false)
		if err == nil || !strings.Contains(err.Error(), "refresh boom") {
			t.Fatalf("refresh error = %v", err)
		}
	})

	t.Run("confirmation issuance fails for empty session", func(t *testing.T) {
		_, _, _, _, prepared := finalCoveragePreparedSession(t, io.Discard, nil)
		defer prepared.Session.Close()

		args := map[string]any{"dependency": "go:alpha", "confirm": true, "allowDirty": false}
		err := invokeLopperAction(context.Background(), prepared, action.ID(staveActionApplyCodemod), args, "", true)
		if err == nil || !strings.Contains(err.Error(), "invalid confirmation") {
			t.Fatalf("confirmation issue error = %v", err)
		}
	})

	t.Run("nil-summary handlers reject typed action input", func(t *testing.T) {
		opts := Options{Width: 80}
		view := summaryReportView{}
		state := summaryState{page: 1, pageSize: 10, sortMode: sortByWaste}
		program, err := newLopperStaveProgram(nil, &opts, &view, &state)
		if err != nil {
			t.Fatalf("newLopperStaveProgram: %v", err)
		}
		prepared, err := program.NewSession(context.Background(), staveSessionOptions(opts, false))
		if err != nil {
			t.Fatalf("program.NewSession: %v", err)
		}
		defer prepared.Session.Close()

		args := map[string]any{"dependency": "go:alpha", "confirm": true, "allowDirty": false}
		err = invokeLopperAction(context.Background(), prepared, action.ID(staveActionApplyCodemod), args, "lopper-preview", true)
		if err == nil || !strings.Contains(err.Error(), "invalid codemod action input") {
			t.Fatalf("codemod input guard error = %v", err)
		}

		err = invokeLopperAction(context.Background(), prepared, action.ID(staveActionSaveBaseline), preparedLopperActionArgs(t, prepared, action.ID(staveActionSaveBaseline), map[string]any{}), "lopper-preview", false)
		if err == nil || !strings.Contains(err.Error(), "invalid Save baseline action input") {
			t.Fatalf("baseline input guard error = %v", err)
		}
	})

	t.Run("baseline action propagates writer failure", func(t *testing.T) {
		_, opts, view, state, prepared := finalCoveragePreparedSession(t, &staveCoverageErrWriter{}, nil)
		defer prepared.Session.Close()

		err := invokeLopperAction(context.Background(), prepared, action.ID(staveActionCompareBaseline), preparedLopperActionArgs(t, prepared, action.ID(staveActionCompareBaseline), map[string]any{"key": "nightly"}), "lopper-preview", false)
		if err != nil {
			t.Fatalf("compare baseline unexpectedly failed: %v (opts=%+v view=%+v state=%+v)", err, opts, *view, *state)
		}
	})
}

func TestStaveProgramFinalCoverageRegistryGuards(t *testing.T) {
	analyzer := &stubAnalyzer{report: report.Report{
		SchemaVersion: report.SchemaVersion,
		Dependencies:  []report.DependencyReport{{Language: "go", Name: "alpha", UsedPercent: 25, EstimatedUnusedBytes: 8}},
	}}
	summary := NewSummary(io.Discard, strings.NewReader(""), analyzer, report.NewFormatter())

	t.Run("summary command action requires state and view", func(t *testing.T) {
		reg, err := lopperSummaryActions(&staveSummaryShared{summary: summary})
		if err != nil {
			t.Fatalf("lopperSummaryActions: %v", err)
		}

		result := reg.Invoke(context.Background(), action.Call{CallID: "final-coverage", ActionID: action.ID("lopper.summary.sort.v1"), Arguments: json.RawMessage(`{"value":"name"}`), SessionID: "final-coverage"})
		if result.Error != nil {
			t.Fatalf("stateless sort action failed: %+v", result.Error)
		}
	})

	t.Run("summary command action reports invalid value", func(t *testing.T) {
		reg, err := lopperSummaryActions(&staveSummaryShared{summary: summary})
		if err != nil {
			t.Fatalf("lopperSummaryActions: %v", err)
		}

		result := finalCoverageInvokeRegistry(t, reg, action.ID("lopper.summary.sort.v1"), `{"value":"bogus"}`)
		if got := result.Error.Message; !strings.Contains(got, "invalid Sort value") {
			t.Fatalf("sort invalid value message = %q", got)
		}
	})
}

func TestStaveTerminalFinalCoverageBranches(t *testing.T) {
	t.Run("paste rejects truncation after UTF-8 normalization", func(t *testing.T) {
		var sends int
		b := &staveTerminal{
			ctx:      context.Background(),
			prepared: struct{}{},
			sendEvent: func(context.Context, any, event.Event) error {
				sends++
				return nil
			},
			snapshot: func(context.Context, any) (staveTerminalSnapshot, error) {
				return staveTerminalSnapshot{model: staveSummaryModel{interaction: staveSummaryInteraction{commandMode: true}}}, nil
			},
		}

		content := string(bytes.Repeat([]byte{0xff, 'a'}, staveinput.DefaultMaxPasteBytes/2))
		err := b.paste(content)
		if err == nil || !strings.Contains(err.Error(), "pasted command exceeds") {
			t.Fatalf("paste truncation error = %v", err)
		}
		if sends != 0 {
			t.Fatalf("paste sent %d events before truncation", sends)
		}
	})

	t.Run("plus key hits fallback parse error", func(t *testing.T) {
		b := &staveTerminal{
			ctx:       context.Background(),
			prepared:  struct{}{},
			sendEvent: func(context.Context, any, event.Event) error { return nil },
		}

		err := b.key(tea.KeyPressMsg{Text: "+"})
		if err == nil || !strings.Contains(err.Error(), "empty key") {
			t.Fatalf("key fallback error = %v", err)
		}
	})

	t.Run("session snapshot and run surface clone failure", func(t *testing.T) {
		preview, opts, view, state, prepared := finalCoverageSnapshotFailurePrepared(t)
		defer prepared.Session.Close()

		b := &staveTerminal{ctx: context.Background(), prepared: prepared}
		_, err := b.sessionSnapshot()
		if err == nil || !strings.Contains(err.Error(), "clone boom") {
			t.Fatalf("sessionSnapshot error = %v", err)
		}

		err = preview.runStaveTerminal(context.Background(), opts, *view, *state, prepared, strings.NewReader(""), io.Discard, false)
		if err == nil || !strings.Contains(err.Error(), "clone boom") {
			t.Fatalf("runStaveTerminal clone failure = %v", err)
		}
	})

	t.Run("runStaveTerminal executes typed refresh action", func(t *testing.T) {
		preview, opts, view, state, prepared := finalCoveragePreparedSession(t, io.Discard, nil)
		defer prepared.Session.Close()

		input := &finalCoverageRefreshThenQuitReader{prepared: prepared}
		if err := preview.runStaveTerminal(context.Background(), opts, *view, *state, prepared, input, io.Discard, false); err != nil {
			t.Fatalf("runStaveTerminal: %v", err)
		}

		snap, err := prepared.Session.Snapshot()
		if err != nil {
			t.Fatalf("Session.Snapshot: %v", err)
		}
		if !snap.Model.interaction.quit || snap.Model.interaction.status != "Refreshed" {
			t.Fatalf("terminal action state = %+v", snap.Model.interaction)
		}
	})
}
