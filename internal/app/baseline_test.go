package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	baselineutil "github.com/ben-ranford/lopper/internal/baseline"
	"github.com/ben-ranford/lopper/internal/featureflags"
	"github.com/ben-ranford/lopper/internal/report"
)

func TestExecuteBaselineList(t *testing.T) {
	t.Parallel()

	dir := saveBaselineDiscoveryFixtures(t)
	application := &App{}
	req := baselineDiscoveryRequest(t, dir)

	output, err := application.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("execute baseline list: %v", err)
	}
	if !strings.Contains(output, "TYPE") || !strings.Contains(output, "label:nightly") || strings.Contains(output, "commit:abc123") {
		t.Fatalf("unexpected newest-first limited table: %s", output)
	}

	req.Baseline.Format = "json"
	output, err = application.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("execute baseline JSON list: %v", err)
	}
	if !strings.Contains(output, `"keyType": "label"`) || !strings.Contains(output, `"dependencyCount": 1`) {
		t.Fatalf("missing JSON metadata: %s", output)
	}
	if strings.Contains(output, "private-dependency-row") {
		t.Fatalf("baseline list dumped dependency report content: %s", output)
	}
}

func TestExecuteBaselineShow(t *testing.T) {
	t.Parallel()

	dir := saveBaselineDiscoveryFixtures(t)
	application := &App{}
	req := baselineDiscoveryRequest(t, dir)
	req.Baseline.Action = "show"
	req.Baseline.Key = "commit:abc123"
	req.Baseline.Format = "table"
	output, err := application.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("execute baseline show: %v", err)
	}
	for _, want := range []string{"Key: commit:abc123", "Type: commit", "Commit: abc123", "Repository: /repo/app", "Dependencies: 1"} {
		if !strings.Contains(output, want) {
			t.Fatalf("show output missing %q: %s", want, output)
		}
	}
	if strings.Contains(output, "old-dependency") {
		t.Fatalf("baseline show dumped dependency report content: %s", output)
	}

	req.Baseline.Key = "label:nightly"
	req.Baseline.Format = "json"
	output, err = application.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("execute baseline JSON show: %v", err)
	}
	if !strings.Contains(output, `"label": "nightly"`) || strings.Contains(output, "private-dependency-row") {
		t.Fatalf("unexpected JSON show output: %s", output)
	}
}

func saveBaselineDiscoveryFixtures(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	older := time.Date(2026, time.July, 10, 1, 0, 0, 0, time.UTC)
	if _, err := report.SaveSnapshot(dir, "commit:abc123", appBaselineReport(older, "old-dependency"), older); err != nil {
		t.Fatalf("save commit baseline: %v", err)
	}
	newer := older.Add(time.Hour)
	if _, err := report.SaveSnapshot(dir, "label:nightly", appBaselineReport(newer, "private-dependency-row"), newer); err != nil {
		t.Fatalf("save label baseline: %v", err)
	}
	return dir
}

func baselineDiscoveryRequest(t *testing.T, dir string) Request {
	t.Helper()
	req := DefaultRequest()
	req.Mode = ModeBaseline
	req.Baseline.Action = "list"
	req.Baseline.StorePath = dir
	req.Baseline.Limit = 1
	req.Baseline.Features = enabledBaselineDiscovery(t)
	return req
}

func TestExecuteBaselineValidationAndEmptyStore(t *testing.T) {
	t.Parallel()

	application := &App{}
	req := DefaultRequest()
	req.Mode = ModeBaseline
	req.Baseline.Action = "list"
	req.Baseline.StorePath = filepath.Join(t.TempDir(), "missing")

	if _, err := application.Execute(context.Background(), req); !errors.Is(err, ErrBaselineFeatureDisabled) {
		t.Fatalf("expected disabled preview error, got %v", err)
	}
	req.Baseline.Features = enabledBaselineDiscovery(t)
	output, err := application.Execute(context.Background(), req)
	if err != nil || !strings.Contains(output, "No baseline snapshots found") {
		t.Fatalf("expected safe empty-store output, output=%q err=%v", output, err)
	}

	req.Baseline.Format = "yaml"
	if _, err := application.Execute(context.Background(), req); err == nil || !strings.Contains(err.Error(), "invalid baseline format") {
		t.Fatalf("expected invalid format error, got %v", err)
	}
	req.Baseline.Format = "table"
	req.Baseline.Limit = 0
	if _, err := application.Execute(context.Background(), req); err == nil || !strings.Contains(err.Error(), "limit must be greater than zero") {
		t.Fatalf("expected invalid list limit error, got %v", err)
	}
	req.Baseline.Limit = 50
	req.Baseline.Action = "unknown"
	if _, err := application.Execute(context.Background(), req); err == nil || !strings.Contains(err.Error(), "unknown baseline action") {
		t.Fatalf("expected unknown action error, got %v", err)
	}
	req.Baseline.Action = "show"
	req.Baseline.Key = "label:missing"
	if _, err := application.Execute(context.Background(), req); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected safe missing snapshot error, got %v", err)
	}
	if _, err := formatBaselineJSON(make(chan int)); err == nil {
		t.Fatalf("expected unsupported JSON value error")
	}
}

