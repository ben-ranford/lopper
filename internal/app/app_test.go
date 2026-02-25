package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ben-ranford/lopper/internal/analysis"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/thresholds"
	"github.com/ben-ranford/lopper/internal/ui"
)

const testSnapshotPath = "snapshot.txt"

type fakeAnalyzer struct {
	report  report.Report
	err     error
	lastReq analysis.Request
}

func (f *fakeAnalyzer) Analyse(_ context.Context, req analysis.Request) (report.Report, error) {
	f.lastReq = req
	return f.report, f.err
}

type fakeTUI struct {
	startErr       error
	snapshotErr    error
	startCalled    bool
	snapshotCalled bool
	lastOptions    ui.Options
	lastSnapshot   string
}

func (f *fakeTUI) Start(_ context.Context, opts ui.Options) error {
	f.startCalled = true
	f.lastOptions = opts
	return f.startErr
}

func (f *fakeTUI) Snapshot(_ context.Context, opts ui.Options, outputPath string) error {
	f.snapshotCalled = true
	f.lastOptions = opts
	f.lastSnapshot = outputPath
	return f.snapshotErr
}

func TestExecuteAnalyseEmitsEffectiveThresholds(t *testing.T) {
	analyzer := &fakeAnalyzer{
		report: report.Report{
			RepoPath: ".",
			Dependencies: []report.DependencyReport{
				{Name: "lodash", UsedExportsCount: 1, TotalExportsCount: 2, UsedPercent: 50},
			},
		},
	}
	application := &App{Analyzer: analyzer, Formatter: report.NewFormatter()}

	req := DefaultRequest()
	req.Mode = ModeAnalyse
	req.Analyse.TopN = 1
	req.Analyse.Format = report.FormatJSON
	req.Analyse.SuggestOnly = true
	req.Analyse.RuntimeProfile = "browser-import"
	req.Analyse.CacheEnabled = false
	req.Analyse.CachePath = "/tmp/lopper-cache"
	req.Analyse.CacheReadOnly = true
	req.Analyse.PolicySources = []string{"cli", "defaults"}
	req.Analyse.Thresholds = thresholds.Values{
		FailOnIncreasePercent:             0,
		LowConfidenceWarningPercent:       33,
		MinUsagePercentForRecommendations: 44,
		RemovalCandidateWeightUsage:       0.6,
		RemovalCandidateWeightImpact:      0.2,
		RemovalCandidateWeightConfidence:  0.2,
	}

	output, err := application.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("execute analyse: %v", err)
	}
	if !strings.Contains(output, "\"effectiveThresholds\"") {
		t.Fatalf("expected effectiveThresholds in output JSON")
	}
	if !strings.Contains(output, "\"effectivePolicy\"") {
		t.Fatalf("expected effectivePolicy in output JSON")
	}
	if !strings.Contains(output, "\"sources\": [") || !strings.Contains(output, "\"cli\"") {
		t.Fatalf("expected policy sources in output JSON")
	}
	if !strings.Contains(output, "\"lowConfidenceWarningPercent\": 33") {
		t.Fatalf("expected lowConfidenceWarningPercent value in output JSON")
	}
	if analyzer.lastReq.LowConfidenceWarningPercent == nil || *analyzer.lastReq.LowConfidenceWarningPercent != 33 {
		t.Fatalf("expected low confidence threshold forwarded to analysis, got %#v", analyzer.lastReq.LowConfidenceWarningPercent)
	}
	if analyzer.lastReq.MinUsagePercentForRecommendations == nil || *analyzer.lastReq.MinUsagePercentForRecommendations != 44 {
		t.Fatalf("expected min usage threshold forwarded to analysis, got %#v", analyzer.lastReq.MinUsagePercentForRecommendations)
	}
	if analyzer.lastReq.RuntimeProfile != "browser-import" {
		t.Fatalf("expected runtime profile to be forwarded, got %q", analyzer.lastReq.RuntimeProfile)
	}
	if analyzer.lastReq.Cache == nil {
		t.Fatalf("expected cache options to be forwarded")
	}
	if analyzer.lastReq.Cache.Enabled || analyzer.lastReq.Cache.Path != "/tmp/lopper-cache" || !analyzer.lastReq.Cache.ReadOnly {
		t.Fatalf("unexpected cache options forwarded: %#v", analyzer.lastReq.Cache)
	}
	if !analyzer.lastReq.SuggestOnly {
		t.Fatalf("expected suggest-only flag to be forwarded")
	}
	if analyzer.lastReq.RemovalCandidateWeights == nil {
		t.Fatalf("expected removal candidate weights to be forwarded")
	}
	if analyzer.lastReq.RemovalCandidateWeights.Usage != 0.6 || analyzer.lastReq.RemovalCandidateWeights.Impact != 0.2 || analyzer.lastReq.RemovalCandidateWeights.Confidence != 0.2 {
		t.Fatalf("unexpected forwarded removal candidate weights: %#v", analyzer.lastReq.RemovalCandidateWeights)
	}
}

