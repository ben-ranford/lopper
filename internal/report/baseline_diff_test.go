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

func TestComputeBaselineComparisonDuplicatePairingIsOrderIndependent(t *testing.T) {
	current := []DependencyReport{
		{Language: "js-ts", Name: "duplicate", UsedExportsCount: 1, TotalExportsCount: 2},
		{Language: "js-ts", Name: "duplicate", UsedExportsCount: 11, TotalExportsCount: 12},
	}
	baseline := []DependencyReport{
		{Language: "js-ts", Name: "duplicate", TotalExportsCount: 2},
		{Language: "js-ts", Name: "duplicate", UsedExportsCount: 2, TotalExportsCount: 12},
	}

	first := ComputeBaselineComparison(Report{Dependencies: current}, Report{Dependencies: baseline})
	reversedCurrent := Report{Dependencies: []DependencyReport{current[1], current[0]}}
	reversedBaseline := Report{Dependencies: []DependencyReport{baseline[1], baseline[0]}}
	second := ComputeBaselineComparison(reversedCurrent, reversedBaseline)

	if !slices.Equal(first.Dependencies, second.Dependencies) {
		t.Fatalf("expected duplicate delta pairing to ignore input order, got %#v vs %#v", first.Dependencies, second.Dependencies)
	}
}

func TestComputeBaselineComparisonUsesVersionlessPURLIdentity(t *testing.T) {
	current := Report{Dependencies: []DependencyReport{{
		Name:              "lib",
		Language:          "js-ts",
		UsedExportsCount:  1,
		TotalExportsCount: 2,
		UsedPercent:       50,
		Identity:          &DependencyIdentity{PURL: "pkg:npm/lib@1.1.0"},
		License:           &DependencyLicense{SPDX: "GPL-3.0-ONLY", Denied: true},
	}}}
	baseline := Report{Dependencies: []DependencyReport{{
		Name:              "lib",
		Language:          "js-ts",
		UsedExportsCount:  1,
		TotalExportsCount: 2,
		UsedPercent:       50,
		Identity:          &DependencyIdentity{PURL: "pkg:npm/lib@1.0.0"},
		License:           &DependencyLicense{SPDX: "MIT"},
	}}}

	comparison := ComputeBaselineComparison(current, baseline)
	if len(comparison.Added) != 0 || len(comparison.NewDeniedLicenses) != 1 {
		t.Fatalf("expected same package across versions to compare in-place, got %#v", comparison)
	}
}

func TestComputeBaselineComparisonBridgesLegacyBaseline(t *testing.T) {
	current := Report{Dependencies: []DependencyReport{{
		Name:              "lib",
		Language:          "js-ts",
		UsedExportsCount:  2,
		TotalExportsCount: 2,
		UsedPercent:       100,
		Identity:          &DependencyIdentity{PURL: "pkg:npm/lib@1.0.0"},
	}}}
	baseline := Report{Dependencies: []DependencyReport{{
		Name:              "lib",
		Language:          "js-ts",
		UsedExportsCount:  1,
		TotalExportsCount: 2,
		UsedPercent:       50,
	}}}

	comparison := ComputeBaselineComparison(current, baseline)
	if len(comparison.Added) != 0 || len(comparison.Removed) != 0 {
		t.Fatalf("expected legacy anonymous baseline dependency to bridge to stable identity, got %#v", comparison)
	}
	if len(comparison.Dependencies) != 1 || comparison.Dependencies[0].Kind != DependencyDeltaChanged {
		t.Fatalf("expected one bridged changed dependency, got %#v", comparison.Dependencies)
	}
	if comparison.Dependencies[0].DependencyKey != DependencyVersionlessKey(current.Dependencies[0]) {
		t.Fatalf("expected bridged dependency to retain current identity key, got %#v", comparison.Dependencies[0])
	}

	pairs := PairDependencyInstances(current.Dependencies, baseline.Dependencies)
	if len(pairs) != 1 || !pairs[0].HasCurrent || !pairs[0].HasBaseline {
		t.Fatalf("expected public PairDependencyInstances bridge pair, got %#v", pairs)
	}
}

func TestComputeBaselineComparisonBridgesLegacyCurrentDeterministically(t *testing.T) {
	current := Report{Dependencies: []DependencyReport{{
		Name:              "lib",
		Language:          "js-ts",
		UsedExportsCount:  2,
		TotalExportsCount: 3,
		UsedPercent:       66.67,
	}}}
	baseline := Report{Dependencies: []DependencyReport{{
		Name:              "lib",
		Language:          "js-ts",
		UsedExportsCount:  1,
		TotalExportsCount: 3,
		UsedPercent:       33.33,
		Identity:          &DependencyIdentity{PURL: "pkg:npm/lib@1.0.0"},
	}}}

	first := ComputeBaselineComparison(current, baseline)
	second := ComputeBaselineComparison(Report{Dependencies: []DependencyReport{current.Dependencies[0]}}, Report{Dependencies: []DependencyReport{baseline.Dependencies[0]}})
	if len(first.Added) != 0 || len(first.Removed) != 0 || len(first.Dependencies) != 1 {
		t.Fatalf("expected one bridged changed dependency without churn, got %#v", first)
	}
	if !slices.Equal(first.Dependencies, second.Dependencies) {
		t.Fatalf("expected public bridge pairing to remain deterministic, got %#v vs %#v", first.Dependencies, second.Dependencies)
	}
}

func TestComputeBaselineComparisonCheckedRejectsAmbiguousLegacyIdentityBridge(t *testing.T) {
	current := Report{Dependencies: []DependencyReport{
		{
			Name:              "lib",
			Language:          "js-ts",
			UsedExportsCount:  1,
			TotalExportsCount: 2,
			UsedPercent:       50,
			Identity:          &DependencyIdentity{PURL: "pkg:npm/lib@1.0.0"},
		},
		{
			Name:              "lib",
			Language:          "js-ts",
			UsedExportsCount:  1,
			TotalExportsCount: 2,
			UsedPercent:       50,
			Identity:          &DependencyIdentity{PURL: "pkg:npm/lib@2.0.0"},
		},
	}}
	baseline := Report{Dependencies: []DependencyReport{{
		Name:              "lib",
		Language:          "js-ts",
		UsedExportsCount:  1,
		TotalExportsCount: 2,
		UsedPercent:       50,
	}}}

	_, err := ComputeBaselineComparisonChecked(current, baseline)
	if !errors.Is(err, ErrBaselineAmbiguousIdentityBridge) {
		t.Fatalf("expected ambiguous identity bridge error, got %v", err)
	}
	if !strings.Contains(err.Error(), "regenerate the baseline") {
		t.Fatalf("expected actionable regenerate-baseline guidance, got %v", err)
	}
}

func TestComputeBaselineComparisonCheckedTreatsDefaultUnknownIdentityAsAnonymous(t *testing.T) {
	defaultIdentity := func() *DependencyIdentity {
		return &DependencyIdentity{
			Ecosystem: "npm", Name: "lib", VersionStatus: "unknown", PURLStatus: "unavailable",
			Source: "language-adapter", Confidence: "low",
		}
	}
	current := Report{Dependencies: []DependencyReport{
		{Name: "lib", Language: "js-ts", Identity: defaultIdentity()},
		{Name: "lib", Language: "js-ts", Identity: defaultIdentity()},
	}}
	baseline := Report{Dependencies: []DependencyReport{
		{Name: "lib", Language: "js-ts"},
		{Name: "lib", Language: "js-ts"},
	}}

	comparison, err := ComputeBaselineComparisonChecked(current, baseline)
	if err != nil {
		t.Fatalf("expected default unknown identities to bridge as anonymous rows: %v", err)
	}
	if len(comparison.Added) != 0 || len(comparison.Removed) != 0 || comparison.UnchangedRows != 2 {
		t.Fatalf("expected default unknown identities to pair without churn, got %#v", comparison)
	}
}

