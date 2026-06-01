package analysis

import (
	"testing"
	"time"

	"github.com/ben-ranford/lopper/internal/report"
)

func TestMergeReportsCoordinatesFamiliesInStableOrder(t *testing.T) {
	firstGeneratedAt := time.Date(2026, time.January, 10, 10, 0, 0, 0, time.UTC)
	secondGeneratedAt := firstGeneratedAt.Add(2 * time.Hour)

	firstSamples := []string{"a.js", "b.js", "c.js"}
	secondSamples := []string{"d.js", "e.js", "f.js"}
	firstDependencies := []report.DependencyReport{
		dependencyReport("js-ts", "lodash", 1, 2, "map"),
		dependencyReport("go", "cobra", 1, 1, ""),
	}
	secondDependencies := []report.DependencyReport{
		dependencyReport("js-ts", "lodash", 2, 3, "filter"),
		dependencyReport("python", "requests", 1, 2, ""),
	}
	secondDependencies[0].ReachabilityConfidence = &report.ReachabilityConfidence{
		Model: "static",
		Score: 0.7,
	}
	secondDependencies[0].RemovalCandidate = &report.RemovalCandidate{Score: 42}
	secondDependencies[0].License = &report.DependencyLicense{SPDX: "MIT"}
	secondDependencies[0].Provenance = &report.DependencyProvenance{Source: "registry"}

	reports := []report.Report{
		mergeFamilyReport(firstGeneratedAt, "w-first", 1, 2, firstSamples, firstDependencies...),
		mergeFamilyReport(secondGeneratedAt, "w-second", 3, 4, secondSamples, secondDependencies...),
	}

	merged := mergeReports("/repo", reports)

	assertMergedReportMetadata(t, merged, secondGeneratedAt)
	assertMergedUsageUncertainty(t, merged)
	assertMergedDependencies(t, merged)
}

func mergeFamilyReport(generatedAt time.Time, warning string, confirmed, uncertain int, samples []string, dependencies ...report.DependencyReport) report.Report {
	return report.Report{
		GeneratedAt:      generatedAt,
		Warnings:         []string{warning},
		UsageUncertainty: usageUncertainty(confirmed, uncertain, samples),
		Dependencies:     dependencies,
	}
}

func usageUncertainty(confirmed, uncertain int, sampleFiles []string) *report.UsageUncertainty {
	samples := make([]report.Location, 0, len(sampleFiles))
	for _, file := range sampleFiles {
		samples = append(samples, report.Location{File: file})
	}
	return &report.UsageUncertainty{
		ConfirmedImportUses: confirmed,
		UncertainImportUses: uncertain,
		Samples:             samples,
	}
}

func dependencyReport(language, name string, usedExports, totalExports int, usedImport string) report.DependencyReport {
	dependency := report.DependencyReport{
		Language:          language,
		Name:              name,
		UsedExportsCount:  usedExports,
		TotalExportsCount: totalExports,
	}
	if usedImport != "" {
		dependency.UsedImports = []report.ImportUse{{Module: name, Name: usedImport}}
	}
	return dependency
}

func assertMergedReportMetadata(t *testing.T, merged report.Report, wantGeneratedAt time.Time) {
	t.Helper()

	if merged.RepoPath != "/repo" {
		t.Fatalf("expected repo path to be preserved, got %q", merged.RepoPath)
	}
	if merged.GeneratedAt != wantGeneratedAt {
		t.Fatalf("expected latest generatedAt timestamp, got %v want %v", merged.GeneratedAt, wantGeneratedAt)
	}
	if len(merged.Warnings) != 2 || merged.Warnings[0] != "w-first" || merged.Warnings[1] != "w-second" {
		t.Fatalf("expected warning merge order to follow report order, got %#v", merged.Warnings)
	}
}

func assertMergedUsageUncertainty(t *testing.T, merged report.Report) {
	t.Helper()

	if merged.UsageUncertainty == nil {
		t.Fatal("expected merged usage uncertainty")
	}
	if merged.UsageUncertainty.ConfirmedImportUses != 4 || merged.UsageUncertainty.UncertainImportUses != 6 {
		t.Fatalf("unexpected merged usage uncertainty counts: %#v", merged.UsageUncertainty)
	}
	if len(merged.UsageUncertainty.Samples) != 5 {
		t.Fatalf("expected capped sample list of five, got %#v", merged.UsageUncertainty.Samples)
	}
	if got := merged.UsageUncertainty.Samples[0].File; got != "a.js" {
		t.Fatalf("expected first sample to come from first report, got %q", got)
	}
	if got := merged.UsageUncertainty.Samples[4].File; got != "e.js" {
		t.Fatalf("expected last kept sample to come from second report, got %q", got)
	}
}

func assertMergedDependencies(t *testing.T, merged report.Report) {
	t.Helper()

	if len(merged.Dependencies) != 3 {
		t.Fatalf("expected three merged dependencies, got %#v", merged.Dependencies)
	}
	if merged.Dependencies[0].Language != "go" || merged.Dependencies[0].Name != "cobra" {
		t.Fatalf("expected deterministic dependency sort order, got first row %#v", merged.Dependencies[0])
	}
	if merged.Dependencies[1].Language != "js-ts" || merged.Dependencies[1].Name != "lodash" {
		t.Fatalf("expected lodash row to be second, got %#v", merged.Dependencies[1])
	}
	if merged.Dependencies[2].Language != "python" || merged.Dependencies[2].Name != "requests" {
		t.Fatalf("expected requests row to be third, got %#v", merged.Dependencies[2])
	}
	if merged.Dependencies[1].UsedExportsCount != 3 || merged.Dependencies[1].TotalExportsCount != 5 {
		t.Fatalf("expected duplicate dependency rows to merge export counts, got %#v", merged.Dependencies[1])
	}
	if len(merged.Dependencies[1].UsedImports) != 2 {
		t.Fatalf("expected duplicate dependency used imports to merge, got %#v", merged.Dependencies[1].UsedImports)
	}
	assertMergedDependencyMetadata(t, merged.Dependencies[1])
}

func assertMergedDependencyMetadata(t *testing.T, dependency report.DependencyReport) {
	t.Helper()

	if dependency.ReachabilityConfidence == nil || dependency.ReachabilityConfidence.Model != "static" {
		t.Fatalf("expected merged reachability confidence, got %#v", dependency.ReachabilityConfidence)
	}
	if dependency.RemovalCandidate == nil || dependency.RemovalCandidate.Score != 42 {
		t.Fatalf("expected merged removal candidate, got %#v", dependency.RemovalCandidate)
	}
	if dependency.License == nil || dependency.License.SPDX != "MIT" {
		t.Fatalf("expected merged dependency license, got %#v", dependency.License)
	}
	if dependency.Provenance == nil || dependency.Provenance.Source != "registry" {
		t.Fatalf("expected merged dependency provenance, got %#v", dependency.Provenance)
	}
}
