package report

import (
	"slices"
	"testing"
)

func TestAdvisoryVersionMatchEvaluatesOSVMetadata(t *testing.T) {
	lowerBounded := semanticVersionRange(VulnerabilityVersionEvent{Introduced: "2.0.0"}, VulnerabilityVersionEvent{Fixed: "2.0.1"})
	simpleEcosystem := ecosystemVersionRange(VulnerabilityVersionEvent{Introduced: "0"}, VulnerabilityVersionEvent{Fixed: "2.32.0"})
	reintroduced := semanticVersionRange(VulnerabilityVersionEvent{Introduced: "1.0.0"}, VulnerabilityVersionEvent{Fixed: "1.2.0"}, VulnerabilityVersionEvent{Introduced: "2.0.0"}, VulnerabilityVersionEvent{Fixed: "2.1.0"})
	firstWindow := semanticVersionRange(VulnerabilityVersionEvent{Introduced: "1.0.0"}, VulnerabilityVersionEvent{Fixed: "1.1.0"})
	secondWindow := semanticVersionRange(VulnerabilityVersionEvent{Introduced: "3.0.0"}, VulnerabilityVersionEvent{Fixed: "3.1.0"})

	tests := []struct {
		name      string
		advisory  VulnerabilityAdvisory
		installed string
		want      vulnerabilityVersionMatch
	}{
		{name: "before introduced lower bound", advisory: osvVersionAdvisory(lowerBounded), installed: "1.5.0", want: versionUnaffected},
		{name: "at introduced lower bound", advisory: osvVersionAdvisory(lowerBounded), installed: "2.0.0", want: versionAffected},
		{name: "at exclusive fixed bound", advisory: osvVersionAdvisory(lowerBounded), installed: "2.0.1", want: versionUnaffected},
		{name: "inside first reintroduced window", advisory: osvVersionAdvisory(reintroduced), installed: "1.1.0", want: versionAffected},
		{name: "between reintroduced windows", advisory: osvVersionAdvisory(reintroduced), installed: "1.5.0", want: versionUnaffected},
		{name: "inside second reintroduced window", advisory: osvVersionAdvisory(reintroduced), installed: "2.0.0", want: versionAffected},
		{name: "after second reintroduced window", advisory: osvVersionAdvisory(reintroduced), installed: "2.1.0", want: versionUnaffected},
		{name: "simple ecosystem range before fixed", advisory: osvVersionAdvisory(simpleEcosystem), installed: "2.31.0", want: versionAffected},
		{name: "simple ecosystem range includes prerelease before fixed", advisory: osvVersionAdvisory(simpleEcosystem), installed: "2.32.0-rc.1", want: versionAffected},
		{name: "simple ecosystem range at fixed", advisory: osvVersionAdvisory(simpleEcosystem), installed: "2.32.0", want: versionUnaffected},
		{
			name:      "simple ecosystem range before introduced",
			advisory:  osvVersionAdvisory(ecosystemVersionRange(VulnerabilityVersionEvent{Introduced: "2.0.0"}, VulnerabilityVersionEvent{Fixed: "3.0.0"})),
			installed: "1.9.0",
			want:      versionUnaffected,
		},
		{
			name:      "simple ecosystem range supports reordered events",
			advisory:  osvVersionAdvisory(ecosystemVersionRange(VulnerabilityVersionEvent{Fixed: "2.32.0"}, VulnerabilityVersionEvent{Introduced: "0"})),
			installed: "2.31.0",
			want:      versionAffected,
		},
		{
			name: "supported ecosystem match overrides unsupported range",
			advisory: VulnerabilityAdvisory{VersionRanges: []VulnerabilityVersionRange{
				{Type: "GIT", Events: []VulnerabilityVersionEvent{{Introduced: "abc"}}},
				simpleEcosystem,
			}},
			installed: "2.31.0",
			want:      versionAffected,
		},
		{
			name: "unaffected ecosystem range does not hide unsupported range",
			advisory: VulnerabilityAdvisory{VersionRanges: []VulnerabilityVersionRange{
				{Type: "GIT", Events: []VulnerabilityVersionEvent{{Introduced: "abc"}}},
				simpleEcosystem,
			}},
			installed: "2.32.0",
			want:      versionUnevaluable,
		},
		{
			name:      "multiple ranges form a union",
			advisory:  osvVersionAdvisory(firstWindow, secondWindow),
			installed: "3.0.5",
			want:      versionAffected,
		},
		{
			name:      "last affected is inclusive",
			advisory:  osvVersionAdvisory(semanticVersionRange(VulnerabilityVersionEvent{Introduced: "1.0.0"}, VulnerabilityVersionEvent{LastAffected: "1.2.0"})),
			installed: "1.2.0",
			want:      versionAffected,
		},
		{
			name:      "version after last affected is safe",
			advisory:  osvVersionAdvisory(semanticVersionRange(VulnerabilityVersionEvent{Introduced: "1.0.0"}, VulnerabilityVersionEvent{LastAffected: "1.2.0"})),
			installed: "1.2.1",
			want:      versionUnaffected,
		},
		{
			name:      "limit is exclusive",
			advisory:  osvVersionAdvisory(semanticVersionRange(VulnerabilityVersionEvent{Introduced: "0"}, VulnerabilityVersionEvent{Limit: "2.0.0"})),
			installed: "2.0.0",
			want:      versionUnaffected,
		},
		{
			name:      "any higher limit keeps range applicable",
			advisory:  osvVersionAdvisory(semanticVersionRange(VulnerabilityVersionEvent{Introduced: "0"}, VulnerabilityVersionEvent{Limit: "1.0.0"}, VulnerabilityVersionEvent{Limit: "3.0.0"})),
			installed: "2.0.0",
			want:      versionAffected,
		},
		{
			name:      "star limit is unbounded",
			advisory:  osvVersionAdvisory(semanticVersionRange(VulnerabilityVersionEvent{Introduced: "2.0.0"}, VulnerabilityVersionEvent{Limit: "*"})),
			installed: "9.0.0",
			want:      versionAffected,
		},
		{
			name:      "wildcard limit is unbounded",
			advisory:  osvVersionAdvisory(semanticVersionRange(VulnerabilityVersionEvent{Introduced: "2.0.0"}, VulnerabilityVersionEvent{Limit: "2.*"})),
			installed: "9.0.0",
			want:      versionAffected,
		},
		{
			name:      "events are ordered by version",
			advisory:  osvVersionAdvisory(semanticVersionRange(VulnerabilityVersionEvent{Fixed: "2.0.1"}, VulnerabilityVersionEvent{Introduced: "2.0.0"})),
			installed: "2.0.0",
			want:      versionAffected,
		},
		{name: "exact non-semantic version match", advisory: VulnerabilityAdvisory{AffectedVersions: []string{"release-1"}}, installed: "release-1", want: versionAffected},
		{name: "exact version mismatch", advisory: VulnerabilityAdvisory{AffectedVersions: []string{"release-1"}}, installed: "release-2", want: versionUnaffected},
		{
			name: "exact version overrides unsupported range",
			advisory: VulnerabilityAdvisory{
				AffectedVersions: []string{"release-1"},
				VersionRanges:    []VulnerabilityVersionRange{{Type: "ECOSYSTEM", Events: []VulnerabilityVersionEvent{{Introduced: "release-0"}}}},
			},
			installed: "release-1",
			want:      versionAffected,
		},
		{
			name: "supported match overrides unsupported range",
			advisory: VulnerabilityAdvisory{VersionRanges: []VulnerabilityVersionRange{
				{Type: "GIT", Events: []VulnerabilityVersionEvent{{Introduced: "abc"}}},
				lowerBounded,
			}},
			installed: "2.0.0",
			want:      versionAffected,
		},
		{
			name: "non-semantic ecosystem range is unevaluable",
			advisory: VulnerabilityAdvisory{VersionRanges: []VulnerabilityVersionRange{
				{Type: "ECOSYSTEM", Events: []VulnerabilityVersionEvent{{Introduced: "release-1"}}},
			}},
			installed: "release-2",
			want:      versionUnevaluable,
		},
		{
			name:      "complex ecosystem range is unevaluable",
			advisory:  osvVersionAdvisory(ecosystemVersionRange(VulnerabilityVersionEvent{Introduced: "0"}, VulnerabilityVersionEvent{Fixed: "1.0.0"}, VulnerabilityVersionEvent{Introduced: "2.0.0"}, VulnerabilityVersionEvent{Fixed: "3.0.0"})),
			installed: "2.5.0",
			want:      versionUnevaluable,
		},
		{
			name:      "reversed ecosystem bounds are unevaluable",
			advisory:  osvVersionAdvisory(ecosystemVersionRange(VulnerabilityVersionEvent{Introduced: "3.0.0"}, VulnerabilityVersionEvent{Fixed: "2.0.0"})),
			installed: "2.5.0",
			want:      versionUnevaluable,
		},
		{
			name:      "equal ecosystem bounds are unevaluable",
			advisory:  osvVersionAdvisory(ecosystemVersionRange(VulnerabilityVersionEvent{Introduced: "2.0.0"}, VulnerabilityVersionEvent{Fixed: "2.0.0"})),
			installed: "2.0.0",
			want:      versionUnevaluable,
		},
		{
			name:      "non-semantic ecosystem introduced bound is unevaluable",
			advisory:  osvVersionAdvisory(ecosystemVersionRange(VulnerabilityVersionEvent{Introduced: "release-1"}, VulnerabilityVersionEvent{Fixed: "2.0.0"})),
			installed: "1.0.0",
			want:      versionUnevaluable,
		},
		{
			name:      "non-semantic ecosystem fixed bound is unevaluable",
			advisory:  osvVersionAdvisory(ecosystemVersionRange(VulnerabilityVersionEvent{Introduced: "0"}, VulnerabilityVersionEvent{Fixed: "release-2"})),
			installed: "1.0.0",
			want:      versionUnevaluable,
		},
		{
			name:      "duplicate ecosystem introduced events are unevaluable",
			advisory:  osvVersionAdvisory(ecosystemVersionRange(VulnerabilityVersionEvent{Introduced: "0"}, VulnerabilityVersionEvent{Introduced: "1.0.0"})),
			installed: "1.0.0",
			want:      versionUnevaluable,
		},
		{
			name:      "duplicate ecosystem fixed events are unevaluable",
			advisory:  osvVersionAdvisory(ecosystemVersionRange(VulnerabilityVersionEvent{Fixed: "2.0.0"}, VulnerabilityVersionEvent{Fixed: "3.0.0"})),
			installed: "1.0.0",
			want:      versionUnevaluable,
		},
		{
			name:      "multi-field ecosystem event is unevaluable",
			advisory:  osvVersionAdvisory(ecosystemVersionRange(VulnerabilityVersionEvent{Introduced: "0", Fixed: "2.0.0"}, VulnerabilityVersionEvent{Fixed: "3.0.0"})),
			installed: "1.0.0",
			want:      versionUnevaluable,
		},
		{
			name:      "ecosystem last affected range is unevaluable",
			advisory:  osvVersionAdvisory(ecosystemVersionRange(VulnerabilityVersionEvent{Introduced: "0"}, VulnerabilityVersionEvent{LastAffected: "2.32.0"})),
			installed: "2.31.0",
			want:      versionUnevaluable,
		},
		{
			name: "malformed ecosystem metadata overrides fixed version fallback",
			advisory: VulnerabilityAdvisory{
				FixedVersion:  "2.32.0",
				VersionRanges: []VulnerabilityVersionRange{ecosystemVersionRange(VulnerabilityVersionEvent{Introduced: "0"}, VulnerabilityVersionEvent{LastAffected: "2.31.0"})},
			},
			installed: "2.31.0",
			want:      versionUnevaluable,
		},
		{
			name: "unsupported range prevents a false safe result",
			advisory: VulnerabilityAdvisory{VersionRanges: []VulnerabilityVersionRange{
				lowerBounded,
				{Type: "GIT", Events: []VulnerabilityVersionEvent{{Introduced: "abc"}}},
			}},
			installed: "1.5.0",
			want:      versionUnevaluable,
		},
		{name: "blank installed version is unevaluable", advisory: osvVersionAdvisory(lowerBounded), installed: "", want: versionUnevaluable},
		{name: "invalid installed version is unevaluable", advisory: osvVersionAdvisory(lowerBounded), installed: "release", want: versionUnevaluable},
		{
			name:      "invalid event is unevaluable",
			advisory:  osvVersionAdvisory(semanticVersionRange(VulnerabilityVersionEvent{Introduced: "0", Fixed: "2.0.0"})),
			installed: "1.0.0",
			want:      versionUnevaluable,
		},
		{
			name:      "invalid semantic boundary is unevaluable",
			advisory:  osvVersionAdvisory(semanticVersionRange(VulnerabilityVersionEvent{Introduced: "release"})),
			installed: "1.0.0",
			want:      versionUnevaluable,
		},
		{
			name:      "range requires introduced event",
			advisory:  osvVersionAdvisory(semanticVersionRange(VulnerabilityVersionEvent{Fixed: "2.0.0"})),
			installed: "1.0.0",
			want:      versionUnevaluable,
		},
		{
			name:      "fixed and last affected cannot coexist",
			advisory:  osvVersionAdvisory(semanticVersionRange(VulnerabilityVersionEvent{Introduced: "0"}, VulnerabilityVersionEvent{Fixed: "2.0.0"}, VulnerabilityVersionEvent{LastAffected: "1.9.0"})),
			installed: "1.0.0",
			want:      versionUnevaluable,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := advisoryVersionMatch(test.advisory, test.installed); got != test.want {
				t.Fatalf("advisoryVersionMatch(%q) = %v, want %v", test.installed, got, test.want)
			}
		})
	}
}