func TestApplyBaselineWithKeysRejectsAmbiguousLegacyIdentityBridge(t *testing.T) {
	current := Report{
		Dependencies: []DependencyReport{
			{Name: "lib", Language: "js-ts", UsedExportsCount: 1, TotalExportsCount: 2, UsedPercent: 50, Identity: &DependencyIdentity{PURL: "pkg:npm/lib@1.0.0"}},
			{Name: "lib", Language: "js-ts", UsedExportsCount: 1, TotalExportsCount: 2, UsedPercent: 50, Identity: &DependencyIdentity{PURL: "pkg:npm/lib@2.0.0"}},
		},
	}
	baseline := Report{
		Dependencies: []DependencyReport{
			{Name: "lib", Language: "js-ts", UsedExportsCount: 1, TotalExportsCount: 2, UsedPercent: 50},
		},
	}

	_, err := ApplyBaselineWithKeys(current, baseline, "label:baseline", "commit:head")
	if !errors.Is(err, ErrBaselineAmbiguousIdentityBridge) {
		t.Fatalf("expected ambiguous identity bridge error, got %v", err)
	}
}

func TestPairDependencyInstancesFallsBackOnAmbiguity(t *testing.T) {
	current := []DependencyReport{
		{Name: "lib", Language: "js-ts", Identity: &DependencyIdentity{PURL: "pkg:npm/lib@1.0.0"}},
		{Name: "lib", Language: "js-ts", Identity: &DependencyIdentity{PURL: "pkg:npm/lib@2.0.0"}},
	}
	baseline := []DependencyReport{
		{Name: "lib", Language: "js-ts"},
	}

	pairs := PairDependencyInstances(current, baseline)
	if len(pairs) != 3 {
		t.Fatalf("expected unbridged fallback pairs on ambiguity, got %#v", pairs)
	}
	currentCount := 0
	baselineCount := 0
	for _, pair := range pairs {
		if pair.HasCurrent {
			currentCount++
		}
		if pair.HasBaseline {
			baselineCount++
		}
	}
	if currentCount != 2 || baselineCount != 1 {
		t.Fatalf("expected fallback to preserve two current rows and one baseline row, got %#v", pairs)
	}
}

func TestPlanDependencyIdentityBridgeNoBridge(t *testing.T) {
	bridge, err := planDependencyIdentityBridge("js-ts\x00lib", dependencyRawKeyGroup{anonymousCount: 1}, dependencyRawKeyGroup{anonymousCount: 1})
	if err != nil {
		t.Fatalf("expected no-bridge case to succeed, got %v", err)
	}
	if bridge != nil {
		t.Fatalf("expected no bridge when both sides remain anonymous, got %#v", bridge)
	}
}

func TestIndexDependencyRawGroupsSkipsEmptyEntries(t *testing.T) {
	groups := indexDependencyRawGroups(map[string][]DependencyReport{
		"empty": nil,
		"purl\x00pkg:npm/lib": {{
			Name:     "lib",
			Language: "js-ts",
			Identity: &DependencyIdentity{PURL: "pkg:npm/lib@1.0.0"},
		}},
	})

	if len(groups) != 1 {
		t.Fatalf("expected empty dependency buckets to be ignored, got %#v", groups)
	}
	if _, ok := groups["js-ts\x00lib"]; !ok {
		t.Fatalf("expected stable raw group to remain indexed, got %#v", groups)
	}
}

func TestComputeBaselineComparisonCheckedBridgeSafety(t *testing.T) {
	t.Run("unrelated dependencies stay added and removed", func(t *testing.T) {
		current := Report{Dependencies: []DependencyReport{{
			Name:              "lib",
			Language:          "js-ts",
			UsedExportsCount:  1,
			TotalExportsCount: 1,
			UsedPercent:       100,
			Identity:          &DependencyIdentity{PURL: "pkg:npm/lib@1.0.0"},
		}}}
		baseline := Report{Dependencies: []DependencyReport{{
			Name:              "other-lib",
			Language:          "js-ts",
			UsedExportsCount:  1,
			TotalExportsCount: 1,
			UsedPercent:       100,
		}}}

		comparison, err := ComputeBaselineComparisonChecked(current, baseline)
		if err != nil {
			t.Fatalf("checked baseline comparison: %v", err)
		}
		if len(comparison.Added) != 1 || len(comparison.Removed) != 1 {
			t.Fatalf("expected unrelated dependencies to stay unpaired, got %#v", comparison)
		}
	})

	t.Run("same raw different stable identities stay distinct", func(t *testing.T) {
		current := Report{Dependencies: []DependencyReport{{
			Name:              "lib",
			Language:          "js-ts",
			UsedExportsCount:  1,
			TotalExportsCount: 1,
			UsedPercent:       100,
			Identity:          &DependencyIdentity{PURL: "pkg:npm/lib@1.0.0"},
		}}}
		baseline := Report{Dependencies: []DependencyReport{{
			Name:              "lib",
			Language:          "js-ts",
			UsedExportsCount:  1,
			TotalExportsCount: 1,
			UsedPercent:       100,
			Identity:          &DependencyIdentity{PURL: "pkg:gem/lib@1.0.0"},
		}}}

		comparison, err := ComputeBaselineComparisonChecked(current, baseline)
		if err != nil {
			t.Fatalf("checked baseline comparison: %v", err)
		}
		if len(comparison.Added) != 1 || len(comparison.Removed) != 1 || len(comparison.Dependencies) != 2 {
			t.Fatalf("expected different stable identities with the same raw name to stay distinct, got %#v", comparison)
		}
	})
}

func TestDependencyInstancePairKeyUsesCurrentBaselineAndFallbackKeys(t *testing.T) {
	current := DependencyReport{Name: "lib", Language: "js-ts", Identity: &DependencyIdentity{PURL: "pkg:npm/lib@1.0.0"}}
	baseline := DependencyReport{Name: "lib", Language: "js-ts"}

	if got := dependencyInstancePairKey(DependencyInstancePair{Key: "ignored", Current: current, HasCurrent: true}); got != DependencyVersionlessKey(current) {
		t.Fatalf("expected current dependency key, got %q", got)
	}
	if got := dependencyInstancePairKey(DependencyInstancePair{Key: "ignored", Baseline: baseline, HasBaseline: true}); got != DependencyVersionlessKey(baseline) {
		t.Fatalf("expected baseline dependency key, got %q", got)
	}
	if got := dependencyInstancePairKey(DependencyInstancePair{Key: " raw-key "}); got != "raw-key" {
		t.Fatalf("expected fallback pair key, got %q", got)
	}
}

