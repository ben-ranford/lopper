package report

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	baselineutil "github.com/ben-ranford/lopper/internal/baseline"
	"github.com/ben-ranford/lopper/internal/testutil"
)

const testLabelX = "label:x"

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

func TestSaveSnapshotRemovesPartialFileOnEncodeError(t *testing.T) {
	now := time.Date(2026, time.February, 22, 10, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	const snapshotKey = "label:nan"

	badReport := Report{
		RepoPath: ".",
		Dependencies: []DependencyReport{
			{Name: "dep", Language: "js-ts", UsedPercent: math.NaN()},
		},
	}
	if _, err := SaveSnapshot(dir, snapshotKey, badReport, now); err == nil || !strings.Contains(err.Error(), "unsupported value: NaN") {
		t.Fatalf("expected json encoder NaN error, got %v", err)
	}

	path := BaselineSnapshotPath(dir, snapshotKey)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected no snapshot file after failed encode, stat err=%v", err)
	}

	goodReport := Report{
		RepoPath: ".",
		Dependencies: []DependencyReport{
			{Name: "dep", Language: "js-ts", UsedExportsCount: 1, TotalExportsCount: 2, UsedPercent: 50},
		},
	}
	if _, err := SaveSnapshot(dir, snapshotKey, goodReport, now); err != nil {
		t.Fatalf("expected save to succeed after failed encode cleanup, got %v", err)
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

func TestLoadWithKeyPreservesOversizedExplicitBaselineCompatibility(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oversized-explicit.json")
	content := `{"schemaVersion":"0.1.0","generatedAt":"2026-01-01T00:00:00Z","repoPath":".","dependencies":[]}`
	testutil.MustWritePaddedFile(t, path, content, baselineutil.MaxSnapshotBytes+1)

	rep, key, err := LoadWithKey(path)
	if err != nil {
		t.Fatalf("load oversized explicit baseline: %v", err)
	}
	if key != "" || rep.RepoPath != "." {
		t.Fatalf("unexpected oversized explicit baseline: key=%q report=%#v", key, rep)
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

type loadWithKeyCompatibilityCase struct {
	name     string
	content  string
	wantKey  string
	wantRepo string
	wantErr  string
}

func assertLoadWithKeyCompatibility(t *testing.T, tc loadWithKeyCompatibilityCase) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "snapshot.json")
	if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}

	rep, key, err := LoadWithKey(path)
	if tc.wantErr != "" {
		if err == nil || err.Error() != tc.wantErr {
			t.Fatalf("LoadWithKey() error = %v, want %q", err, tc.wantErr)
		}
		if rep.RepoPath != "" || rep.SchemaVersion != "" || len(rep.Dependencies) != 0 || key != "" {
			t.Fatalf("LoadWithKey() error result = %#v, %q, want zero values", rep, key)
		}
		return
	}
	if err != nil {
		t.Fatalf("LoadWithKey() error = %v", err)
	}
	if rep.RepoPath != tc.wantRepo || key != tc.wantKey {
		t.Fatalf("LoadWithKey() = repo=%q key=%q, want repo=%q key=%q", rep.RepoPath, key, tc.wantRepo, tc.wantKey)
	}
}

func TestLoadWithKeySnapshotSchemaVersionCompatibility(t *testing.T) {
	t.Parallel()

	tests := []loadWithKeyCompatibilityCase{
		{
			name:     "exact version accepted",
			content:  `{"baselineSchemaVersion":"1.0.0","key":" label:exact ","savedAt":"2026-01-01T00:00:00Z","report":{"schemaVersion":"0.1.0","generatedAt":"2026-01-01T00:00:00Z","repoPath":".","dependencies":[]}}` + "\n",
			wantKey:  "label:exact",
			wantRepo: ".",
		},
		{
			name:    "padded version rejected with raw unsupported value",
			content: `{"baselineSchemaVersion":" 1.0.0 ","key":"label:padded","savedAt":"2026-01-01T00:00:00Z","report":{"schemaVersion":"0.1.0","generatedAt":"2026-01-01T00:00:00Z","repoPath":".","dependencies":[]}}` + "\n",
			wantErr: "unsupported baseline schema version:  1.0.0 ",
		},
		{
			name:     "whitespace version matches missing legacy behavior",
			content:  `{"baselineSchemaVersion":"   ","schemaVersion":"0.1.0","generatedAt":"2026-01-01T00:00:00Z","repoPath":"legacy","dependencies":[]}` + "\n",
			wantRepo: "legacy",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assertLoadWithKeyCompatibility(t, tc)
		})
	}
}

