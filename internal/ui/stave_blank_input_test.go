package ui

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/analysis"
	"github.com/ben-ranford/lopper/internal/report"
)

type blankInputAnalyzer struct {
	calls  int
	report report.Report
}

func (a *blankInputAnalyzer) Analyse(context.Context, analysis.Request) (report.Report, error) {
	a.calls++
	return a.report, nil
}

func TestStaveLineBlankInputIsNoopAndRefreshIsExplicit(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		wantCalls   int
	}{
		{name: "blank line", input: "\nq\n", wantCalls: 1},
		{name: "explicit refresh", input: "refresh\nq\n", wantCalls: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			analyzer := &blankInputAnalyzer{report: report.Report{
				SchemaVersion: report.SchemaVersion,
				Dependencies:  []report.DependencyReport{{Language: "go", Name: "alpha"}},
			}}
			summary := NewSummary(io.Discard, strings.NewReader(tc.input), analyzer, report.NewFormatter())
			err := NewStavePreview(summary).Start(context.Background(), Options{UseStavePreview: true, Features: previewFeatures(t), Width: 80})
			if err != nil {
				t.Fatalf("line preview: %v", err)
			}
			if analyzer.calls != tc.wantCalls {
				t.Fatalf("analysis calls = %d, want %d", analyzer.calls, tc.wantCalls)
			}
		})
	}

	if _, _, _, handled := lopperStaveInput("", summaryState{}); handled {
		t.Fatal("empty command prompt input unexpectedly invoked an action")
	}
}

func TestStaveLineBlankInputPreservesSessionState(t *testing.T) {
	summary := NewSummary(io.Discard, strings.NewReader(""), &stubAnalyzer{}, report.NewFormatter())
	opts := summary.applyDefaults(Options{Width: 80})
	view := summaryReportView{}
	state := buildSummaryState(opts)
	program, err := newLopperStaveProgram(summary, &opts, &view, &state)
	if err != nil {
		t.Fatal(err)
	}
	program.Initial.interaction.status = "prior status"
	program.Initial.interaction.error = "prior error"
	program.Initial.interaction.filterBuffer = "prior input"
	prepared, err := program.NewSession(context.Background(), staveSessionOptions(opts, false))
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Session.Close()
	before, err := prepared.Session.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	line := staveLineSession{prepared: prepared}
	for _, input := range []string{"", " \t "} {
		if quit, err := line.command(context.Background(), input); quit || err != nil {
			t.Fatalf("blank command: quit=%t error=%v", quit, err)
		}
		after, err := prepared.Session.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		if after.Sequence != before.Sequence || after.Hashes.Model != before.Hashes.Model {
			t.Fatalf("blank command changed session: sequence %d -> %d, model %s -> %s", before.Sequence, after.Sequence, before.Hashes.Model, after.Hashes.Model)
		}
	}
}