func TestComputeBaselineComparisonPairsDuplicateVersionChangesAgainstUnmatchedRows(t *testing.T) {
	baseline := Report{Dependencies: []DependencyReport{
		{Name: "lib", Language: "js-ts", UsedExportsCount: 1, TotalExportsCount: 2, Identity: &DependencyIdentity{PURL: "pkg:npm/lib@1.0.0"}},
		{Name: "lib", Language: "js-ts", UsedExportsCount: 2, TotalExportsCount: 3, Identity: &DependencyIdentity{PURL: "pkg:npm/lib@2.0.0"}},
	}}
	current := Report{Dependencies: []DependencyReport{
		{Name: "lib", Language: "js-ts", UsedExportsCount: 1, TotalExportsCount: 2, Identity: &DependencyIdentity{PURL: "pkg:npm/lib@1.0.0"}},
		{Name: "lib", Language: "js-ts", UsedExportsCount: 3, TotalExportsCount: 4, Identity: &DependencyIdentity{PURL: "pkg:npm/lib@1.1.0"}},
	}}

	pairs := PairDependencyInstances(current.Dependencies, baseline.Dependencies)
	if len(pairs) != 2 || !pairs[1].HasCurrent || !pairs[1].HasBaseline ||
		PURLVersion(pairs[1].Current.Identity.PURL) != "1.1.0" || PURLVersion(pairs[1].Baseline.Identity.PURL) != "2.0.0" {
		t.Fatalf("expected changed duplicate versions to pair after the exact match, got %#v", pairs)
	}

	comparison := ComputeBaselineComparison(current, baseline)
	if len(comparison.Dependencies) != 1 || comparison.Dependencies[0].Kind != DependencyDeltaChanged {
		t.Fatalf("expected one changed duplicate-version delta, got %#v", comparison.Dependencies)
	}
	if len(comparison.Added) != 0 || len(comparison.Removed) != 0 || comparison.UnchangedRows != 1 {
		t.Fatalf("expected version pairing without added/removed churn, got %#v", comparison)
	}
}

func TestDependencyVersionlessKeyPreservesQualifiersAndSubpaths(t *testing.T) {
	depA := DependencyReport{Identity: &DependencyIdentity{PURL: "pkg:npm/lib@1.0.0?classifier=a#dist/subpath"}}
	depB := DependencyReport{Identity: &DependencyIdentity{PURL: "pkg:npm/lib@2.0.0?classifier=b#dist/subpath"}}
	if DependencyVersionlessKey(depA) == DependencyVersionlessKey(depB) {
		t.Fatalf("expected qualifiers to keep packages distinct: %q", DependencyVersionlessKey(depA))
	}
}

func TestDependencyVersionlessKeyNormalizesLegacyPURLAliases(t *testing.T) {
	for _, tc := range []struct {
		name  string
		left  DependencyReport
		right DependencyReport
	}{
		{
			name:  "scoped npm",
			left:  DependencyReport{Identity: &DependencyIdentity{PURL: "pkg:npm/@scope/lib@1.0.0"}},
			right: DependencyReport{Identity: &DependencyIdentity{PURL: "pkg:npm/%40scope/lib@2.0.0"}},
		},
		{
			name:  "cargo alias",
			left:  DependencyReport{Identity: &DependencyIdentity{PURL: "pkg:cargo/demo@1.0.0", Ecosystem: "cargo"}},
			right: DependencyReport{Identity: &DependencyIdentity{PURL: "pkg:crates.io/demo@1.0.0", Ecosystem: "crates.io"}},
		},
		{
			name:  "composer alias",
			left:  DependencyReport{Identity: &DependencyIdentity{PURL: "pkg:composer/acme/lib@1.2.3", Ecosystem: "composer"}},
			right: DependencyReport{Identity: &DependencyIdentity{PURL: "pkg:packagist/acme/lib@1.2.3", Ecosystem: "packagist"}},
		},
	} {
		if DependencyVersionlessKey(tc.left) != DependencyVersionlessKey(tc.right) {
			t.Fatalf("expected %s versionless key compatibility, got %q vs %q", tc.name, DependencyVersionlessKey(tc.left), DependencyVersionlessKey(tc.right))
		}
	}
}

func dependencyLanguageForAliasPairingTest(ecosystem string) string {
	switch CanonicalPackageEcosystem(ecosystem) {
	case "cargo":
		return "rust"
	case "composer":
		return "php"
	default:
		return "js-ts"
	}
}

func TestComputeBaselineComparisonKeepsAliasEquivalentDependenciesStable(t *testing.T) {
	composerVariants := []string{
		"pkg:composer/acme/lib@1.2.3",
		"pkg:packagist/acme/lib@1.2.3",
		"pkg:composer/acme%2Flib@1.2.3",
	}
	for _, currentComposerPURL := range composerVariants {
		for _, baselineComposerPURL := range composerVariants {
			current := Report{
				Dependencies: []DependencyReport{
					{Name: "@scope/lib", Language: "js-ts", Identity: &DependencyIdentity{PURL: "pkg:npm/%40scope/lib@1.0.0", Ecosystem: "npm", Name: "@scope/lib", Version: "1.0.0"}},
					{Name: "demo", Language: "rust", Identity: &DependencyIdentity{PURL: "pkg:cargo/demo@1.0.0", Ecosystem: "cargo", Name: "demo", Version: "1.0.0"}},
					{Name: "acme/lib", Language: "php", Identity: &DependencyIdentity{PURL: currentComposerPURL, Ecosystem: "composer", Name: "acme/lib", Version: "1.2.3"}},
				},
			}
			baseline := Report{
				Dependencies: []DependencyReport{
					{Name: "@scope/lib", Language: "js-ts", Identity: &DependencyIdentity{PURL: "pkg:npm/@scope/lib@1.0.0", Ecosystem: "npm", Name: "@scope/lib", Version: "1.0.0"}},
					{Name: "demo", Language: "rust", Identity: &DependencyIdentity{PURL: "pkg:crates.io/demo@1.0.0", Ecosystem: "crates.io", Name: "demo", Version: "1.0.0"}},
					{Name: "acme/lib", Language: "php", Identity: &DependencyIdentity{PURL: baselineComposerPURL, Ecosystem: "packagist", Name: "acme/lib", Version: "1.2.3"}},
				},
			}

			comparison := ComputeBaselineComparison(current, baseline)
			if len(comparison.Added) != 0 || len(comparison.Removed) != 0 || len(comparison.Dependencies) != 0 {
				t.Fatalf("expected alias-equivalent dependencies to avoid baseline churn for current=%q baseline=%q, got %#v", currentComposerPURL, baselineComposerPURL, comparison)
			}
		}
	}
}

