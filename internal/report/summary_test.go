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

func TestComputeSummaryCountsUnknownDeniedLicense(t *testing.T) {
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
	}
	if summary.UnknownLicenseCount != 1 {
		t.Fatalf("expected one unknown license, got %#v", summary)
	}
	if summary.DeniedLicenseCount != 1 {
		t.Fatalf("expected denied count to include unknown denied licenses, got %#v", summary)
	}
	if summary.KnownLicenseCount != 0 {
		t.Fatalf("expected no known licenses, got %#v", summary)
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
