package report

import (
	"math"
	"strings"
	"testing"
)

const unexpectedErrFmt = "unexpected error: %v"

func TestFormatTable(t *testing.T) {
	reportData := Report{
		EffectiveThresholds: &EffectiveThresholds{
			FailOnIncreasePercent:             2,
			LowConfidenceWarningPercent:       35,
			MinUsagePercentForRecommendations: 45,
		},
		LanguageBreakdown: []LanguageSummary{
			{Language: "js-ts", DependencyCount: 1, UsedExportsCount: 2, TotalExportsCount: 10, UsedPercent: 20.0},
		},
		Dependencies: []DependencyReport{
			{
				Language:             "js-ts",
				Name:                 "lodash",
				UsedExportsCount:     2,
				TotalExportsCount:    10,
				EstimatedUnusedBytes: 1024,
				TopUsedSymbols: []SymbolUsage{
					{Name: "map", Count: 3},
				},
			},
		},
	}

	output, err := NewFormatter().Format(reportData, FormatTable)
	if err != nil {
		t.Fatalf(unexpectedErrFmt, err)
	}
	if !strings.Contains(output, "lodash") {
		t.Fatalf("expected output to include dependency name")
	}
	if !strings.Contains(output, "Language") {
		t.Fatalf("expected output to include language column")
	}
	if !strings.Contains(output, "Languages:") {
		t.Fatalf("expected output to include language breakdown")
	}
	if !strings.Contains(output, "Effective thresholds:") {
		t.Fatalf("expected output to include effective thresholds")
	}
	if !strings.Contains(output, "map") {
		t.Fatalf("expected output to include top symbol")
	}
}

func TestFormatJSON(t *testing.T) {
	reportData := Report{RepoPath: "."}
	output, err := NewFormatter().Format(reportData, FormatJSON)
	if err != nil {
		t.Fatalf(unexpectedErrFmt, err)
	}
	if !strings.Contains(output, "repoPath") {
		t.Fatalf("expected json output to include repoPath")
	}
}

func TestFormatEmptyAndWarnings(t *testing.T) {
	reportData := Report{
		Warnings: []string{"warning-1"},
		EffectiveThresholds: &EffectiveThresholds{
			FailOnIncreasePercent:             1,
			LowConfidenceWarningPercent:       40,
			MinUsagePercentForRecommendations: 35,
		},
	}
	output, err := NewFormatter().Format(reportData, FormatTable)
	if err != nil {
		t.Fatalf(unexpectedErrFmt, err)
	}
	if !strings.Contains(output, "No dependencies to report.") {
		t.Fatalf("expected empty report marker")
	}
	if !strings.Contains(output, "Warnings:") {
		t.Fatalf("expected warnings section")
	}
	if !strings.Contains(output, "fail_on_increase_percent") {
		t.Fatalf("expected threshold values in empty report output")
	}
}

func TestFormattingHelpers(t *testing.T) {
	if got := formatBytes(0); got != "0 B" {
		t.Fatalf("unexpected 0-byte format: %q", got)
	}
	if got := formatBytes(1024); !strings.Contains(got, "KB") {
		t.Fatalf("expected KB format, got %q", got)
	}
	if got := formatBytes(-1024 * 1024); !strings.HasPrefix(got, "-") || !strings.Contains(got, "MB") {
		t.Fatalf("expected negative MB format, got %q", got)
	}
	if got := formatTopSymbols(nil); got != "-" {
		t.Fatalf("expected top symbols dash for empty list, got %q", got)
	}
	if got := formatTopSymbols([]SymbolUsage{{Name: "map", Count: 2}}); !strings.Contains(got, "map (2)") {
		t.Fatalf("expected symbol count annotation, got %q", got)
	}
	if hasLanguageColumn([]DependencyReport{{Name: "x"}}) {
		t.Fatalf("did not expect language column without language values")
	}
}

func TestFormatUnknownFormat(t *testing.T) {
	if _, err := NewFormatter().Format(Report{}, Format("weird")); err == nil {
		t.Fatalf("expected unknown format error")
	}
}

func TestFormatTableUsedPercentFallbackAndNoLanguageColumn(t *testing.T) {
	reportData := Report{
		Dependencies: []DependencyReport{
			{
				Name:              "dep",
				UsedExportsCount:  1,
				TotalExportsCount: 4,
				UsedPercent:       0,
			},
		},
	}
	output, err := NewFormatter().Format(reportData, FormatTable)
	if err != nil {
		t.Fatalf("format table: %v", err)
	}
	if strings.Contains(output, "Language\tDependency") {
		t.Fatalf("did not expect language column for single-language anonymous rows")
	}
	if !strings.Contains(output, "25.0") {
		t.Fatalf("expected used-percent fallback calculation in output, got %q", output)
	}
}

func TestFormatBytesGB(t *testing.T) {
	value := int64(1024 * 1024 * 1024)
	if got := formatBytes(value); !strings.Contains(got, "GB") {
		t.Fatalf("expected GB output, got %q", got)
	}
}

func TestFormatCandidateFields(t *testing.T) {
	candidate := &RemovalCandidate{
		Score:      87.34,
		Usage:      91.2,
		Impact:     45.6,
		Confidence: 78.9,
	}
	if got := formatCandidateScore(candidate); got != "87.3" {
		t.Fatalf("unexpected candidate score format: %q", got)
	}
	if got := formatScoreComponents(candidate); got != "U:91.2 I:45.6 C:78.9" {
		t.Fatalf("unexpected score components format: %q", got)
	}
}

func TestFormatTopSymbolsSingleCountOmitsCounter(t *testing.T) {
	if got := formatTopSymbols([]SymbolUsage{{Name: "uniq", Count: 1}}); got != "uniq" {
		t.Fatalf("expected single-count symbol without annotation, got %q", got)
	}
}

func TestFormatTableIncludesSummary(t *testing.T) {
	reportData := Report{
		Summary: &Summary{
			DependencyCount:   1,
			UsedExportsCount:  2,
			TotalExportsCount: 4,
			UsedPercent:       50,
		},
		Dependencies: []DependencyReport{
			{Name: "dep", UsedExportsCount: 2, TotalExportsCount: 4, UsedPercent: 50},
		},
	}
	output, err := NewFormatter().Format(reportData, FormatTable)
	if err != nil {
		t.Fatalf("format table with summary: %v", err)
	}
	if !strings.Contains(output, "Summary: 1 deps, Used/Total: 2/4 (50.0%)") {
		t.Fatalf("expected summary header in output, got %q", output)
	}
}

func TestFormatJSONReturnsMarshalErrorForNonFiniteValue(t *testing.T) {
	reportData := Report{
		Dependencies: []DependencyReport{
			{
				Name: "dep",
				RemovalCandidate: &RemovalCandidate{
					Score: math.NaN(),
				},
			},
		},
	}
	if _, err := NewFormatter().Format(reportData, FormatJSON); err == nil {
		t.Fatalf("expected json marshal error for NaN candidate score")
	}
}
