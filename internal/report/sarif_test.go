package report

import (
	"encoding/json"
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

func TestNormalizeRuleToken(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "empty", input: "", want: "unknown"},
		{name: "special chars", input: "rule id / with\\special*chars?", want: "rule-id-with-special-chars"},
		{name: "unicode", input: "unicodé-✓", want: "unicod"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeRuleToken(tc.input); got != tc.want {
				t.Fatalf("normalizeRuleToken(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
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

func TestToSARIFLocationsNilAndOnlyInvalid(t *testing.T) {
	if got := toSARIFLocations(nil); got != nil {
		t.Fatalf("expected nil for nil input, got %v", got)
	}
	if got := toSARIFLocations([]Location{{File: "   "}}); got != nil {
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
			{Locations: []Location{{File: "a/file.go", Line: 3, Column: 1}}},
		},
	}
	anchor := dependencyAnchorLocation(dep)
	if anchor == nil {
		t.Fatalf("expected non-nil anchor")
	}
	if anchor.PhysicalLocation.ArtifactLocation.URI != "a/file.go" {
		t.Fatalf("expected sorted anchor path, got %#v", anchor)
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
