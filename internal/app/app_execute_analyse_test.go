package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/featureflags"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/thresholds"
)

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
	req.Analyse.ScopeMode = ScopeModeChangedPackages
	req.Analyse.Format = report.FormatJSON
	req.Analyse.SuggestOnly = true
	req.Analyse.RuntimeProfile = "browser-import"
	req.Analyse.CacheEnabled = false
	req.Analyse.CachePath = "/tmp/lopper-cache"
	req.Analyse.CacheReadOnly = true
	req.Analyse.Features = mustEnabledPreviewFeatureSet(t)
	req.Analyse.PolicySources = []string{"cli", "defaults"}
	req.Analyse.Thresholds = thresholds.Values{
		FailOnIncreasePercent:             0,
		LowConfidenceWarningPercent:       33,
		MinUsagePercentForRecommendations: 44,
		RemovalCandidateWeightUsage:       0.6,
		RemovalCandidateWeightImpact:      0.2,
		RemovalCandidateWeightConfidence:  0.2,
	}
	featureRegistry, err := featureflags.NewRegistry([]featureflags.Flag{{
		Code:      "LOP-FEAT-0001",
		Name:      "powershell-adapter-preview",
		Lifecycle: featureflags.LifecyclePreview,
	}})
	if err != nil {
		t.Fatalf("new feature registry: %v", err)
	}
	resolvedFeatures, err := featureRegistry.Resolve(featureflags.ResolveOptions{
		Channel: featureflags.ChannelDev,
		Enable:  []string{"powershell-adapter-preview"},
	})
	if err != nil {
		t.Fatalf("resolve feature set: %v", err)
	}
	req.Analyse.Features = resolvedFeatures

	output, err := application.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf(executeAnalyseErrFmt, err)
	}
	assertContainsAll(t, output, []string{`"effectiveThresholds"`, `"effectivePolicy"`, `"sources": [`, `"cli"`, `"lowConfidenceWarningPercent": 33`})
	assertForwardedAnalyseRequest(t, analyzer.lastReq)
	if !analyzer.lastReq.Features.Enabled("powershell-adapter-preview") {
		t.Fatalf("expected feature set to be forwarded to analysis request")
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
		t.Fatalf(executeAnalyseErrFmt, err)
	}
	if analyzer.lastReq.MinUsagePercentForRecommendations == nil || *analyzer.lastReq.MinUsagePercentForRecommendations != 70 {
		t.Fatalf("expected min-usage threshold to be forwarded for rust analysis, got %#v", analyzer.lastReq.MinUsagePercentForRecommendations)
	}
}

func TestExecuteAnalyseForwardsFeatureFlags(t *testing.T) {
	analyzer := &fakeAnalyzer{
		report: report.Report{
			RepoPath:      ".",
			Dependencies:  []report.DependencyReport{{Name: "rxswift", Language: "swift"}},
			SchemaVersion: "0.1.0",
		},
	}
	application := &App{Analyzer: analyzer, Formatter: report.NewFormatter()}

	registry, err := featureflags.NewRegistry([]featureflags.Flag{{
		Code:      "LOP-FEAT-0001",
		Name:      "swift-carthage-preview",
		Lifecycle: featureflags.LifecyclePreview,
	}})
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	resolved, err := registry.Resolve(featureflags.ResolveOptions{
		Channel: featureflags.ChannelDev,
		Enable:  []string{"swift-carthage-preview"},
	})
	if err != nil {
		t.Fatalf("resolve feature set: %v", err)
	}

	req := DefaultRequest()
	req.Mode = ModeAnalyse
	req.Analyse.TopN = 1
	req.Analyse.Format = report.FormatJSON
	req.Analyse.Features = resolved

	if _, err := application.Execute(context.Background(), req); err != nil {
		t.Fatalf(executeAnalyseErrFmt, err)
	}
	if !analyzer.lastReq.Features.Enabled("swift-carthage-preview") {
		t.Fatalf("expected analyse request features to be forwarded, got %#v", analyzer.lastReq.Features)
	}
}

func TestExecuteAnalyseLockfileDriftWarnPolicy(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "package.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write package.json: %v", err)
	}
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
	req.RepoPath = repo
	req.Analyse.TopN = 1
	req.Analyse.Format = report.FormatJSON
	req.Analyse.Thresholds.LockfileDriftPolicy = "warn"

	output, err := application.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("execute analyse with lockfile drift warn: %v", err)
	}
	if !strings.Contains(output, "lockfile drift detected") {
		t.Fatalf("expected lockfile drift warning in output, got %q", output)
	}
}

func TestExecuteAnalyseLockfileDriftFailPolicy(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module example.com/demo\n\ngo 1.22\n\nrequire github.com/some/dep v1.0.0\n"), 0o600); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
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
	req.RepoPath = repo
	req.Analyse.TopN = 1
	req.Analyse.Thresholds.LockfileDriftPolicy = "fail"

	_, err := application.Execute(context.Background(), req)
	if !errors.Is(err, ErrLockfileDrift) {
		t.Fatalf("expected ErrLockfileDrift, got %v", err)
	}
	if analyzer.called {
		t.Fatalf("expected pre-analysis lockfile check to fail before analyzer execution")
	}
}

func TestExecuteAnalyseReturnsFormattedOutputWhenSaveBaselineValidationFails(t *testing.T) {
	analyzer := &fakeAnalyzer{
		report: report.Report{
			RepoPath:      ".",
			Dependencies:  []report.DependencyReport{{Name: "dep", UsedExportsCount: 1, TotalExportsCount: 2, UsedPercent: 50}},
			SchemaVersion: "0.1.0",
		},
	}
	application := &App{Analyzer: analyzer, Formatter: report.NewFormatter()}

	req := DefaultRequest()
	req.Mode = ModeAnalyse
	req.Analyse.TopN = 1
	req.Analyse.Format = report.FormatJSON
	req.Analyse.SaveBaseline = true

	output, err := application.Execute(context.Background(), req)
	if err == nil {
		t.Fatalf("expected save-baseline validation error")
	}
	if !strings.Contains(err.Error(), saveBaselineStoreErr) {
		t.Fatalf("unexpected save-baseline error: %v", err)
	}
	if !strings.Contains(output, "\"dependencies\"") {
		t.Fatalf("expected formatted output to be returned alongside error, got %q", output)
	}
}

func TestExecuteAnalyseReturnsFormatterErrorWhenNoPriorError(t *testing.T) {
	analyzer := &fakeAnalyzer{
		report: report.Report{
			RepoPath:     ".",
			Dependencies: []report.DependencyReport{{Name: "dep", UsedExportsCount: 1, TotalExportsCount: 2, UsedPercent: 50}},
		},
	}
	application := &App{Analyzer: analyzer, Formatter: report.NewFormatter()}

	req := DefaultRequest()
	req.Mode = ModeAnalyse
	req.Analyse.TopN = 1
	req.Analyse.Format = report.Format("invalid")

	_, err := application.Execute(context.Background(), req)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "unknown format") {
		t.Fatalf("expected formatter error, got %v", err)
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
	req.Analyse.BaselinePath = filepath.Join(t.TempDir(), missingBaselineFileName)

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
	if !errors.Is(err, ErrFailOnIncrease) {
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
	if !strings.Contains(err.Error(), saveBaselineStoreErr) {
		t.Fatalf("expected save-baseline store error, got %v", err)
	}
}