func TestComputeBaselineComparisonPairsAliasEquivalentVersionChangesWithoutChurn(t *testing.T) {
	for _, tc := range []struct {
		name         string
		currentPURL  string
		currentEco   string
		currentName  string
		baselinePURL string
		baselineEco  string
		baselineName string
	}{
		{
			name:         "cargo to crates.io",
			currentPURL:  "pkg:cargo/demo@1.1.0",
			currentEco:   "cargo",
			currentName:  "demo",
			baselinePURL: "pkg:crates.io/demo@1.0.0",
			baselineEco:  "crates.io",
			baselineName: "demo",
		},
		{
			name:         "composer to packagist",
			currentPURL:  "pkg:composer/acme/lib@1.1.0",
			currentEco:   "composer",
			currentName:  "acme/lib",
			baselinePURL: "pkg:packagist/acme/lib@1.0.0",
			baselineEco:  "packagist",
			baselineName: "acme/lib",
		},
		{
			name:         "composer escaped slash to canonical slash",
			currentPURL:  "pkg:composer/acme/lib@1.1.0",
			currentEco:   "composer",
			currentName:  "acme/lib",
			baselinePURL: "pkg:composer/acme%2Flib@1.0.0",
			baselineEco:  "composer",
			baselineName: "acme/lib",
		},
		{
			name:         "raw to encoded scoped npm",
			currentPURL:  "pkg:npm/%40scope/lib@1.1.0",
			currentEco:   "npm",
			currentName:  "@scope/lib",
			baselinePURL: "pkg:npm/@scope/lib@1.0.0",
			baselineEco:  "npm",
			baselineName: "@scope/lib",
		},
	} {
		current := Report{Dependencies: []DependencyReport{{
			Name:              tc.currentName,
			Language:          dependencyLanguageForAliasPairingTest(tc.currentEco),
			UsedExportsCount:  2,
			TotalExportsCount: 3,
			UsedPercent:       66.67,
			Identity:          &DependencyIdentity{PURL: tc.currentPURL, Ecosystem: tc.currentEco, Name: tc.currentName, Version: "1.1.0"},
		}}}
		baseline := Report{Dependencies: []DependencyReport{{
			Name:              tc.baselineName,
			Language:          dependencyLanguageForAliasPairingTest(tc.baselineEco),
			UsedExportsCount:  1,
			TotalExportsCount: 3,
			UsedPercent:       33.33,
			Identity:          &DependencyIdentity{PURL: tc.baselinePURL, Ecosystem: tc.baselineEco, Name: tc.baselineName, Version: "1.0.0"},
		}}}

		comparison := ComputeBaselineComparison(current, baseline)
		if len(comparison.Added) != 0 || len(comparison.Removed) != 0 {
			t.Fatalf("%s: expected zero added/removed churn, got %#v", tc.name, comparison)
		}
		if len(comparison.Dependencies) != 1 || comparison.Dependencies[0].Kind != DependencyDeltaChanged {
			t.Fatalf("%s: expected one changed dependency delta, got %#v", tc.name, comparison.Dependencies)
		}
	}
}

func TestComputeBaselineComparisonPairsPypiPEP503AliasesAndFallbackIdentities(t *testing.T) {
	current := Report{Dependencies: []DependencyReport{
		{
			Name:     "my-package",
			Language: "python",
			Identity: &DependencyIdentity{
				Ecosystem:  "pypi",
				Name:       "my-package",
				Version:    "1.1.0",
				PURL:       "pkg:pypi/my-package@1.1.0",
				PURLStatus: "resolved",
			},
		},
		{
			Name:     "fallback-pkg",
			Language: "python",
			Identity: &DependencyIdentity{
				Ecosystem:  "pypi",
				Name:       "fallback-pkg",
				Version:    "2.0.0",
				PURLStatus: "unavailable",
			},
		},
	}}
	baseline := Report{Dependencies: []DependencyReport{
		{
			Name:     "my_package",
			Language: "python",
			Identity: &DependencyIdentity{
				Ecosystem:  "pypi",
				Name:       "My_Package",
				Version:    "1.0.0",
				PURL:       "pkg:pypi/My_Package@1.0.0",
				PURLStatus: "resolved",
			},
		},
		{
			Name:     "fallback_pkg",
			Language: "python",
			Identity: &DependencyIdentity{
				Ecosystem:  "pypi",
				Name:       "Fallback_Pkg",
				Version:    "1.9.0",
				PURLStatus: "unavailable",
			},
		},
	}}

	comparison := ComputeBaselineComparison(current, baseline)
	if len(comparison.Added) != 0 || len(comparison.Removed) != 0 {
		t.Fatalf("expected PEP 503 equivalent identities to pair without churn, got %#v", comparison)
	}
}

func TestDependencyVersionlessKeyDoesNotCollapseIncompleteOrMalformedAtValues(t *testing.T) {
	keys := []string{
		DependencyVersionlessKey(DependencyReport{Identity: &DependencyIdentity{PURL: "pkg:npm/@scope"}}),
		DependencyVersionlessKey(DependencyReport{Identity: &DependencyIdentity{PURL: "pkg:npm/@other"}}),
		DependencyVersionlessKey(DependencyReport{Identity: &DependencyIdentity{PURL: "pkg:npm/@scope/lib@1.0.0"}}),
		DependencyVersionlessKey(DependencyReport{Identity: &DependencyIdentity{PURL: "name@not-a-purl"}}),
	}
	seen := map[string]struct{}{}
	for _, key := range keys {
		if _, ok := seen[key]; ok {
			t.Fatalf("expected versionless keys to stay distinct for malformed or incomplete @ values, got duplicate key %q", key)
		}
		seen[key] = struct{}{}
	}
}

func TestComputeBaselineComparisonPreservesRuntimeOnlyDuplicatePairing(t *testing.T) {
	baseline := Report{Dependencies: []DependencyReport{
		{
			Name:              "lib",
			Language:          "js-ts",
			UsedExportsCount:  1,
			TotalExportsCount: 2,
			Identity:          &DependencyIdentity{PURL: "pkg:npm/lib@1.0.0"},
		},
		{
			Name:              "lib",
			Language:          "js-ts",
			UsedExportsCount:  1,
			TotalExportsCount: 2,
			Identity:          &DependencyIdentity{PURL: "pkg:npm/lib@1.1.0"},
		},
	}}
	current := Report{Dependencies: []DependencyReport{
		{
			Name:              "lib",
			Language:          "js-ts",
			UsedExportsCount:  1,
			TotalExportsCount: 2,
			Identity:          &DependencyIdentity{PURL: "pkg:npm/lib@1.1.0"},
		},
		{
			Name:              "lib",
			Language:          "js-ts",
			UsedExportsCount:  1,
			TotalExportsCount: 2,
			Identity:          &DependencyIdentity{PURL: "pkg:npm/lib@1.0.0"},
			RuntimeUsage: &RuntimeUsage{
				LoadCount:   1,
				Correlation: RuntimeCorrelationRuntimeOnly,
				RuntimeOnly: true,
				Modules:     []RuntimeModuleUsage{{Module: "runtime/lib", Count: 1}},
			},
		},
	}}

	comparison := ComputeBaselineComparison(current, baseline)
	if len(comparison.Dependencies) != 1 {
		t.Fatalf("expected only the runtime-changed duplicate instance to emit a delta, got %#v", comparison.Dependencies)
	}
	if comparison.Dependencies[0].RuntimeDelta == nil || !comparison.Dependencies[0].RuntimeDelta.CurrentPresent || comparison.Dependencies[0].RuntimeDelta.BaselinePresent {
		t.Fatalf("expected runtime-only duplicate pairing to preserve the runtime regression, got %#v", comparison.Dependencies[0])
	}
	if comparison.UnchangedRows != 1 {
		t.Fatalf("expected one exact unchanged duplicate match, got %#v", comparison)
	}
}

