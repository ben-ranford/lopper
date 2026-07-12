package report

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"
)

func decodeCycloneDXBOM(t *testing.T, output string) cycloneDXBOM {
	t.Helper()
	var bom cycloneDXBOM
	if err := json.Unmarshal([]byte(output), &bom); err != nil {
		t.Fatalf("decode CycloneDX BOM: %v\n%s", err, output)
	}
	return bom
}

func cycloneDXPropertyValue(properties []cycloneDXProperty, name string) (string, bool) {
	for _, property := range properties {
		if property.Name == name {
			return property.Value, true
		}
	}
	return "", false
}

func requireCycloneDXProperty(t *testing.T, properties []cycloneDXProperty, name, want string) {
	t.Helper()
	got, ok := cycloneDXPropertyValue(properties, name)
	if !ok {
		t.Fatalf("expected CycloneDX property %q in %#v", name, properties)
	}
	if got != want {
		t.Fatalf("expected CycloneDX property %q=%q, got %q", name, want, got)
	}
}

func TestParseFormatCycloneDXJSON(t *testing.T) {
	got, err := ParseFormat(" CycloneDX-JSON ")
	if err != nil {
		t.Fatalf("parse cyclonedx-json format: %v", err)
	}
	if got != FormatCycloneDX {
		t.Fatalf("expected format %q, got %q", FormatCycloneDX, got)
	}
}

func TestFormatCycloneDXJSONDispatch(t *testing.T) {
	reportData := Report{
		SchemaVersion: SchemaVersion,
		GeneratedAt:   time.Date(2026, time.June, 25, 1, 2, 3, 0, time.UTC),
		RepoPath:      ".",
		Dependencies: []DependencyReport{
			{Name: "lodash", Language: "js-ts", UsedExportsCount: 1, TotalExportsCount: 2, UsedPercent: 50},
		},
	}
	output, err := NewFormatter().Format(reportData, FormatCycloneDX)
	if err != nil {
		t.Fatalf("format CycloneDX JSON: %v", err)
	}

	bom := decodeCycloneDXBOM(t, output)
	if bom.BOMFormat != "CycloneDX" || bom.SpecVersion != "1.6" || bom.Version != 1 {
		t.Fatalf("unexpected CycloneDX header: %#v", bom)
	}
	if bom.Metadata == nil || bom.Metadata.Timestamp != "2026-06-25T01:02:03Z" {
		t.Fatalf("expected deterministic metadata timestamp, got %#v", bom.Metadata)
	}
	if len(bom.Components) != 1 || bom.Components[0].Name != "lodash" {
		t.Fatalf("expected lodash component, got %#v", bom.Components)
	}
	requireCycloneDXProperty(t, bom.Properties, "lopper:export:coverage", "direct-dependencies")
}

func TestFormatCycloneDXJSONDeterministicOrdering(t *testing.T) {
	first := Report{Dependencies: []DependencyReport{
		{
			Name:     "zeta",
			Language: "go",
			UsedImports: []ImportUse{
				{Name: "B", Module: "mod/b", Provenance: []string{"second", "first"}},
				{Name: "A", Module: "mod/a"},
			},
		},
		{
			Name:     "alpha",
			Language: "js-ts",
			License:  &DependencyLicense{SPDX: "MIT", Evidence: []string{"package.json", "LICENSE"}},
		},
	}}
	second := Report{Dependencies: []DependencyReport{
		{
			Name:     "alpha",
			Language: "js-ts",
			License:  &DependencyLicense{SPDX: "MIT", Evidence: []string{"LICENSE", "package.json"}},
		},
		{
			Name:     "zeta",
			Language: "go",
			UsedImports: []ImportUse{
				{Name: "A", Module: "mod/a"},
				{Name: "B", Module: "mod/b", Provenance: []string{"first", "second"}},
			},
		},
	}}

	firstOutput, err := NewFormatter().Format(first, FormatCycloneDX)
	if err != nil {
		t.Fatalf("format first CycloneDX JSON: %v", err)
	}
	secondOutput, err := NewFormatter().Format(second, FormatCycloneDX)
	if err != nil {
		t.Fatalf("format second CycloneDX JSON: %v", err)
	}
	if firstOutput != secondOutput {
		t.Fatalf("expected deterministic CycloneDX output\nfirst:\n%s\nsecond:\n%s", firstOutput, secondOutput)
	}

	bom := decodeCycloneDXBOM(t, firstOutput)
	if got := []string{bom.Components[0].Name, bom.Components[1].Name}; got[0] != "zeta" || got[1] != "alpha" {
		t.Fatalf("expected components sorted by language then name, got %#v", got)
	}
	usedImports, ok := cycloneDXPropertyValue(bom.Components[0].Properties, "lopper:used-imports")
	if !ok || !strings.Contains(usedImports, `"module":"mod/a"`) || !strings.Contains(usedImports, `"provenance":["first","second"]`) {
		t.Fatalf("expected sorted import metadata, got %q", usedImports)
	}
}

