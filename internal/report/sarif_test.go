package report

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	errParseSARIFOutput = "parse sarif output: %v"
	errExpectedOneRun   = "expected one run, got %d"
	testFileGo          = "file.go"
	testAFileGo         = "a/file.go"
)

func TestFormatSARIFGolden(t *testing.T) {
	reportData := sampleSARIFReport()

	output, err := NewFormatter().Format(reportData, FormatSARIF)
	if err != nil {
		t.Fatalf("format sarif: %v", err)
	}

	goldenPath := filepath.Join("..", "..", "testdata", "report", "sarif.golden")
	golden, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden file: %v", err)
	}

	if output != string(golden) {
		t.Fatalf("sarif output did not match golden")
	}

	var payload sarifLog
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		t.Fatalf(errParseSARIFOutput, err)
	}
	if len(payload.Runs) != 1 {
		t.Fatalf(errExpectedOneRun, len(payload.Runs))
	}
	if len(payload.Runs[0].Tool.Driver.Rules) == 0 {
		t.Fatalf("expected at least one rule")
	}
	if len(payload.Runs[0].Results) == 0 {
		t.Fatalf("expected at least one result")
	}
}

func TestFormatSARIFEmptyReport(t *testing.T) {
	output, err := NewFormatter().Format(Report{}, FormatSARIF)
	if err != nil {
		t.Fatalf("format empty sarif report: %v", err)
	}
	if !strings.Contains(output, "\"results\": []") {
		t.Fatalf("expected empty results array in sarif output")
	}

	var payload sarifLog
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		t.Fatalf(errParseSARIFOutput, err)
	}
	if len(payload.Runs) != 1 {
		t.Fatalf(errExpectedOneRun, len(payload.Runs))
	}
	if len(payload.Runs[0].Results) != 0 {
		t.Fatalf("expected no results for empty report, got %d", len(payload.Runs[0].Results))
	}
}

func TestFormatSARIFWasteOnlyReport(t *testing.T) {
	wasteIncrease := 3.0
	output, err := NewFormatter().Format(Report{WasteIncreasePercent: &wasteIncrease}, FormatSARIF)
	if err != nil {
		t.Fatalf("format waste-only sarif report: %v", err)
	}

	var payload sarifLog
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		t.Fatalf(errParseSARIFOutput, err)
	}
	if len(payload.Runs) != 1 {
		t.Fatalf(errExpectedOneRun, len(payload.Runs))
	}
	if len(payload.Runs[0].Results) != 1 {
		t.Fatalf("expected one result for waste-only report, got %d", len(payload.Runs[0].Results))
	}
	if payload.Runs[0].Results[0].RuleID != "lopper/waste/increase" {
		t.Fatalf("expected waste increase rule, got %q", payload.Runs[0].Results[0].RuleID)
	}
}

func TestFormatSARIFIncludesRuntimeAndBaselineContext(t *testing.T) {
	reportData := sampleSARIFRuntimeAndBaselineReport()

	output, err := NewFormatter().Format(reportData, FormatSARIF)
	if err != nil {
		t.Fatalf("format sarif with runtime context: %v", err)
	}

	payload := mustSARIFLog(t, output)
	dependencyProps, wasteProps := mustSARIFProperties(t, payload.Runs[0].Results)
	assertSARIFRuntimeProperties(t, dependencyProps["runtime"])
	assertSARIFBaselineContext(t, dependencyProps["baselineContext"], true)
	assertSARIFBaselineContextPresent(t, wasteProps["baselineContext"])
}

