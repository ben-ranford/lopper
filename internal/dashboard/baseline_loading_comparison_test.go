package dashboard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDashboardBaselineLoadBranches(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	snapshot := Report{
		GeneratedAt: time.Date(2026, time.March, 10, 0, 0, 0, 0, time.UTC),
		Repos: []RepoResult{
			{Name: "web", Path: "./web", DependencyCount: 2, WasteCandidateCount: 1, CriticalCVEs: 1},
		},
	}
	path, err := SaveSnapshot(tmp, " label:load ", snapshot, time.Date(2026, time.March, 11, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("SaveSnapshot() error = %v", err)
	}
	if loaded, err := Load(path); err != nil || loaded.Summary.TotalRepos != 1 {
		t.Fatalf("Load() = summary=%+v err=%v", loaded.Summary, err)
	}
	if got := BaselineSnapshotPath(tmp, " label:load "); got != path {
		t.Fatalf("BaselineSnapshotPath() = %q, want %q", got, path)
	}

	legacyPath := filepath.Join(tmp, "legacy.json")
	legacyJSON := `{"generated_at":"2026-03-12T00:00:00Z","repos":[{"name":"api","path":"./api","dependency_count":3,"waste_candidate_count":2,"critical_cves":1}]}`
	if err := os.WriteFile(legacyPath, []byte(legacyJSON), 0o600); err != nil {
		t.Fatalf("write legacy dashboard report: %v", err)
	}
	legacy, key, err := LoadWithKey(legacyPath)
	if err != nil {
		t.Fatalf("LoadWithKey(legacy) error = %v", err)
	}
	if key != "" || legacy.Summary.TotalRepos != 1 || legacy.Summary.TotalDeps != 3 || legacy.Summary.CriticalCVEs != 1 {
		t.Fatalf("unexpected legacy load result: key=%q summary=%+v", key, legacy.Summary)
	}

	badPath := filepath.Join(tmp, "bad-schema.json")
	badJSON := `{"baselineSchemaVersion":"9.9.9","key":"label:bad","report":{"repos":[]}}`
	if err := os.WriteFile(badPath, []byte(badJSON), 0o600); err != nil {
		t.Fatalf("write bad dashboard snapshot: %v", err)
	}
	if _, _, err := LoadWithKey(badPath); err == nil || !strings.Contains(err.Error(), "unsupported dashboard baseline schema version") {
		t.Fatalf("expected unsupported schema error, got %v", err)
	}

	if _, err := Load(filepath.Join(tmp, "missing.json")); err == nil {
		t.Fatalf("expected Load to return missing file error")
	}
	invalidPath := filepath.Join(tmp, "invalid.json")
	if err := os.WriteFile(invalidPath, []byte("{"), 0o600); err != nil {
		t.Fatalf("write invalid dashboard report: %v", err)
	}
	if _, _, err := LoadWithKey(invalidPath); err == nil {
		t.Fatalf("expected invalid dashboard json error")
	}
}

func TestDashboardBaselineComparisonBranches(t *testing.T) {
	t.Parallel()

	current := Report{
		Repos: []RepoResult{
			{Name: "api", Path: "./api", DependencyCount: 2, WasteCandidateCount: 1, WasteCandidatePercent: 50, CriticalCVEs: 1, DeniedLicenseCount: 1, Error: "current"},
			{Name: "same", Path: "./same", DependencyCount: 1},
		},
	}
	baseline := Report{
		Repos: []RepoResult{
			{Name: "api", Path: "./api", DependencyCount: 1, WasteCandidateCount: 0, WasteCandidatePercent: 0, CriticalCVEs: 0, DeniedLicenseCount: 0, Error: "baseline"},
			{Name: "same", Path: "./same", DependencyCount: 1},
			{Name: "old", Path: "./old", DependencyCount: 4, WasteCandidateCount: 2, WasteCandidatePercent: 50, CriticalCVEs: 1, DeniedLicenseCount: 1, Error: "gone"},
		},
	}

	comparison := ComputeBaselineComparison(current, baseline)
	if len(comparison.Changed) != 1 || comparison.Changed[0].Name != "api" {
		t.Fatalf("expected api changed delta, got %#v", comparison.Changed)
	}
	if len(comparison.Removed) != 1 || comparison.Removed[0].Name != "old" {
		t.Fatalf("expected old removed delta, got %#v", comparison.Removed)
	}
	if len(comparison.RepoDeltas) != 2 {
		t.Fatalf("expected unchanged repo to be omitted, got %#v", comparison.RepoDeltas)
	}
}

func TestDashboardBaselineSnapshotSortsSameNameByPath(t *testing.T) {
	t.Parallel()

	reportData := Report{
		Repos: []RepoResult{
			{Name: "api", Path: "./z"},
			{Name: "api", Path: "./a"},
		},
	}
	path, err := SaveSnapshot(t.TempDir(), "label:sorted", reportData, time.Date(2026, time.March, 11, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("SaveSnapshot() error = %v", err)
	}
	loaded, _, err := LoadWithKey(path)
	if err != nil {
		t.Fatalf("LoadWithKey() error = %v", err)
	}
	if len(loaded.Repos) != 2 || loaded.Repos[0].Path != "./a" || loaded.Repos[1].Path != "./z" {
		t.Fatalf("expected repos sorted by name then path, got %#v", loaded.Repos)
	}
}

func TestDashboardBaselineCSVAndHTMLRendering(t *testing.T) {
	t.Parallel()

	reportData := Report{
		GeneratedAt: time.Date(2026, time.March, 12, 0, 0, 0, 0, time.UTC),
		Repos: []RepoResult{
			{Name: "api", Path: "./api", Language: "go", DependencyCount: 2, WasteCandidateCount: 1, WasteCandidatePercent: 50, TopRiskSeverity: "critical", CriticalCVEs: 1, DeniedLicenseCount: 1, Error: "<err>"},
		},
		CrossRepoDeps: []CrossRepoDependency{
			{Name: "shared", Count: 3, Repositories: []string{"api", "web", "worker"}},
		},
		Summary: Summary{TotalRepos: 1, TotalDeps: 2, TotalWasteCandidates: 1, CrossRepoDuplicates: 1, CriticalCVEs: 1},
	}
	reportData.BaselineComparison = newBaselineComparison("label:base", "commit:head", SummaryDelta{TotalReposDelta: 1, TotalDepsDelta: 2, TotalWasteCandidatesDelta: 1, CrossRepoDuplicatesDelta: 1, CriticalCVEsDelta: 1}, RepoDelta{Kind: RepoDeltaChanged, Name: "api", Path: "./api", DependencyCountDelta: 1, WasteCandidateCountDelta: 1, WasteCandidatePercentDelta: 50, CriticalCVEsDelta: 1, DeniedLicenseCountDelta: 1, CurrentError: "<current>", BaselineError: "<baseline>"})

	csvOutput, err := FormatReport(reportData, FormatCSV)
	if err != nil {
		t.Fatalf("FormatReport(csv) error = %v", err)
	}
	for _, want := range []string{"baseline_key,label:base", "current_key,commit:head", "repo_name,repo_path,kind", "api,./api,changed"} {
		if !strings.Contains(csvOutput, want) {
			t.Fatalf("expected csv output to contain %q, got %q", want, csvOutput)
		}
	}

	htmlOutput, err := FormatReport(reportData, FormatHTML)
	if err != nil {
		t.Fatalf("FormatReport(html) error = %v", err)
	}
	for _, want := range []string{"Baseline Comparison", "label:base", "Cross-Repo Duplicate Dependencies", "&lt;current&gt;", "&lt;baseline&gt;"} {
		if !strings.Contains(htmlOutput, want) {
			t.Fatalf("expected html output to contain %q, got %q", want, htmlOutput)
		}
	}
}

func TestWriteDashboardBaselineRowsCSVBranches(t *testing.T) {
	t.Parallel()

	comparison := newBaselineComparison("base", "head", SummaryDelta{TotalReposDelta: 1, TotalDepsDelta: 2, TotalWasteCandidatesDelta: 3, CrossRepoDuplicatesDelta: 4, CriticalCVEsDelta: 5}, RepoDelta{Kind: RepoDeltaChanged, Name: "api", Path: "./api", DependencyCountDelta: 1, WasteCandidateCountDelta: 1, WasteCandidatePercentDelta: 10, CriticalCVEsDelta: 1, DeniedLicenseCountDelta: 1, CurrentError: "current", BaselineError: "baseline"})

	if err := writeDashboardBaselineRowsCSV((&failOnCSVWrite{failOn: 0}).Write, nil); err != nil {
		t.Fatalf("nil comparison should be ignored, got %v", err)
	}
	if err := writeDashboardBaselineRowsCSV((&failOnCSVWrite{failOn: 0}).Write, &BaselineComparison{BaselineKey: "base"}); err != nil {
		t.Fatalf("summary-only comparison should write, got %v", err)
	}

	for _, failOn := range []int{1, 2, 3, 4, 9, 10, 11} {
		writer := &failOnCSVWrite{failOn: failOn}
		if err := writeDashboardBaselineRowsCSV(writer.Write, comparison); err == nil {
			t.Fatalf("expected baseline csv write failure on call %d", failOn)
		}
	}
}

func newBaselineComparison(baselineKey, currentKey string, summary SummaryDelta, repoDelta RepoDelta) *BaselineComparison {
	return &BaselineComparison{
		BaselineKey:  baselineKey,
		CurrentKey:   currentKey,
		SummaryDelta: summary,
		RepoDeltas:   []RepoDelta{repoDelta},
	}
}