func TestFormatCycloneDXJSONPreservesDependencySurfaceMetadata(t *testing.T) {
	loadDelta := 3
	reportData := Report{
		SchemaVersion: SchemaVersion,
		RepoPath:      ".",
		Scope:         &ScopeMetadata{Mode: "package", Packages: []string{"packages/b", "packages/a"}},
		EffectivePolicy: &EffectivePolicy{
			Sources: []string{"repo", "defaults"},
			License: LicensePolicy{Deny: []string{"GPL-3.0-only"}, FailOnDenied: true},
		},
		BaselineComparison: &BaselineComparison{
			BaselineKey:   "commit:base",
			CurrentKey:    "commit:head",
			UnchangedRows: 2,
			Dependencies: []DependencyDelta{{
				Kind:                      DependencyDeltaChanged,
				Language:                  "js-ts",
				Name:                      "lodash",
				UsedPercentDelta:          -5,
				WastePercentDelta:         5,
				EstimatedUnusedBytesDelta: 128,
				RuntimeDelta: &RuntimeDelta{
					Comparable:      true,
					BaselinePresent: true,
					CurrentPresent:  true,
					LoadCountDelta:  &loadDelta,
					NewRuntimeLoads: true,
				},
				DeniedIntroduced: true,
			}},
		},
		Dependencies: []DependencyReport{{
			Name:                 "lodash",
			Language:             "js-ts",
			UsedExportsCount:     1,
			TotalExportsCount:    4,
			UsedPercent:          25,
			EstimatedUnusedBytes: 2048,
			UsedImports: []ImportUse{{
				Name:                  "map",
				Module:                "lodash/map",
				Locations:             []Location{{File: "src/app.ts", Line: 7, Column: 3}},
				Provenance:            []string{"direct-import"},
				ConfidenceScore:       98,
				ConfidenceReasonCodes: []string{"static-import"},
			}},
			RuntimeUsage: &RuntimeUsage{
				LoadCount:   3,
				Correlation: RuntimeCorrelationOverlap,
				Modules:     []RuntimeModuleUsage{{Module: "lodash/map", Count: 2}},
				TopSymbols:  []RuntimeSymbolUsage{{Symbol: "map", Module: "lodash", Count: 2}},
			},
			ReachabilityConfidence: &ReachabilityConfidence{
				Model:          "v2",
				Score:          87.5,
				Summary:        "high confidence",
				RationaleCodes: []string{"runtime-overlap", "static-import"},
				Signals:        []ReachabilitySignal{{Code: "runtime-overlap", Score: 100, Weight: 0.2, Contribution: 20}},
			},
			RemovalCandidate: &RemovalCandidate{
				Score:      71,
				Usage:      75,
				Impact:     50,
				Confidence: 87.5,
				Weights:    RemovalCandidateWeights{Usage: 0.5, Impact: 0.3, Confidence: 0.2},
				Rationale:  []string{"low usage"},
			},
			License:    &DependencyLicense{SPDX: "MIT", Source: "package.json", Confidence: "high", Denied: true, Evidence: []string{"package.json"}},
			Provenance: &DependencyProvenance{Source: "package.json", Confidence: "high", Signals: []string{"registry-metadata", "package-json"}},
		}},
	}

	output, err := NewFormatter().Format(reportData, FormatCycloneDX)
	if err != nil {
		t.Fatalf("format CycloneDX JSON: %v", err)
	}
	bom := decodeCycloneDXBOM(t, output)
	if len(bom.Components) != 1 {
		t.Fatalf("expected one component, got %#v", bom.Components)
	}
	component := bom.Components[0]
	if len(component.Licenses) != 1 || component.Licenses[0].License.Name != "MIT" {
		t.Fatalf("expected CycloneDX license metadata, got %#v", component.Licenses)
	}

	requireCycloneDXProperty(t, component.Properties, "lopper:dependency:language", "js-ts")
	requireCycloneDXProperty(t, component.Properties, "lopper:dependency:version:status", "unknown")
	requireCycloneDXProperty(t, component.Properties, "lopper:dependency:purl:status", "unavailable")
	requireCycloneDXProperty(t, component.Properties, "lopper:waste-percent", "75")
	requireCycloneDXProperty(t, component.Properties, "lopper:runtime:correlation", "overlap")
	requireCycloneDXProperty(t, component.Properties, "lopper:reachability:score", "87.5")
	requireCycloneDXProperty(t, component.Properties, "lopper:license:denied", "true")
	requireCycloneDXProperty(t, component.Properties, "lopper:provenance:source", "package.json")
	requireCycloneDXProperty(t, component.Properties, "lopper:removal-candidate:score", "71")
	requireCycloneDXProperty(t, component.Properties, "lopper:baseline:kind", "changed")
	requireCycloneDXProperty(t, component.Properties, "lopper:baseline:denied-introduced", "true")
	runtimeDelta, ok := cycloneDXPropertyValue(component.Properties, "lopper:baseline:runtime-delta")
	if !ok || !strings.Contains(runtimeDelta, `"newRuntimeLoads":true`) {
		t.Fatalf("expected CycloneDX runtime baseline delta, got %q", runtimeDelta)
	}
	requireCycloneDXProperty(t, bom.Properties, "lopper:baseline:key", "commit:base")

	provenanceSignals, ok := cycloneDXPropertyValue(component.Properties, "lopper:provenance:signals")
	if !ok || provenanceSignals != `["package-json","registry-metadata"]` {
		t.Fatalf("expected sorted provenance signals, got %q", provenanceSignals)
	}
}

