package app

import (
	"testing"

	"github.com/ben-ranford/lopper/internal/analysis"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/thresholds"
)

func TestPrepareAnalysisPolicyRequiresCompleteCoverageForEnforcedGates(t *testing.T) {
	base := analysis.Request{Language: "all"}
	tests := []struct {
		name   string
		policy analysisRequestPolicy
		want   bool
	}{
		{
			name: "license fail gate",
			policy: analysisRequestPolicy{thresholds: thresholds.Values{
				LicenseDenyList:   []string{"GPL-3.0-ONLY"},
				LicenseFailOnDeny: true,
			}},
			want: true,
		},
		{
			name: "reachable advisory gate",
			policy: analysisRequestPolicy{
				advisorySourcePath: "security/advisories.yml",
				thresholds: thresholds.Values{
					ReachableVulnerabilityPriority: report.VulnerabilityPriorityHigh,
				},
			},
			want: true,
		},
		{
			name: "advisory annotation without threshold",
			policy: analysisRequestPolicy{
				advisorySourcePath: "security/advisories.yml",
				thresholds: thresholds.Values{
					ReachableVulnerabilityPriority: report.VulnerabilityPriorityOff,
				},
			},
			want: false,
		},
		{
			name: "license deny list without fail",
			policy: analysisRequestPolicy{thresholds: thresholds.Values{
				LicenseDenyList: []string{"GPL-3.0-ONLY"},
			}},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := prepareAnalysisPolicy(base, tt.policy)
			if got.request.RequireCompleteCoverage != tt.want {
				t.Fatalf("expected RequireCompleteCoverage=%v, got %v", tt.want, got.request.RequireCompleteCoverage)
			}
		})
	}
}
