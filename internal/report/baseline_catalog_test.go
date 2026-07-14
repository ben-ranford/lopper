package report

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	baselineutil "github.com/ben-ranford/lopper/internal/baseline"
)

func TestListAndInspectBaselineSnapshots(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	older := time.Date(2026, time.July, 10, 1, 0, 0, 0, time.UTC)
	newer := older.Add(time.Hour)
	if _, err := SaveSnapshot(dir, "commit:abc123", baselineCatalogReport(older, 1), older); err != nil {
		t.Fatalf("save older snapshot: %v", err)
	}
	if _, err := SaveSnapshot(dir, "label:release-candidate", baselineCatalogReport(newer, 2), newer); err != nil {
		t.Fatalf("save newer snapshot: %v", err)
	}

	catalog, err := ListBaselineSnapshots(dir, 1)
	if err != nil {
		t.Fatalf("list snapshots: %v", err)
	}
	if catalog.Store != dir || len(catalog.Snapshots) != 1 || len(catalog.Diagnostics) != 0 {
		t.Fatalf("unexpected limited catalog: %#v", catalog)
	}
	listed := catalog.Snapshots[0]
	if listed.Key != "label:release-candidate" || listed.KeyType != "label" || listed.Label != "release-candidate" || listed.Commit != "" {
		t.Fatalf("unexpected label metadata: %#v", listed)
	}
	if listed.RepoIdentity != "/repo/example" || listed.Summary.DependencyCount != 2 || listed.ReportSchemaVersion != SchemaVersion {
		t.Fatalf("missing report metadata: %#v", listed)
	}

	shown, err := InspectBaselineSnapshot(dir, "commit:abc123")
	if err != nil {
		t.Fatalf("inspect snapshot: %v", err)
	}
	if shown.KeyType != "commit" || shown.Commit != "abc123" || shown.Label != "" || shown.CreatedAt != older {
		t.Fatalf("unexpected commit metadata: %#v", shown)
	}
	if _, _, _, err := LoadSnapshot(dir, "commit:abc123"); err != nil {
		t.Fatalf("load keyed snapshot: %v", err)
	}
}

func TestListBaselineSnapshotsReturnsSafePerEntryDiagnostics(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "corrupt.json"), []byte("{"), 0o600); err != nil {
		t.Fatalf("write corrupt snapshot: %v", err)
	}
	oversized := filepath.Join(dir, "oversized.json")
	if err := os.WriteFile(oversized, []byte("x"), 0o600); err != nil {
		t.Fatalf("write oversized snapshot: %v", err)
	}
	if err := os.Truncate(oversized, baselineutil.MaxSnapshotBytes+1); err != nil {
		t.Fatalf("grow oversized snapshot: %v", err)
	}
	outside := filepath.Join(t.TempDir(), "outside.json")
	if err := os.WriteFile(outside, []byte(`{"doNotRead":true}`), 0o600); err != nil {
		t.Fatalf("write outside fixture: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "symlink.json")); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("ignored"), 0o600); err != nil {
		t.Fatalf("write non-snapshot: %v", err)
	}

	catalog, err := ListBaselineSnapshots(dir, 10)
	if err != nil {
		t.Fatalf("list diagnostic catalog: %v", err)
	}
	if len(catalog.Snapshots) != 0 || len(catalog.Diagnostics) != 3 {
		t.Fatalf("expected three safe diagnostics, got %#v", catalog)
	}
	got := catalog.Diagnostics[0].File + " " + catalog.Diagnostics[0].Error + "\n" +
		catalog.Diagnostics[1].File + " " + catalog.Diagnostics[1].Error + "\n" +
		catalog.Diagnostics[2].File + " " + catalog.Diagnostics[2].Error
	for _, want := range []string{"corrupt.json", "oversized.json", "exceeds size limit", "symlink.json", "must not be a symlink"} {
		if !strings.Contains(got, want) {
			t.Fatalf("diagnostics missing %q: %s", want, got)
		}
	}
}