func TestFormatCycloneDXJSONEmptyAndMissingOptionalFields(t *testing.T) {
	emptyOutput, err := NewFormatter().Format(Report{}, FormatCycloneDX)
	if err != nil {
		t.Fatalf("format empty CycloneDX JSON: %v", err)
	}
	emptyBOM := decodeCycloneDXBOM(t, emptyOutput)
	if len(emptyBOM.Components) != 0 {
		t.Fatalf("expected empty components, got %#v", emptyBOM.Components)
	}
	if emptyBOM.Metadata != nil {
		t.Fatalf("expected omitted metadata for empty report, got %#v", emptyBOM.Metadata)
	}

	output, err := NewFormatter().Format(Report{Dependencies: []DependencyReport{{Name: "unknown-license", Language: "python"}}}, FormatCycloneDX)
	if err != nil {
		t.Fatalf("format missing optional fields CycloneDX JSON: %v", err)
	}
	bom := decodeCycloneDXBOM(t, output)
	if len(bom.Components) != 1 {
		t.Fatalf("expected one component, got %#v", bom.Components)
	}
	component := bom.Components[0]
	if len(component.Licenses) != 0 {
		t.Fatalf("expected no CycloneDX licenses when license is unknown, got %#v", component.Licenses)
	}
	requireCycloneDXProperty(t, component.Properties, "lopper:license:unknown", "true")
	if _, ok := cycloneDXPropertyValue(component.Properties, "lopper:used-imports"); ok {
		t.Fatalf("expected empty optional import details to be omitted, got %#v", component.Properties)
	}
}

