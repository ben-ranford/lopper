package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/thresholds"
)

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
	if !errors.Is(err, ErrFailOnIncrease) {
		t.Fatalf("expected ErrFailOnIncrease, got %v", err)
	}
}

func TestApplyBaselineIfNeededWithBaselineFile(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, testBaselinePath)
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

func TestValidateDeniedLicenses(t *testing.T) {
	reportData := report.Report{
		Dependencies: []report.DependencyReport{
			{Name: "a", License: &report.DependencyLicense{SPDX: "MIT", Denied: false}},
			{Name: "b", License: &report.DependencyLicense{SPDX: deniedLicenseSPDX, Denied: true}},
		},
	}
	if err := validateDeniedLicenses(reportData, true); !errors.Is(err, ErrDeniedLicenses) {
		t.Fatalf("expected denied license error, got %v", err)
	}
	if err := validateDeniedLicenses(reportData, false); err != nil {
		t.Fatalf("expected no error when policy disabled, got %v", err)
	}
}

func TestValidateDeniedLicensesNoDeniedReturnsNil(t *testing.T) {
	reportData := report.Report{
		Dependencies: []report.DependencyReport{
			{Name: "a", License: &report.DependencyLicense{SPDX: "MIT", Denied: false}},
		},
	}
	if err := validateDeniedLicenses(reportData, true); err != nil {
		t.Fatalf("expected no denied license error, got %v", err)
	}
}

func TestValidateDeniedLicensesBaselineNewDeniedBranch(t *testing.T) {
	reportData := report.Report{
		Dependencies: []report.DependencyReport{
			{Name: "safe", License: &report.DependencyLicense{SPDX: "MIT", Denied: false}},
		},
		BaselineComparison: &report.BaselineComparison{
			NewDeniedLicenses: []report.DeniedLicenseDelta{
				{Name: "unsafe", Language: "js-ts", SPDX: deniedLicenseSPDX},
			},
		},
	}
	if err := validateDeniedLicenses(reportData, true); !errors.Is(err, ErrDeniedLicenses) {
		t.Fatalf("expected denied license error from baseline new-denied branch, got %v", err)
	}

	reportData.BaselineComparison.NewDeniedLicenses = nil
	reportData.Dependencies = []report.DependencyReport{
		{Name: "existing-denied", License: &report.DependencyLicense{SPDX: deniedLicenseSPDX, Denied: true}},
	}
	if err := validateDeniedLicenses(reportData, true); err != nil {
		t.Fatalf("expected no denied-license error for baseline mode without newly introduced denied licenses, got %v", err)
	}
}

func TestValidateUncertaintyThreshold(t *testing.T) {
	reportData := report.Report{
		UsageUncertainty: &report.UsageUncertainty{
			UncertainImportUses: 2,
		},
	}
	if err := validateUncertaintyThreshold(reportData, 2); err != nil {
		t.Fatalf("expected no uncertainty threshold error at boundary, got %v", err)
	}
	if err := validateUncertaintyThreshold(reportData, 1); !errors.Is(err, ErrUncertaintyThresholdExceeded) {
		t.Fatalf("expected uncertainty threshold error, got %v", err)
	}
}

func TestExecuteAnalyseUncertaintyThresholdError(t *testing.T) {
	analyzer := &fakeAnalyzer{
		report: report.Report{
			RepoPath:     ".",
			Dependencies: []report.DependencyReport{{Name: "lodash", UsedExportsCount: 1, TotalExportsCount: 2, UsedPercent: 50}},
			UsageUncertainty: &report.UsageUncertainty{
				UncertainImportUses: 2,
			},
		},
	}
	application := &App{Analyzer: analyzer, Formatter: report.NewFormatter()}

	req := DefaultRequest()
	req.Mode = ModeAnalyse
	req.Analyse.TopN = 1
	req.Analyse.Format = report.FormatJSON
	req.Analyse.Thresholds.MaxUncertainImportCount = 1

	output, err := application.Execute(context.Background(), req)
	if !errors.Is(err, ErrUncertaintyThresholdExceeded) {
		t.Fatalf("expected uncertainty threshold error, got %v", err)
	}
	if !strings.Contains(output, `"effectiveThresholds"`) {
		t.Fatalf("expected formatted output on threshold failure, got %q", output)
	}
}

func TestExecuteAnalyseDeniedLicensesError(t *testing.T) {
	analyzer := &fakeAnalyzer{
		report: report.Report{
			RepoPath: ".",
			Dependencies: []report.DependencyReport{
				{
					Name:    "copyleft",
					License: &report.DependencyLicense{SPDX: deniedLicenseSPDX, Denied: true},
				},
			},
		},
	}
	application := &App{Analyzer: analyzer, Formatter: report.NewFormatter()}

	req := DefaultRequest()
	req.Mode = ModeAnalyse
	req.Analyse.TopN = 1
	req.Analyse.Format = report.FormatJSON
	req.Analyse.Thresholds.LicenseFailOnDeny = true

	output, err := application.Execute(context.Background(), req)
	if !errors.Is(err, ErrDeniedLicenses) {
		t.Fatalf("expected denied licenses error, got %v", err)
	}
	if !strings.Contains(output, `"effectivePolicy"`) {
		t.Fatalf("expected formatted output on denied-license failure, got %q", output)
	}
}

func TestApplyBaselineIfNeededFormatAndLoadErrors(t *testing.T) {
	application := &App{Formatter: report.NewFormatter()}

	_, err := application.applyBaselineIfNeeded(report.Report{}, ".", AnalyseRequest{
		Format:       report.FormatJSON,
		BaselinePath: filepath.Join(t.TempDir(), missingBaselineFileName),
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
	req.Analyse.BaselinePath = filepath.Join(t.TempDir(), missingBaselineFileName)
	if _, err := application.Execute(context.Background(), req); err == nil {
		t.Fatalf("expected execute analyse error when baseline path is missing")
	}

	tmp := t.TempDir()
	baselinePath := filepath.Join(tmp, testBaselinePath)
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

func TestApplyBaselineIfNeededNoopWhenNoBaselineConfigured(t *testing.T) {
	application := &App{Formatter: report.NewFormatter()}
	input := report.Report{RepoPath: ".", Warnings: []string{"keep"}}

	updated, err := application.applyBaselineIfNeeded(input, ".", AnalyseRequest{})
	if err != nil {
		t.Fatalf("apply baseline noop: %v", err)
	}
	if len(updated.Warnings) != 1 || updated.Warnings[0] != "keep" {
		t.Fatalf("expected report to remain unchanged, got %#v", updated)
	}
}

func TestApplyBaselineIfNeededReturnsConfigResolutionError(t *testing.T) {
	application := &App{Formatter: report.NewFormatter()}
	input := report.Report{RepoPath: ".", Warnings: []string{"keep"}}

	_, err := application.applyBaselineIfNeeded(input, filepath.Join(t.TempDir(), "nonexistent", "repo"), AnalyseRequest{
		BaselineStorePath: ".artifacts/baselines",
	})
	if err == nil {
		t.Fatalf("expected baseline config resolution error")
	}
}
