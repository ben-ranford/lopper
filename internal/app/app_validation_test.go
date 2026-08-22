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

func TestExecuteAnalyseFailOnIncreaseZeroToleranceThreshold(t *testing.T) {
	delta := 0.1
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
		FailOnIncreasePercent:             0,
		MaxUncertainImportCount:           -1,
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
		SchemaVersion: report.SchemaVersion,
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
	if updated.SchemaVersion != report.SchemaVersion {
		t.Fatalf("expected current report schema version %q to be preserved, got %q", report.SchemaVersion, updated.SchemaVersion)
	}
}

func TestApplyBaselineIfNeededPropagatesAmbiguousLegacyIdentityBridge(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, testBaselinePath)
	data := `{"schemaVersion":"0.1.0","generatedAt":"2026-01-01T00:00:00Z","repoPath":".","dependencies":[{"language":"js-ts","name":"lib","usedExportsCount":1,"totalExportsCount":2,"usedPercent":50}]}` + "\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("write baseline: %v", err)
	}

	application := &App{Formatter: report.NewFormatter()}
	current := report.Report{
		SchemaVersion: report.SchemaVersion,
		Dependencies: []report.DependencyReport{
			{Name: "lib", Language: "js-ts", UsedExportsCount: 1, TotalExportsCount: 2, UsedPercent: 50, Identity: &report.DependencyIdentity{PURL: "pkg:npm/lib@1.0.0"}},
			{Name: "lib", Language: "js-ts", UsedExportsCount: 1, TotalExportsCount: 2, UsedPercent: 50, Identity: &report.DependencyIdentity{PURL: "pkg:npm/lib@2.0.0"}},
		},
	}

	_, err := application.applyBaselineIfNeeded(current, ".", AnalyseRequest{BaselinePath: path, Format: report.FormatJSON})
	if !errors.Is(err, report.ErrBaselineAmbiguousIdentityBridge) {
		t.Fatalf("expected ambiguous identity bridge error, got %v", err)
	}
	if !strings.Contains(err.Error(), "regenerate the baseline") {
		t.Fatalf("expected actionable regenerate-baseline guidance, got %v", err)
	}
}

