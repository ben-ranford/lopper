package analysis

import (
	"testing"

	"github.com/ben-ranford/lopper/internal/report"
)

func TestAnnotateDerivedDependencyMetrics(t *testing.T) {
	dependencies := []report.DependencyReport{
		{
			Name:              "derived-percent-overlap",
			UsedExportsCount:  1,
			TotalExportsCount: 4,
			RuntimeUsage:      &report.RuntimeUsage{LoadCount: 2},
		},
		{
			Name:         "runtime-only",
			RuntimeUsage: &report.RuntimeUsage{RuntimeOnly: true},
		},
		{
			Name:         "static-only",
			RuntimeUsage: &report.RuntimeUsage{},
		},
		{
			Name:              "preserve-explicit-values",
			UsedExportsCount:  1,
			TotalExportsCount: 4,
			UsedPercent:       80,
			RuntimeUsage: &report.RuntimeUsage{
				LoadCount:   3,
				Correlation: report.RuntimeCorrelationRuntimeOnly,
			},
		},
	}

	annotateDerivedDependencyMetrics(dependencies)

	if dependencies[0].UsedPercent != 25 {
		t.Fatalf("expected used percent to be derived from counts, got %.1f", dependencies[0].UsedPercent)
	}
	if dependencies[0].RuntimeUsage.Correlation != report.RuntimeCorrelationOverlap {
		t.Fatalf("expected load count to derive overlap correlation, got %q", dependencies[0].RuntimeUsage.Correlation)
	}
	if dependencies[1].RuntimeUsage.Correlation != report.RuntimeCorrelationRuntimeOnly {
		t.Fatalf("expected runtime-only correlation, got %q", dependencies[1].RuntimeUsage.Correlation)
	}
	if dependencies[2].RuntimeUsage.Correlation != report.RuntimeCorrelationStaticOnly {
		t.Fatalf("expected static-only correlation, got %q", dependencies[2].RuntimeUsage.Correlation)
	}
	if dependencies[3].UsedPercent != 80 || dependencies[3].RuntimeUsage.Correlation != report.RuntimeCorrelationRuntimeOnly {
		t.Fatalf("expected explicit values to be preserved, got %#v", dependencies[3])
	}
}
