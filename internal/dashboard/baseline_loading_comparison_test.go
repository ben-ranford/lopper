package dashboard

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	baselineutil "github.com/ben-ranford/lopper/internal/baseline"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestDashboardBaselineLoadPreservesRemoteMetadata(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	snapshot := Report{
		GeneratedAt: time.Date(2026, time.March, 10, 0, 0, 0, 0, time.UTC),
		Repos: []RepoResult{
			{
				Name:                "web",
				Path:                "./web",
				RepoURL:             "https://github.com/example/web.git",
				Revision:            &RepoRevision{Branch: "release/2.0"},
				ResolvedCommit:      "0123456789abcdef0123456789abcdef01234567",
				DependencyCount:     2,
				WasteCandidateCount: 1,
				CriticalCVEs:        1,
			},
		},
	}
	path, err := SaveSnapshot(tmp, " label:load ", snapshot, time.Date(2026, time.March, 11, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("SaveSnapshot() error = %v", err)
	}
	if loaded, err := Load(path); err != nil || loaded.Summary.TotalRepos != 1 {
		t.Fatalf("Load() = summary=%+v err=%v", loaded.Summary, err)
	}
	loaded, key, err := LoadWithKey(path)
	if err != nil {
		t.Fatalf("LoadWithKey(snapshot) error = %v", err)
	}
	if key != "label:load" || loaded.Repos[0].RepoURL != "https://github.com/example/web.git" || loaded.Repos[0].Revision == nil || loaded.Repos[0].Revision.Branch != "release/2.0" || loaded.Repos[0].ResolvedCommit != "0123456789abcdef0123456789abcdef01234567" {
		t.Fatalf("expected snapshot to preserve remote revision metadata, key=%q repo=%#v", key, loaded.Repos[0])
	}
	if got := BaselineSnapshotPath(tmp, " label:load "); got != path {
		t.Fatalf("BaselineSnapshotPath() = %q, want %q", got, path)
	}
}

func TestDashboardBaselineLoadLegacyRemoteFieldsEmpty(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
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
	if legacy.Repos[0].RepoURL != "" || legacy.Repos[0].Revision != nil || legacy.Repos[0].ResolvedCommit != "" {
		t.Fatalf("expected legacy baseline remote metadata fields to remain empty, got %#v", legacy.Repos[0])
	}
}

func TestDashboardLoadWithKeyPreservesOversizedExplicitBaselineCompatibility(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oversized-explicit.json")
	content := `{"generated_at":"2026-03-12T00:00:00Z","repos":[{"name":"api","path":"./api","dependency_count":3}]}`
	testutil.MustWritePaddedFile(t, path, content, baselineutil.MaxSnapshotBytes+1)

	rep, key, err := LoadWithKey(path)
	if err != nil {
		t.Fatalf("load oversized explicit dashboard baseline: %v", err)
	}
	if key != "" || rep.Summary.TotalDeps != 3 {
		t.Fatalf("unexpected oversized explicit dashboard baseline: key=%q report=%#v", key, rep)
	}
}

func TestDashboardBaselineLoadRejectsUnsupportedSchema(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	badPath := filepath.Join(tmp, "bad-schema.json")
	badJSON := `{"baselineSchemaVersion":"9.9.9","key":"label:bad","report":{"repos":[]}}`
	if err := os.WriteFile(badPath, []byte(badJSON), 0o600); err != nil {
		t.Fatalf("write bad dashboard snapshot: %v", err)
	}
	if _, _, err := LoadWithKey(badPath); err == nil || !strings.Contains(err.Error(), "unsupported dashboard baseline schema version") {
		t.Fatalf("expected unsupported schema error, got %v", err)
	}
}

func TestDashboardBaselineLoadReportsReadErrors(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
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

func TestDashboardKeyedBaselineLoadCompatibilityAndIdentity(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	now := time.Date(2026, time.March, 11, 0, 0, 0, 0, time.UTC)
	path, err := SaveSnapshot(dir, "label:current", Report{GeneratedAt: now, Repos: []RepoResult{{Name: "api"}}}, now)
	if err != nil {
		t.Fatalf("save current snapshot: %v", err)
	}
	loaded, key, resolvedPath, err := LoadSnapshot(dir, "label:current")
	if err != nil || key != "label:current" || resolvedPath != path || loaded.Summary.TotalRepos != 1 {
		t.Fatalf("keyed load failed: key=%q path=%q report=%#v err=%v", key, resolvedPath, loaded, err)
	}
	if ResolveBaselineSnapshotPath(dir, "label:current") != path {
		t.Fatalf("expected current snapshot path resolution")
	}
	if _, _, _, err := LoadSnapshot(dir, "label:missing"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected missing keyed snapshot error, got %v", err)
	}

	legacyDir := t.TempDir()
	newPath, err := SaveSnapshot(legacyDir, "label:a/b", Report{GeneratedAt: now}, now)
	if err != nil {
		t.Fatalf("save legacy fixture: %v", err)
	}
	legacyPath := baselineutil.LegacySnapshotPath(legacyDir, "label:a/b")
	if err := os.Rename(newPath, legacyPath); err != nil {
		t.Fatalf("move snapshot to legacy path: %v", err)
	}
	if _, key, resolvedPath, err := LoadSnapshot(legacyDir, "label:a/b"); err != nil || key != "label:a/b" || resolvedPath != legacyPath {
		t.Fatalf("legacy keyed load failed: key=%q path=%q err=%v", key, resolvedPath, err)
	}
	if _, _, _, err := LoadSnapshot(legacyDir, "label:a?b"); !errors.Is(err, ErrBaselineKeyMismatch) {
		t.Fatalf("expected colliding dashboard key mismatch, got %v", err)
	}

	corruptName := baselineutil.SnapshotFileName("label:corrupt")
	if err := os.WriteFile(filepath.Join(legacyDir, corruptName), []byte("{"), 0o600); err != nil {
		t.Fatalf("write corrupt keyed snapshot: %v", err)
	}
	if _, _, _, err := LoadSnapshot(legacyDir, "label:corrupt"); err == nil {
		t.Fatalf("expected corrupt keyed snapshot error")
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
