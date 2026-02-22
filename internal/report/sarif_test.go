package report

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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

	if strings.TrimSpace(output) != strings.TrimSpace(string(golden)) {
		t.Fatalf("sarif output did not match golden")
	}
}