func TestNormalizeAdvisoriesPreservesUnionOfOSVVersionMetadata(t *testing.T) {
	firstRange := semanticVersionRange(VulnerabilityVersionEvent{Introduced: " 0 "}, VulnerabilityVersionEvent{Fixed: "1.1.0"})
	firstRange.Type = " semver "
	secondRange := semanticVersionRange(VulnerabilityVersionEvent{Introduced: "2.0.0"}, VulnerabilityVersionEvent{Fixed: "2.1.0"})

	normalized := normalizeAdvisories([]VulnerabilityAdvisory{
		{ID: "OSV-union", Package: "example", Ecosystem: "npm", AffectedVersions: []string{" 1.0.0 ", "1.0.0"}, VersionRanges: []VulnerabilityVersionRange{secondRange, firstRange}},
		{ID: "OSV-union", Package: "example", Ecosystem: "npm", AffectedVersions: []string{"2.0.0"}, VersionRanges: []VulnerabilityVersionRange{secondRange}},
	})

	if len(normalized) != 1 {
		t.Fatalf("expected duplicate advisories to merge, got %#v", normalized)
	}
	got := normalized[0]
	if !slices.Equal(got.AffectedVersions, []string{"1.0.0", "2.0.0"}) {
		t.Fatalf("expected exact affected versions to be merged, got %#v", got.AffectedVersions)
	}
	if len(got.VersionRanges) != 2 || got.VersionRanges[0].Type != "SEMVER" || got.VersionRanges[0].Events[0].Introduced != "0" {
		t.Fatalf("expected normalized, deduplicated ranges, got %#v", got.VersionRanges)
	}
}