func TestFormatBaselineCatalogDiagnosticsAndCustomMetadata(t *testing.T) {
	t.Parallel()

	catalog := report.BaselineCatalog{
		Store:     "store\x1b",
		Snapshots: []report.BaselineSnapshotMetadata{},
		Diagnostics: []report.BaselineStoreDiagnostic{
			{File: "bad\x1b.json", Error: "corrupt\x1b"},
		},
	}
	output := formatBaselineCatalog(catalog)
	if !strings.Contains(output, "No baseline snapshots") || !strings.Contains(output, "Diagnostics:") || !strings.Contains(output, `bad\x1b.json: corrupt\x1b`) || strings.ContainsRune(output, '\x1b') {
		t.Fatalf("unexpected diagnostic catalog output: %s", output)
	}
	metadata := report.BaselineSnapshotMetadata{
		Key: "owner:nightly", KeyType: "custom", CreatedAt: time.Date(2026, time.July, 10, 1, 0, 0, 0, time.UTC),
		Summary: report.Summary{},
	}
	output = formatBaselineMetadata(metadata)
	if !strings.Contains(output, "Type: custom") || strings.Contains(output, "Label:") || strings.Contains(output, "Commit:") {
		t.Fatalf("unexpected custom metadata output: %s", output)
	}
	metadata.Key = "label:nightly\x1b"
	metadata.KeyType = "label"
	metadata.Label = "nightly\x1b"
	output = formatBaselineMetadata(metadata)
	if !strings.Contains(output, `Label: nightly\x1b`) || strings.ContainsRune(output, '\x1b') {
		t.Fatalf("expected human label metadata: %s", output)
	}
}

func TestAnalyseBaselineStoreRejectsLegacyKeyCollision(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	now := time.Date(2026, time.July, 10, 1, 0, 0, 0, time.UTC)
	newPath, err := report.SaveSnapshot(dir, "label:a/b", appBaselineReport(now, "dep"), now)
	if err != nil {
		t.Fatalf("save collision fixture: %v", err)
	}
	legacyPath := baselineutil.LegacySnapshotPath(dir, "label:a/b")
	if err := os.Rename(newPath, legacyPath); err != nil {
		t.Fatalf("move fixture to legacy path: %v", err)
	}

	current := appBaselineReport(now.Add(time.Hour), "dep")
	application := &App{Formatter: report.NewFormatter()}
	if _, err := application.applyBaselineIfNeeded(current, ".", AnalyseRequest{
		BaselineStorePath: dir,
		BaselineKey:       "label:a?b",
		Format:            report.FormatJSON,
	}); !errors.Is(err, report.ErrBaselineKeyMismatch) {
		t.Fatalf("expected legacy filename collision mismatch, got %v", err)
	}
	if _, err := application.applyBaselineIfNeeded(current, ".", AnalyseRequest{
		BaselineStorePath: dir,
		BaselineKey:       "label:a/b",
		Format:            report.FormatJSON,
	}); err != nil {
		t.Fatalf("expected matching legacy key comparison to remain compatible, got %v", err)
	}
}

func enabledBaselineDiscovery(t *testing.T) featureflags.Set {
	t.Helper()
	features, err := featureflags.DefaultRegistry().Resolve(featureflags.ResolveOptions{Enable: []string{BaselineStoreDiscoveryPreviewFeature}})
	if err != nil {
		t.Fatalf("resolve baseline discovery feature: %v", err)
	}
	return features
}

func appBaselineReport(generatedAt time.Time, dependency string) report.Report {
	return report.Report{
		SchemaVersion: report.SchemaVersion,
		GeneratedAt:   generatedAt,
		RepoPath:      "/repo/app",
		Dependencies: []report.DependencyReport{{
			Language:          "go",
			Name:              dependency,
			UsedExportsCount:  1,
			TotalExportsCount: 2,
			UsedPercent:       50,
		}},
	}
}
