package dashboard

import (
	"bytes"
	"encoding/csv"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ben-ranford/lopper/internal/report"
)

func TestDashboardAdditionalHelperBranches(t *testing.T) {
	if !hasWasteCandidateRecommendation([]report.Recommendation{
		{Code: " "},
		{Code: "low-usage-risk"},
	}) {
		t.Fatalf("expected low-usage recommendation code to count as waste candidate")
	}
	if hasWasteCandidateRecommendation([]report.Recommendation{{Code: "keep-current"}}) {
		t.Fatalf("did not expect non-removal recommendation to count as waste candidate")
	}

	var buffer bytes.Buffer
	writer := csv.NewWriter(&buffer)
	if err := writeDashboardCrossRepoRowsCSV(writer, nil); err != nil {
		t.Fatalf("expected empty cross-repo dependency set to be ignored, got %v", err)
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		t.Fatalf("flush empty cross-repo rows: %v", err)
	}
	if buffer.Len() != 0 {
		t.Fatalf("expected no CSV output for empty cross-repo dependencies, got %q", buffer.String())
	}
}

func TestLoadConfigMissingFile(t *testing.T) {
	_, err := LoadConfig(filepath.Join(t.TempDir(), "missing-dashboard.yml"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected missing config file error, got %v", err)
	}
}