func TestPairDependencyInstancesPreservesStableIdentityResidualsAndAnonymousMatches(t *testing.T) {
	current := []DependencyReport{
		{
			Name:     "anon",
			Language: "js-ts",
		},
		{
			Name:     "stable",
			Language: "js-ts",
			Identity: &DependencyIdentity{Ecosystem: "npm", Name: "stable", Version: "1.1.0", PURL: "pkg:npm/stable@1.1.0"},
		},
	}
	baseline := []DependencyReport{
		{
			Name:     "anon",
			Language: "js-ts",
		},
		{
			Name:     "stable",
			Language: "js-ts",
			Identity: &DependencyIdentity{Ecosystem: "npm", Name: "stable", Version: "1.0.0", PURL: "pkg:npm/stable@1.0.0"},
		},
		{
			Name:     "stable",
			Language: "js-ts",
			Identity: &DependencyIdentity{Ecosystem: "npm", Name: "stable", Version: "2.0.0", PURL: "pkg:npm/stable@2.0.0"},
		},
	}

	pairs := PairDependencyInstances(current, baseline)
	if len(pairs) != 3 {
		t.Fatalf("expected stable residuals plus anonymous match, got %#v", pairs)
	}
	if !pairs[0].HasCurrent || !pairs[0].HasBaseline || pairs[0].Current.Name != "anon" || pairs[0].Baseline.Name != "anon" {
		t.Fatalf("expected anonymous dependency to pair by position, got %#v", pairs[0])
	}
	if !pairs[1].HasCurrent || !pairs[1].HasBaseline || pairs[1].Current.Identity.Version != "1.1.0" || pairs[1].Baseline.Identity.Version != "1.0.0" {
		t.Fatalf("expected nearest stable identities to pair, got %#v", pairs[1])
	}
	if pairs[2].HasCurrent || !pairs[2].HasBaseline || pairs[2].Baseline.Identity.Version != "2.0.0" {
		t.Fatalf("expected unmatched baseline stable identity to remain residual, got %#v", pairs[2])
	}
}

func TestPairDependencyInstancesKeepsExactMatchesBeforeMissingVersionStableFallback(t *testing.T) {
	stable := func(version, purl string) DependencyReport {
		return DependencyReport{
			Name:     "stable",
			Language: "js-ts",
			Identity: &DependencyIdentity{Ecosystem: "npm", Name: "stable", Version: version, PURL: purl},
		}
	}
	current := []DependencyReport{
		{Name: "anon", Language: "js-ts"},
		stable("1.0.0", "pkg:npm/stable@1.0.0"),
		stable("", "pkg:npm/stable"),
	}
	baseline := []DependencyReport{
		{Name: "anon", Language: "js-ts"},
		stable("1.0.0", "pkg:npm/stable@1.0.0"),
		stable("2.0.0", "pkg:npm/stable@2.0.0"),
	}

	pairs := PairDependencyInstances(current, baseline)
	if len(pairs) != 3 {
		t.Fatalf("expected exact, missing-version stable, and anonymous pairs, got %#v", pairs)
	}
	if !pairs[0].HasCurrent || !pairs[0].HasBaseline || pairs[0].Current.Name != "anon" || pairs[0].Baseline.Name != "anon" {
		t.Fatalf("expected anonymous dependency to remain paired by position, got %#v", pairs[0])
	}
	if !pairs[1].HasCurrent || !pairs[1].HasBaseline || pairs[1].Current.Identity.Version != "1.0.0" || pairs[1].Baseline.Identity.Version != "1.0.0" {
		t.Fatalf("expected exact stable match to win before missing-version fallback, got %#v", pairs[1])
	}
	if !pairs[2].HasCurrent || !pairs[2].HasBaseline || pairs[2].Current.Identity.Version != "" || pairs[2].Baseline.Identity.Version != "2.0.0" {
		t.Fatalf("expected missing-version residual to pair only after exact matches, got %#v", pairs[2])
	}
}

func TestBaselineDependencyDeltasForDependenciesFallsBackDeterministicallyWithoutOrdinals(t *testing.T) {
	dependencies := []DependencyReport{
		{
			Name:     "dup",
			Language: "js-ts",
			Identity: &DependencyIdentity{Ecosystem: "npm", Name: "dup", Version: "2.0.0", PURL: "pkg:npm/dup@2.0.0"},
		},
		{
			Name:     "dup",
			Language: "js-ts",
			Identity: &DependencyIdentity{Ecosystem: "npm", Name: "dup", Version: "1.0.0", PURL: "pkg:npm/dup@1.0.0"},
		},
	}
	comparison := &BaselineComparison{
		Dependencies: []DependencyDelta{
			{
				DependencyKey:         DependencyVersionlessKey(dependencies[0]),
				CurrentOrdinal:        -1,
				Kind:                  DependencyDeltaRemoved,
				Name:                  "dup",
				Language:              "js-ts",
				UsedExportsCountDelta: 9,
				RuntimeDelta:          &RuntimeDelta{BaselinePresent: true, RemovedRuntimeLoads: true},
			},
			{
				DependencyKey:         DependencyVersionlessKey(dependencies[0]),
				CurrentOrdinal:        -1,
				Kind:                  DependencyDeltaAdded,
				Name:                  "dup",
				Language:              "js-ts",
				UsedExportsCountDelta: 1,
				RuntimeDelta:          &RuntimeDelta{CurrentPresent: true, NewRuntimeLoads: true},
			},
		},
	}

	aligned := baselineDependencyDeltasForDependencies(dependencies, comparison)
	if len(aligned) != 2 || aligned[0] == nil || aligned[1] == nil {
		t.Fatalf("expected fallback alignment for duplicate dependencies, got %#v", aligned)
	}
	if aligned[0].Kind != DependencyDeltaRemoved || !aligned[0].RuntimeDelta.RemovedRuntimeLoads {
		t.Fatalf("expected later version to receive lexicographically first fallback delta, got %#v", aligned[0])
	}
	if aligned[1].Kind != DependencyDeltaAdded || !aligned[1].RuntimeDelta.NewRuntimeLoads {
		t.Fatalf("expected earlier version to receive second fallback delta, got %#v", aligned[1])
	}
}

func TestDependencyIdentityVersionForPairingUsesIdentityVersionAndNormalizedPURL(t *testing.T) {
	if got := dependencyIdentityVersionForPairing(&DependencyIdentity{Version: " 2.4.0 ", PURL: "pkg:npm/lib@9.9.9"}); got != "2.4.0" {
		t.Fatalf("expected explicit identity version to win, got %q", got)
	}
	if got := dependencyIdentityVersionForPairing(&DependencyIdentity{PURL: "pkg:npm/lib@1.2.3?classifier=prod#dist"}); got != "1.2.3" {
		t.Fatalf("expected PURL version to ignore qualifiers and subpath, got %q", got)
	}
	if got := dependencyIdentityVersionForPairing(&DependencyIdentity{PURL: "pkg:npm/lib"}); got != "" {
		t.Fatalf("expected empty version when PURL has no version, got %q", got)
	}
}

func TestDependencyStableIdentityCompatibleAllowsMissingVersionButRejectsDifferentPrefixes(t *testing.T) {
	current := DependencyReport{
		Name:     "lib",
		Language: "js-ts",
		Identity: &DependencyIdentity{Ecosystem: "npm", Name: "lib", Version: "1.0.0", PURL: "pkg:npm/lib@1.0.0"},
	}
	missingVersion := DependencyReport{
		Name:     "lib",
		Language: "js-ts",
		Identity: &DependencyIdentity{Ecosystem: "npm", Name: "lib", PURL: "pkg:npm/lib"},
	}
	differentQualifier := DependencyReport{
		Name:     "lib",
		Language: "js-ts",
		Identity: &DependencyIdentity{Ecosystem: "npm", Name: "lib", Version: "1.0.1", PURL: "pkg:npm/lib@1.0.1?classifier=browser"},
	}

	if !dependencyStableIdentityCompatible(current, missingVersion) {
		t.Fatal("expected missing version metadata to remain pairable for the same stable identity")
	}
	if dependencyStableIdentityCompatible(current, differentQualifier) {
		t.Fatalf("expected qualifier-specific version prefixes to stay distinct: %q vs %q", dependencyIdentityVersionPrefix(current), dependencyIdentityVersionPrefix(differentQualifier))
	}
}

