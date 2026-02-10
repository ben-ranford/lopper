package ui

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/report"
)

func TestDetailShowsRiskCues(t *testing.T) {
	analyzer := stubAnalyzer{
		report: report.Report{
			Dependencies: []report.DependencyReport{
				{
					Name:              "risky",
					UsedExportsCount:  1,
					TotalExportsCount: 3,
					UsedPercent:       33.3,
					RiskCues: []report.RiskCue{
						{Code: "dynamic-loader", Severity: "medium", Message: "dynamic require/import usage found"},
					},
					RuntimeUsage: &report.RuntimeUsage{
						LoadCount: 1,
					},
					Recommendations: []report.Recommendation{
						{Code: "prefer-subpath-imports", Priority: "medium", Message: "Prefer subpath imports."},
					},
				},
			},
		},
	}

	var out bytes.Buffer
	detail := NewDetail(&out, analyzer, report.NewFormatter(), ".", "js-ts")
	if err := detail.Show(context.Background(), "risky"); err != nil {
		t.Fatalf("show detail: %v", err)
	}

	output := out.String()
	if !strings.Contains(output, "Risk cues (1)") {
		t.Fatalf("expected risk cues section, got: %s", output)
	}
	if !strings.Contains(output, "[MEDIUM] dynamic-loader") {
		t.Fatalf("expected risk cue entry, got: %s", output)
	}
	if !strings.Contains(output, "Runtime usage") || !strings.Contains(output, "load count: 1") {
		t.Fatalf("expected runtime section, got: %s", output)
	}
	if !strings.Contains(output, "Recommendations (1)") {
		t.Fatalf("expected recommendations section, got: %s", output)
	}
	if !strings.Contains(output, "[MEDIUM] prefer-subpath-imports") {
		t.Fatalf("expected recommendation entry, got: %s", output)
	}
}
