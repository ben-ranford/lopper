package report

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestLoad(t *testing.T) {
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
	const snapshotKey = "label:weekly"
	reportData := Report{
		SchemaVersion: "0.1.0",
		RepoPath:      ".",
		Dependencies: []DependencyReport{
			{Name: "dep-a", Language: "js-ts", UsedExportsCount: 1, TotalExportsCount: 4, UsedPercent: 25},
		},
	}
	path, err := SaveSnapshot(dir, snapshotKey, reportData, now)
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
	if key != snapshotKey {
		t.Fatalf("expected saved key, got %q", key)
	}
	if rep.Summary == nil || rep.Summary.DependencyCount != 1 {
		t.Fatalf("expected computed summary in loaded report, got %#v", rep.Summary)
	}

	_, err = SaveSnapshot(dir, snapshotKey, Report{RepoPath: "."}, now)
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

func TestSanitizeBaselineKey(t *testing.T) {
	tests := []struct {
		name string
		key  string
		want string
	}{
		{name: "empty", key: "", want: "baseline"},
		{name: "valid", key: "release-1.2_prod", want: "release-1.2_prod"},
		{name: "uppercase", key: "Release-1.2_Prod", want: "Release-1.2_Prod"},
		{name: "replaces invalid and trims separators", key: "../feature branch#", want: "feature_branch"},
		{name: "all separators fallback", key: "._-", want: "baseline"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizeBaselineKey(tc.key); got != tc.want {
				t.Fatalf("sanitizeBaselineKey(%q) = %q, want %q", tc.key, got, tc.want)
			}
		})
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

func TestLoadWithKeySnapshotComputesMissingFields(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "snapshot.json")
	content := `{"baselineSchemaVersion":"1.0.0","key":" label:manual ","savedAt":"2026-01-01T00:00:00Z","report":{"schemaVersion":"0.1.0","generatedAt":"2026-01-01T00:00:00Z","repoPath":".","dependencies":[{"language":"js-ts","name":"dep","usedExportsCount":1,"totalExportsCount":2}]}}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}

	rep, key, err := LoadWithKey(path)
	if err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	if key != "label:manual" {
		t.Fatalf("expected trimmed snapshot key, got %q", key)
	}
	if rep.Summary == nil || rep.Summary.DependencyCount != 1 {
		t.Fatalf("expected computed summary, got %#v", rep.Summary)
	}
	if len(rep.LanguageBreakdown) != 1 || rep.LanguageBreakdown[0].Language != "js-ts" {
		t.Fatalf("expected computed language breakdown, got %#v", rep.LanguageBreakdown)
	}
}