func TestFormatCycloneDXJSONConservativeBranches(t *testing.T) {
	reportData := Report{
		SchemaVersion: SchemaVersion,
		Summary:       &Summary{DependencyCount: 2, UsedExportsCount: 1, TotalExportsCount: 4, UsedPercent: 25},
		LanguageBreakdown: []LanguageSummary{
			{Language: "python", DependencyCount: 1},
			{Language: "js-ts", DependencyCount: 1},
		},
		EffectivePolicy: &EffectivePolicy{
			Sources: []string{"defaults"},
			Thresholds: EffectiveThresholds{
				FailOnIncreasePercent:             5,
				LowConfidenceWarningPercent:       35,
				MinUsagePercentForRecommendations: 45,
				MaxUncertainImportCount:           2,
			},
			RemovalCandidateWeights: RemovalCandidateWeights{Usage: 0.5, Impact: 0.3, Confidence: 0.2},
			License:                 LicensePolicy{Deny: []string{"GPL-3.0-only"}, IncludeRegistryProvenance: true},
			MergeTrace: []PolicyMergeTrace{
				{Field: "thresholds.fail_on_increase", Source: "defaults"},
				{Field: "license.deny", Source: "defaults"},
			},
		},
		BaselineComparison: &BaselineComparison{
			NewDeniedLicenses: []DeniedLicenseDelta{
				{Language: "python", Name: "badpy", SPDX: "GPL-2.0-only"},
				{Language: "js-ts", Name: "left-pad", SPDX: "GPL-3.0-only"},
			},
		},
		Dependencies: []DependencyReport{
			{
				Language:          "js-ts",
				Name:              "",
				UsedPercent:       math.NaN(),
				TotalExportsCount: 2,
				TopUsedSymbols:    []SymbolUsage{{Name: "zpad", Module: "left-pad/z", Count: 2}, {Name: "pad", Module: "left-pad", Count: 1}},
				UnusedImports:     []ImportUse{{Name: "zunused", Module: "left-pad/zunused"}, {Name: "unused", Module: "left-pad/unused"}},
				UnusedExports:     []SymbolRef{{Name: "zoldPad", Module: "left-pad/z", ConfidenceReasonCodes: []string{"z"}}, {Name: "oldPad", Module: "left-pad", ConfidenceReasonCodes: []string{"unused-export"}}},
				RiskCues:          []RiskCue{{Code: "runtime-only", Severity: "medium", ConfidenceReasonCodes: []string{"runtime"}}, {Code: "license-denied", Severity: "high", ConfidenceReasonCodes: []string{"policy"}}},
				Recommendations:   []Recommendation{{Code: "review", Priority: "medium", ConfidenceReasonCodes: []string{"runtime"}}, {Code: "remove", Priority: "high", ConfidenceReasonCodes: []string{"unused"}}},
				License:           &DependencyLicense{Raw: "Custom", Unknown: true},
				Provenance:        &DependencyProvenance{Source: "unknown"},
				RemovalCandidate:  &RemovalCandidate{Weights: RemovalCandidateWeights{Usage: 0.5}},
				RuntimeUsage: &RuntimeUsage{
					RuntimeOnly: true,
					Modules:     []RuntimeModuleUsage{{Module: "left-pad/z", Count: 2}, {Module: "left-pad", Count: 1}},
					TopSymbols:  []RuntimeSymbolUsage{{Module: "left-pad/z", Symbol: "zpad", Count: 2}, {Module: "left-pad", Symbol: "pad", Count: 1}},
				},
				ReachabilityConfidence: &ReachabilityConfidence{
					Signals: []ReachabilitySignal{{Code: "z-signal"}, {Code: "a-signal"}},
				},
				UsedExportsCount:     0,
				EstimatedUnusedBytes: 10,
			},
			{Language: "js-ts", Name: ""},
		},
	}

	output, err := NewFormatter().Format(reportData, FormatCycloneDX)
	if err != nil {
		t.Fatalf("format conservative CycloneDX branches: %v", err)
	}
	bom := decodeCycloneDXBOM(t, output)
	if len(bom.Components) != 2 {
		t.Fatalf("expected duplicate unknown-name components, got %#v", bom.Components)
	}
	if bom.Components[0].BOMRef == bom.Components[1].BOMRef {
		t.Fatalf("expected duplicate bom-ref suffix, got %#v", bom.Components)
	}
	var richComponent *cycloneDXComponent
	for i := range bom.Components {
		if _, ok := cycloneDXPropertyValue(bom.Components[i].Properties, "lopper:runtime:runtime-only"); ok {
			richComponent = &bom.Components[i]
		}
	}
	if richComponent == nil {
		t.Fatalf("expected component with runtime metadata, got %#v", bom.Components)
	}
	requireCycloneDXProperty(t, richComponent.Properties, "lopper:dependency:name:status", "unknown")
	requireCycloneDXProperty(t, richComponent.Properties, "lopper:used-percent", "0")
	requireCycloneDXProperty(t, richComponent.Properties, "lopper:waste-percent", "0")
	requireCycloneDXProperty(t, richComponent.Properties, "lopper:runtime:runtime-only", "true")
	requireCycloneDXProperty(t, bom.Properties, "lopper:export:format", string(FormatCycloneDX))
	if _, ok := cycloneDXPropertyValue(bom.Properties, "lopper:policy:merge-trace"); !ok {
		t.Fatalf("expected policy merge trace root property, got %#v", bom.Properties)
	}
	if _, ok := cycloneDXPropertyValue(bom.Properties, "lopper:baseline:new-denied-licenses"); !ok {
		t.Fatalf("expected new denied license root property, got %#v", bom.Properties)
	}
}