func TestSARIFDependencyPropertiesIncludePerInstanceMetadataAndExtras(t *testing.T) {
	loadDelta := 2
	dependency := DependencyReport{
		Name:     "lib",
		Language: "js-ts",
		License: &DependencyLicense{
			SPDX:       "MIT",
			Source:     "package.json",
			Confidence: "high",
			Unknown:    false,
			Denied:     true,
			Evidence:   []string{"license field"},
		},
		Provenance: &DependencyProvenance{
			Source:     "lockfile",
			Confidence: "high",
			Signals:    []string{"resolved"},
		},
		RuntimeUsage: &RuntimeUsage{
			LoadCount:     3,
			Correlation:   RuntimeCorrelationOverlap,
			RuntimeOnly:   true,
			Modules:       []RuntimeModuleUsage{{Module: "lib/core", Count: 2}},
			ParentModules: []RuntimeModuleUsage{{Module: "app/root", Count: 1}},
			Entrypoints:   []RuntimeModuleUsage{{Module: "cmd/start", Count: 1}},
			TopSymbols:    []RuntimeSymbolUsage{{Symbol: "run", Module: "lib/core", Count: 2}},
		},
		ReachabilityConfidence: &ReachabilityConfidence{
			Model:          "v1",
			Score:          0.91,
			Summary:        "strong evidence",
			RationaleCodes: []string{"runtime"},
		},
		Vulnerabilities: []VulnerabilityFinding{{AdvisoryID: "GHSA-123", Priority: VulnerabilityPriorityHigh}},
	}
	delta := &DependencyDelta{
		Kind:                      DependencyDeltaChanged,
		UsedExportsCountDelta:     1,
		TotalExportsCountDelta:    -1,
		UsedPercentDelta:          5,
		EstimatedUnusedBytesDelta: 128,
		WastePercentDelta:         -5,
		DeniedIntroduced:          true,
		RuntimeDelta: &RuntimeDelta{
			Comparable:      true,
			BaselinePresent: true,
			CurrentPresent:  true,
			LoadCountDelta:  &loadDelta,
		},
	}
	props := sarifDependencyProperties(dependency, delta, map[string]any{"custom": "value"})

	runtimeProps, ok := props["runtime"].(map[string]any)
	if !ok || len(runtimeProps["modules"].([]map[string]any)) != 1 || len(runtimeProps["topSymbols"].([]map[string]any)) != 1 {
		t.Fatalf("expected runtime property bags to include modules and symbols, got %#v", runtimeProps)
	}
	if props["custom"] != "value" {
		t.Fatalf("expected extra SARIF property to be preserved, got %#v", props)
	}
	baselineContext, ok := props["baselineContext"].(map[string]any)
	if !ok || baselineContext["deniedIntroduced"] != true || baselineContext["runtimeDelta"] == nil {
		t.Fatalf("expected baseline context in SARIF properties, got %#v", baselineContext)
	}
}

func TestSARIFPropertyBagsReturnNilForEmptyInputs(t *testing.T) {
	if got := runtimeModulePropertyBag(nil); len(got) != 0 {
		t.Fatalf("expected nil runtime module bag for nil input, got %#v", got)
	}
	if got := runtimeSymbolPropertyBag(nil); len(got) != 0 {
		t.Fatalf("expected nil runtime symbol bag for nil input, got %#v", got)
	}
}

func TestFormatSARIFIncludesVulnerabilityFindings(t *testing.T) {
	reportData := Report{
		Dependencies: []DependencyReport{
			{
				Language: "js-ts",
				Name:     "reachable-lib",
				UsedImports: []ImportUse{
					{
						Name:   "default",
						Module: "reachable-lib",
						Locations: []Location{
							{File: "src/app.ts", Line: 7, Column: 1},
						},
					},
				},
				Vulnerabilities: []VulnerabilityFinding{
					{
						AdvisoryID:    "GHSA-1234",
						Package:       "reachable-lib",
						Severity:      "high",
						FixedVersion:  "1.2.3",
						Source:        "security-team",
						Priority:      VulnerabilityPriorityCritical,
						PriorityScore: 91,
						Reachable:     true,
						Evidence:      []string{"static_location: src/app.ts:7"},
					},
					{
						AdvisoryID: "GHSA-suppressed",
						Package:    "reachable-lib",
						Decision: &VulnerabilityExceptionDecision{
							Status: "accepted-risk",
						},
					},
				},
			},
		},
	}

	output, err := NewFormatter().Format(reportData, FormatSARIF)
	if err != nil {
		t.Fatalf("format sarif with vulnerability finding: %v", err)
	}

	payload := mustSARIFLog(t, output)
	var finding *sarifResult
	for i := range payload.Runs[0].Results {
		switch payload.Runs[0].Results[i].RuleID {
		case "lopper/vulnerability/ghsa-1234":
			finding = &payload.Runs[0].Results[i]
		case "lopper/vulnerability/ghsa-suppressed":
			t.Fatalf("expected accepted-risk finding to be omitted from SARIF, got %#v", payload.Runs[0].Results[i])
		}
	}
	if finding == nil {
		t.Fatalf("expected vulnerability SARIF result, got %#v", payload.Runs[0].Results)
	}
	if finding.Level != "error" {
		t.Fatalf("expected critical priority vulnerability to be SARIF error, got %#v", finding)
	}
	if !strings.Contains(finding.Message.Text, "reachability-weighted priority") || strings.Contains(strings.ToLower(finding.Message.Text), "exploit") {
		t.Fatalf("unexpected vulnerability SARIF message: %q", finding.Message.Text)
	}
	if finding.Properties["advisoryId"] != "GHSA-1234" || finding.Properties["fixedVersion"] != "1.2.3" || finding.Properties["priority"] != VulnerabilityPriorityCritical || finding.Properties["reachable"] != true {
		t.Fatalf("expected vulnerability properties, got %#v", finding.Properties)
	}
}