func TestListAndInspectBaselineSnapshotsHandlesEmptyReportSummary(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	now := time.Date(2026, time.July, 10, 1, 0, 0, 0, time.UTC)
	report := Report{
		SchemaVersion: SchemaVersion,
		GeneratedAt:   now,
		RepoPath:      "/repo/empty",
		Dependencies:  []DependencyReport{},
	}
	if _, err := SaveSnapshot(dir, "label:empty", report, now); err != nil {
		t.Fatalf("save empty snapshot: %v", err)
	}

	catalog, err := ListBaselineSnapshots(dir, 10)
	if err != nil {
		t.Fatalf("list empty snapshot: %v", err)
	}
	if len(catalog.Snapshots) != 1 || len(catalog.Diagnostics) != 0 {
		t.Fatalf("unexpected empty catalog: %#v", catalog)
	}
	if catalog.Snapshots[0].Summary.DependencyCount != 0 {
		t.Fatalf("expected zero-value summary for empty snapshot, got %#v", catalog.Snapshots[0].Summary)
	}

	shown, err := InspectBaselineSnapshot(dir, "label:empty")
	if err != nil {
		t.Fatalf("inspect empty snapshot: %v", err)
	}
	if shown.Summary.DependencyCount != 0 || shown.RepoIdentity != "/repo/empty" {
		t.Fatalf("unexpected empty snapshot metadata: %#v", shown)
	}
}

func TestBaselineSnapshotLegacyFallbackRejectsCollidingKey(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	now := time.Date(2026, time.July, 10, 1, 0, 0, 0, time.UTC)
	legacyPath := baselineutil.LegacySnapshotPath(dir, "label:a/b")
	snapshot := newBaselineSnapshot("label:a/b", baselineCatalogReport(now, 1), now)
	data, err := jsonMarshal(snapshot)
	if err != nil {
		t.Fatalf("marshal legacy snapshot: %v", err)
	}
	if err := os.WriteFile(legacyPath, data, 0o600); err != nil {
		t.Fatalf("write legacy snapshot: %v", err)
	}

	loaded, key, path, err := LoadSnapshot(dir, "label:a/b")
	if err != nil || key != "label:a/b" || path != legacyPath || loaded.Summary == nil {
		t.Fatalf("legacy fallback failed: key=%q path=%q report=%#v err=%v", key, path, loaded, err)
	}
	if _, err := InspectBaselineSnapshot(dir, "label:a?b"); !errors.Is(err, ErrBaselineKeyMismatch) {
		t.Fatalf("expected colliding requested key mismatch, got %v", err)
	}
	if _, _, _, err := LoadSnapshot(dir, "label:a?b"); !errors.Is(err, ErrBaselineKeyMismatch) {
		t.Fatalf("expected keyed load mismatch, got %v", err)
	}
}

func TestBaselineCatalogValidationBranches(t *testing.T) {
	t.Parallel()

	if _, err := ListBaselineSnapshots(t.TempDir(), 0); err == nil {
		t.Fatalf("expected non-positive limit error")
	}
	if kind, value := baselineKeyType("owner:nightly"); kind != "custom" || value != "owner:nightly" {
		t.Fatalf("unexpected custom key classification: %q %q", kind, value)
	}

	now := time.Date(2026, time.July, 10, 1, 0, 0, 0, time.UTC)
	valid := newBaselineSnapshot("label:valid", baselineCatalogReport(now, 1), now)
	tests := []struct {
		name     string
		fileName string
		mutate   func(*BaselineSnapshot)
		want     string
	}{
		{name: "missing schema", fileName: baselineutil.SnapshotFileName("label:valid"), mutate: func(snapshot *BaselineSnapshot) { snapshot.BaselineSchemaVersion = "" }, want: "missing baseline schema"},
		{name: "unsupported schema", fileName: baselineutil.SnapshotFileName("label:valid"), mutate: func(snapshot *BaselineSnapshot) { snapshot.BaselineSchemaVersion = "9.9.9" }, want: "unsupported baseline schema"},
		{name: "missing key", fileName: baselineutil.SnapshotFileName("label:valid"), mutate: func(snapshot *BaselineSnapshot) { snapshot.Key = "" }, want: "missing baseline snapshot key"},
		{name: "wrong filename", fileName: "wrong.json", mutate: func(*BaselineSnapshot) {}, want: "filename does not match"},
		{name: "missing creation time", fileName: baselineutil.SnapshotFileName("label:valid"), mutate: func(snapshot *BaselineSnapshot) { snapshot.SavedAt = time.Time{} }, want: "missing baseline creation time"},
		{name: "missing report schema", fileName: baselineutil.SnapshotFileName("label:valid"), mutate: func(snapshot *BaselineSnapshot) { snapshot.Report.SchemaVersion = "" }, want: "missing baseline report schema"},
		{name: "missing report generation", fileName: baselineutil.SnapshotFileName("label:valid"), mutate: func(snapshot *BaselineSnapshot) { snapshot.Report.GeneratedAt = time.Time{} }, want: "missing baseline report generation"},
		{name: "missing repo identity", fileName: baselineutil.SnapshotFileName("label:valid"), mutate: func(snapshot *BaselineSnapshot) { snapshot.Report.RepoPath = "" }, want: "missing baseline repository identity"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			snapshot := valid
			tc.mutate(&snapshot)
			data, err := jsonMarshal(snapshot)
			if err != nil {
				t.Fatalf("marshal fixture: %v", err)
			}
			if _, err := decodeBaselineSnapshotMetadata(tc.fileName, data); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q error, got %v", tc.want, err)
			}
		})
	}
	if _, err := decodeBaselineSnapshotMetadata("bad.json", []byte("{")); err == nil || !strings.Contains(err.Error(), "decode baseline snapshot") {
		t.Fatalf("expected corrupt JSON error, got %v", err)
	}
}

