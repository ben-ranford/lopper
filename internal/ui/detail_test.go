package ui

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/analysis"
	"github.com/ben-ranford/lopper/internal/report"
)

func TestDetailShowsRiskCues(t *testing.T) {
	analyzer := stubAnalyzer{
		report: report.Report{
			Dependencies: []report.DependencyReport{
				{
					Name:              "risky",
					UsedExportsCount:  1,
					TotalExportsCount: 3,
					UsedPercent:       33.3,
					RiskCues: []report.RiskCue{
						{Code: "dynamic-loader", Severity: "medium", Message: "dynamic require/import usage found"},
					},
					RuntimeUsage: &report.RuntimeUsage{
						LoadCount: 1,
					},
					Recommendations: []report.Recommendation{
						{Code: "prefer-subpath-imports", Priority: "medium", Message: "Prefer subpath imports."},
					},
				},
			},
		},
	}

	var out bytes.Buffer
	detail := NewDetail(&out, analyzer, report.NewFormatter(), ".", "js-ts")
	if err := detail.Show(context.Background(), "risky"); err != nil {
		t.Fatalf("show detail: %v", err)
	}

	output := out.String()
	if !strings.Contains(output, "Risk cues (1)") {
		t.Fatalf("expected risk cues section, got: %s", output)
	}
	if !strings.Contains(output, "[MEDIUM] dynamic-loader") {
		t.Fatalf("expected risk cue entry, got: %s", output)
	}
	if !strings.Contains(output, "Runtime usage") || !strings.Contains(output, "load count: 1") {
		t.Fatalf("expected runtime section, got: %s", output)
	}
	if !strings.Contains(output, "Recommendations (1)") {
		t.Fatalf("expected recommendations section, got: %s", output)
	}
	if !strings.Contains(output, "[MEDIUM] prefer-subpath-imports") {
		t.Fatalf("expected recommendation entry, got: %s", output)
	}
}

type captureAnalyzer struct {
	lastRequest analysis.Request
	report      report.Report
}

func (c *captureAnalyzer) Analyse(ctx context.Context, req analysis.Request) (report.Report, error) {
	c.lastRequest = req
	return c.report, nil
}

func TestDetailParsesLanguagePrefix(t *testing.T) {
	analyzer := &captureAnalyzer{
		report: report.Report{
			Dependencies: []report.DependencyReport{
				{Name: "requests", Language: "python"},
			},
		},
	}

	var out bytes.Buffer
	detail := NewDetail(&out, analyzer, report.NewFormatter(), ".", "all")
	if err := detail.Show(context.Background(), "python:requests"); err != nil {
		t.Fatalf("show detail: %v", err)
	}
	if analyzer.lastRequest.Language != "python" {
		t.Fatalf("expected language override python, got %q", analyzer.lastRequest.Language)
	}
	if analyzer.lastRequest.Dependency != "requests" {
		t.Fatalf("expected dependency requests, got %q", analyzer.lastRequest.Dependency)
	}
}

func TestDetailHelpersAndErrors(t *testing.T) {
	var out bytes.Buffer
	detail := NewDetail(&out, stubAnalyzer{report: report.Report{}}, report.NewFormatter(), ".", "")
	if err := detail.Show(context.Background(), ""); err == nil {
		t.Fatalf("expected error when dependency is empty")
	}
	if !strings.Contains(NewDetail(&out, stubAnalyzer{report: report.Report{}}, report.NewFormatter(), ".", "").Language, "auto") {
		t.Fatalf("expected default language to be auto")
	}

	out.Reset()
	printImportList(&out, "Used imports", nil)
	printImportList(&out, "Used imports", []report.ImportUse{{Name: "map", Module: "lodash", Locations: []report.Location{{File: "index.js", Line: 2}}}})
	printExportsList(&out, "Unused exports", nil)
	printExportsList(&out, "Unused exports", []report.SymbolRef{{Name: "mystery"}})
	printRiskCues(&out, nil)
	printRecommendations(&out, nil)
	printRuntimeUsage(&out, nil)
	if !strings.Contains(out.String(), "(none)") {
		t.Fatalf("expected none labels in helper output")
	}
	if !strings.Contains(out.String(), "(unknown)") {
		t.Fatalf("expected unknown module label for empty module exports")
	}

	if dep, ok := isDetailCommand("open lodash"); !ok || dep != "lodash" {
		t.Fatalf("expected open detail command parse")
	}
	if dep, ok := isDetailCommand("detail js-ts:lodash"); !ok || dep != "js-ts:lodash" {
		t.Fatalf("expected detail command parse")
	}
	if _, ok := isDetailCommand("open"); ok {
		t.Fatalf("expected invalid detail command to fail")
	}
}

type failingDetailAnalyzer struct {
	err error
}

func (f failingDetailAnalyzer) Analyse(context.Context, analysis.Request) (report.Report, error) {
	return report.Report{}, f.err
}

func TestDetailShowNoDataAndAnalyzerError(t *testing.T) {
	var out bytes.Buffer
	noData := NewDetail(&out, stubAnalyzer{report: report.Report{}}, report.NewFormatter(), ".", "js-ts")
	if err := noData.Show(context.Background(), "missing"); err != nil {
		t.Fatalf("show detail no data: %v", err)
	}
	if !strings.Contains(out.String(), `No data for dependency "missing"`) {
		t.Fatalf("expected no-data message, got %q", out.String())
	}

	expected := errors.New("analyse failed")
	errDetail := NewDetail(&out, failingDetailAnalyzer{err: expected}, report.NewFormatter(), ".", "js-ts")
	if err := errDetail.Show(context.Background(), "dep"); !errors.Is(err, expected) {
		t.Fatalf("expected analyzer error to propagate, got %v", err)
	}
}

func TestDetailRationaleAndRuntimeOnlyOutput(t *testing.T) {
	var out bytes.Buffer
	printRecommendations(&out, []report.Recommendation{
		{
			Code:      "rec",
			Priority:  "high",
			Message:   "message",
			Rationale: "because",
		},
	})
	printRuntimeUsage(&out, &report.RuntimeUsage{LoadCount: 2, RuntimeOnly: true})
	text := out.String()
	if !strings.Contains(text, "rationale: because") {
		t.Fatalf("expected rationale output, got %q", text)
	}
	if !strings.Contains(text, "runtime-only: true") {
		t.Fatalf("expected runtime-only output, got %q", text)
	}
}

func TestDetailShowWarningsAndCommandBranches(t *testing.T) {
	analyzer := stubAnalyzer{
		report: report.Report{
			Warnings: []string{"warn-1"},
			Dependencies: []report.DependencyReport{
				{Name: "dep", UsedExportsCount: 1, TotalExportsCount: 2, UsedPercent: 50},
			},
		},
	}
	var out bytes.Buffer
	detail := NewDetail(&out, analyzer, report.NewFormatter(), ".", "js-ts")
	if err := detail.Show(context.Background(), "dep"); err != nil {
		t.Fatalf("show detail: %v", err)
	}
	if !strings.Contains(out.String(), "Warnings:") {
		t.Fatalf("expected warnings section in detail output, got %q", out.String())
	}

	if _, ok := isDetailCommand(""); ok {
		t.Fatalf("expected empty input not to be a detail command")
	}
	if _, ok := isDetailCommand("noop dep"); ok {
		t.Fatalf("expected non-open command not to be a detail command")
	}
}
