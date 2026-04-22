package app

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/report"
)

func TestSaveBaselineIfNeeded(t *testing.T) {
	application := &App{Formatter: report.NewFormatter()}
	base := report.Report{
		SchemaVersion: "0.1.0",
		RepoPath:      ".",
		Dependencies: []report.DependencyReport{
			{Name: "dep", UsedExportsCount: 1, TotalExportsCount: 2, UsedPercent: 50},
		},
	}
	dir := t.TempDir()
	now := testTime()

	saveReq := AnalyseRequest{
		SaveBaseline:      true,
		BaselineStorePath: dir,
		BaselineLabel:     "nightly",
	}
	updated, err := application.saveBaselineIfNeeded(base, ".", saveReq, now)
	if err != nil {
		t.Fatalf("save baseline: %v", err)
	}
	if len(updated.Warnings) == 0 || !strings.Contains(updated.Warnings[0], "saved immutable baseline snapshot:") {
		t.Fatalf("expected save warning, got %#v", updated.Warnings)
	}
}

func TestSaveBaselineIfNeededRequiresStorePath(t *testing.T) {
	application := &App{Formatter: report.NewFormatter()}
	_, err := application.saveBaselineIfNeeded(report.Report{}, ".", AnalyseRequest{SaveBaseline: true}, testTime())
	if err == nil || !strings.Contains(err.Error(), saveBaselineStoreErr) {
		t.Fatalf("expected missing baseline-store error, got %v", err)
	}
}

func TestSaveBaselineIfNeededResolveSaveBaselineKeyError(t *testing.T) {
	application := &App{Formatter: report.NewFormatter()}
	nonRepo := filepath.Join(t.TempDir(), "nonexistent", "repo")
	saveReq := AnalyseRequest{
		SaveBaseline:      true,
		BaselineStorePath: t.TempDir(),
	}
	_, err := application.saveBaselineIfNeeded(report.Report{}, nonRepo, saveReq, testTime())
	if err == nil || !strings.Contains(err.Error(), "unable to resolve git commit") {
		t.Fatalf("expected save-baseline key resolution error, got %v", err)
	}
}

func TestResolveSaveBaselineKeyBranches(t *testing.T) {
	if key, err := resolveSaveBaselineKey(".", AnalyseRequest{BaselineLabel: "nightly"}); err != nil || key != "label:nightly" {
		t.Fatalf("expected label-based key, got key=%q err=%v", key, err)
	}
	if key, err := resolveSaveBaselineKey(".", AnalyseRequest{BaselineKey: "commit:abc"}); err != nil || key != "commit:abc" {
		t.Fatalf("expected explicit key, got key=%q err=%v", key, err)
	}
	if key, err := resolveSaveBaselineKey(".", AnalyseRequest{}); err != nil || !strings.HasPrefix(key, "commit:") {
		t.Fatalf("expected commit-derived key, got key=%q err=%v", key, err)
	}

	nonRepo := filepath.Join(t.TempDir(), "nonexistent", "repo")
	if _, err := resolveSaveBaselineKey(nonRepo, AnalyseRequest{}); err == nil || !strings.Contains(err.Error(), "unable to resolve git commit") {
		t.Fatalf("expected missing git key resolution error, got %v", err)
	}
}

func TestResolveBaselineComparisonPathsBranches(t *testing.T) {
	path, key, currentKey, shouldApply, err := resolveBaselineComparisonPaths(".", AnalyseRequest{BaselinePath: testBaselinePath})
	if err != nil {
		t.Fatalf("baseline path branch: %v", err)
	}
	if !shouldApply || path != testBaselinePath || key != "" {
		t.Fatalf("unexpected baseline path resolution: path=%q key=%q shouldApply=%v", path, key, shouldApply)
	}
	if currentKey == "" {
		t.Fatalf("expected current key to resolve in git repo")
	}

	path, key, currentKey, shouldApply, err = resolveBaselineComparisonPaths(".", AnalyseRequest{
		BaselineStorePath: baselineStorePath,
		BaselineKey:       "label:weekly",
	})
	if err != nil {
		t.Fatalf("baseline store branch: %v", err)
	}
	if !shouldApply || key != "label:weekly" || !strings.HasSuffix(path, "label_weekly.json") {
		t.Fatalf("unexpected baseline-store resolution: path=%q key=%q shouldApply=%v", path, key, shouldApply)
	}
	if currentKey == "" {
		t.Fatalf("expected current key with baseline-store branch")
	}

	nonRepo := filepath.Join(t.TempDir(), "nonexistent", "repo")
	if _, _, _, _, err := resolveBaselineComparisonPaths(nonRepo, AnalyseRequest{BaselineStorePath: baselineStorePath}); err == nil {
		t.Fatalf("expected baseline-store without key in non-git dir to fail")
	}
}

func TestResolveCurrentBaselineKeyBranches(t *testing.T) {
	if key := resolveCurrentBaselineKey("."); !strings.HasPrefix(key, "commit:") {
		t.Fatalf("expected commit key in repo, got %q", key)
	}
	nonRepo := filepath.Join(t.TempDir(), "nonexistent", "repo")
	if key := resolveCurrentBaselineKey(nonRepo); key != "" {
		t.Fatalf("expected empty key outside git repo, got %q", key)
	}
}

func TestSaveBaselineIfNeededAlreadyExistsError(t *testing.T) {
	application := &App{Formatter: report.NewFormatter()}
	base := report.Report{SchemaVersion: "0.1.0", RepoPath: "."}
	req := AnalyseRequest{
		SaveBaseline:      true,
		BaselineStorePath: t.TempDir(),
		BaselineLabel:     "nightly",
	}
	if _, err := application.saveBaselineIfNeeded(base, ".", req, testTime()); err != nil {
		t.Fatalf("first save baseline: %v", err)
	}
	if _, err := application.saveBaselineIfNeeded(base, ".", req, testTime()); err == nil {
		t.Fatalf("expected immutable baseline key reuse to fail")
	}
}

func TestSaveBaselineIfNeededDisabledNoop(t *testing.T) {
	application := &App{Formatter: report.NewFormatter()}
	input := report.Report{RepoPath: ".", Warnings: []string{"keep"}}
	updated, err := application.saveBaselineIfNeeded(input, ".", AnalyseRequest{}, testTime())
	if err != nil {
		t.Fatalf("save baseline noop: %v", err)
	}
	if len(updated.Warnings) != 1 || updated.Warnings[0] != "keep" {
		t.Fatalf("expected unchanged report on noop save baseline, got %#v", updated)
	}
}
