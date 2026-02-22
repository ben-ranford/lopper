package report

import (
	"os"
	"path/filepath"
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

func TestComputeLanguageBreakdown(t *testing.T) {
	dependencies := []DependencyReport{
		{Language: "js-ts", Name: "lodash", UsedExportsCount: 2, TotalExportsCount: 4},
		{Language: "python", Name: "requests", UsedExportsCount: 1, TotalExportsCount: 2},
		{Language: "js-ts", Name: "react", UsedExportsCount: 1, TotalExportsCount: 2},
	}

	breakdown := ComputeLanguageBreakdown(dependencies)
	if len(breakdown) != 2 {
		t.Fatalf("expected two language summaries, got %d", len(breakdown))
	}
	if breakdown[0].Language != "js-ts" || breakdown[0].DependencyCount != 2 {
		t.Fatalf("unexpected js-ts breakdown: %#v", breakdown[0])
	}
	if breakdown[1].Language != "python" || breakdown[1].DependencyCount != 1 {
		t.Fatalf("unexpected python breakdown: %#v", breakdown[1])
	}
}

func TestLoadAndParseFormat(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "report.json")
	content := `{"schemaVersion":"0.1.0","generatedAt":"2026-01-01T00:00:00Z","repoPath":".","dependencies":[]}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write report: %v", err)
	}
	if _, err := Load(path); err != nil {
		t.Fatalf("load report: %v", err)
	}
	if _, err := Load(filepath.Join(tmp, "missing.json")); err == nil {
		t.Fatalf("expected load error for missing file")
	}

	if _, err := ParseFormat("table"); err != nil {
		t.Fatalf("parse table format: %v", err)
	}
	if _, err := ParseFormat("json"); err != nil {
		t.Fatalf("parse json format: %v", err)
	}
	if _, err := ParseFormat("sarif"); err != nil {
		t.Fatalf("parse sarif format: %v", err)
	}
	if _, err := ParseFormat("nope"); err == nil {
		t.Fatalf("expected unknown format error")
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
	if err != ErrBaselineMissing {
		t.Fatalf("expected ErrBaselineMissing, got %v", err)
	}

	_, err = ApplyBaseline(
		Report{Dependencies: []DependencyReport{{Name: "dep", UsedExportsCount: 1, TotalExportsCount: 2}}},
		Report{Summary: &Summary{DependencyCount: 1, UsedExportsCount: 0, TotalExportsCount: 0, UsedPercent: 0}},
	)
	if err == nil || !strings.Contains(err.Error(), "baseline total exports count is zero") {
		t.Fatalf("expected baseline zero-total error, got %v", err)
	}
}

func TestApplyBaselineCurrentWithoutTotalsError(t *testing.T) {
	_, err := ApplyBaseline(
		Report{Dependencies: []DependencyReport{{Name: "dep", UsedExportsCount: 0, TotalExportsCount: 0}}},
		Report{Dependencies: []DependencyReport{{Name: "dep", UsedExportsCount: 1, TotalExportsCount: 1}}},
	)
	if err == nil || !strings.Contains(err.Error(), "current report has no export totals") {
		t.Fatalf("expected current report totals error, got %v", err)
	}
}

func TestLoadInvalidJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatalf("write bad json: %v", err)
	}
	if _, err := Load(path); err == nil {
		t.Fatalf("expected load parse error for invalid JSON")
	}
}

func TestComputeSummaryAndLanguageBreakdownEmpty(t *testing.T) {
	if got := ComputeSummary(nil); got != nil {
		t.Fatalf("expected nil summary for empty dependencies, got %#v", got)
	}
	if got := ComputeLanguageBreakdown(nil); got != nil {
		t.Fatalf("expected nil language breakdown for empty dependencies, got %#v", got)
	}
}