func TestFormatSARIFMapsDuplicatePURLBaselineContextPerInstance(t *testing.T) {
	current := Report{Dependencies: []DependencyReport{
		{
			Language:          "js-ts",
			Name:              "dup",
			Identity:          &DependencyIdentity{PURL: "pkg:npm/dup@1.0.0"},
			UsedExportsCount:  1,
			TotalExportsCount: 2,
			UnusedExports:     []SymbolRef{{Name: "alpha", Module: "dup/alpha"}},
		},
		{
			Language:          "js-ts",
			Name:              "dup",
			Identity:          &DependencyIdentity{PURL: "pkg:npm/dup@2.0.0"},
			UsedExportsCount:  11,
			TotalExportsCount: 12,
			UnusedExports:     []SymbolRef{{Name: "beta", Module: "dup/beta"}},
		},
	}}
	baseline := Report{Dependencies: []DependencyReport{
		{Language: "js-ts", Name: "dup", Identity: &DependencyIdentity{PURL: "pkg:npm/dup@1.0.0"}, TotalExportsCount: 2},
		{Language: "js-ts", Name: "dup", Identity: &DependencyIdentity{PURL: "pkg:npm/dup@2.0.0"}, UsedExportsCount: 2, TotalExportsCount: 12},
	}}
	comparison := ComputeBaselineComparison(current, baseline)
	current.BaselineComparison = &comparison

	output, err := NewFormatter().Format(current, FormatSARIF)
	if err != nil {
		t.Fatalf("format sarif duplicate purls: %v", err)
	}

	payload := mustSARIFLog(t, output)
	deltasByModule := map[string]float64{}
	for _, result := range payload.Runs[0].Results {
		if result.RuleID != "lopper/waste/unused-export" {
			continue
		}
		module, _ := result.Properties["module"].(string)
		baselineContext, _ := result.Properties["baselineContext"].(map[string]any)
		deltasByModule[module] = baselineContext["usedExportsCountDelta"].(float64)
	}
	if deltasByModule["dup/alpha"] != 1 || deltasByModule["dup/beta"] != 9 {
		t.Fatalf("expected per-instance duplicate SARIF baseline deltas, got %#v", deltasByModule)
	}
}

func assertWasteOnlySARIFWithoutResult(t *testing.T, wasteIncrease float64) {
	t.Helper()

	output, err := NewFormatter().Format(Report{WasteIncreasePercent: &wasteIncrease}, FormatSARIF)
	if err != nil {
		t.Fatalf("format waste-only sarif report: %v", err)
	}

	var payload sarifLog
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		t.Fatalf(errParseSARIFOutput, err)
	}
	if len(payload.Runs) != 1 {
		t.Fatalf(errExpectedOneRun, len(payload.Runs))
	}
	if len(payload.Runs[0].Results) != 0 {
		t.Fatalf("expected no results for non-positive waste delta, got %d", len(payload.Runs[0].Results))
	}
	if len(payload.Runs[0].Tool.Driver.Rules) != 0 {
		t.Fatalf("expected no rules for non-positive waste delta, got %d", len(payload.Runs[0].Tool.Driver.Rules))
	}
}

