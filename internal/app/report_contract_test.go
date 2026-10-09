package app

import (
	"context"
	"errors"
	"github.com/ben-ranford/lopper/internal/report"
	"strings"
	"testing"
)

func TestDisabledReachableThresholdIgnoresExistingFinding(t *testing.T) {
	data := report.Report{Dependencies: []report.DependencyReport{{Name: "reachable", Vulnerabilities: []report.VulnerabilityFinding{{Priority: report.VulnerabilityPriorityHigh, Reachable: true}}}}}
	if !hasReachableVulnerabilityAtOrAbove(data, report.VulnerabilityPriorityHigh) {
		t.Fatal("positive control did not find reachable vulnerability")
	}
	for _, threshold := range []string{"", " ", report.VulnerabilityPriorityOff} {
		if hasReachableVulnerabilityAtOrAbove(data, threshold) {
			t.Fatalf("disabled threshold %q still active", threshold)
		}
	}
}

func TestWarningPrefixRejectsTruncatedUnicodeMatch(t *testing.T) {
	for _, value := range []string{"", "s", "ſkipped", "ſkip"} {
		if suffix, found := equalFoldCutPrefix(value, "skipped "); found || suffix != "" {
			t.Fatalf("truncated prefix %q accepted: %q %t", value, suffix, found)
		}
	}
	if suffix, found := equalFoldCutPrefix("ſkipped path", "skipped "); !found || suffix != "path" {
		t.Fatalf("complete Unicode prefix rejected: %q %t", suffix, found)
	}
}

func TestUnreachableFindingDoesNotTriggerThreshold(t *testing.T) {
	data := report.Report{Dependencies: []report.DependencyReport{{Name: "unreachable", Vulnerabilities: []report.VulnerabilityFinding{{Priority: report.VulnerabilityPriorityHigh, Reachable: false}}}}}
	if err := validateReachableVulnerabilityThreshold(data, report.VulnerabilityPriorityHigh); err != nil {
		t.Fatalf("unreachable finding failed threshold: %v", err)
	}
}

func TestUnrelatedCoverageGapDoesNotTriggerRubyThreshold(t *testing.T) {
	data := report.Report{CoverageGaps: []report.CoverageGap{{Code: "unrelated-gap", Path: "example.gemspec"}}}
	if err := validateReachableVulnerabilityThreshold(data, report.VulnerabilityPriorityHigh); err != nil {
		t.Fatalf("unrelated gap treated as oversized Ruby declaration: %v", err)
	}
	data.CoverageGaps[0].Code = report.CoverageGapRubyOversizedGemspec
	if err := validateReachableVulnerabilityThreshold(data, report.VulnerabilityPriorityHigh); !errors.Is(err, ErrReachableVulnerabilities) {
		t.Fatalf("oversized Ruby positive control returned %v", err)
	}
}

func TestExecuteDashboardRejectsDisabledPreviewFormat(t *testing.T) {
	app := &App{Analyzer: &mapAnalyzer{}}
	req := DefaultRequest()
	req.Mode = ModeDashboard
	req.Dashboard.Repos = []DashboardRepo{{Path: t.TempDir()}}
	req.Dashboard.Format = "slack-summary"
	output, err := app.Execute(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), "requires --enable-feature") || output != "" {
		t.Fatalf("disabled preview rendered output %q, err %v", output, err)
	}
}
