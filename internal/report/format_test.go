package report

import (
	"strings"
	"testing"
)

func TestFormatTable(t *testing.T) {
	reportData := Report{
		Dependencies: []DependencyReport{
			{
				Name:                 "lodash",
				UsedExportsCount:     2,
				TotalExportsCount:    10,
				EstimatedUnusedBytes: 1024,
				TopUsedSymbols: []SymbolUsage{
					{Name: "map", Count: 3},
				},
			},
		},
	}

	output, err := NewFormatter().Format(reportData, FormatTable)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(output, "lodash") {
		t.Fatalf("expected output to include dependency name")
	}
	if !strings.Contains(output, "map") {
		t.Fatalf("expected output to include top symbol")
	}
}

func TestFormatJSON(t *testing.T) {
	reportData := Report{RepoPath: "."}
	output, err := NewFormatter().Format(reportData, FormatJSON)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(output, "repoPath") {
		t.Fatalf("expected json output to include repoPath")
	}
}
