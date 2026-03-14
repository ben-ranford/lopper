package report

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"text/tabwriter"
)

func TestAppendEffectivePolicyAndBaselineComparisonMoreBranches(t *testing.T) {
	var buffer bytes.Buffer
	appendEffectivePolicy(&buffer, Report{
		EffectivePolicy: &EffectivePolicy{
			Sources: []string{"repo", "defaults"},
			Thresholds: EffectiveThresholds{
				FailOnIncreasePercent:             1,
				LowConfidenceWarningPercent:       35,
				MinUsagePercentForRecommendations: 45,
				MaxUncertainImportCount:           2,
			},
			RemovalCandidateWeights: RemovalCandidateWeights{
				Usage:      0.6,
				Impact:     0.2,
				Confidence: 0.2,
			},
			License: LicensePolicy{
				Deny:                      []string{"GPL-3.0-only"},
				FailOnDenied:              true,
				IncludeRegistryProvenance: true,
			},
		},
	})
	if got := buffer.String(); !strings.Contains(got, "license_deny: GPL-3.0-only") {
		t.Fatalf("expected effective policy deny list in output, got %q", got)
	}

	buffer.Reset()
	appendBaselineComparison(&buffer, &BaselineComparison{
		Progressions: []DependencyDelta{
			{Language: "go", Name: "pkg", WastePercentDelta: -2.5, UsedPercentDelta: 2.5},
		},
	})
	if got := buffer.String(); !strings.Contains(got, "progression go/pkg waste -2.5% used +2.5%") {
		t.Fatalf("expected progression summary in output, got %q", got)
	}
}

func TestFormatRuntimeUsageAndLicenseMoreBranches(t *testing.T) {
	if got := formatRuntimeUsage(&RuntimeUsage{LoadCount: 1}); !strings.Contains(got, "overlap (1 loads)") {
		t.Fatalf("expected overlap fallback correlation, got %q", got)
	}
	if got := formatDependencyLicense(&DependencyLicense{Unknown: true}); got != "unknown" {
		t.Fatalf("expected unknown license fallback, got %q", got)
	}
	if got := formatDependencyLicense(&DependencyLicense{SPDX: "MIT"}); got != "MIT" {
		t.Fatalf("expected SPDX pass-through, got %q", got)
	}
}

type failingReportWriter struct{}

func (w *failingReportWriter) Write(_ []byte) (int, error) {
	return 0, bytes.ErrTooLarge
}

func TestTableWriterErrorHelpers(t *testing.T) {
	t.Run("write table line returns writer failure", func(t *testing.T) {
		writer := tabwriter.NewWriter(&failingReportWriter{}, 0, 0, 2, ' ', 0)
		if _, err := fmt.Fprintln(writer, strings.Repeat("x", 5000)); err == nil {
			t.Fatal("expected fmt.Fprintln to return writer failure")
		}
	})

	t.Run("flush table writer returns flush failure", func(t *testing.T) {
		writer := tabwriter.NewWriter(&failingReportWriter{}, 0, 0, 2, ' ', 0)
		if _, err := writer.Write([]byte("col1\tcol2\n")); err != nil {
			t.Fatalf("seed tabwriter: %v", err)
		}
		if err := writer.Flush(); err == nil {
			t.Fatal("expected Flush to return flush failure")
		}
	})
}