func TestDependencyStableIdentityMutualNearestRejectsNonNearestPairs(t *testing.T) {
	currentNear := DependencyReport{
		Name:     "lib",
		Language: "js-ts",
		Identity: &DependencyIdentity{Ecosystem: "npm", Name: "lib", Version: "1.0.0", PURL: "pkg:npm/lib@1.0.0"},
	}
	currentFar := DependencyReport{
		Name:     "lib",
		Language: "js-ts",
		Identity: &DependencyIdentity{Ecosystem: "npm", Name: "lib", Version: "10.0.0", PURL: "pkg:npm/lib@10.0.0"},
	}
	baselineNear := DependencyReport{
		Name:     "lib",
		Language: "js-ts",
		Identity: &DependencyIdentity{Ecosystem: "npm", Name: "lib", Version: "1.1.0", PURL: "pkg:npm/lib@1.1.0"},
	}
	baselineFar := DependencyReport{
		Name:     "lib",
		Language: "js-ts",
		Identity: &DependencyIdentity{Ecosystem: "npm", Name: "lib", Version: "11.0.0", PURL: "pkg:npm/lib@11.0.0"},
	}

	if !dependencyStableIdentityMutualNearest(currentFar, baselineFar, []DependencyReport{currentNear, currentFar}, []DependencyReport{baselineNear, baselineFar}) {
		t.Fatal("expected mutually nearest stable identities to remain pairable")
	}
	if dependencyStableIdentityMutualNearest(currentNear, baselineFar, []DependencyReport{currentNear, currentFar}, []DependencyReport{baselineNear, baselineFar}) {
		t.Fatal("expected non-nearest stable identities to be rejected once closer candidates exist")
	}
}

func TestDependencyVersionDistanceHandlesNumericTextAndMissingVersions(t *testing.T) {
	numericLeft := DependencyReport{Identity: &DependencyIdentity{Version: "1.2.3", PURL: "pkg:npm/lib@1.2.3"}}
	numericRight := DependencyReport{Identity: &DependencyIdentity{Version: "1.4.0", PURL: "pkg:npm/lib@1.4.0"}}
	if distance, ok := dependencyVersionDistance(numericLeft, numericRight); !ok || distance != 203 {
		t.Fatalf("expected weighted numeric distance, got distance=%d ok=%t", distance, ok)
	}

	textLeft := DependencyReport{Identity: &DependencyIdentity{Version: "release-a", PURL: "pkg:npm/lib@release-a"}}
	textRight := DependencyReport{Identity: &DependencyIdentity{Version: "release-b", PURL: "pkg:npm/lib@release-b"}}
	if distance, ok := dependencyVersionDistance(textLeft, textRight); !ok || distance != 1 {
		t.Fatalf("expected non-numeric versions to fall back to distance 1, got distance=%d ok=%t", distance, ok)
	}
	if distance, ok := dependencyVersionDistance(textLeft, textLeft); !ok || distance != 0 {
		t.Fatalf("expected identical text versions to compare as zero distance, got distance=%d ok=%t", distance, ok)
	}

	missing := DependencyReport{Identity: &DependencyIdentity{PURL: "pkg:npm/lib"}}
	if _, ok := dependencyVersionDistance(missing, textLeft); ok {
		t.Fatal("expected missing version metadata to make version distance unavailable")
	}
}

func TestDependencyIdentityVersionPrefixIncludesVersionlessIdentityFields(t *testing.T) {
	dep := DependencyReport{
		Name:     "lib",
		Language: "js-ts",
		Identity: &DependencyIdentity{
			Ecosystem:  "npm",
			Namespace:  "@scope",
			Name:       "lib",
			PURL:       "pkg:npm/%40scope/lib@1.2.3?classifier=prod#dist",
			PURLStatus: "declared",
		},
	}

	prefix := dependencyIdentityVersionPrefix(dep)
	if !strings.Contains(prefix, "pkg:npm/%40scope/lib?classifier=prod#dist") {
		t.Fatalf("expected versionless PURL to be part of prefix, got %q", prefix)
	}
	if !strings.Contains(prefix, "declared") {
		t.Fatalf("expected PURL status in prefix, got %q", prefix)
	}
	if strings.Contains(prefix, "@scope") {
		t.Fatalf("expected canonical PURL-owned prefix not to add redundant raw namespace fields, got %q", prefix)
	}
	if got := dependencyIdentityVersionPrefix(DependencyReport{}); got != "" {
		t.Fatalf("expected dependencies without identity metadata to have an empty prefix, got %q", got)
	}
}

func TestDependencyIdentityVersionPrefixIgnoresRedundantRawIdentityWhenPURLExists(t *testing.T) {
	left := DependencyReport{
		Name:     "demo",
		Language: "rust",
		Identity: &DependencyIdentity{
			Ecosystem:  "cargo",
			Namespace:  "wrong-left",
			Name:       "wrong-left",
			PURL:       "pkg:cargo/demo@1.0.0",
			PURLStatus: "resolved",
		},
	}
	right := DependencyReport{
		Name:     "demo",
		Language: "rust",
		Identity: &DependencyIdentity{
			Ecosystem:  "crates.io",
			Namespace:  "wrong-right",
			Name:       "wrong-right",
			PURL:       "pkg:crates.io/demo@2.0.0",
			PURLStatus: "resolved",
		},
	}

	if dependencyIdentityVersionPrefix(left) != dependencyIdentityVersionPrefix(right) {
		t.Fatalf("expected alias-equivalent canonical PURLs to own the prefix despite disagreeing raw identity metadata: %q vs %q", dependencyIdentityVersionPrefix(left), dependencyIdentityVersionPrefix(right))
	}
}

func TestDependencyNearestVersionDistanceAndNumericVersionParsingFailures(t *testing.T) {
	source := DependencyReport{
		Name:     "lib",
		Language: "js-ts",
		Identity: &DependencyIdentity{Ecosystem: "npm", Name: "lib", Version: "2.0.0", PURL: "pkg:npm/lib@2.0.0"},
	}
	candidates := []DependencyReport{
		{Identity: &DependencyIdentity{Ecosystem: "npm", Name: "other", Version: "2.0.1", PURL: "pkg:npm/other@2.0.1"}},
		{Identity: &DependencyIdentity{Ecosystem: "npm", Name: "lib", Version: "2.1.0", PURL: "pkg:npm/lib@2.1.0"}},
		{Identity: &DependencyIdentity{Ecosystem: "npm", Name: "lib", Version: "9.0.0", PURL: "pkg:npm/lib@9.0.0"}},
	}

	distance, ok := dependencyNearestVersionDistance(source, candidates)
	if !ok || distance != 100 {
		t.Fatalf("expected nearest compatible version distance to prefer 2.1.0, got distance=%d ok=%t", distance, ok)
	}
	if _, ok := dependencyNearestVersionDistance(source, []DependencyReport{{Identity: &DependencyIdentity{Ecosystem: "npm", Name: "other", Version: "1.0.0", PURL: "pkg:npm/other@1.0.0"}}}); ok {
		t.Fatal("expected no nearest version distance when no candidates share the stable identity prefix")
	}

	if parts, ok := numericDependencyVersionParts("release-a"); ok || len(parts) != 0 {
		t.Fatalf("expected non-numeric version string to fail numeric parsing, got parts=%v ok=%t", parts, ok)
	}
	if parts, ok := numericDependencyVersionParts("v2beta3"); !ok || !slices.Equal(parts, []int{2, 3}) {
		t.Fatalf("expected embedded digits to be parsed in order, got parts=%v ok=%t", parts, ok)
	}
}

