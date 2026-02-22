package report

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

const (
	errParseSARIFOutput = "parse sarif output: %v"
	errExpectedOneRun   = "expected one run, got %d"
	testFileGo          = "file.go"
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
	if len(got) != 2 {
		t.Fatalf("expected 2 valid locations, got %d", len(got))
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