func TestNewApp(t *testing.T) {
	appInstance := New(&bytes.Buffer{}, strings.NewReader(""))
	if appInstance == nil {
		t.Fatalf("expected app instance")
	}
}

func TestExecuteAnalyseFailOnIncreaseThreshold(t *testing.T) {
	delta := 3.5
	analyzer := &fakeAnalyzer{
		report: report.Report{
			RepoPath:             ".",
			Dependencies:         []report.DependencyReport{{Name: "lodash", UsedExportsCount: 1, TotalExportsCount: 2, UsedPercent: 50}},
			WasteIncreasePercent: &delta,
		},
	}
	application := &App{Analyzer: analyzer, Formatter: report.NewFormatter()}

	req := DefaultRequest()
	req.Mode = ModeAnalyse
	req.Analyse.TopN = 1
	req.Analyse.Thresholds = thresholds.Values{
		FailOnIncreasePercent:             2,
		LowConfidenceWarningPercent:       thresholds.DefaultLowConfidenceWarningPercent,
		MinUsagePercentForRecommendations: thresholds.DefaultMinUsagePercentForRecommendations,
	}

	_, err := application.Execute(context.Background(), req)
	if err == nil {
		t.Fatalf("expected fail-on-increase error")
	}
	if err != ErrFailOnIncrease {
		t.Fatalf("expected ErrFailOnIncrease, got %v", err)
	}
}

func TestExecuteTUIStartAndSnapshot(t *testing.T) {
	tui := &fakeTUI{}
	application := &App{
		Analyzer:  &fakeAnalyzer{},
		Formatter: report.NewFormatter(),
		TUI:       tui,
	}

	req := DefaultRequest()
	req.Mode = ModeTUI
	req.TUI.TopN = 5
	req.TUI.Language = "all"
	req.TUI.Filter = "lod"
	req.TUI.Sort = "name"
	req.TUI.PageSize = 3

	if _, err := application.Execute(context.Background(), req); err != nil {
		t.Fatalf("execute tui start: %v", err)
	}
	if !tui.startCalled || tui.snapshotCalled {
		t.Fatalf("expected Start to be called only once")
	}

	req.TUI.SnapshotPath = testSnapshotPath
	if _, err := application.Execute(context.Background(), req); err != nil {
		t.Fatalf("execute tui snapshot: %v", err)
	}
	if !tui.snapshotCalled || tui.lastSnapshot != testSnapshotPath {
		t.Fatalf("expected Snapshot call with output path, got called=%v path=%q", tui.snapshotCalled, tui.lastSnapshot)
	}
}

func TestExecuteUnknownMode(t *testing.T) {
	application := &App{Analyzer: &fakeAnalyzer{}, Formatter: report.NewFormatter()}
	_, err := application.Execute(context.Background(), Request{Mode: "unknown"})
	if !errors.Is(err, ErrUnknownMode) {
		t.Fatalf("expected ErrUnknownMode, got %v", err)
	}
}

func TestApplyBaselineIfNeededWithBaselineFile(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "baseline.json")
	data := `{"schemaVersion":"0.1.0","generatedAt":"2026-01-01T00:00:00Z","repoPath":".","dependencies":[{"name":"dep","usedExportsCount":5,"totalExportsCount":10,"usedPercent":50,"estimatedUnusedBytes":0}]}` + "\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("write baseline: %v", err)
	}

	application := &App{Formatter: report.NewFormatter()}
	current := report.Report{
		Dependencies: []report.DependencyReport{
			{Name: "dep", UsedExportsCount: 4, TotalExportsCount: 10, UsedPercent: 40},
		},
	}
	updated, err := application.applyBaselineIfNeeded(current, ".", AnalyseRequest{BaselinePath: path, Format: report.FormatJSON})
	if err != nil {
		t.Fatalf("apply baseline: %v", err)
	}
	if updated.WasteIncreasePercent == nil {
		t.Fatalf("expected waste increase to be computed")
	}
	if updated.BaselineComparison == nil {
		t.Fatalf("expected baseline comparison details to be present")
	}
}

