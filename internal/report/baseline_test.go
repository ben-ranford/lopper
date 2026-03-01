package report

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
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
	if _, err := ParseFormat("pr-comment"); err != nil {
		t.Fatalf("parse pr-comment format: %v", err)
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

	_, err = ApplyBaseline(Report{Dependencies: []DependencyReport{{Name: "dep", UsedExportsCount: 1, TotalExportsCount: 2}}}, Report{Summary: &Summary{DependencyCount: 1, UsedExportsCount: 0, TotalExportsCount: 0, UsedPercent: 0}})
	if err == nil || !strings.Contains(err.Error(), "baseline total exports count is zero") {
		t.Fatalf("expected baseline zero-total error, got %v", err)
	}
}

func TestApplyBaselineCurrentWithoutTotalsError(t *testing.T) {
	_, err := ApplyBaseline(Report{Dependencies: []DependencyReport{{Name: "dep", UsedExportsCount: 0, TotalExportsCount: 0}}}, Report{Dependencies: []DependencyReport{{Name: "dep", UsedExportsCount: 1, TotalExportsCount: 1}}})
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

func TestSaveSnapshotAndLoadWithKey(t *testing.T) {
	now := time.Date(2026, time.February, 22, 10, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	reportData := Report{
		SchemaVersion: "0.1.0",
		RepoPath:      ".",
		Dependencies: []DependencyReport{
			{Name: "dep-a", Language: "js-ts", UsedExportsCount: 1, TotalExportsCount: 4, UsedPercent: 25},
		},
	}
	path, err := SaveSnapshot(dir, "label:weekly", reportData, now)
	if err != nil {
		t.Fatalf("save snapshot: %v", err)
	}
	if !strings.HasSuffix(path, ".json") {
		t.Fatalf("expected snapshot path to be json, got %q", path)
	}

	rep, key, err := LoadWithKey(path)
	if err != nil {
		t.Fatalf("load with key: %v", err)
	}
	if key != "label:weekly" {
		t.Fatalf("expected saved key, got %q", key)
	}
	if rep.Summary == nil || rep.Summary.DependencyCount != 1 {
		t.Fatalf("expected computed summary in loaded report, got %#v", rep.Summary)
	}

	_, err = SaveSnapshot(dir, "label:weekly", Report{RepoPath: "."}, now)
	if err == nil || !strings.Contains(err.Error(), ErrBaselineAlreadyExists.Error()) {
		t.Fatalf("expected immutable snapshot exists error, got %v", err)
	}
}

func TestLoadWithKeySupportsLegacyReportFile(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "legacy.json")
	content := `{"schemaVersion":"0.1.0","generatedAt":"2026-01-01T00:00:00Z","repoPath":".","dependencies":[{"language":"js-ts","name":"dep","usedExportsCount":1,"totalExportsCount":2,"usedPercent":50,"estimatedUnusedBytes":0}]}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write legacy report: %v", err)
	}
	rep, key, err := LoadWithKey(path)
	if err != nil {
		t.Fatalf("load legacy report: %v", err)
	}
	if key != "" {
		t.Fatalf("expected empty key for legacy report, got %q", key)
	}
	if rep.Summary == nil || rep.Summary.TotalExportsCount != 2 {
		t.Fatalf("expected computed summary from legacy report, got %#v", rep.Summary)
	}
}

func TestComputeBaselineComparisonDeterministic(t *testing.T) {
	current := Report{
		Dependencies: []DependencyReport{
			{Name: "b", Language: "js-ts", UsedExportsCount: 1, TotalExportsCount: 4, UsedPercent: 25, EstimatedUnusedBytes: 100},
			{Name: "a", Language: "go", UsedExportsCount: 3, TotalExportsCount: 3, UsedPercent: 100, EstimatedUnusedBytes: 0},
		},
	}
	baseline := Report{
		Dependencies: []DependencyReport{
			{Name: "b", Language: "js-ts", UsedExportsCount: 2, TotalExportsCount: 4, UsedPercent: 50, EstimatedUnusedBytes: 50},
			{Name: "c", Language: "python", UsedExportsCount: 1, TotalExportsCount: 2, UsedPercent: 50, EstimatedUnusedBytes: 10},
		},
	}

	comparison := ComputeBaselineComparison(current, baseline)
	gotOrder := make([]string, 0, len(comparison.Dependencies))
	for _, dep := range comparison.Dependencies {
		gotOrder = append(gotOrder, dep.Language+"/"+dep.Name)
	}
	wantOrder := []string{"go/a", "js-ts/b", "python/c"}
	if !slices.Equal(gotOrder, wantOrder) {
		t.Fatalf("unexpected deterministic delta ordering: got=%v want=%v", gotOrder, wantOrder)
	}
	if len(comparison.Added) != 1 || comparison.Added[0].Name != "a" {
		t.Fatalf("expected one added dependency, got %#v", comparison.Added)
	}
	if len(comparison.Removed) != 1 || comparison.Removed[0].Name != "c" {
		t.Fatalf("expected one removed dependency, got %#v", comparison.Removed)
	}
	if len(comparison.Regressions) != 1 || comparison.Regressions[0].Name != "b" {
		t.Fatalf("expected one regression dependency, got %#v", comparison.Regressions)
	}
	if len(comparison.Progressions) != 1 || comparison.Progressions[0].Name != "c" {
		t.Fatalf("expected one progression dependency, got %#v", comparison.Progressions)
	}
}

func TestLoadWithKeyUnsupportedSnapshotSchema(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "snapshot.json")
	content := `{"baselineSchemaVersion":"9.9.9","key":"label:bad","savedAt":"2026-01-01T00:00:00Z","report":{"schemaVersion":"0.1.0","generatedAt":"2026-01-01T00:00:00Z","repoPath":".","dependencies":[]}}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}
	if _, _, err := LoadWithKey(path); err == nil || !strings.Contains(err.Error(), "unsupported baseline schema version") {
		t.Fatalf("expected unsupported schema error, got %v", err)
	}
}