func TestBaselineCatalogLoadAndStoreErrors(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if _, _, _, err := LoadSnapshot(dir, "label:missing"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected missing keyed snapshot error, got %v", err)
	}
	if _, err := InspectBaselineSnapshot(dir, "label:missing"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected missing inspection error, got %v", err)
	}
	corruptName := baselineutil.SnapshotFileName("label:corrupt")
	if err := os.WriteFile(filepath.Join(dir, corruptName), []byte("{"), 0o600); err != nil {
		t.Fatalf("write corrupt keyed snapshot: %v", err)
	}
	if _, _, _, err := LoadSnapshot(dir, "label:corrupt"); err == nil || !strings.Contains(err.Error(), "unexpected end of JSON") {
		t.Fatalf("expected corrupt keyed load error, got %v", err)
	}
	if _, err := InspectBaselineSnapshot(dir, "label:corrupt"); err == nil || !strings.Contains(err.Error(), "decode baseline snapshot") {
		t.Fatalf("expected corrupt inspection error, got %v", err)
	}

	target := filepath.Join(t.TempDir(), "actual")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatalf("create target store: %v", err)
	}
	link := filepath.Join(t.TempDir(), "linked-store")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	if _, err := ListBaselineSnapshots(link, 10); err == nil || !strings.Contains(err.Error(), "path component must not be a symlink") {
		t.Fatalf("expected symlinked store rejection, got %v", err)
	}
}

func TestBaselineCatalogDeterministicTieBreaks(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	now := time.Date(2026, time.July, 10, 1, 0, 0, 0, time.UTC)
	for _, key := range []string{"label:zeta", "label:alpha"} {
		if _, err := SaveSnapshot(dir, key, baselineCatalogReport(now, 1), now); err != nil {
			t.Fatalf("save %s: %v", key, err)
		}
	}
	duplicateKey := "label:duplicate"
	newPath, err := SaveSnapshot(dir, duplicateKey, baselineCatalogReport(now, 1), now)
	if err != nil {
		t.Fatalf("save duplicate fixture: %v", err)
	}
	data, err := os.ReadFile(newPath)
	if err != nil {
		t.Fatalf("read duplicate fixture: %v", err)
	}
	if err := os.WriteFile(baselineutil.LegacySnapshotPath(dir, duplicateKey), data, 0o600); err != nil {
		t.Fatalf("write compatible legacy duplicate: %v", err)
	}

	catalog, err := ListBaselineSnapshots(dir, 10)
	if err != nil {
		t.Fatalf("list tied snapshots: %v", err)
	}
	if len(catalog.Snapshots) != 4 {
		t.Fatalf("expected four tied snapshots, got %#v", catalog.Snapshots)
	}
	keys := []string{catalog.Snapshots[0].Key, catalog.Snapshots[1].Key, catalog.Snapshots[2].Key, catalog.Snapshots[3].Key}
	if keys[0] != "label:alpha" || keys[1] != duplicateKey || keys[2] != duplicateKey || keys[3] != "label:zeta" {
		t.Fatalf("unexpected deterministic key order: %v", keys)
	}
	if catalog.Snapshots[1].File >= catalog.Snapshots[2].File {
		t.Fatalf("expected filename tie-break order, got %q then %q", catalog.Snapshots[1].File, catalog.Snapshots[2].File)
	}
}

func baselineCatalogReport(generatedAt time.Time, dependencyCount int) Report {
	dependencies := make([]DependencyReport, dependencyCount)
	for index := range dependencies {
		dependencies[index] = DependencyReport{
			Name:              "dep-" + string(rune('a'+index)),
			Language:          "go",
			UsedExportsCount:  1,
			TotalExportsCount: 2,
			UsedPercent:       50,
		}
	}
	return Report{SchemaVersion: SchemaVersion, GeneratedAt: generatedAt, RepoPath: "/repo/example", Dependencies: dependencies}
}

func jsonMarshal(value any) ([]byte, error) {
	return json.Marshal(value)
}