func TestValidateFailOnIncreaseRequiresBaseline(t *testing.T) {
	err := validateFailOnIncrease(report.Report{}, 2)
	if !errors.Is(err, ErrBaselineRequired) {
		t.Fatalf("expected ErrBaselineRequired, got %v", err)
	}
	if err := validateFailOnIncrease(report.Report{}, 0); err != nil {
		t.Fatalf("expected no error when threshold disabled, got %v", err)
	}
}

func TestExecuteAnalyseAnalyzerError(t *testing.T) {
	expected := errors.New("analyse failed")
	application := &App{
		Analyzer:  &fakeAnalyzer{err: expected},
		Formatter: report.NewFormatter(),
	}
	req := DefaultRequest()
	req.Mode = ModeAnalyse
	req.Analyse.Dependency = "lodash"
	_, err := application.Execute(context.Background(), req)
	if !errors.Is(err, expected) {
		t.Fatalf("expected analyzer error, got %v", err)
	}
}

func TestApplyBaselineIfNeededFormatAndLoadErrors(t *testing.T) {
	application := &App{Formatter: report.NewFormatter()}

	_, err := application.applyBaselineIfNeeded(report.Report{}, ".", AnalyseRequest{
		Format:       report.FormatJSON,
		BaselinePath: filepath.Join(t.TempDir(), "missing.json"),
	})
	if err == nil {
		t.Fatalf("expected missing baseline load error")
	}

	_, err = application.applyBaselineIfNeeded(report.Report{}, ".", AnalyseRequest{
		Format:            report.FormatJSON,
		BaselineStorePath: filepath.Join(t.TempDir(), "baselines"),
	})
	if err == nil {
		t.Fatalf("expected baseline-store comparison error without key/commit")
	}
}

func TestValidateFailOnIncreaseAllowsWithinThreshold(t *testing.T) {
	delta := 2.0
	err := validateFailOnIncrease(report.Report{WasteIncreasePercent: &delta}, 2)
	if err != nil {
		t.Fatalf("expected no error at threshold boundary, got %v", err)
	}
}

func TestExecuteTUIPropagatesErrors(t *testing.T) {
	tui := &fakeTUI{startErr: errors.New("start failed")}
	application := &App{
		Analyzer:  &fakeAnalyzer{},
		Formatter: report.NewFormatter(),
		TUI:       tui,
	}
	req := DefaultRequest()
	req.Mode = ModeTUI
	if _, err := application.Execute(context.Background(), req); err == nil {
		t.Fatalf("expected start error")
	}

	tui = &fakeTUI{snapshotErr: errors.New("snapshot failed")}
	application.TUI = tui
	req.TUI.SnapshotPath = testSnapshotPath
	if _, err := application.Execute(context.Background(), req); err == nil {
		t.Fatalf("expected snapshot error")
	}
}

func TestExecuteAnalyseBaselineAndApplyBaselineErrors(t *testing.T) {
	analyzer := &fakeAnalyzer{
		report: report.Report{
			Dependencies: []report.DependencyReport{
				{Name: "dep", UsedExportsCount: 1, TotalExportsCount: 2, UsedPercent: 50},
			},
		},
	}
	application := &App{Analyzer: analyzer, Formatter: report.NewFormatter()}

	req := DefaultRequest()
	req.Mode = ModeAnalyse
	req.Analyse.Dependency = "dep"
	req.Analyse.Format = report.FormatJSON
	req.Analyse.BaselinePath = filepath.Join(t.TempDir(), "missing.json")
	if _, err := application.Execute(context.Background(), req); err == nil {
		t.Fatalf("expected execute analyse error when baseline path is missing")
	}

	tmp := t.TempDir()
	baselinePath := filepath.Join(tmp, "baseline.json")
	content := `{"schemaVersion":"0.1.0","generatedAt":"2026-01-01T00:00:00Z","repoPath":".","dependencies":[{"name":"dep","usedExportsCount":0,"totalExportsCount":0,"usedPercent":0}]}` + "\n"
	if err := os.WriteFile(baselinePath, []byte(content), 0o600); err != nil {
		t.Fatalf("write baseline file: %v", err)
	}
	current := report.Report{
		Dependencies: []report.DependencyReport{{Name: "dep", UsedExportsCount: 1, TotalExportsCount: 2, UsedPercent: 50}},
	}
	_, err := application.applyBaselineIfNeeded(current, ".", AnalyseRequest{BaselinePath: baselinePath, Format: report.FormatJSON})
	if err == nil {
		t.Fatalf("expected baseline application error for zero baseline totals")
	}
}

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
	if err == nil || !strings.Contains(err.Error(), "--save-baseline requires --baseline-store") {
		t.Fatalf("expected missing baseline-store error, got %v", err)
	}
}