func TestValidateFailOnIncreaseRequiresBaseline(t *testing.T) {
	err := validateFailOnIncrease(report.Report{}, 2)
	if !errors.Is(err, ErrBaselineRequired) {
		t.Fatalf("expected ErrBaselineRequired, got %v", err)
	}
	if err := validateFailOnIncrease(report.Report{}, 0); !errors.Is(err, ErrBaselineRequired) {
		t.Fatalf("expected zero-threshold fail-on-increase to require baseline, got %v", err)
	}
	if err := validateFailOnIncrease(report.Report{}, -1); err != nil {
		t.Fatalf("expected no error when threshold disabled via -1 sentinel, got %v", err)
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

func TestValidateReachableVulnerabilityThreshold(t *testing.T) {
	reportData := report.Report{
		Dependencies: []report.DependencyReport{
			{
				Name: "reachable",
				Vulnerabilities: []report.VulnerabilityFinding{
					{
						AdvisoryID: "GHSA-reachable",
						Package:    "reachable",
						Priority:   report.VulnerabilityPriorityMedium,
						Reachable:  true,
					},
				},
			},
		},
	}

	if err := validateReachableVulnerabilityThreshold(reportData, report.VulnerabilityPriorityHigh); err != nil {
		t.Fatalf("expected no error below threshold, got %v", err)
	}
	if err := validateReachableVulnerabilityThreshold(reportData, report.VulnerabilityPriorityMedium); !errors.Is(err, ErrReachableVulnerabilities) {
		t.Fatalf("expected reachable vulnerability threshold error, got %v", err)
	}
	if err := validateReachableVulnerabilityThreshold(reportData, report.VulnerabilityPriorityOff); err != nil {
		t.Fatalf("expected off threshold to disable validation, got %v", err)
	}
	if err := validateReachableVulnerabilityThreshold(reportData, "urgent"); err == nil {
		t.Fatalf("expected invalid reachable vulnerability threshold error")
	}
}

func TestValidateReachableVulnerabilityThresholdFailsClosedForOversizedRubyGemspecCoverage(t *testing.T) {
	warningCases := []struct {
		name    string
		warning string
	}{
		{
			name:    "lowercase extension",
			warning: "skipped oversized.gemspec because it exceeds 1048576 bytes",
		},
		{
			name:    "uppercase extension",
			warning: "skipped oversized.GEMSPEC because it exceeds 1048576 bytes",
		},
		{
			name:    "mixed case extension",
			warning: "skipped oversized.GeMsPeC because it exceeds 1048576 bytes",
		},
		{
			name:    "unicode simple-fold extension",
			warning: "skipped oversized.gem\u017fpec because it exceeds 1048576 bytes",
		},
		{
			name:    "review warning prefix preserves lowercase extension",
			warning: "head abcdef123456: skipped oversized.gemspec because it exceeds 1048576 bytes",
		},
		{
			name:    "review warning prefix preserves unicode simple-fold extension",
			warning: "head abcdef123456: skipped oversized.gem\u017fpec because it exceeds 1048576 bytes",
		},
	}

	for _, tc := range warningCases {
		t.Run(tc.name, func(t *testing.T) {
			oversized := report.Report{
				Warnings: []string{tc.warning},
			}
			if err := validateReachableVulnerabilityThreshold(oversized, report.VulnerabilityPriorityHigh); !errors.Is(err, ErrReachableVulnerabilities) {
				t.Fatalf("expected oversized gemspec coverage to fail closed under reachable-vulnerability threshold, got %v", err)
			}

			if err := validateReachableVulnerabilityThreshold(oversized, report.VulnerabilityPriorityOff); err != nil {
				t.Fatalf("expected off threshold to allow oversized gemspec warning, got %v", err)
			}
		})
	}

	exactLimit := report.Report{}
	if err := validateReachableVulnerabilityThreshold(exactLimit, report.VulnerabilityPriorityHigh); err != nil {
		t.Fatalf("expected exact-limit gemspec coverage without warning to pass, got %v", err)
	}
}

func TestEqualFoldCutPrefixHandlesUnicodeSimpleFoldPrefix(t *testing.T) {
	suffix, ok := equalFoldCutPrefix("\u017fkipped oversized.gemspec because it exceeds 1048576 bytes", "skipped ")
	if !ok {
		t.Fatalf("expected unicode long-s prefix to match")
	}
	if suffix != "oversized.gemspec because it exceeds 1048576 bytes" {
		t.Fatalf("unexpected suffix %q", suffix)
	}
}

func TestValidateReachableVulnerabilityThresholdUsesBaselineNewFindings(t *testing.T) {
	reportData := report.Report{
		Dependencies: []report.DependencyReport{
			{
				Name: "existing",
				Vulnerabilities: []report.VulnerabilityFinding{
					{
						AdvisoryID: "GHSA-existing",
						Package:    "existing",
						Priority:   report.VulnerabilityPriorityCritical,
						Reachable:  true,
					},
				},
			},
		},
		BaselineComparison: &report.BaselineComparison{},
	}
	if err := validateReachableVulnerabilityThreshold(reportData, report.VulnerabilityPriorityHigh); err != nil {
		t.Fatalf("expected baseline mode to ignore existing current findings, got %v", err)
	}

	reportData.BaselineComparison.NewReachableVulnerabilities = []report.VulnerabilityDelta{
		{
			Name:       "new",
			AdvisoryID: "GHSA-new",
			Package:    "new",
			Priority:   report.VulnerabilityPriorityHigh,
		},
	}
	if err := validateReachableVulnerabilityThreshold(reportData, report.VulnerabilityPriorityHigh); !errors.Is(err, ErrReachableVulnerabilities) {
		t.Fatalf("expected baseline new reachable vulnerability error, got %v", err)
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
	if err := validateUncertaintyThreshold(reportData, 0); !errors.Is(err, ErrUncertaintyThresholdExceeded) {
		t.Fatalf("expected zero-threshold uncertainty validation error, got %v", err)
	}
	if err := validateUncertaintyThreshold(reportData, -1); err != nil {
		t.Fatalf("expected -1 sentinel to disable uncertainty threshold, got %v", err)
	}
}

func TestExecuteAnalyseUncertaintyThresholdError(t *testing.T) {
	analyzer := &fakeAnalyzer{
		report: report.Report{
			RepoPath:     ".",
			Dependencies: []report.DependencyReport{{Name: "lodash", UsedExportsCount: 1, TotalExportsCount: 2, UsedPercent: 50}},
			UsageUncertainty: &report.UsageUncertainty{
				UncertainImportUses: 1,
			},
		},
	}
	application := &App{Analyzer: analyzer, Formatter: report.NewFormatter()}

	req := DefaultRequest()
	req.Mode = ModeAnalyse
	req.Analyse.TopN = 1
	req.Analyse.Format = report.FormatJSON
	req.Analyse.Thresholds.MaxUncertainImportCount = 0

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

func TestExecuteAnalyseReachableVulnerabilityThresholdError(t *testing.T) {
	tmp := t.TempDir()
	advisoryPath := filepath.Join(tmp, "advisories.yml")
	advisorySource := `advisories:
  - id: GHSA-threshold
    package: reachable-lib
    ecosystem: npm
    severity: high
    fixedVersion: 1.2.3
    source: security-team
`
	if err := os.WriteFile(advisoryPath, []byte(advisorySource), 0o600); err != nil {
		t.Fatalf("write advisory source: %v", err)
	}
	analyzer := &fakeAnalyzer{
		report: report.Report{
			RepoPath: tmp,
			Dependencies: []report.DependencyReport{
				{
					Language:          "js-ts",
					Name:              "reachable-lib",
					UsedExportsCount:  1,
					TotalExportsCount: 1,
					UsedPercent:       100,
					UsedImports: []report.ImportUse{
						{Name: "default", Module: "reachable-lib"},
					},
				},
			},
		},
	}
	application := &App{Analyzer: analyzer, Formatter: report.NewFormatter()}

	req := DefaultRequest()
	req.Mode = ModeAnalyse
	req.RepoPath = tmp
	req.Analyse.TopN = 1
	req.Analyse.Format = report.FormatJSON
	req.Analyse.AdvisorySourcePath = advisoryPath
	req.Analyse.Thresholds.ReachableVulnerabilityPriority = report.VulnerabilityPriorityHigh
	req.Analyse.Features = mustVulnerabilityPreviewFeatureSet(t)

	output, err := application.Execute(context.Background(), req)
	if !errors.Is(err, ErrReachableVulnerabilities) {
		t.Fatalf("expected reachable vulnerabilities error, got %v", err)
	}
	if !strings.Contains(output, `"vulnerabilities"`) || !strings.Contains(output, `"GHSA-threshold"`) {
		t.Fatalf("expected formatted vulnerability output on threshold failure, got %q", output)
	}
}

func TestExecuteAnalyseReachableVulnerabilityThresholdUsesOSVCVSSVector(t *testing.T) {
	advisorySource := `id: GHSA-osv-vector
severity:
  - type: CVSS_V3
    score: "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H"
affected:
  - package:
      ecosystem: npm
      name: reachable-lib
    versions:
      - "1.0.0"
`
	assertReachableVulnerabilityThresholdFromAdvisory(t, "osv.yml", advisorySource, "GHSA-osv-vector", "critical")
}

func TestExecuteAnalyseReachableVulnerabilityThresholdUsesAffectedOSVSeverity(t *testing.T) {
	advisorySource := `id: GHSA-multi-affected
affected:
  - package:
      ecosystem: npm
      name: safe-lib
    versions:
      - "1.0.0"
    database_specific:
      severity: low
  - package:
      ecosystem: npm
      name: reachable-lib
    versions:
      - "1.0.0"
    database_specific:
      severity: high
`
	assertReachableVulnerabilityThresholdFromAdvisory(t, "osv-multi.yml", advisorySource, "GHSA-multi-affected", "high")
}

func TestExecuteAnalyseReachableVulnerabilityThresholdFailsClosedForUnevaluableOSVMatches(t *testing.T) {
	tests := []struct {
		name           string
		fileName       string
		advisorySource string
		version        string
		wantID         string
	}{
		{
			name:     "blank installed version",
			fileName: "osv-blank-version.yml",
			advisorySource: `id: GHSA-osv-blank
affected:
  - package:
      ecosystem: npm
      name: reachable-lib
    versions:
      - "1.0.0"
`,
			version: "",
			wantID:  "GHSA-osv-blank",
		},
		{
			name:     "unsupported range type",
			fileName: "osv-unsupported-range.yml",
			advisorySource: `id: GHSA-osv-unsupported
affected:
  - package:
      ecosystem: npm
      name: reachable-lib
    ranges:
      - type: GIT
        events:
          - introduced: abc123
`,
			version: "1.0.0",
			wantID:  "GHSA-osv-unsupported",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assertReachableVulnerabilityThresholdFromAdvisoryVersion(t, tc.fileName, tc.advisorySource, tc.version, tc.wantID, "unknown", true)
		})
	}
}

func TestExecuteAnalyseReachableVulnerabilityThresholdSkipsConfirmedUnaffectedOSVMatch(t *testing.T) {
	advisorySource := `id: GHSA-osv-safe
affected:
  - package:
      ecosystem: npm
      name: reachable-lib
    ranges:
      - type: ECOSYSTEM
        events:
          - introduced: "0"
          - fixed: "1.0.0"
`
	assertReachableVulnerabilityThresholdFromAdvisoryVersion(t, "osv-safe.yml", advisorySource, "1.0.0", "GHSA-osv-safe", "low", false)
}

func assertReachableVulnerabilityThresholdFromAdvisory(t *testing.T, fileName string, advisorySource string, wantID string, wantSeverity string) {
	t.Helper()
	assertReachableVulnerabilityThresholdFromAdvisoryVersion(t, fileName, advisorySource, "1.0.0", wantID, wantSeverity, true)
}

func assertReachableVulnerabilityThresholdFromAdvisoryVersion(t *testing.T, fileName string, advisorySource string, version string, wantID string, wantSeverity string, wantThresholdError bool) {
	t.Helper()
	tmp := t.TempDir()
	advisoryPath := filepath.Join(tmp, fileName)
	if err := os.WriteFile(advisoryPath, []byte(advisorySource), 0o600); err != nil {
		t.Fatalf("write advisory source: %v", err)
	}
	analyzer := &fakeAnalyzer{
		report: report.Report{
			RepoPath: tmp,
			Dependencies: []report.DependencyReport{
				{
					Language:          "js-ts",
					Name:              "reachable-lib",
					Identity:          &report.DependencyIdentity{Ecosystem: "npm", Name: "reachable-lib", Version: version},
					UsedExportsCount:  1,
					TotalExportsCount: 1,
					UsedPercent:       100,
					UsedImports: []report.ImportUse{
						{Name: "default", Module: "reachable-lib"},
					},
				},
			},
		},
	}
	application := &App{Analyzer: analyzer, Formatter: report.NewFormatter()}

	req := DefaultRequest()
	req.Mode = ModeAnalyse
	req.RepoPath = tmp
	req.Analyse.TopN = 1
	req.Analyse.Format = report.FormatJSON
	req.Analyse.AdvisorySourcePath = advisoryPath
	req.Analyse.Thresholds.ReachableVulnerabilityPriority = report.VulnerabilityPriorityHigh
	req.Analyse.Features = mustVulnerabilityPreviewFeatureSet(t)

	output, err := application.Execute(context.Background(), req)
	if wantThresholdError {
		if !errors.Is(err, ErrReachableVulnerabilities) {
			t.Fatalf("expected reachable vulnerabilities error, got %v output=%q", err, output)
		}
		if !strings.Contains(output, `"`+wantID+`"`) || !strings.Contains(output, `"`+wantSeverity+`"`) {
			t.Fatalf("expected advisory %q with severity %q in output, got %q", wantID, wantSeverity, output)
		}
		return
	}
	if err != nil {
		t.Fatalf("expected no reachable vulnerability threshold error, got %v output=%q", err, output)
	}
	if strings.Contains(output, `"`+wantID+`"`) {
		t.Fatalf("expected confirmed unaffected advisory %q to stay out of output, got %q", wantID, output)
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
