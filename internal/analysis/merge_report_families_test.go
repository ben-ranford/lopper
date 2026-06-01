package analysis

import (
	"testing"
	"time"

	"github.com/ben-ranford/lopper/internal/report"
)

func TestMergeReportsCoordinatesFamiliesInStableOrder(t *testing.T) {
	firstGeneratedAt := time.Date(2026, time.January, 10, 10, 0, 0, 0, time.UTC)
	secondGeneratedAt := firstGeneratedAt.Add(2 * time.Hour)

	reports := []report.Report{
		{
			GeneratedAt: firstGeneratedAt,
			Warnings:    []string{"w-first"},
			UsageUncertainty: &report.UsageUncertainty{
				ConfirmedImportUses: 1,
				UncertainImportUses: 2,
				Samples: []report.Location{
					{File: "a.js"},
					{File: "b.js"},
					{File: "c.js"},
				},
			},
			Dependencies: []report.DependencyReport{
				{Language: "js-ts", Name: "lodash", UsedExportsCount: 1, TotalExportsCount: 2, UsedImports: []report.ImportUse{{Module: "lodash", Name: "map"}}},
				{Language: "go", Name: "cobra", UsedExportsCount: 1, TotalExportsCount: 1},
			},
		},
		{
			GeneratedAt: secondGeneratedAt,
			Warnings:    []string{"w-second"},
			UsageUncertainty: &report.UsageUncertainty{
				ConfirmedImportUses: 3,
				UncertainImportUses: 4,
				Samples: []report.Location{
					{File: "d.js"},
					{File: "e.js"},
					{File: "f.js"},
				},
			},
			Dependencies: []report.DependencyReport{
				{Language: "js-ts", Name: "lodash", UsedExportsCount: 2, TotalExportsCount: 3, UsedImports: []report.ImportUse{{Module: "lodash", Name: "filter"}}},
				{Language: "python", Name: "requests", UsedExportsCount: 1, TotalExportsCount: 2},
			},
		},
	}

	merged := mergeReports("/repo", reports)

	if merged.RepoPath != "/repo" {
		t.Fatalf("expected repo path to be preserved, got %q", merged.RepoPath)
	}
	if merged.GeneratedAt != secondGeneratedAt {
		t.Fatalf("expected latest generatedAt timestamp, got %v want %v", merged.GeneratedAt, secondGeneratedAt)
	}
	if len(merged.Warnings) != 2 || merged.Warnings[0] != "w-first" || merged.Warnings[1] != "w-second" {
		t.Fatalf("expected warning merge order to follow report order, got %#v", merged.Warnings)
	}
	if merged.UsageUncertainty == nil {
		t.Fatal("expected merged usage uncertainty")
	}
	if merged.UsageUncertainty.ConfirmedImportUses != 4 || merged.UsageUncertainty.UncertainImportUses != 6 {
		t.Fatalf("unexpected merged usage uncertainty counts: %#v", merged.UsageUncertainty)
	}
	if len(merged.UsageUncertainty.Samples) != 5 {
		t.Fatalf("expected capped sample list of five, got %#v", merged.UsageUncertainty.Samples)
	}
	if got := merged.UsageUncertainty.Samples[0].File; got != "a.js" {
		t.Fatalf("expected first sample to come from first report, got %q", got)
	}
	if got := merged.UsageUncertainty.Samples[4].File; got != "e.js" {
		t.Fatalf("expected last kept sample to come from second report, got %q", got)
	}

	if len(merged.Dependencies) != 3 {
		t.Fatalf("expected three merged dependencies, got %#v", merged.Dependencies)
	}
	if merged.Dependencies[0].Language != "go" || merged.Dependencies[0].Name != "cobra" {
		t.Fatalf("expected deterministic dependency sort order, got first row %#v", merged.Dependencies[0])
	}
	if merged.Dependencies[1].Language != "js-ts" || merged.Dependencies[1].Name != "lodash" {
		t.Fatalf("expected lodash row to be second, got %#v", merged.Dependencies[1])
	}
	if merged.Dependencies[2].Language != "python" || merged.Dependencies[2].Name != "requests" {
		t.Fatalf("expected requests row to be third, got %#v", merged.Dependencies[2])
	}
	if merged.Dependencies[1].UsedExportsCount != 3 || merged.Dependencies[1].TotalExportsCount != 5 {
		t.Fatalf("expected duplicate dependency rows to merge export counts, got %#v", merged.Dependencies[1])
	}
	if len(merged.Dependencies[1].UsedImports) != 2 {
		t.Fatalf("expected duplicate dependency used imports to merge, got %#v", merged.Dependencies[1].UsedImports)
	}
}