func TestDependencyStableIdentityCompatibleRejectsNilIdentitiesAndAllowsExactResidualMatch(t *testing.T) {
	left := DependencyReport{
		Name:     "lib",
		Language: "js-ts",
		Identity: &DependencyIdentity{Ecosystem: "npm", Name: "lib", Version: "1.0.0", PURL: "pkg:npm/lib@1.0.0"},
	}
	right := DependencyReport{
		Name:     "lib",
		Language: "js-ts",
		Identity: &DependencyIdentity{Ecosystem: "npm", Name: "lib", Version: "9.9.9", PURL: "pkg:npm/lib@9.9.9"},
	}

	if dependencyStableIdentityCompatible(DependencyReport{}, left) {
		t.Fatal("expected missing identity metadata to be incompatible")
	}
	if !dependencyStableIdentityCompatible(left, right) {
		t.Fatal("expected identical residual identities to remain compatible across versions")
	}
}

func TestInstanceAggregatorsPreservePerPairDeniedAndReachableFindings(t *testing.T) {
	key := "purl\x00pkg:npm/lib"
	otherKey := "purl\x00pkg:golang/example.com/alpha"
	currentByKey := map[string][]DependencyReport{
		key: {
			{
				Name:     "lib",
				Language: "js-ts",
				Identity: &DependencyIdentity{Ecosystem: "npm", Name: "lib", Version: "1.0.0", PURL: "pkg:npm/lib@1.0.0"},
				License:  &DependencyLicense{SPDX: "GPL-3.0-only", Denied: true},
			},
			{
				Name:     "lib",
				Language: "js-ts",
				Identity: &DependencyIdentity{Ecosystem: "npm", Name: "lib", Version: "2.0.0", PURL: "pkg:npm/lib@2.0.0"},
				Vulnerabilities: []VulnerabilityFinding{{
					AdvisoryID:    "GHSA-new",
					Package:       "lib",
					Severity:      VulnerabilityPriorityHigh,
					Priority:      VulnerabilityPriorityCritical,
					PriorityScore: 98,
					Reachable:     true,
					Evidence:      []string{"runtime import"},
				}},
			},
		},
		otherKey: {
			{
				Name:     "alpha",
				Language: "go",
				Identity: &DependencyIdentity{Ecosystem: "golang", Name: "example.com/alpha", Version: "1.0.0", PURL: "pkg:golang/example.com/alpha@1.0.0"},
				License:  &DependencyLicense{Denied: true},
				Vulnerabilities: []VulnerabilityFinding{{
					AdvisoryID:    "GO-2026-0001",
					Package:       "example.com/alpha",
					Severity:      VulnerabilityPriorityMedium,
					Priority:      VulnerabilityPriorityHigh,
					PriorityScore: 80,
					Reachable:     true,
				}},
			},
		},
	}
	baselineByKey := map[string][]DependencyReport{
		key: {
			{
				Name:     "lib",
				Language: "js-ts",
				Identity: &DependencyIdentity{Ecosystem: "npm", Name: "lib", Version: "0.9.0", PURL: "pkg:npm/lib@0.9.0"},
				License:  &DependencyLicense{SPDX: "MIT"},
			},
			{
				Name:     "lib",
				Language: "js-ts",
				Identity: &DependencyIdentity{Ecosystem: "npm", Name: "lib", Version: "1.9.0", PURL: "pkg:npm/lib@1.9.0"},
			},
		},
		otherKey: {
			{
				Name:     "alpha",
				Language: "go",
				Identity: &DependencyIdentity{Ecosystem: "golang", Name: "example.com/alpha", Version: "0.9.0", PURL: "pkg:golang/example.com/alpha@0.9.0"},
				License:  &DependencyLicense{SPDX: "GPL-3.0-only", Denied: true},
				Vulnerabilities: []VulnerabilityFinding{{
					AdvisoryID:    "GO-2026-0001",
					Package:       "example.com/alpha",
					Severity:      VulnerabilityPriorityMedium,
					Priority:      VulnerabilityPriorityHigh,
					PriorityScore: 80,
					Reachable:     true,
				}},
			},
		},
	}

	denied := newlyDeniedLicensesByInstances(currentByKey, baselineByKey)
	if len(denied) != 1 || denied[0].SPDX != "GPL-3.0-only" || denied[0].Name != "lib" {
		t.Fatalf("expected one newly denied duplicate instance, got %#v", denied)
	}

	reachable := newlyReachableVulnerabilitiesByInstances(currentByKey, baselineByKey)
	if len(reachable) != 1 || reachable[0].AdvisoryID != "GHSA-new" || reachable[0].CurrentOrdinal < 0 || reachable[0].DependencyKey != key {
		t.Fatalf("expected one newly reachable duplicate instance with pairing metadata, got %#v", reachable)
	}
}

