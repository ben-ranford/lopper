package ui

import (
	"context"
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
}

func (s *stubAnalyzer) Analyse(ctx context.Context, req analysis.Request) (report.Report, error) {
	return s.report, nil
}

type captureSummaryFormatter struct {
	display summaryDisplayView
}

func (c *captureSummaryFormatter) FormatSummary(display summaryDisplayView) (string, error) {
	c.display = display
	return "formatted table\n", nil
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
	formatter := &captureSummaryFormatter{}
	summary.Formatter = formatter

	rendered, err := summary.renderSummary(mapSummaryReportView(reportData), summaryState{
		sortMode: sortByName,
		page:     1,
		pageSize: 1,
	})
	if err != nil {
		t.Fatalf("render summary: %v", err)
	}

	if formatter.display.Summary == nil || formatter.display.Summary.DependencyCount != 2 {
		t.Fatalf("expected summary metrics for sorted dependency set, got %#v", formatter.display.Summary)
	}
	if len(formatter.display.LanguageBreakdown) != 2 {
		t.Fatalf("expected language breakdown for both dependencies, got %#v", formatter.display.LanguageBreakdown)
	}
	if len(formatter.display.Dependencies) != 1 || formatter.display.Dependencies[0].Name != "alpha" {
		t.Fatalf("expected paged display dependency to be alpha, got %#v", formatter.display.Dependencies)
	}
	if len(formatter.display.Warnings) != 1 || formatter.display.Warnings[0] != "warning" {
		t.Fatalf("expected warnings to flow through display view, got %#v", formatter.display.Warnings)
	}
	if !strings.Contains(rendered, "formatted table") {
		t.Fatalf("expected rendered frame to include formatter output, got %q", rendered)
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