func TestSaveSnapshotValidationErrors(t *testing.T) {
	now := time.Date(2026, time.February, 22, 10, 0, 0, 0, time.UTC)
	if _, err := SaveSnapshot("", testLabelX, Report{}, now); err == nil || !strings.Contains(err.Error(), "baseline store directory is required") {
		t.Fatalf("expected missing directory validation error, got %v", err)
	}
	if _, err := SaveSnapshot(t.TempDir(), "  ", Report{}, now); err == nil || !strings.Contains(err.Error(), "baseline key is required") {
		t.Fatalf("expected missing key validation error, got %v", err)
	}
}

func TestBaselineSnapshotPathSanitizesKey(t *testing.T) {
	path := BaselineSnapshotPath("/tmp/baselines", " label:release candidate/1 ")
	if filepath.Base(path) != baselineutil.SnapshotFileName("label:release candidate/1") {
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
			if got := baselineutil.SanitizeKey(tc.key); got != tc.want {
				t.Fatalf("SanitizeKey(%q) = %q, want %q", tc.key, got, tc.want)
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
	if _, err := SaveSnapshot(filepath.Join(blocking, "nested"), testLabelX, Report{}, now); err == nil {
		t.Fatalf("expected mkdir failure when parent is a file")
	}
}

func TestSaveSnapshotRejectsSymlinkedStoreDir(t *testing.T) {
	now := time.Date(2026, time.February, 22, 10, 0, 0, 0, time.UTC)
	root := t.TempDir()
	target := filepath.Join(root, "actual-store")
	if err := os.MkdirAll(target, 0o750); err != nil {
		t.Fatalf("create target store: %v", err)
	}
	link := filepath.Join(root, "store-link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	if _, err := SaveSnapshot(link, testLabelX, Report{}, now); err == nil || !strings.Contains(err.Error(), "must not be a symlink") {
		t.Fatalf("expected symlink rejection, got %v", err)
	}

	if entries, err := os.ReadDir(target); err != nil {
		t.Fatalf("read target store: %v", err)
	} else if len(entries) != 0 {
		t.Fatalf("expected no snapshot files written via symlinked store dir, got %d entries", len(entries))
	}
}

func TestSaveSnapshotCanonicalizesReport(t *testing.T) {
	now := time.Date(2026, time.February, 22, 10, 0, 0, 0, time.UTC)
	reportData := Report{
		Dependencies: []DependencyReport{
			{Name: "zeta", Language: "python", TotalExportsCount: 1},
			{Name: "alpha", Language: "go", TotalExportsCount: 1},
			{Name: "beta", Language: "go", TotalExportsCount: 1},
		},
	}
	path, err := SaveSnapshot(t.TempDir(), "label:sorted", reportData, now)
	if err != nil {
		t.Fatalf("save snapshot: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	var snapshot BaselineSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		t.Fatalf("unmarshal snapshot: %v", err)
	}
	gotOrder := []string{
		snapshot.Report.Dependencies[0].Language + "/" + snapshot.Report.Dependencies[0].Name,
		snapshot.Report.Dependencies[1].Language + "/" + snapshot.Report.Dependencies[1].Name,
		snapshot.Report.Dependencies[2].Language + "/" + snapshot.Report.Dependencies[2].Name,
	}
	wantOrder := []string{"go/alpha", "go/beta", "python/zeta"}
	if !slices.Equal(gotOrder, wantOrder) {
		t.Fatalf("unexpected dependency order: got=%v want=%v", gotOrder, wantOrder)
	}
	if snapshot.Report.SchemaVersion != SchemaVersion {
		t.Fatalf("saved schema version = %q, want %q", snapshot.Report.SchemaVersion, SchemaVersion)
	}
	if snapshot.Report.Summary == nil || snapshot.Report.Summary.DependencyCount != 3 {
		t.Fatalf("saved summary = %#v, want computed dependency count", snapshot.Report.Summary)
	}
	if len(snapshot.Report.LanguageBreakdown) != 2 || snapshot.Report.LanguageBreakdown[0].Language != "go" || snapshot.Report.LanguageBreakdown[1].Language != "python" {
		t.Fatalf("saved language breakdown = %#v, want canonical languages", snapshot.Report.LanguageBreakdown)
	}
}

func TestSaveSnapshotRejectsIncompleteUsageCoverage(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.February, 22, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		rep  Report
	}{
		{
			name: "report-level",
			rep: Report{
				SchemaVersion:   SchemaVersion,
				GeneratedAt:     now,
				RepoPath:        ".",
				UsageIncomplete: true,
			},
		},
		{
			name: "dependency-level",
			rep: Report{
				SchemaVersion: SchemaVersion,
				GeneratedAt:   now,
				RepoPath:      ".",
				Dependencies: []DependencyReport{{
					Name:            "vendor/lib",
					UsageIncomplete: true,
				}},
			},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			path, err := SaveSnapshot(dir, "label:partial", tc.rep, now)
			if !errors.Is(err, ErrIncompleteBaselineReport) {
				t.Fatalf("SaveSnapshot() error = %v, want %v", err, ErrIncompleteBaselineReport)
			}
			if path != "" {
				t.Fatalf("SaveSnapshot() path = %q, want empty path on rejected save", path)
			}
			if _, _, err := LoadWithKey(BaselineSnapshotPath(dir, "label:partial")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("LoadWithKey() after rejected save error = %v, want os.ErrNotExist", err)
			}
		})
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

func assertLoadWithKeyPreservesReportData(t *testing.T, content, wantKey string) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "baseline.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write baseline: %v", err)
	}
	rep, key, err := LoadWithKey(path)
	if err != nil {
		t.Fatalf("LoadWithKey() error = %v", err)
	}
	if key != wantKey {
		t.Fatalf("LoadWithKey() key = %q, want %q", key, wantKey)
	}
	if len(rep.Dependencies) != 3 {
		t.Fatalf("LoadWithKey() dependencies = %#v, want three supplied values", rep.Dependencies)
	}
	gotDependencies := []string{
		rep.Dependencies[0].Language + "/" + rep.Dependencies[0].Name,
		rep.Dependencies[1].Language + "/" + rep.Dependencies[1].Name,
		rep.Dependencies[2].Language + "/" + rep.Dependencies[2].Name,
	}
	wantDependencies := []string{"python/zeta", "go/alpha", "go/alpha"}
	if !slices.Equal(gotDependencies, wantDependencies) {
		t.Fatalf("LoadWithKey() dependencies = %v, want %v", gotDependencies, wantDependencies)
	}
	if rep.SchemaVersion != "" {
		t.Fatalf("LoadWithKey() schema version = %q, want preserved empty value", rep.SchemaVersion)
	}
	if rep.Summary == nil || rep.Summary.DependencyCount != 99 {
		t.Fatalf("LoadWithKey() summary = %#v, want supplied summary", rep.Summary)
	}
	if len(rep.LanguageBreakdown) != 1 || rep.LanguageBreakdown[0].Language != "preserved" || rep.LanguageBreakdown[0].DependencyCount != 77 {
		t.Fatalf("LoadWithKey() language breakdown = %#v, want supplied value", rep.LanguageBreakdown)
	}
	if !slices.Equal(rep.Warnings, []string{" first ", "first"}) {
		t.Fatalf("LoadWithKey() warnings = %q, want supplied values", rep.Warnings)
	}
}

func TestLoadWithKeyPreservesReportData(t *testing.T) {
	t.Parallel()

	const reportJSON = `{"generatedAt":"2026-01-01T00:00:00Z","repoPath":"preserved","dependencies":[{"language":"python","name":"zeta"},{"language":"go","name":"alpha"},{"language":"go","name":"alpha"}],"summary":{"dependencyCount":99},"languageBreakdown":[{"language":"preserved","dependencyCount":77}],"warnings":[" first ","first"]}`
	tests := []struct {
		name    string
		content string
		wantKey string
	}{
		{
			name:    "envelope",
			content: `{"baselineSchemaVersion":"1.0.0","key":" label:preserved ","savedAt":"2026-01-02T00:00:00Z","report":` + reportJSON + `}`,
			wantKey: "label:preserved",
		},
		{
			name:    "legacy",
			content: reportJSON,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assertLoadWithKeyPreservesReportData(t, tc.content, tc.wantKey)
		})
	}
}