func TestFormatSARIFWasteOnlyReportNonPositiveDelta(t *testing.T) {
	testCases := []struct {
		name          string
		wasteIncrease float64
	}{
		{name: "zero", wasteIncrease: 0},
		{name: "negative", wasteIncrease: -3.5},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			assertWasteOnlySARIFWithoutResult(t, tc.wasteIncrease)
		})
	}
}

func TestNormalizeRuleToken(t *testing.T) {
	if got := normalizeRuleToken(""); got != "unknown" {
		t.Fatalf("normalizeRuleToken(%q) = %q, want %q", "", got, "unknown")
	}
	if got := normalizeRuleToken("rule id / with\\special*chars?"); got != "rule-id-with-special-chars" {
		t.Fatalf("normalizeRuleToken(%q) = %q, want %q", "rule id / with\\special*chars?", got, "rule-id-with-special-chars")
	}
	if got := normalizeRuleToken("unicodé-✓"); got != "unicod" {
		t.Fatalf("normalizeRuleToken(%q) = %q, want %q", "unicodé-✓", got, "unicod")
	}
}

func sampleSARIFRuntimeAndBaselineReport() Report {
	wasteIncrease := 1.5
	baselineLoads := 0
	currentLoads := 4
	loadDelta := 4
	return Report{
		Dependencies: []DependencyReport{
			{
				Language: "go",
				Name:     "github.com/acme/pkg",
				UnusedExports: []SymbolRef{
					{Name: "Unused", Module: "github.com/acme/pkg"},
				},
				RuntimeUsage: &RuntimeUsage{
					LoadCount:   4,
					Correlation: RuntimeCorrelationOverlap,
					RuntimeOnly: true,
					Modules: []RuntimeModuleUsage{
						{Module: "pkg/runtime", Count: 4},
					},
					ParentModules: []RuntimeModuleUsage{
						{Module: "cmd/api", Count: 2},
					},
					Entrypoints: []RuntimeModuleUsage{
						{Module: "cmd/server", Count: 1},
					},
					TopSymbols: []RuntimeSymbolUsage{
						{Symbol: "Serve", Module: "pkg/runtime", Count: 3},
					},
				},
			},
		},
		WasteIncreasePercent: &wasteIncrease,
		BaselineComparison: &BaselineComparison{
			BaselineKey: "base",
			CurrentKey:  "head",
			SummaryDelta: SummaryDelta{
				DependencyCountDelta: 1,
			},
			Dependencies: []DependencyDelta{
				{
					Kind:                      DependencyDeltaChanged,
					Language:                  "go",
					Name:                      "github.com/acme/pkg",
					UsedExportsCountDelta:     -1,
					TotalExportsCountDelta:    0,
					UsedPercentDelta:          -10,
					EstimatedUnusedBytesDelta: 512,
					WastePercentDelta:         10,
					RuntimeDelta: &RuntimeDelta{
						Comparable:            true,
						BaselinePresent:       true,
						CurrentPresent:        true,
						BaselineLoadCount:     &baselineLoads,
						CurrentLoadCount:      &currentLoads,
						LoadCountDelta:        &loadDelta,
						BaselineCorrelation:   RuntimeCorrelationStaticOnly,
						CurrentCorrelation:    RuntimeCorrelationOverlap,
						NewRuntimeLoads:       true,
						RuntimeOnlyRegression: true,
					},
					DeniedIntroduced: true,
				},
			},
		},
	}
}

func mustSARIFLog(t *testing.T, output string) sarifLog {
	t.Helper()

	var payload sarifLog
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		t.Fatalf(errParseSARIFOutput, err)
	}
	if len(payload.Runs) != 1 {
		t.Fatalf(errExpectedOneRun, len(payload.Runs))
	}
	if len(payload.Runs[0].Results) == 0 {
		t.Fatalf("expected sarif results, got %#v", payload.Runs)
	}
	return payload
}

