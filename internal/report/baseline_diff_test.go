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

func TestWasteFromDependencyRecomputesNonPositiveUsedPercent(t *testing.T) {
	dependency := DependencyReport{
		UsedExportsCount:  1,
		TotalExportsCount: 4,
		UsedPercent:       -1,
	}

	if got := wasteFromDependency(dependency); got != 75 {
		t.Fatalf("expected waste to be recomputed from used/total counts, got %f", got)
	}

	dependency.UsedPercent = 0
	if got := wasteFromDependency(dependency); got != 75 {
		t.Fatalf("expected zero used percent to be recomputed from used/total counts, got %f", got)
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
	if len(comparison.Progressions) != 0 {
		t.Fatalf("expected no progression dependencies, got %#v", comparison.Progressions)
	}
}

func TestComputeBaselineComparisonPreservesDuplicateDependencyDeltas(t *testing.T) {
	current := Report{Dependencies: []DependencyReport{
		{Language: "js-ts", Name: "duplicate", UsedExportsCount: 1, TotalExportsCount: 2},
		{Language: "js-ts", Name: "duplicate", UsedExportsCount: 11, TotalExportsCount: 12},
	}}
	baseline := Report{Dependencies: []DependencyReport{
		{Language: "js-ts", Name: "duplicate", TotalExportsCount: 2},
		{Language: "js-ts", Name: "duplicate", UsedExportsCount: 2, TotalExportsCount: 12},
	}}

	comparison := ComputeBaselineComparison(current, baseline)
	if len(comparison.Dependencies) != 2 {
		t.Fatalf("expected both duplicate dependency deltas, got %#v", comparison.Dependencies)
	}
	if comparison.Dependencies[0].UsedExportsCountDelta != 1 || comparison.Dependencies[1].UsedExportsCountDelta != 9 {
		t.Fatalf("expected per-instance duplicate deltas 1 and 9, got %#v", comparison.Dependencies)
	}
}

func TestAppendDependencyDeltaClassifiesRegressionsOnlyForChangedDependencies(t *testing.T) {
	comparison := BaselineComparison{}

	appendDependencyDelta(&comparison, DependencyDelta{Kind: DependencyDeltaAdded, Name: "added", WastePercentDelta: 90})
	appendDependencyDelta(&comparison, DependencyDelta{Kind: DependencyDeltaRemoved, Name: "removed", WastePercentDelta: -90})
	appendDependencyDelta(&comparison, DependencyDelta{Kind: DependencyDeltaChanged, Name: "reg", WastePercentDelta: 1})
	appendDependencyDelta(&comparison, DependencyDelta{Kind: DependencyDeltaChanged, Name: "prog", WastePercentDelta: -1})

	if len(comparison.Regressions) != 1 || comparison.Regressions[0].Name != "reg" {
		t.Fatalf("expected only changed dependency regressions, got %#v", comparison.Regressions)
	}
	if len(comparison.Progressions) != 1 || comparison.Progressions[0].Name != "prog" {
		t.Fatalf("expected only changed dependency progressions, got %#v", comparison.Progressions)
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
	if comparison.SummaryDelta.KnownLicenseCountDelta != -1 {
		t.Fatalf("expected denied SPDX license to leave known count, got %d", comparison.SummaryDelta.KnownLicenseCountDelta)
	}
	if comparison.SummaryDelta.UnknownLicenseCountDelta != 0 {
		t.Fatalf("expected unknown license count delta to remain 0, got %d", comparison.SummaryDelta.UnknownLicenseCountDelta)
	}
}

func TestComputeBaselineComparisonRuntimeMissingBaselineDataIsNotZeroLoads(t *testing.T) {
	current := Report{
		Dependencies: []DependencyReport{{
			Name:              "alpha",
			Language:          "js-ts",
			UsedExportsCount:  1,
			TotalExportsCount: 4,
			UsedPercent:       25,
			RuntimeUsage: &RuntimeUsage{
				LoadCount:   3,
				Correlation: RuntimeCorrelationOverlap,
			},
		}},
	}
	baseline := Report{
		Dependencies: []DependencyReport{{
			Name:              "alpha",
			Language:          "js-ts",
			UsedExportsCount:  2,
			TotalExportsCount: 4,
			UsedPercent:       50,
		}},
	}

	comparison := ComputeBaselineComparison(current, baseline)
	if len(comparison.RuntimeRegressions) != 0 {
		t.Fatalf("missing baseline runtime data must not create runtime regressions: %#v", comparison.RuntimeRegressions)
	}
	if len(comparison.Dependencies) != 1 {
		t.Fatalf("expected export delta to keep dependency row, got %#v", comparison.Dependencies)
	}
	runtimeDelta := comparison.Dependencies[0].RuntimeDelta
	if runtimeDelta == nil {
		t.Fatalf("expected runtime availability context on changed dependency")
	}
	if runtimeDelta.Comparable {
		t.Fatalf("expected runtime delta with missing baseline to be non-comparable")
	}
	if runtimeDelta.BaselinePresent || !runtimeDelta.CurrentPresent {
		t.Fatalf("unexpected runtime availability flags: %#v", runtimeDelta)
	}
	if runtimeDelta.LoadCountDelta != nil {
		t.Fatalf("missing baseline runtime data must not produce load delta, got %d", *runtimeDelta.LoadCountDelta)
	}
}

func TestComputeBaselineComparisonRuntimeOnlyChangesDoNotPolluteAddedRemovedDependencies(t *testing.T) {
	current := Report{
		Dependencies: []DependencyReport{{
			Name:              "new-runtime",
			Language:          "js-ts",
			UsedExportsCount:  1,
			TotalExportsCount: 1,
			UsedPercent:       100,
			RuntimeUsage:      &RuntimeUsage{LoadCount: 3, Correlation: RuntimeCorrelationRuntimeOnly, RuntimeOnly: true},
		}},
	}
	baseline := Report{
		Dependencies: []DependencyReport{{
			Name:              "removed-runtime",
			Language:          "js-ts",
			UsedExportsCount:  1,
			TotalExportsCount: 1,
			UsedPercent:       100,
			RuntimeUsage:      &RuntimeUsage{LoadCount: 2, Correlation: RuntimeCorrelationOverlap},
		}},
	}

	comparison := ComputeBaselineComparison(current, baseline)
	if len(comparison.Added) != 1 || comparison.Added[0].RuntimeDelta != nil {
		t.Fatalf("added dependencies should not produce runtime baseline deltas: %#v", comparison.Added)
	}
	if len(comparison.Removed) != 1 || comparison.Removed[0].RuntimeDelta != nil {
		t.Fatalf("removed dependencies should not produce runtime baseline deltas: %#v", comparison.Removed)
	}
	if len(comparison.RuntimeRegressions) != 0 || len(comparison.RuntimeImprovements) != 0 {
		t.Fatalf("new/removed dependencies should not be classified as runtime regressions/improvements: %#v %#v", comparison.RuntimeRegressions, comparison.RuntimeImprovements)
	}
}

func TestComputeBaselineComparisonRuntimeCorrelationTransitions(t *testing.T) {
	current := Report{
		Dependencies: []DependencyReport{
			{
				Name:              "regression",
				Language:          "js-ts",
				UsedExportsCount:  1,
				TotalExportsCount: 2,
				UsedPercent:       50,
				RuntimeUsage: &RuntimeUsage{
					LoadCount:     2,
					Correlation:   RuntimeCorrelationRuntimeOnly,
					RuntimeOnly:   true,
					Modules:       []RuntimeModuleUsage{{Module: "regression/runtime", Count: 2}},
					ParentModules: []RuntimeModuleUsage{{Module: "src/new-parent.js", Count: 2}},
					Entrypoints:   []RuntimeModuleUsage{{Module: "src/new-entry.js", Count: 2}},
				},
			},
			{
				Name:              "improvement",
				Language:          "js-ts",
				UsedExportsCount:  1,
				TotalExportsCount: 2,
				UsedPercent:       50,
				RuntimeUsage: &RuntimeUsage{
					LoadCount:   0,
					Correlation: RuntimeCorrelationStaticOnly,
				},
			},
		},
	}
	baseline := Report{
		Dependencies: []DependencyReport{
			{
				Name:              "regression",
				Language:          "js-ts",
				UsedExportsCount:  1,
				TotalExportsCount: 2,
				UsedPercent:       50,
				RuntimeUsage: &RuntimeUsage{
					LoadCount:     0,
					Correlation:   RuntimeCorrelationStaticOnly,
					ParentModules: []RuntimeModuleUsage{{Module: "src/old-parent.js", Count: 1}},
					Entrypoints:   []RuntimeModuleUsage{{Module: "src/old-entry.js", Count: 1}},
				},
			},
			{
				Name:              "improvement",
				Language:          "js-ts",
				UsedExportsCount:  1,
				TotalExportsCount: 2,
				UsedPercent:       50,
				RuntimeUsage: &RuntimeUsage{
					LoadCount:   3,
					Correlation: RuntimeCorrelationRuntimeOnly,
					RuntimeOnly: true,
				},
			},
		},
	}

	comparison := ComputeBaselineComparison(current, baseline)
	if len(comparison.RuntimeRegressions) != 1 || comparison.RuntimeRegressions[0].Name != "regression" {
		t.Fatalf("expected one runtime regression, got %#v", comparison.RuntimeRegressions)
	}
	regressionDelta := comparison.RuntimeRegressions[0].RuntimeDelta
	if regressionDelta == nil || !regressionDelta.NewRuntimeLoads || !regressionDelta.RuntimeOnlyRegression {
		t.Fatalf("expected new runtime load and runtime-only regression flags, got %#v", regressionDelta)
	}
	if regressionDelta.BaselineCorrelation != RuntimeCorrelationStaticOnly || regressionDelta.CurrentCorrelation != RuntimeCorrelationRuntimeOnly {
		t.Fatalf("expected correlation transition, got %#v", regressionDelta)
	}
	if len(regressionDelta.ParentModulesAdded) != 1 || len(regressionDelta.ParentModulesRemoved) != 1 {
		t.Fatalf("expected parent module changes, got %#v", regressionDelta)
	}
	if len(regressionDelta.EntrypointsAdded) != 1 || len(regressionDelta.EntrypointsRemoved) != 1 {
		t.Fatalf("expected entrypoint changes, got %#v", regressionDelta)
	}

	if len(comparison.RuntimeImprovements) != 1 || comparison.RuntimeImprovements[0].Name != "improvement" {
		t.Fatalf("expected one runtime improvement, got %#v", comparison.RuntimeImprovements)
	}
	improvementDelta := comparison.RuntimeImprovements[0].RuntimeDelta
	if improvementDelta == nil || !improvementDelta.RemovedRuntimeLoads || !improvementDelta.RuntimeOnlyImprovement {
		t.Fatalf("expected removed runtime load and runtime-only improvement flags, got %#v", improvementDelta)
	}
}

func TestBaselineDiffAdditionalBranches(t *testing.T) {
	current := Report{
		Dependencies: []DependencyReport{
			{Name: "same", Language: "go", UsedExportsCount: 1, TotalExportsCount: 2, UsedPercent: 50},
		},
	}
	baseline := Report{
		Dependencies: []DependencyReport{
			{Name: "same", Language: "go", UsedExportsCount: 1, TotalExportsCount: 2, UsedPercent: 50},
		},
	}

	comparison := ComputeBaselineComparison(current, baseline)
	if comparison.UnchangedRows != 1 {
		t.Fatalf("expected one unchanged row, got %d", comparison.UnchangedRows)
	}
	if len(comparison.Dependencies) != 0 {
		t.Fatalf("expected unchanged dependencies to be omitted, got %#v", comparison.Dependencies)
	}

	if got := wasteFromDependency(DependencyReport{}); got != 0 {
		t.Fatalf("expected zero waste for zero exports, got %f", got)
	}

	currentDenied := map[string]DependencyReport{
		"go/alpha": {
			Name:     "alpha",
			Language: "go",
			License:  &DependencyLicense{SPDX: "GPL-3.0", Denied: true},
		},
		"js/beta": {
			Name:     "beta",
			Language: "js",
			License:  &DependencyLicense{SPDX: "AGPL-3.0", Denied: true},
		},
		"py/gamma": {
			Name:     "gamma",
			Language: "py",
			License:  &DependencyLicense{SPDX: "MIT", Denied: false},
		},
	}
	baselineDenied := map[string]DependencyReport{
		"js/beta": {
			Name:     "beta",
			Language: "js",
			License:  &DependencyLicense{SPDX: "AGPL-3.0", Denied: true},
		},
	}

	denied := newlyDeniedLicenses(currentDenied, baselineDenied)
	if len(denied) != 1 {
		t.Fatalf("expected only newly denied licenses, got %#v", denied)
	}
	if denied[0].Language != "go" || denied[0].Name != "alpha" || denied[0].SPDX != "GPL-3.0" {
		t.Fatalf("unexpected denied license delta: %#v", denied[0])
	}
}
