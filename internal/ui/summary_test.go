package ui

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ben-ranford/lopper/internal/analysis"
	"github.com/ben-ranford/lopper/internal/report"
)

type stubAnalyzer struct {
	report report.Report
	err    error
}

func (s *stubAnalyzer) Analyse(ctx context.Context, req analysis.Request) (report.Report, error) {
	if s.err != nil {
		return report.Report{}, s.err
	}
	return s.report, nil
}

func TestSummarySnapshotGolden(t *testing.T) {
	tmp := t.TempDir()
	outputPath := filepath.Join(tmp, "summary.txt")
	goldenPath := filepath.Join("..", "..", "testdata", "ui", "summary.golden")

	reportData := report.Report{
		SchemaVersion: report.SchemaVersion,
		GeneratedAt:   mustParseTime(t, "2024-01-01T00:00:00Z"),
		RepoPath:      "/repo",
		Dependencies: []report.DependencyReport{
			{Name: "alpha", UsedExportsCount: 1, TotalExportsCount: 10, UsedPercent: 10.0, TopUsedSymbols: []report.SymbolUsage{{Name: "foo", Count: 2}}},
			{Name: "beta", UsedExportsCount: 0, TotalExportsCount: 5, UsedPercent: 0.0},
		},
	}

	analyzer := &stubAnalyzer{report: reportData}
	summary := NewSummary(io.Discard, strings.NewReader(""), analyzer, report.NewFormatter())

	opts := Options{
		RepoPath: ".",
		Sort:     "name",
		PageSize: 10,
	}

	if err := summary.Snapshot(context.Background(), opts, outputPath); err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	output, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	golden, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}

	if strings.TrimSpace(string(output)) != strings.TrimSpace(string(golden)) {
		t.Fatalf("snapshot output did not match golden")
	}
}

func TestSummaryRenderBuildsDisplayViewFromBoundary(t *testing.T) {
	reportData := report.Report{
		Warnings: []string{"warning"},
		Dependencies: []report.DependencyReport{
			{Name: "beta", Language: "js-ts", UsedExportsCount: 1, TotalExportsCount: 10, UsedPercent: 10},
			{Name: "alpha", Language: "python", UsedExportsCount: 2, TotalExportsCount: 10, UsedPercent: 20},
		},
	}

	summary := NewSummary(io.Discard, strings.NewReader(""), &stubAnalyzer{report: reportData}, report.NewFormatter())
	var display summaryDisplayView
	summary.Formatter = func(view summaryDisplayView) (string, error) {
		display = view
		return "formatted table\n", nil
	}

	rendered, err := summary.renderSummary(mapSummaryReportView(reportData), summaryState{
		sortMode: sortByName,
		page:     1,
		pageSize: 1,
	})
	if err != nil {
		t.Fatalf("render summary: %v", err)
	}

	if display.Summary == nil || display.Summary.DependencyCount != 2 {
		t.Fatalf("expected summary metrics for sorted dependency set, got %#v", display.Summary)
	}
	if len(display.LanguageBreakdown) != 2 {
		t.Fatalf("expected language breakdown for both dependencies, got %#v", display.LanguageBreakdown)
	}
	if len(display.Dependencies) != 1 || display.Dependencies[0].Name != "alpha" {
		t.Fatalf("expected paged display dependency to be alpha, got %#v", display.Dependencies)
	}
	if len(display.Warnings) != 1 || display.Warnings[0] != "warning" {
		t.Fatalf("expected warnings to flow through display view, got %#v", display.Warnings)
	}
	if !strings.Contains(rendered, "formatted table") {
		t.Fatalf("expected rendered frame to include formatter output, got %q", rendered)
	}
}

func TestSummarySnapshotIncludesBaselineComparison(t *testing.T) {
	tmp := t.TempDir()
	baselineStore := filepath.Join(tmp, "baselines")
	baselineReport := report.Report{
		SchemaVersion: report.SchemaVersion,
		GeneratedAt:   mustParseTime(t, "2024-01-01T00:00:00Z"),
		RepoPath:      "/repo",
		Dependencies: []report.DependencyReport{
			{Name: "alpha", UsedExportsCount: 1, TotalExportsCount: 10, UsedPercent: 10.0},
		},
	}
	if _, err := report.SaveSnapshot(baselineStore, "label:baseline", baselineReport, mustParseTime(t, "2024-01-02T00:00:00Z")); err != nil {
		t.Fatalf("save baseline snapshot: %v", err)
	}

	reportData := report.Report{
		SchemaVersion: report.SchemaVersion,
		GeneratedAt:   mustParseTime(t, "2024-02-01T00:00:00Z"),
		RepoPath:      "/repo",
		Dependencies: []report.DependencyReport{
			{Name: "alpha", UsedExportsCount: 2, TotalExportsCount: 10, UsedPercent: 20.0},
		},
	}

	analyzer := &stubAnalyzer{report: reportData}
	summary := NewSummary(io.Discard, strings.NewReader(""), analyzer, report.NewFormatter())
	opts := Options{
		RepoPath:          ".",
		Sort:              "name",
		PageSize:          10,
		BaselineStorePath: baselineStore,
		BaselineKey:       "label:baseline",
	}

	outputPath := filepath.Join(tmp, "summary-baseline.txt")
	if err := summary.Snapshot(context.Background(), opts, outputPath); err != nil {
		t.Fatalf("snapshot with baseline: %v", err)
	}

	output, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if !strings.Contains(string(output), "Baseline comparison:") {
		t.Fatalf("expected baseline comparison section, got %q", string(output))
	}
	if !strings.Contains(string(output), "baseline_key: label:baseline") {
		t.Fatalf("expected baseline key in output, got %q", string(output))
	}
}

