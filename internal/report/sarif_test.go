package report

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFormatSARIFGolden(t *testing.T) {
	wasteIncrease := 2.5
	reportData := Report{
		SchemaVersion: "0.1.0",
		Dependencies: []DependencyReport{
			{
				Language: "js-ts",
				Name:     "lodash",
				UsedImports: []ImportUse{
					{
						Name:   "map",
						Module: "lodash/map",
						Locations: []Location{
							{File: "src/main.ts", Line: 12, Column: 4},
						},
					},
				},
				UnusedImports: []ImportUse{
					{
						Name:   "debounce",
						Module: "lodash",
						Locations: []Location{
							{File: "src/main.ts", Line: 3, Column: 1},
						},
					},
				},
				UnusedExports: []SymbolRef{
					{Name: "omit", Module: "lodash"},
				},
				RiskCues: []RiskCue{
					{Code: "dynamic-loader", Severity: "medium", Message: "dynamic module loading detected"},
				},
				Recommendations: []Recommendation{
					{Code: "prefer-subpath-imports", Priority: "high", Message: "switch to subpath imports"},
				},
			},
		},
		WasteIncreasePercent: &wasteIncrease,
	}

	output, err := NewFormatter().Format(reportData, FormatSARIF)
	if err != nil {
		t.Fatalf("format sarif: %v", err)
	}

	goldenPath := filepath.Join("..", "..", "testdata", "report", "sarif.golden")
	golden, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden file: %v", err)
	}

	if strings.TrimSpace(output) != strings.TrimSpace(string(golden)) {
		t.Fatalf("sarif output did not match golden")
	}
}
