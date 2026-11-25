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
			},
			Usage: map[string]int{"Serialize": 1},
		}},
		CoverageGaps: []report.CoverageGap{{Code: report.CoverageGapRustUseStatementLimit, Language: rustAdapterID, Path: srcLibRS}},
		Warnings:     []string{rustUseStatementLimitWarning},
	}

	minUsage := 80
	requests := []language.Request{
		{Dependency: "serde", MinUsagePercentForRecommendations: &minUsage},
		{TopN: 1, MinUsagePercentForRecommendations: &minUsage},
	}
	for _, request := range requests {
		dependencies, warnings := buildRequestedRustDependencies(request, scan)
		if len(warnings) != 0 || len(dependencies) != 1 {
			t.Fatalf("unexpected report result: dependencies=%#v warnings=%#v", dependencies, warnings)
		}
		dependency := dependencies[0]
		if !dependency.UsageIncomplete || len(dependency.UsedImports) != 1 || len(dependency.UnusedImports) != 0 || len(dependency.SuppressedUnusedImports) != 1 {
			t.Fatalf("expected incomplete report to retain used and suppress unused evidence, got %#v", dependency)
		}
		if dependency.UsedExportsCount != 0 || dependency.TotalExportsCount != 0 || dependency.UsedPercent != 0 {
			t.Fatalf("expected incomplete report to omit usage totals, got %#v", dependency)
		}
		if dependency.RemovalCandidate != nil || len(dependency.Recommendations) != 0 {
			t.Fatalf("expected incomplete report to suppress usage advice and scoring, got %#v", dependency)
		}
	}
	if !slices.Equal(scan.Warnings, []string{rustUseStatementLimitWarning}) {
		t.Fatalf("expected scan diagnostic to remain unchanged, got %#v", scan.Warnings)
	}
}
