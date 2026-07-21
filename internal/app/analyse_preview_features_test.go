package app

import (
	"strings"
	"testing"
	"time"

	"github.com/ben-ranford/lopper/internal/dashboard"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/thresholds"
)

func TestValidateAnalysePreviewFeatureGates(t *testing.T) {
	cases := []struct {
		name    string
		req     AnalyseRequest
		feature string
		want    string
	}{
		{name: "cyclonedx", req: AnalyseRequest{Format: report.FormatCycloneDX}, feature: report.SBOMAttestationExportsPreviewFeature, want: "cyclonedx-json"},
		{name: "spdx", req: AnalyseRequest{Format: report.FormatSPDX}, feature: report.SPDXSBOMExportPreviewFeature, want: "spdx-json"},
		{name: "vex", req: AnalyseRequest{Format: report.FormatVEX}, feature: report.VulnerabilityExceptionsVEXPreviewFeature, want: "cyclonedx-vex-json"},
		{name: "exceptions", req: AnalyseRequest{VulnerabilityExceptions: []report.VulnerabilityException{{VulnerabilityID: "GHSA-test"}}}, feature: report.VulnerabilityExceptionsVEXPreviewFeature, want: "vulnerability exceptions"},
		{name: "advisory source", req: AnalyseRequest{AdvisorySourcePath: "advisories.json"}, feature: report.ReachabilityVulnerabilityPrioritizationPreviewFeature, want: "reachable vulnerability prioritization"},
		{name: "reachable threshold", req: AnalyseRequest{Thresholds: thresholds.Values{ReachableVulnerabilityPriority: report.VulnerabilityPriorityHigh}}, feature: report.ReachabilityVulnerabilityPrioritizationPreviewFeature, want: "reachable vulnerability prioritization"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateAnalyseFeatures(tc.req); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected preview gate error containing %q, got %v", tc.want, err)
			}
			tc.req.Features = mustResolveAppTestFeatures(t, tc.feature)
			if err := validateAnalyseFeatures(tc.req); err != nil {
				t.Fatalf("expected feature-enabled request to validate: %v", err)
			}
		})
	}
}

func TestValidateDashboardPreviewFeatureGates(t *testing.T) {
	if err := validateDashboardFeatures(DashboardRequest{}, resolvedDashboardRequest{format: dashboard.FormatSlackSummary}); err == nil || !strings.Contains(err.Error(), DashboardRemediationRoutingSummariesPreviewFeature) {
		t.Fatalf("expected slack summary feature gate, got %v", err)
	}
	if err := validateDashboardFeatures(DashboardRequest{Features: mustResolveAppTestFeatures(t, DashboardRemediationRoutingSummariesPreviewFeature)}, resolvedDashboardRequest{format: dashboard.FormatTeamsSummary}); err != nil {
		t.Fatalf("expected routing summary feature to allow team summaries: %v", err)
	}
	if err := validateDashboardFeatures(DashboardRequest{}, resolvedDashboardRequest{format: dashboard.FormatCycloneDXJSON}); err == nil || !strings.Contains(err.Error(), DashboardCycloneDXPortfolioPreviewFeature) {
		t.Fatalf("expected portfolio CycloneDX feature gate, got %v", err)
	}
	if err := validateDashboardFeatures(DashboardRequest{Features: mustResolveAppTestFeatures(t, DashboardCycloneDXPortfolioPreviewFeature)}, resolvedDashboardRequest{format: dashboard.FormatCycloneDXJSON}); err != nil {
		t.Fatalf("expected portfolio feature to allow CycloneDX dashboard: %v", err)
	}
	if err := validateDashboardFeatures(DashboardRequest{}, resolvedDashboardRequest{format: dashboard.FormatJSON}); err != nil {
		t.Fatalf("expected default dashboard format to validate: %v", err)
	}
}

func TestApplyVulnerabilityExceptionsIfNeededAddsDiagnostics(t *testing.T) {
	reportData := report.Report{Dependencies: []report.DependencyReport{{
		Name: "lib",
		Vulnerabilities: []report.VulnerabilityFinding{{
			AdvisoryID: "GHSA-test",
			Package:    "lib",
		}},
	}}}
	req := AnalyseRequest{VulnerabilityExceptions: []report.VulnerabilityException{{
		VulnerabilityID: "GHSA-test",
		Package:         "lib",
		Owner:           "security",
		Reason:          "temporary",
		Status:          "accepted-risk",
		Expires:         "2026-01-01",
	}}}

	got, err := applyVulnerabilityExceptionsIfNeeded(reportData, req, time.Date(2026, time.July, 13, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("apply vulnerability exceptions: %v", err)
	}
	if len(got.Warnings) != 1 || !strings.Contains(got.Warnings[0], "expired vulnerability exception restored GHSA-test") {
		t.Fatalf("expected expired exception warning, got %#v", got.Warnings)
	}
	if got.Summary == nil || got.Summary.Vulnerabilities == nil || got.Summary.Vulnerabilities.ReachableFindings != 0 {
		t.Fatalf("expected suppressed finding summary to be recomputed, got %#v", got.Summary)
	}

	unchanged, err := applyVulnerabilityExceptionsIfNeeded(reportData, AnalyseRequest{}, time.Time{})
	if err != nil || len(unchanged.Warnings) != 0 {
		t.Fatalf("expected no-op without exceptions, got warnings=%#v err=%v", unchanged.Warnings, err)
	}
}
