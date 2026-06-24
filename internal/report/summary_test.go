package report

import "testing"

func TestComputeLanguageBreakdown(t *testing.T) {
	dependencies := []DependencyReport{
		{Language: "js-ts", Name: "lodash", UsedExportsCount: 2, TotalExportsCount: 4},
		{Language: "python", Name: "requests", UsedExportsCount: 1, TotalExportsCount: 2},
		{Language: "js-ts", Name: "react", UsedExportsCount: 1, TotalExportsCount: 2},
	}

	breakdown := ComputeLanguageBreakdown(dependencies)
	if len(breakdown) != 2 {
		t.Fatalf("expected two language summaries, got %d", len(breakdown))
	}
	if breakdown[0].Language != "js-ts" || breakdown[0].DependencyCount != 2 {
		t.Fatalf("unexpected js-ts breakdown: %#v", breakdown[0])
	}
	if breakdown[1].Language != "python" || breakdown[1].DependencyCount != 1 {
		t.Fatalf("unexpected python breakdown: %#v", breakdown[1])
	}
}

func TestComputeSummaryIncludesReachabilityRollup(t *testing.T) {
	summary := ComputeSummary([]DependencyReport{
		{Name: "alpha", ReachabilityConfidence: &ReachabilityConfidence{Score: 80}},
		{Name: "beta", ReachabilityConfidence: &ReachabilityConfidence{Score: 60}},
	})

	if summary == nil || summary.Reachability == nil {
		t.Fatalf("expected reachability rollup on summary, got %#v", summary)
	}
	if summary.Reachability.Model != reachabilityConfidenceModelV2 {
		t.Fatalf("expected reachability rollup model, got %#v", summary.Reachability)
	}
	if summary.Reachability.AverageScore != 70 || summary.Reachability.LowestScore != 60 || summary.Reachability.HighestScore != 80 {
		t.Fatalf("unexpected reachability rollup values: %#v", summary.Reachability)
	}
}

func TestComputeSummaryCountsDeniedUnknownLicenseSeparately(t *testing.T) {
	summary := ComputeSummary([]DependencyReport{
		{
			Name: "mystery",
			License: &DependencyLicense{
				Unknown: true,
				Denied:  true,
			},
		},
	})

	if summary == nil {
		t.Fatal("expected summary")
		return
	}
	if summary.UnknownLicenseCount != 0 {
		t.Fatalf("expected denied unknown license to be excluded from unknown count, got %#v", summary)
	}
	if summary.DeniedLicenseCount != 1 {
		t.Fatalf("expected denied count to include unknown denied licenses, got %#v", summary)
	}
	if summary.KnownLicenseCount != 0 {
		t.Fatalf("expected no known licenses, got %#v", summary)
	}
}

func TestComputeSummaryCountsDeniedSPDXLicenseSeparately(t *testing.T) {
	summary := ComputeSummary([]DependencyReport{
		{
			Name: "copyleft",
			License: &DependencyLicense{
				SPDX:   "GPL-3.0-ONLY",
				Denied: true,
			},
		},
		{
			Name: "permissive",
			License: &DependencyLicense{
				SPDX: "MIT",
			},
		},
		{Name: "unlicensed"},
	})

	if summary == nil {
		t.Fatal("expected summary")
		return
	}
	if summary.KnownLicenseCount != 1 {
		t.Fatalf("expected only non-denied SPDX license in known count, got %#v", summary)
	}
	if summary.UnknownLicenseCount != 1 {
		t.Fatalf("expected only non-denied unknown license in unknown count, got %#v", summary)
	}
	if summary.DeniedLicenseCount != 1 {
		t.Fatalf("expected denied SPDX license in denied count, got %#v", summary)
	}
	if got := summary.KnownLicenseCount + summary.UnknownLicenseCount + summary.DeniedLicenseCount; got != summary.DependencyCount {
		t.Fatalf("expected disjoint license counts to sum to dependencies, got %d for %#v", got, summary)
	}
}

func TestComputeSummaryAndLanguageBreakdownEmpty(t *testing.T) {
	if got := ComputeSummary(nil); got != nil {
		t.Fatalf("expected nil summary for empty dependencies, got %#v", got)
	}
	if got := ComputeLanguageBreakdown(nil); len(got) != 0 {
		t.Fatalf("expected empty language breakdown for empty dependencies, got %#v", got)
	}
}

func TestComputeLanguageBreakdownAdditionalBranches(t *testing.T) {
	if got := ComputeLanguageBreakdown([]DependencyReport{{Name: "dep", UsedExportsCount: 1, TotalExportsCount: 2}}); len(got) != 0 {
		t.Fatalf("expected empty breakdown when all dependencies have empty language, got %#v", got)
	}

	breakdown := ComputeLanguageBreakdown([]DependencyReport{
		{Language: "go", Name: "dep", UsedExportsCount: 1, TotalExportsCount: 0},
	})
	if len(breakdown) != 1 {
		t.Fatalf("expected one language summary, got %#v", breakdown)
	}
	if breakdown[0].UsedPercent != 0 {
		t.Fatalf("expected zero used percent when totals are zero, got %#v", breakdown[0])
	}
}
