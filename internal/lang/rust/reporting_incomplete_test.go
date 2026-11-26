package rust

import (
	"slices"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
)

func TestRustUseCoverageGapSuppressesUsageAdviceForDirectAndTopNReports(t *testing.T) {
	scan := scanResult{
		Files: []fileScan{{
			Path: srcLibRS,
			Imports: []importBinding{
				{Dependency: "serde", Module: "serde", Name: "Serialize", Local: "Serialize", Location: report.Location{File: srcLibRS, Line: 1, Column: 1}},
				{Dependency: "serde", Module: "serde", Name: "Deserialize", Local: "Deserialize", Location: report.Location{File: srcLibRS, Line: 2, Column: 1}},
				{Dependency: "toml", Module: "toml", Name: "from_str", Local: "from_str", Location: report.Location{File: srcLibRS, Line: 3, Column: 1}},
			},
			Usage: map[string]int{"Serialize": 1, "from_str": 1},
		}},
		CoverageGaps: []report.CoverageGap{{Code: report.CoverageGapRustUseStatementLimit, Language: rustAdapterID, Path: srcLibRS}},
		Warnings:     []string{rustUseStatementLimitWarning},
	}

	minUsage := 80
	directDependencies, warnings := buildRequestedRustDependencies(language.Request{Dependency: "serde", MinUsagePercentForRecommendations: &minUsage}, scan)
	if len(warnings) != 0 || len(directDependencies) != 1 {
		t.Fatalf("unexpected direct report result: dependencies=%#v warnings=%#v", directDependencies, warnings)
	}
	assertIncompleteRustReport(t, directDependencies[0], 1, 1)

	topDependencies, warnings := buildRequestedRustDependencies(language.Request{TopN: 1, MinUsagePercentForRecommendations: &minUsage}, scan)
	if len(topDependencies) != 2 || topDependencies[0].Name != "serde" || topDependencies[1].Name != "toml" {
		t.Fatalf("expected incomplete top-N reports in name order, got %#v", topDependencies)
	}
	if !slices.Equal(warnings, []string{"top-N removal ranking disabled because Rust use-statement coverage is incomplete"}) {
		t.Fatalf("unexpected incomplete top-N warnings: %#v", warnings)
	}
	assertIncompleteRustReport(t, topDependencies[0], 1, 1)
	assertIncompleteRustReport(t, topDependencies[1], 1, 0)

	completeScan := scan
	completeScan.CoverageGaps = nil
	completeDependencies, warnings := buildRequestedRustDependencies(language.Request{TopN: 1, MinUsagePercentForRecommendations: &minUsage}, completeScan)
	if len(warnings) != 0 || len(completeDependencies) != 1 {
		t.Fatalf("expected complete top-N report to remain limited, got dependencies=%#v warnings=%#v", completeDependencies, warnings)
	}
	if !slices.Equal(scan.Warnings, []string{rustUseStatementLimitWarning}) {
		t.Fatalf("expected scan diagnostic to remain unchanged, got %#v", scan.Warnings)
	}
}

func assertIncompleteRustReport(t *testing.T, dependency report.DependencyReport, used, suppressed int) {
	t.Helper()
	if !dependency.UsageIncomplete || len(dependency.UsedImports) != used || len(dependency.UnusedImports) != 0 || len(dependency.SuppressedUnusedImports) != suppressed {
		t.Fatalf("expected incomplete report to retain used and suppress unused evidence, got %#v", dependency)
	}
	if dependency.UsedExportsCount != 0 || dependency.TotalExportsCount != 0 || dependency.UsedPercent != 0 {
		t.Fatalf("expected incomplete report to omit usage totals, got %#v", dependency)
	}
	if dependency.RemovalCandidate != nil || len(dependency.Recommendations) != 0 {
		t.Fatalf("expected incomplete report to suppress usage advice and scoring, got %#v", dependency)
	}
}