func TestAppendCycloneDXJSONPropertySkipsMarshalErrors(t *testing.T) {
	props := []cycloneDXProperty{{Name: "existing", Value: "kept"}}
	appendCycloneDXJSONProperty(&props, "bad", make(chan int))
	if len(props) != 1 || props[0].Name != "existing" {
		t.Fatalf("expected marshal error to leave properties unchanged, got %#v", props)
	}
}

func TestFormatCycloneDXJSONKeepsDuplicateComponentsStableAndAttributed(t *testing.T) {
	dependencies := []DependencyReport{
		{
			Language:          "js-ts",
			Name:              "dup",
			UsedExportsCount:  1,
			TotalExportsCount: 2,
			UsedImports:       []ImportUse{{Name: "a", Module: "m/a"}},
		},
		{
			Language:          "js-ts",
			Name:              "dup",
			UsedExportsCount:  2,
			TotalExportsCount: 3,
			UsedImports:       []ImportUse{{Name: "b", Module: "m/b"}},
		},
	}
	deltas := []DependencyDelta{
		{Kind: DependencyDeltaChanged, Language: "js-ts", Name: "dup", UsedExportsCountDelta: 1},
		{Kind: DependencyDeltaChanged, Language: "js-ts", Name: "dup", UsedExportsCountDelta: 9},
	}

	permutations := []Report{
		{Dependencies: dependencies, BaselineComparison: &BaselineComparison{Dependencies: deltas}},
		{Dependencies: []DependencyReport{dependencies[1], dependencies[0]}, BaselineComparison: &BaselineComparison{Dependencies: deltas}},
		{Dependencies: dependencies, BaselineComparison: &BaselineComparison{Dependencies: []DependencyDelta{deltas[1], deltas[0]}}},
		{Dependencies: []DependencyReport{dependencies[1], dependencies[0]}, BaselineComparison: &BaselineComparison{Dependencies: []DependencyDelta{deltas[1], deltas[0]}}},
	}

	var firstOutput string
	for index, reportData := range permutations {
		output, err := NewFormatter().Format(reportData, FormatCycloneDX)
		if err != nil {
			t.Fatalf("format permutation %d: %v", index, err)
		}
		if index == 0 {
			firstOutput = output
			continue
		}
		if output != firstOutput {
			t.Fatalf("permutation %d changed CycloneDX output\nfirst:\n%s\npermuted:\n%s", index, firstOutput, output)
		}
	}

	bom := decodeCycloneDXBOM(t, firstOutput)
	if len(bom.Components) != 2 {
		t.Fatalf("expected two duplicate components, got %#v", bom.Components)
	}
	if bom.Components[0].BOMRef != "lopper:dependency:js-ts:dup" || bom.Components[1].BOMRef != "lopper:dependency:js-ts:dup:2" {
		t.Fatalf("expected stable unique duplicate refs, got %q and %q", bom.Components[0].BOMRef, bom.Components[1].BOMRef)
	}
	requireCycloneDXProperty(t, bom.Components[0].Properties, "lopper:used-imports", `[{"name":"a","module":"m/a"}]`)
	requireCycloneDXProperty(t, bom.Components[0].Properties, "lopper:baseline:used-exports-count-delta", "1")
	requireCycloneDXProperty(t, bom.Components[1].Properties, "lopper:used-imports", `[{"name":"b","module":"m/b"}]`)
	requireCycloneDXProperty(t, bom.Components[1].Properties, "lopper:baseline:used-exports-count-delta", "9")
}