func TestAnnotateVulnerabilitiesWarnsWithoutReportingUnevaluableOSVRanges(t *testing.T) {
	reportData := Report{
		Warnings: []string{"z existing warning", "a existing warning"},
		Dependencies: []DependencyReport{
			{Name: "example-lib", Language: "js-ts", Identity: &DependencyIdentity{Ecosystem: "npm", Name: "example-lib", Version: "1.0.0"}},
			{Name: "unknown-lib", Language: "js-ts", Identity: &DependencyIdentity{Ecosystem: "npm", Name: "unknown-lib"}},
		},
	}
	advisories := []VulnerabilityAdvisory{
		{
			ID:            "OSV-unsupported",
			Package:       "example-lib",
			Ecosystem:     "npm",
			VersionRanges: []VulnerabilityVersionRange{{Type: "ECOSYSTEM", Events: []VulnerabilityVersionEvent{{Introduced: "release-1"}}}},
		},
		{
			ID:               "OSV-unknown-version",
			Package:          "unknown-lib",
			Ecosystem:        "npm",
			AffectedVersions: []string{"1.0.0"},
		},
	}

	AnnotateVulnerabilities(&reportData, advisories)
	AnnotateVulnerabilities(&reportData, advisories)

	for _, dependency := range reportData.Dependencies {
		if len(dependency.Vulnerabilities) != 0 {
			t.Fatalf("expected unevaluable ranges not to create findings, got %#v", dependency.Vulnerabilities)
		}
	}
	wantWarnings := []string{
		"z existing warning",
		"a existing warning",
		"unable to evaluate OSV advisory OSV-unknown-version for npm/unknown-lib@unknown",
		"unable to evaluate OSV advisory OSV-unsupported for npm/example-lib@1.0.0",
	}
	if !slices.Equal(reportData.Warnings, wantWarnings) {
		t.Fatalf("unexpected OSV evaluation warnings: got %#v, want %#v", reportData.Warnings, wantWarnings)
	}
}