func TestComputeBaselineComparisonTracksDuplicateVersionlessPoliciesAndVulnerabilities(t *testing.T) {
	baseFirst := DependencyReport{
		Name:              "lib",
		Language:          "js-ts",
		UsedExportsCount:  1,
		TotalExportsCount: 2,
		UsedPercent:       50,
		Identity:          &DependencyIdentity{PURL: "pkg:npm/lib@1.0.0"},
		License:           &DependencyLicense{SPDX: "MIT"},
	}
	baseSecond := DependencyReport{
		Name:              "lib",
		Language:          "js-ts",
		UsedExportsCount:  1,
		TotalExportsCount: 2,
		UsedPercent:       50,
		Identity:          &DependencyIdentity{PURL: "pkg:npm/lib@1.1.0"},
		License:           &DependencyLicense{SPDX: "MIT"},
	}
	headDenied := baseFirst
	headDenied.License = &DependencyLicense{SPDX: "GPL-3.0-only", Denied: true}
	headReachable := baseSecond
	headReachable.Vulnerabilities = []VulnerabilityFinding{{
		AdvisoryID:    "GHSA-new",
		Package:       "lib",
		Severity:      VulnerabilityPriorityHigh,
		Priority:      VulnerabilityPriorityCritical,
		PriorityScore: 95,
		Reachable:     true,
	}}

	currentReport := Report{Dependencies: []DependencyReport{headReachable, headDenied}}
	baselineReport := Report{Dependencies: []DependencyReport{baseSecond, baseFirst}}
	comparison := ComputeBaselineComparison(currentReport, baselineReport)
	if len(comparison.NewDeniedLicenses) != 1 || comparison.NewDeniedLicenses[0].SPDX != "GPL-3.0-only" {
		t.Fatalf("expected one denied duplicate instance, got %#v", comparison.NewDeniedLicenses)
	}
	if len(comparison.NewReachableVulnerabilities) != 1 || comparison.NewReachableVulnerabilities[0].AdvisoryID != "GHSA-new" {
		t.Fatalf("expected one newly reachable duplicate instance, got %#v", comparison.NewReachableVulnerabilities)
	}

	reversedCurrent := Report{Dependencies: []DependencyReport{headDenied, headReachable}}
	reversedBaseline := Report{Dependencies: []DependencyReport{baseFirst, baseSecond}}
	reversed := ComputeBaselineComparison(reversedCurrent, reversedBaseline)
	if !slices.Equal(comparison.NewDeniedLicenses, reversed.NewDeniedLicenses) {
		t.Fatalf("expected denied duplicate pairing to be order-independent, got %#v vs %#v", comparison.NewDeniedLicenses, reversed.NewDeniedLicenses)
	}
	if !slices.EqualFunc(comparison.NewReachableVulnerabilities, reversed.NewReachableVulnerabilities, func(a, b VulnerabilityDelta) bool {
		return a.Language == b.Language &&
			a.Name == b.Name &&
			a.AdvisoryID == b.AdvisoryID &&
			a.Package == b.Package &&
			a.Severity == b.Severity &&
			a.FixedVersion == b.FixedVersion &&
			a.Source == b.Source &&
			a.Priority == b.Priority &&
			a.PriorityScore == b.PriorityScore &&
			slices.Equal(a.Evidence, b.Evidence)
	}) {
		t.Fatalf("expected reachable duplicate pairing to be order-independent, got %#v vs %#v", comparison.NewReachableVulnerabilities, reversed.NewReachableVulnerabilities)
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

func TestDependencyDeltaRetainsReachableCountOnlyChanges(t *testing.T) {
	base := DependencyReport{
		Name:     "lib",
		Language: "js-ts",
		Vulnerabilities: []VulnerabilityFinding{{
			AdvisoryID: "GHSA-1",
			Package:    "lib",
			Reachable:  true,
		}},
	}
	currSuppressed := DependencyReport{
		Name:     "lib",
		Language: "js-ts",
		Vulnerabilities: []VulnerabilityFinding{{
			AdvisoryID: "GHSA-1",
			Package:    "lib",
			Reachable:  true,
			Decision:   &VulnerabilityExceptionDecision{Status: "not-affected"},
		}},
	}
	currIntroduced := DependencyReport{
		Name:     "lib",
		Language: "js-ts",
		Vulnerabilities: []VulnerabilityFinding{{
			AdvisoryID: "GHSA-2",
			Package:    "lib",
			Reachable:  true,
		}},
	}

	suppressedDelta, ok := dependencyDelta(currSuppressed, true, base, true)
	if !ok {
		t.Fatal("expected reachable-count-only suppression to retain a dependency delta")
	}
	if suppressedDelta.ReachableVulnerabilityCountDelta != -1 || suppressedDelta.ReachableVulnerabilitiesIntroduced {
		t.Fatalf("unexpected suppression delta: %#v", suppressedDelta)
	}

	introducedDelta, ok := dependencyDelta(currIntroduced, true, base, true)
	if !ok {
		t.Fatal("expected introduced reachable finding with equal counts to retain a dependency delta")
	}
	if introducedDelta.ReachableVulnerabilityCountDelta != 0 || !introducedDelta.ReachableVulnerabilitiesIntroduced {
		t.Fatalf("unexpected introduced delta: %#v", introducedDelta)
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

func TestComputeBaselineComparisonSortsNewPolicyDeltas(t *testing.T) {
	current := Report{Dependencies: []DependencyReport{
		{Name: "z-vuln", Language: "go", UsedExportsCount: 1, TotalExportsCount: 1, UsedPercent: 100, Vulnerabilities: []VulnerabilityFinding{{AdvisoryID: "GHSA-z", Priority: VulnerabilityPriorityHigh, PriorityScore: 50, Reachable: true}}},
		{Name: "a-vuln", Language: "go", UsedExportsCount: 1, TotalExportsCount: 1, UsedPercent: 100, Vulnerabilities: []VulnerabilityFinding{{AdvisoryID: "GHSA-a", Priority: VulnerabilityPriorityHigh, PriorityScore: 50, Reachable: true}}},
		{Name: "license", Language: "go", UsedExportsCount: 1, TotalExportsCount: 1, UsedPercent: 100, License: &DependencyLicense{SPDX: "AGPL-3.0-only", Denied: true}},
	}}
	baseline := Report{Dependencies: []DependencyReport{
		{Name: "z-vuln", Language: "go", UsedExportsCount: 1, TotalExportsCount: 1, UsedPercent: 100},
		{Name: "a-vuln", Language: "go", UsedExportsCount: 1, TotalExportsCount: 1, UsedPercent: 100},
		{Name: "license", Language: "go", UsedExportsCount: 1, TotalExportsCount: 1, UsedPercent: 100},
	}}

	comparison := ComputeBaselineComparison(current, baseline)
	if len(comparison.NewReachableVulnerabilities) != 2 || comparison.NewReachableVulnerabilities[0].Name != "a-vuln" || comparison.NewReachableVulnerabilities[1].Name != "z-vuln" {
		t.Fatalf("unexpected reachable vulnerability ordering: %#v", comparison.NewReachableVulnerabilities)
	}
	if len(comparison.NewDeniedLicenses) != 1 || comparison.NewDeniedLicenses[0].SPDX != "AGPL-3.0-only" {
		t.Fatalf("unexpected denied license deltas: %#v", comparison.NewDeniedLicenses)
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

func TestBaselineRuntimeDeltasForDependenciesAlignsByInstance(t *testing.T) {
	loadDeltaV1 := 1
	loadDeltaV2 := 5
	dependencies := []DependencyReport{
		{
			Name:     "duplicate",
			Language: "js-ts",
			Identity: &DependencyIdentity{Version: "2.0.0", PURL: "pkg:npm/duplicate@2.0.0"},
		},
		{
			Name:     "duplicate",
			Language: "js-ts",
			Identity: &DependencyIdentity{Version: "1.0.0", PURL: "pkg:npm/duplicate@1.0.0"},
		},
	}
	comparison := &BaselineComparison{
		Dependencies: []DependencyDelta{
			{
				Kind:           DependencyDeltaChanged,
				Language:       "js-ts",
				Name:           "duplicate",
				DependencyKey:  DependencyVersionlessKey(dependencies[0]),
				CurrentOrdinal: 0,
				RuntimeDelta:   &RuntimeDelta{Comparable: true, CurrentPresent: true, BaselinePresent: true, LoadCountDelta: &loadDeltaV1},
			},
			{
				Kind:           DependencyDeltaChanged,
				Language:       "js-ts",
				Name:           "duplicate",
				DependencyKey:  DependencyVersionlessKey(dependencies[0]),
				CurrentOrdinal: 1,
				RuntimeDelta:   &RuntimeDelta{Comparable: true, CurrentPresent: true, BaselinePresent: true, LoadCountDelta: &loadDeltaV2},
			},
		},
	}

	aligned := BaselineRuntimeDeltasForDependencies(dependencies, comparison)
	if len(aligned) != 2 || aligned[0] == nil || aligned[1] == nil {
		t.Fatalf("expected aligned runtime deltas for duplicate dependencies, got %#v", aligned)
	}
	if aligned[0].LoadCountDelta == nil || *aligned[0].LoadCountDelta != loadDeltaV2 {
		t.Fatalf("expected reversed v2 duplicate to keep its runtime delta, got %#v", aligned[0])
	}
	if aligned[1].LoadCountDelta == nil || *aligned[1].LoadCountDelta != loadDeltaV1 {
		t.Fatalf("expected reversed v1 duplicate to keep its runtime delta, got %#v", aligned[1])
	}
}
