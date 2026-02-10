package report

import "testing"

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