func TestMapSummaryReportViewKeepsPerInstanceRuntimeDeltasForDuplicates(t *testing.T) {
	loadDeltaV2 := 2
	loadDeltaV3 := 7
	reportView := mapSummaryReportView(report.Report{
		Dependencies: []report.DependencyReport{
			{
				Name:     "duplicate",
				Language: "js-ts",
				Identity: &report.DependencyIdentity{Version: "3.0.0", PURL: "pkg:npm/duplicate@3.0.0"},
				RuntimeUsage: &report.RuntimeUsage{
					LoadCount: 30,
				},
			},
			{
				Name:     "duplicate",
				Language: "js-ts",
				Identity: &report.DependencyIdentity{Version: "2.0.0", PURL: "pkg:npm/duplicate@2.0.0"},
				RuntimeUsage: &report.RuntimeUsage{
					LoadCount: 20,
				},
			},
		},
		BaselineComparison: &report.BaselineComparison{
			Dependencies: []report.DependencyDelta{
				{
					Kind:           report.DependencyDeltaChanged,
					Language:       "js-ts",
					Name:           "duplicate",
					DependencyKey:  report.DependencyVersionlessKey(report.DependencyReport{Name: "duplicate", Language: "js-ts", Identity: &report.DependencyIdentity{PURL: "pkg:npm/duplicate@2.0.0"}}),
					CurrentOrdinal: 0,
					RuntimeDelta:   &report.RuntimeDelta{Comparable: true, CurrentPresent: true, BaselinePresent: true, LoadCountDelta: &loadDeltaV2, RuntimeOnlyRegression: true},
				},
				{
					Kind:           report.DependencyDeltaChanged,
					Language:       "js-ts",
					Name:           "duplicate",
					DependencyKey:  report.DependencyVersionlessKey(report.DependencyReport{Name: "duplicate", Language: "js-ts", Identity: &report.DependencyIdentity{PURL: "pkg:npm/duplicate@3.0.0"}}),
					CurrentOrdinal: 1,
					RuntimeDelta:   &report.RuntimeDelta{Comparable: true, CurrentPresent: true, BaselinePresent: true, LoadCountDelta: &loadDeltaV3, RuntimeOnlyRegression: true},
				},
			},
		},
	})

	if len(reportView.Dependencies) != 2 {
		t.Fatalf("expected two duplicate summary rows, got %#v", reportView.Dependencies)
	}
	if reportView.Dependencies[0].RuntimeDelta == nil || reportView.Dependencies[0].RuntimeDelta.LoadCountDelta == nil || *reportView.Dependencies[0].RuntimeDelta.LoadCountDelta != loadDeltaV3 {
		t.Fatalf("expected first reversed duplicate row to keep v3 runtime delta, got %#v", reportView.Dependencies[0].RuntimeDelta)
	}
	if reportView.Dependencies[1].RuntimeDelta == nil || reportView.Dependencies[1].RuntimeDelta.LoadCountDelta == nil || *reportView.Dependencies[1].RuntimeDelta.LoadCountDelta != loadDeltaV2 {
		t.Fatalf("expected second reversed duplicate row to keep v2 runtime delta, got %#v", reportView.Dependencies[1].RuntimeDelta)
	}
}

func TestSummaryAnalyseSummaryViewReturnsAnalyzerError(t *testing.T) {
	summary := NewSummary(io.Discard, strings.NewReader(""), &stubAnalyzer{err: errors.New("boom")}, report.NewFormatter())

	_, err := summary.analyseSummaryView(context.Background(), Options{RepoPath: ".", TopN: 5, Language: "go"})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected analyzer error, got %v", err)
	}
}

func TestSummaryAnalyseSummaryViewReturnsBaselineError(t *testing.T) {
	summary := NewSummary(io.Discard, strings.NewReader(""), &stubAnalyzer{report: report.Report{}}, report.NewFormatter())

	_, err := summary.analyseSummaryView(context.Background(), Options{
		RepoPath:     ".",
		BaselinePath: filepath.Join(t.TempDir(), "missing.json"),
	})
	if err == nil {
		t.Fatal("expected baseline resolution error")
	}
}

func TestHandleSummaryDetailInputSelectsDependency(t *testing.T) {
	summary := NewSummary(io.Discard, strings.NewReader(""), &stubAnalyzer{}, report.NewFormatter())
	reportView := mapSummaryReportView(report.Report{
		Dependencies: []report.DependencyReport{
			{Name: "alpha", Language: "go", UsedExportsCount: 1, TotalExportsCount: 1, UsedPercent: 100},
		},
	})
	opts := &Options{RepoPath: ".", Language: "go"}
	state := &summaryState{}

	handled, err := summary.handleSummaryDetailInput(opts, &reportView, state, "detail alpha")
	if err != nil {
		t.Fatalf("handle detail input: %v", err)
	}
	if !handled {
		t.Fatal("expected detail command to be handled")
	}
	if state.selectedDependency != "alpha" {
		t.Fatalf("expected selected dependency to update, got %q", state.selectedDependency)
	}
}

func TestHandleSummaryDetailInputIgnoresNonDetailCommand(t *testing.T) {
	summary := NewSummary(io.Discard, strings.NewReader(""), &stubAnalyzer{}, report.NewFormatter())

	handled, err := summary.handleSummaryDetailInput(&Options{}, &summaryReportView{}, &summaryState{}, "sort waste")
	if err != nil {
		t.Fatalf("handle non-detail input: %v", err)
	}
	if handled {
		t.Fatal("expected non-detail command to pass through")
	}
}

func mustParseTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatalf("parse time: %v", err)
	}
	return parsed
}