func mustSARIFProperties(t *testing.T, results []sarifResult) (map[string]any, map[string]any) {
	t.Helper()

	var dependencyProps map[string]any
	var wasteProps map[string]any
	for _, result := range results {
		switch result.RuleID {
		case "lopper/waste/unused-export":
			dependencyProps = result.Properties
		case "lopper/waste/increase":
			wasteProps = result.Properties
		}
	}
	if dependencyProps == nil {
		t.Fatalf("expected unused-export sarif result, got %#v", results)
	}
	if wasteProps == nil {
		t.Fatalf("expected waste-increase sarif result")
	}
	return dependencyProps, wasteProps
}

func assertSARIFRuntimeProperties(t *testing.T, runtimeValue any) {
	t.Helper()

	runtimeProps, ok := runtimeValue.(map[string]any)
	if !ok {
		t.Fatalf("expected runtime properties, got %#v", runtimeValue)
	}
	for _, key := range []string{"modules", "parentModules", "entrypoints", "topSymbols"} {
		if values, ok := runtimeProps[key].([]any); !ok || len(values) != 1 {
			t.Fatalf("expected one runtime %s entry, got %#v", key, runtimeProps[key])
		}
	}
}

func assertSARIFBaselineContext(t *testing.T, baselineValue any, wantDeniedIntroduced bool) {
	t.Helper()

	baselineContext, ok := baselineValue.(map[string]any)
	if !ok {
		t.Fatalf("expected baseline context, got %#v", baselineValue)
	}
	if baselineContext["deniedIntroduced"] != wantDeniedIntroduced {
		t.Fatalf("expected deniedIntroduced=%v, got %#v", wantDeniedIntroduced, baselineContext["deniedIntroduced"])
	}
	if wantDeniedIntroduced {
		runtimeDelta, ok := baselineContext["runtimeDelta"].(map[string]any)
		if !ok || runtimeDelta["newRuntimeLoads"] != true {
			t.Fatalf("expected runtime delta context, got %#v", baselineContext["runtimeDelta"])
		}
	}
}

func assertSARIFBaselineContextPresent(t *testing.T, baselineValue any) {
	t.Helper()

	if _, ok := baselineValue.(map[string]any); !ok {
		t.Fatalf("expected baseline context, got %#v", baselineValue)
	}
}

func TestSARIFLevelMappings(t *testing.T) {
	testLevelMapping(t, "severity", severityToSARIFLevel, []levelMapping{
		{input: "critical", want: "error"},
		{input: "high", want: "error"},
		{input: "medium", want: "warning"},
		{input: "low", want: "note"},
		{input: "", want: "note"},
	})
	testLevelMapping(t, "priority", priorityToSARIFLevel, []levelMapping{
		{input: "critical", want: "warning"},
		{input: "high", want: "warning"},
		{input: "medium", want: "note"},
		{input: "low", want: "note"},
		{input: "", want: "note"},
	})
}

type levelMapping struct {
	input string
	want  string
}

func testLevelMapping(t *testing.T, name string, fn func(string) string, cases []levelMapping) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		for _, tc := range cases {
			if got := fn(tc.input); got != tc.want {
				t.Fatalf("%s(%q) = %q, want %q", name, tc.input, got, tc.want)
			}
		}
	})
}

func TestToSARIFLocationsEdgeCases(t *testing.T) {
	locations := []Location{
		{File: "", Line: 1, Column: 1},
		{File: testFileGo, Line: 0, Column: 0},
		{File: testFileGo, Line: -1, Column: -5},
	}
	got := toSARIFLocations(locations)
	if len(got) != 1 {
		t.Fatalf("expected 1 valid location, got %d", len(got))
	}
	for _, location := range got {
		if location.PhysicalLocation.ArtifactLocation.URI == "" {
			t.Fatalf("expected location URI to be set")
		}
	}
}

func TestToSARIFLocationsDeduplicates(t *testing.T) {
	locations := []Location{
		{File: testFileGo, Line: 10, Column: 5},
		{File: testFileGo, Line: 10, Column: 5},
	}
	got := toSARIFLocations(locations)
	if len(got) != 1 {
		t.Fatalf("expected duplicate locations to be deduplicated, got %d", len(got))
	}
}