func TestResolveSaveBaselineKeyBranches(t *testing.T) {
	if key, err := resolveSaveBaselineKey(".", AnalyseRequest{BaselineLabel: "nightly"}); err != nil || key != "label:nightly" {
		t.Fatalf("expected label-based key, got key=%q err=%v", key, err)
	}
	if key, err := resolveSaveBaselineKey(".", AnalyseRequest{BaselineKey: "commit:abc"}); err != nil || key != "commit:abc" {
		t.Fatalf("expected explicit key, got key=%q err=%v", key, err)
	}

	nonRepo := filepath.Join(t.TempDir(), "nonexistent", "repo")
	if _, err := resolveSaveBaselineKey(nonRepo, AnalyseRequest{}); err == nil || !strings.Contains(err.Error(), "unable to resolve git commit") {
		t.Fatalf("expected missing git key resolution error, got %v", err)
	}
}

func TestResolveBaselineComparisonPathsBranches(t *testing.T) {
	path, key, currentKey, shouldApply, err := resolveBaselineComparisonPaths(".", AnalyseRequest{BaselinePath: "baseline.json"})
	if err != nil {
		t.Fatalf("baseline path branch: %v", err)
	}
	if !shouldApply || path != "baseline.json" || key != "" {
		t.Fatalf("unexpected baseline path resolution: path=%q key=%q shouldApply=%v", path, key, shouldApply)
	}
	if currentKey == "" {
		t.Fatalf("expected current key to resolve in git repo")
	}

	path, key, currentKey, shouldApply, err = resolveBaselineComparisonPaths(".", AnalyseRequest{
		BaselineStorePath: ".artifacts/baselines",
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
	if _, _, _, _, err := resolveBaselineComparisonPaths(nonRepo, AnalyseRequest{BaselineStorePath: ".artifacts/baselines"}); err == nil {
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

func TestExecuteAnalyseForwardsRustRecommendationThreshold(t *testing.T) {
	analyzer := &fakeAnalyzer{
		report: report.Report{
			RepoPath: ".",
			Dependencies: []report.DependencyReport{
				{Name: "serde", Language: "rust", UsedExportsCount: 1, TotalExportsCount: 4, UsedPercent: 25},
			},
		},
	}
	application := &App{Analyzer: analyzer, Formatter: report.NewFormatter()}

	req := DefaultRequest()
	req.Mode = ModeAnalyse
	req.Analyse.Language = "rust"
	req.Analyse.Dependency = "serde"
	req.Analyse.Format = report.FormatJSON
	req.Analyse.Thresholds = thresholds.Values{
		FailOnIncreasePercent:             0,
		LowConfidenceWarningPercent:       35,
		MinUsagePercentForRecommendations: 70,
	}

	if _, err := application.Execute(context.Background(), req); err != nil {
		t.Fatalf("execute analyse: %v", err)
	}
	if analyzer.lastReq.MinUsagePercentForRecommendations == nil || *analyzer.lastReq.MinUsagePercentForRecommendations != 70 {
		t.Fatalf("expected min-usage threshold to be forwarded for rust analysis, got %#v", analyzer.lastReq.MinUsagePercentForRecommendations)
	}
}

func TestPrepareRuntimeTraceWithRuntimeCommand(t *testing.T) {
	repo := t.TempDir()
	req := DefaultRequest()
	req.RepoPath = repo
	req.Analyse.RuntimeTestCommand = "make -v"

	warnings, tracePath := prepareRuntimeTrace(context.Background(), req)
	if len(warnings) != 0 {
		t.Fatalf("did not expect warnings from runtime capture: %#v", warnings)
	}
	if tracePath != filepath.Join(repo, ".artifacts", "lopper-runtime.ndjson") {
		t.Fatalf("unexpected trace path: %q", tracePath)
	}
	if _, err := os.Stat(filepath.Dir(tracePath)); err != nil {
		t.Fatalf("expected runtime trace directory to exist: %v", err)
	}
}

func testTime() time.Time {
	return time.Date(2026, time.February, 22, 15, 0, 0, 0, time.UTC)
}

func TestPrepareRuntimeTraceFailureReturnsWarning(t *testing.T) {
	repo := t.TempDir()
	req := DefaultRequest()
	req.RepoPath = repo
	req.Analyse.RuntimeTestCommand = "make __missing_target__"

	warnings, tracePath := prepareRuntimeTrace(context.Background(), req)
	if len(warnings) != 1 {
		t.Fatalf("expected one runtime warning, got %#v", warnings)
	}
	if !strings.Contains(warnings[0], "runtime trace command failed") {
		t.Fatalf("unexpected warning: %q", warnings[0])
	}
	if tracePath != "" {
		t.Fatalf("expected trace path to be cleared on capture failure when path was auto-generated, got %q", tracePath)
	}
}

func TestExecuteAnalyseIncludesRuntimeCaptureWarnings(t *testing.T) {
	analyzer := &fakeAnalyzer{
		report: report.Report{
			RepoPath: ".",
			Dependencies: []report.DependencyReport{
				{Name: "lodash", UsedExportsCount: 1, TotalExportsCount: 2, UsedPercent: 50},
			},
		},
	}
	application := &App{Analyzer: analyzer, Formatter: report.NewFormatter()}

	req := DefaultRequest()
	req.Mode = ModeAnalyse
	req.RepoPath = t.TempDir()
	req.Analyse.TopN = 1
	req.Analyse.Format = report.FormatJSON
	req.Analyse.RuntimeTestCommand = "make __missing_target__"

	output, err := application.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("execute analyse with runtime warning: %v", err)
	}
	if !strings.Contains(output, "runtime trace command failed; continuing with static analysis") {
		t.Fatalf("expected runtime warning in output, got %q", output)
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

func TestExecuteAnalyseApplyBaselineErrorPreservesOriginalWhenFormatFails(t *testing.T) {
	analyzer := &fakeAnalyzer{
		report: report.Report{
			RepoPath: ".",
			Dependencies: []report.DependencyReport{
				{Name: "dep", UsedExportsCount: 1, TotalExportsCount: 2, UsedPercent: 50},
			},
		},
	}
	application := &App{Analyzer: analyzer, Formatter: report.NewFormatter()}
	req := DefaultRequest()
	req.Mode = ModeAnalyse
	req.Analyse.TopN = 1
	req.Analyse.Format = report.Format("invalid")
	req.Analyse.BaselinePath = filepath.Join(t.TempDir(), "missing.json")

	_, err := application.Execute(context.Background(), req)
	if err == nil {
		t.Fatalf("expected apply-baseline error")
	}
	if strings.Contains(strings.ToLower(err.Error()), "unknown format") {
		t.Fatalf("expected original baseline error, got %v", err)
	}
}

func TestExecuteAnalyseFailOnIncreasePreservesOriginalWhenFormatFails(t *testing.T) {
	delta := 5.0
	analyzer := &fakeAnalyzer{
		report: report.Report{
			RepoPath:             ".",
			WasteIncreasePercent: &delta,
			Dependencies:         []report.DependencyReport{{Name: "dep", UsedExportsCount: 1, TotalExportsCount: 2, UsedPercent: 50}},
		},
	}
	application := &App{Analyzer: analyzer, Formatter: report.NewFormatter()}
	req := DefaultRequest()
	req.Mode = ModeAnalyse
	req.Analyse.TopN = 1
	req.Analyse.Format = report.Format("invalid")
	req.Analyse.Thresholds.FailOnIncreasePercent = 1

	_, err := application.Execute(context.Background(), req)
	if err != ErrFailOnIncrease {
		t.Fatalf("expected ErrFailOnIncrease, got %v", err)
	}
}

func TestExecuteAnalyseSaveBaselineErrorPreservesOriginalWhenFormatFails(t *testing.T) {
	analyzer := &fakeAnalyzer{
		report: report.Report{
			RepoPath: ".",
			Dependencies: []report.DependencyReport{
				{Name: "dep", UsedExportsCount: 1, TotalExportsCount: 2, UsedPercent: 50},
			},
		},
	}
	application := &App{Analyzer: analyzer, Formatter: report.NewFormatter()}
	req := DefaultRequest()
	req.Mode = ModeAnalyse
	req.Analyse.TopN = 1
	req.Analyse.Format = report.Format("invalid")
	req.Analyse.SaveBaseline = true

	_, err := application.Execute(context.Background(), req)
	if err == nil {
		t.Fatalf("expected save-baseline error")
	}
	if !strings.Contains(err.Error(), "--save-baseline requires --baseline-store") {
		t.Fatalf("expected save-baseline store error, got %v", err)
	}
}
