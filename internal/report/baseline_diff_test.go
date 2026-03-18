package report

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestApplyBaselineComputesDelta(t *testing.T) {
	baseline := Report{
		Dependencies: []DependencyReport{
			{Name: "alpha", UsedExportsCount: 5, TotalExportsCount: 10},
		},
	}
	current := Report{
		Dependencies: []DependencyReport{
			{Name: "alpha", UsedExportsCount: 4, TotalExportsCount: 10},
		},
	}

	updated, err := ApplyBaseline(current, baseline)
	if err != nil {
		t.Fatalf("apply baseline: %v", err)
	}
	if updated.WasteIncreasePercent == nil {
		t.Fatalf("expected waste increase percent to be set")
	}
	if *updated.WasteIncreasePercent <= 0 {
		t.Fatalf("expected waste to increase, got %f", *updated.WasteIncreasePercent)
	}
}

func TestWastePercentNoSummary(t *testing.T) {
	if _, ok := WastePercent(nil); ok {
		t.Fatalf("expected no waste percent for nil summary")
	}
	if _, ok := WastePercent(&Summary{TotalExportsCount: 0}); ok {
		t.Fatalf("expected no waste percent for zero totals")
	}
}

func TestApplyBaselineMissingAndZeroTotalsErrors(t *testing.T) {
	_, err := ApplyBaseline(Report{}, Report{})
	if err == nil {
		t.Fatalf("expected missing baseline summary error")
	}
	if !errors.Is(err, ErrBaselineMissing) {
		t.Fatalf("expected ErrBaselineMissing, got %v", err)
	}

	_, err = ApplyBaseline(Report{Dependencies: []DependencyReport{{Name: "dep", UsedExportsCount: 1, TotalExportsCount: 2}}}, Report{Summary: &Summary{DependencyCount: 1, UsedExportsCount: 0, TotalExportsCount: 0, UsedPercent: 0}})
	if err == nil || !strings.Contains(err.Error(), "baseline total exports count is zero") {
		t.Fatalf("expected baseline zero-total error, got %v", err)
	}
}

func TestApplyBaselineCurrentWithoutTotalsError(t *testing.T) {
	_, err := ApplyBaseline(Report{Dependencies: []DependencyReport{{Name: "dep", UsedExportsCount: 0, TotalExportsCount: 0}}}, Report{Dependencies: []DependencyReport{{Name: "dep", UsedExportsCount: 1, TotalExportsCount: 1}}})
	if err == nil || !strings.Contains(err.Error(), "current report has no export totals") {
		t.Fatalf("expected current report totals error, got %v", err)
	}
}

func TestSummaryHelpersHandleNilInputs(t *testing.T) {
	if got := safeSummaryField(nil, func(summary *Summary) int {
		return summary.DependencyCount
	}); got != 0 {
		t.Fatalf("expected nil safeSummaryField to return 0, got %d", got)
	}

	if got := safeSummaryFloat(nil, func(summary *Summary) float64 {
		return summary.UsedPercent
	}); got != 0 {
		t.Fatalf("expected nil safeSummaryFloat to return 0, got %f", got)
	}

	if got := wasteFromSummary(nil); got != 0 {
		t.Fatalf("expected nil summary waste to return 0, got %f", got)
	}

	if got := wasteFromSummary(&Summary{TotalExportsCount: 0, UsedPercent: 75}); got != 0 {
		t.Fatalf("expected zero-export summary waste to return 0, got %f", got)
	}

	if got := wasteFromSummary(&Summary{TotalExportsCount: 10, UsedPercent: 72.5}); got != 27.5 {
		t.Fatalf("expected waste percentage to be derived from used percent, got %f", got)
	}
}

func TestComputeBaselineComparisonDeterministic(t *testing.T) {
	current := Report{
		Dependencies: []DependencyReport{
			{Name: "b", Language: "js-ts", UsedExportsCount: 1, TotalExportsCount: 4, UsedPercent: 25, EstimatedUnusedBytes: 100},
			{Name: "a", Language: "go", UsedExportsCount: 3, TotalExportsCount: 3, UsedPercent: 100, EstimatedUnusedBytes: 0},
		},
	}
	baseline := Report{
		Dependencies: []DependencyReport{
			{Name: "b", Language: "js-ts", UsedExportsCount: 2, TotalExportsCount: 4, UsedPercent: 50, EstimatedUnusedBytes: 50},
			{Name: "c", Language: "python", UsedExportsCount: 1, TotalExportsCount: 2, UsedPercent: 50, EstimatedUnusedBytes: 10},
		},
	}

	comparison := ComputeBaselineComparison(current, baseline)
	gotOrder := make([]string, 0, len(comparison.Dependencies))
	for _, dep := range comparison.Dependencies {
		gotOrder = append(gotOrder, dep.Language+"/"+dep.Name)
	}
	wantOrder := []string{"go/a", "js-ts/b", "python/c"}
	if !slices.Equal(gotOrder, wantOrder) {
		t.Fatalf("unexpected deterministic delta ordering: got=%v want=%v", gotOrder, wantOrder)
	}
	if len(comparison.Added) != 1 || comparison.Added[0].Name != "a" {
		t.Fatalf("expected one added dependency, got %#v", comparison.Added)
	}
	if len(comparison.Removed) != 1 || comparison.Removed[0].Name != "c" {
		t.Fatalf("expected one removed dependency, got %#v", comparison.Removed)
	}
	if len(comparison.Regressions) != 1 || comparison.Regressions[0].Name != "b" {
		t.Fatalf("expected one regression dependency, got %#v", comparison.Regressions)
	}
	if len(comparison.Progressions) != 1 || comparison.Progressions[0].Name != "c" {
		t.Fatalf("expected one progression dependency, got %#v", comparison.Progressions)
	}
}

func TestComputeBaselineComparisonTracksNewDeniedLicenses(t *testing.T) {
	current := Report{
		Dependencies: []DependencyReport{
			{
				Name:              "a",
				Language:          "js-ts",
				UsedExportsCount:  1,
				TotalExportsCount: 2,
				UsedPercent:       50,
				License: &DependencyLicense{
					SPDX:   "GPL-3.0-ONLY",
					Denied: true,
				},
			},
		},
	}
	baseline := Report{
		Dependencies: []DependencyReport{
			{
				Name:              "a",
				Language:          "js-ts",
				UsedExportsCount:  1,
				TotalExportsCount: 2,
				UsedPercent:       50,
				License: &DependencyLicense{
					SPDX:   "MIT",
					Denied: false,
				},
			},
		},
	}

	comparison := ComputeBaselineComparison(current, baseline)
	if len(comparison.NewDeniedLicenses) != 1 {
		t.Fatalf("expected one new denied license, got %#v", comparison.NewDeniedLicenses)
	}
	if !comparison.Dependencies[0].DeniedIntroduced {
		t.Fatalf("expected dependency delta to flag deniedIntroduced")
	}
	if comparison.SummaryDelta.DeniedLicenseCountDelta != 1 {
		t.Fatalf("expected denied license count delta to be 1, got %d", comparison.SummaryDelta.DeniedLicenseCountDelta)
	}
}