func TestToSARIFLocationsDeduplicatesNormalizedPaths(t *testing.T) {
	locations := []Location{
		{File: "./pkg/file.go", Line: 10, Column: 5},
		{File: "pkg/file.go", Line: 10, Column: 5},
		{File: "pkg/sub/../file.go", Line: 10, Column: 5},
	}
	got := toSARIFLocations(locations)
	if len(got) != 1 {
		t.Fatalf("expected normalized duplicate locations to be deduplicated, got %d", len(got))
	}
}

func TestToSARIFLocationWindowsAbsolutePathUsesFileURI(t *testing.T) {
	got, ok := toSARIFLocation(Location{File: `C:\tmp\sarif schema.json`, Line: 7, Column: 3})
	if !ok {
		t.Fatalf("expected valid sarif location")
	}
	if got.PhysicalLocation.ArtifactLocation.URI != "file:///C:/tmp/sarif%20schema.json" {
		t.Fatalf("unexpected windows sarif uri: %q", got.PhysicalLocation.ArtifactLocation.URI)
	}
	if got.PhysicalLocation.Region == nil || got.PhysicalLocation.Region.StartLine != 7 || got.PhysicalLocation.Region.StartColumn != 3 {
		t.Fatalf("expected region to be preserved, got %#v", got.PhysicalLocation.Region)
	}
}

func TestToSARIFLocationsNilAndOnlyInvalid(t *testing.T) {
	if got := toSARIFLocations(nil); len(got) != 0 {
		t.Fatalf("expected nil for nil input, got %v", got)
	}
	if got := toSARIFLocations([]Location{{File: "   "}}); len(got) != 0 {
		t.Fatalf("expected nil for invalid-only input, got %v", got)
	}
}

func TestSortSARIFResultsAndLocationKey(t *testing.T) {
	results := []sarifResult{
		{RuleID: "b", Message: sarifMessage{Text: "z"}},
		{
			RuleID:  "a",
			Message: sarifMessage{Text: "m"},
			Locations: []sarifLocation{
				{
					PhysicalLocation: sarifPhysicalLocation{
						ArtifactLocation: sarifArtifactLocation{URI: "z.go"},
						Region:           &sarifRegion{StartLine: 2, StartColumn: 3},
					},
				},
			},
		},
		{
			RuleID:  "a",
			Message: sarifMessage{Text: "m"},
			Locations: []sarifLocation{
				{
					PhysicalLocation: sarifPhysicalLocation{
						ArtifactLocation: sarifArtifactLocation{URI: "a.go"},
						Region:           &sarifRegion{StartLine: 1, StartColumn: 1},
					},
				},
			},
		},
		{RuleID: "a", Message: sarifMessage{Text: "a"}},
	}

	sortSARIFResults(results)

	if results[0].RuleID != "a" || results[0].Message.Text != "a" {
		t.Fatalf("expected first result sorted by rule/message")
	}
	if results[1].Locations[0].PhysicalLocation.ArtifactLocation.URI != "a.go" {
		t.Fatalf("expected second result sorted by location URI")
	}
	if results[2].Locations[0].PhysicalLocation.ArtifactLocation.URI != "z.go" {
		t.Fatalf("expected third result sorted by location URI")
	}
	if results[3].RuleID != "b" {
		t.Fatalf("expected last result to have highest rule id")
	}

	if got := resultLocationKey(sarifResult{}); got != "" {
		t.Fatalf("expected empty location key for no locations, got %q", got)
	}
}

func TestSARIFRuleBuilderDeduplicatesAndSorts(t *testing.T) {
	builder := newSARIFRuleBuilder()
	builder.add(sarifRule{ID: "b"})
	builder.add(sarifRule{ID: "a"})
	builder.add(sarifRule{ID: "a"})

	items := builder.list()
	if len(items) != 2 {
		t.Fatalf("expected duplicate rules to be ignored, got %#v", items)
	}
	if items[0].ID != "a" || items[1].ID != "b" {
		t.Fatalf("expected rules to be sorted by ID, got %#v", items)
	}
}

func TestFormatSARIFMarshalError(t *testing.T) {
	waste := math.NaN()
	if _, err := formatSARIF(Report{WasteIncreasePercent: &waste}); err == nil {
		t.Fatalf("expected sarif formatting to fail for NaN payload values")
	}
}