func TestAnnotateVulnerabilitiesUsesSupportedRangeWhenAnotherRangeIsUnevaluable(t *testing.T) {
	reportData := Report{Dependencies: []DependencyReport{{
		Name:     "example-lib",
		Language: "js-ts",
		Identity: &DependencyIdentity{Ecosystem: "npm", Name: "example-lib", Version: "2.0.0"},
	}}}
	advisory := VulnerabilityAdvisory{
		ID:        "OSV-confirmed",
		Package:   "example-lib",
		Ecosystem: "npm",
		VersionRanges: []VulnerabilityVersionRange{
			{Type: "GIT", Events: []VulnerabilityVersionEvent{{Introduced: "abc"}}},
			semanticVersionRange(VulnerabilityVersionEvent{Introduced: "2.0.0"}, VulnerabilityVersionEvent{Fixed: "2.1.0"}),
		},
	}

	AnnotateVulnerabilities(&reportData, []VulnerabilityAdvisory{advisory})

	if len(reportData.Dependencies[0].Vulnerabilities) != 1 || len(reportData.Warnings) != 0 {
		t.Fatalf("expected confirmed semantic match without warning, got report %#v", reportData)
	}
}

func TestAnnotateVulnerabilitiesEvaluatesSimpleEcosystemRange(t *testing.T) {
	reportData := Report{Dependencies: []DependencyReport{{
		Name:     "requests",
		Language: "python",
		Identity: &DependencyIdentity{Ecosystem: "pypi", Name: "requests", Version: "2.31.0"},
	}}}
	advisory := VulnerabilityAdvisory{
		ID:            "OSV-ecosystem",
		Package:       "requests",
		Ecosystem:     "pypi",
		FixedVersion:  "2.32.0",
		VersionRanges: []VulnerabilityVersionRange{ecosystemVersionRange(VulnerabilityVersionEvent{Introduced: "0"}, VulnerabilityVersionEvent{Fixed: "2.32.0"})},
	}

	AnnotateVulnerabilities(&reportData, []VulnerabilityAdvisory{advisory})

	if len(reportData.Dependencies[0].Vulnerabilities) != 1 || len(reportData.Warnings) != 0 {
		t.Fatalf("expected simple ecosystem range finding without warning, got report %#v", reportData)
	}
}

func semanticVersionRange(events ...VulnerabilityVersionEvent) VulnerabilityVersionRange {
	return VulnerabilityVersionRange{Type: "SEMVER", Events: events}
}

func ecosystemVersionRange(events ...VulnerabilityVersionEvent) VulnerabilityVersionRange {
	return VulnerabilityVersionRange{Type: "ECOSYSTEM", Events: events}
}

func osvVersionAdvisory(ranges ...VulnerabilityVersionRange) VulnerabilityAdvisory {
	return VulnerabilityAdvisory{VersionRanges: ranges}
}