func TestSaveSnapshotValidationErrors(t *testing.T) {
	now := time.Date(2026, time.February, 22, 10, 0, 0, 0, time.UTC)
	if _, err := SaveSnapshot("", "label:x", Report{}, now); err == nil || !strings.Contains(err.Error(), "baseline store directory is required") {
		t.Fatalf("expected missing directory validation error, got %v", err)
	}
	if _, err := SaveSnapshot(t.TempDir(), "  ", Report{}, now); err == nil || !strings.Contains(err.Error(), "baseline key is required") {
		t.Fatalf("expected missing key validation error, got %v", err)
	}
}

func TestBaselineSnapshotPathSanitizesKey(t *testing.T) {
	path := BaselineSnapshotPath("/tmp/baselines", " label:release candidate/1 ")
	if !strings.HasSuffix(path, "label_release_candidate_1.json") {
		t.Fatalf("expected sanitized snapshot path, got %q", path)
	}
}

func TestSaveSnapshotMkdirFailure(t *testing.T) {
	now := time.Date(2026, time.February, 22, 10, 0, 0, 0, time.UTC)
	root := t.TempDir()
	blocking := filepath.Join(root, "blocked")
	if err := os.WriteFile(blocking, []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocking file: %v", err)
	}
	if _, err := SaveSnapshot(filepath.Join(blocking, "nested"), "label:x", Report{}, now); err == nil {
		t.Fatalf("expected mkdir failure when parent is a file")
	}
}

func TestSaveSnapshotSortsDependenciesDeterministically(t *testing.T) {
	now := time.Date(2026, time.February, 22, 10, 0, 0, 0, time.UTC)
	reportData := Report{
		Dependencies: []DependencyReport{
			{Name: "zeta", Language: "python"},
			{Name: "alpha", Language: "go"},
			{Name: "beta", Language: "go"},
		},
	}
	path, err := SaveSnapshot(t.TempDir(), "label:sorted", reportData, now)
	if err != nil {
		t.Fatalf("save snapshot: %v", err)
	}
	rep, _, err := LoadWithKey(path)
	if err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	gotOrder := []string{
		rep.Dependencies[0].Language + "/" + rep.Dependencies[0].Name,
		rep.Dependencies[1].Language + "/" + rep.Dependencies[1].Name,
		rep.Dependencies[2].Language + "/" + rep.Dependencies[2].Name,
	}
	wantOrder := []string{"go/alpha", "go/beta", "python/zeta"}
	if !slices.Equal(gotOrder, wantOrder) {
		t.Fatalf("unexpected dependency order: got=%v want=%v", gotOrder, wantOrder)
	}
}

func TestComputeSummaryAndLanguageBreakdownEmpty(t *testing.T) {
	if got := ComputeSummary(nil); got != nil {
		t.Fatalf("expected nil summary for empty dependencies, got %#v", got)
	}
	if got := ComputeLanguageBreakdown(nil); len(got) != 0 {
		t.Fatalf("expected nil language breakdown for empty dependencies, got %#v", got)
	}
}