func TestAppendUnusedImportResultsFallsBackToAnchor(t *testing.T) {
	rules := newSARIFRuleBuilder()
	anchor := &sarifLocation{
		PhysicalLocation: sarifPhysicalLocation{
			ArtifactLocation: sarifArtifactLocation{URI: "anchor.go"},
		},
	}

	results := appendUnusedImportResults(nil, rules, DependencyReport{Name: "pkg", Language: "js-ts", UnusedImports: []ImportUse{{Name: "map", Module: "lodash"}}}, anchor, nil)
	if len(results) != 1 || len(results[0].Locations) != 1 {
		t.Fatalf("expected fallback anchor location, got %#v", results)
	}
	if results[0].Locations[0].PhysicalLocation.ArtifactLocation.URI != "anchor.go" {
		t.Fatalf("expected anchor URI to be preserved, got %#v", results[0].Locations[0])
	}
}

func TestDependencyAnchorLocationBranches(t *testing.T) {
	if anchor := dependencyAnchorLocation(DependencyReport{}); anchor != nil {
		t.Fatalf("expected nil anchor without locations, got %#v", anchor)
	}

	dep := DependencyReport{
		UsedImports: []ImportUse{
			{Locations: []Location{{File: " ", Line: 1, Column: 1}}},
		},
	}
	if anchor := dependencyAnchorLocation(dep); anchor != nil {
		t.Fatalf("expected nil anchor when first location is invalid, got %#v", anchor)
	}

	dep = DependencyReport{
		UsedImports: []ImportUse{
			{Locations: []Location{{File: "b/file.go", Line: 2, Column: 2}}},
		},
		UnusedImports: []ImportUse{
			{Locations: []Location{{File: testAFileGo, Line: 3, Column: 1}}},
		},
	}
	anchor := dependencyAnchorLocation(dep)
	if anchor == nil {
		t.Fatalf("expected non-nil anchor")
		return
	}
	if anchor.PhysicalLocation.ArtifactLocation.URI != testAFileGo {
		t.Fatalf("expected sorted anchor path, got %#v", anchor)
	}
}

func TestDependencyAnchorLocationColumnTieBreakAndNormalizeRuleFallback(t *testing.T) {
	dep := DependencyReport{
		UsedImports: []ImportUse{
			{Locations: []Location{{File: "same.go", Line: 3, Column: 2}}},
		},
		UnusedImports: []ImportUse{
			{Locations: []Location{{File: "same.go", Line: 3, Column: 1}}},
		},
	}
	anchor := dependencyAnchorLocation(dep)
	if anchor == nil || anchor.PhysicalLocation.Region == nil || anchor.PhysicalLocation.Region.StartColumn != 1 {
		t.Fatalf("expected column tie-break to prefer the earliest column, got %#v", anchor)
	}

	if got := normalizeRuleToken("!!!"); got != "unknown" {
		t.Fatalf("expected punctuation-only rule token to normalize to unknown, got %q", got)
	}
}

func TestToSARIFLocationsSortWithAndWithoutRegions(t *testing.T) {
	locations := []Location{
		{File: "z/file.go", Line: 10, Column: 3},
		{File: testAFileGo, Line: 0, Column: 0},
		{File: testAFileGo, Line: 5, Column: 2},
		{File: testAFileGo, Line: 5, Column: 1},
	}
	got := toSARIFLocations(locations)
	if len(got) != 4 {
		t.Fatalf("expected 4 locations, got %d", len(got))
	}
	if got[0].PhysicalLocation.ArtifactLocation.URI != testAFileGo || got[0].PhysicalLocation.Region != nil {
		t.Fatalf("expected region-less a/file.go first, got %#v", got[0])
	}
	if got[1].PhysicalLocation.Region == nil || got[1].PhysicalLocation.Region.StartColumn != 1 {
		t.Fatalf("expected second location sorted by column, got %#v", got[1])
	}
	if got[2].PhysicalLocation.Region == nil || got[2].PhysicalLocation.Region.StartColumn != 2 {
		t.Fatalf("expected third location sorted by column, got %#v", got[2])
	}
}
