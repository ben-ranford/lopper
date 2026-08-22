package analysis

import (
	"reflect"
	"slices"
	"strings"
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

func TestMergeImportUsesPreservesProvenanceDeterministically(t *testing.T) {
	left := []report.ImportUse{{
		Module:    "lodash",
		Name:      "map",
		Locations: []report.Location{{File: "src/z.ts", Line: 3, Column: 1}, {File: "src/a.ts", Line: 9, Column: 2}},
		Provenance: []string{
			"workspace/root-b",
			"shared/barrel",
		},
		ConfidenceScore:       82.5,
		ConfidenceReasonCodes: []string{"static-import", "shared-evidence"},
	}}
	right := []report.ImportUse{{
		Module:    "lodash",
		Name:      "map",
		Locations: []report.Location{{File: "src/m.ts", Line: 5, Column: 4}, {File: "src/a.ts", Line: 1, Column: 8}, {File: "src/a.ts", Line: 1, Column: 7}},
		Provenance: []string{
			"workspace/root-a",
			"shared/barrel",
			"workspace/root-a",
		},
		ConfidenceScore:       61.4,
		ConfidenceReasonCodes: []string{"runtime-evidence", "shared-evidence", "runtime-evidence"},
	}}
	want := []report.ImportUse{{
		Module:                "lodash",
		Name:                  "map",
		Locations:             []report.Location{{File: "src/a.ts", Line: 1, Column: 7}, {File: "src/a.ts", Line: 1, Column: 8}, {File: "src/a.ts", Line: 9, Column: 2}, {File: "src/m.ts", Line: 5, Column: 4}, {File: "src/z.ts", Line: 3, Column: 1}},
		Provenance:            []string{"shared/barrel", "workspace/root-a", "workspace/root-b"},
		ConfidenceScore:       61.4,
		ConfidenceReasonCodes: []string{"runtime-evidence", "shared-evidence", "static-import"},
	}}

	for _, test := range []struct {
		name  string
		left  []report.ImportUse
		right []report.ImportUse
	}{
		{name: "left then right", left: left, right: right},
		{name: "right then left", left: right, right: left},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := mergeImportUses(test.left, test.right); !reflect.DeepEqual(got, want) {
				t.Fatalf("mergeImportUses() = %#v, want %#v", got, want)
			}
		})
	}
}

func TestMergeDependencySuppressesRemovalSignalsWhenUsageIsIncomplete(t *testing.T) {
	complete := report.DependencyReport{
		Name:                 "lodash",
		UsedExportsCount:     1,
		TotalExportsCount:    3,
		UsedPercent:          100.0 / 3.0,
		EstimatedUnusedBytes: 256,
		UsedImports: []report.ImportUse{{
			Module:    "lodash",
			Name:      "map",
			Locations: []report.Location{{File: "src/confirmed.js", Line: 3, Column: 5}},
		}, {
			Module:    "lodash",
			Name:      "filter",
			Locations: []report.Location{{File: "src/used-filter.js", Line: 5, Column: 4}},
		}},
		UnusedImports: []report.ImportUse{{
			Module:    "lodash",
			Name:      "chunk",
			Locations: []report.Location{{File: "src/promoted.js", Line: 9, Column: 2}},
		}},
		UnusedExports:    []report.SymbolRef{{Name: "unused"}},
		Recommendations:  []report.Recommendation{{Code: "remove-unused"}},
		Codemod:          &report.CodemodReport{},
		RemovalCandidate: &report.RemovalCandidate{Score: 80},
		Vulnerabilities:  []report.VulnerabilityFinding{{AdvisoryID: "GHSA-1"}},
	}
	incomplete := report.DependencyReport{
		Name: "lodash",
		UsedImports: []report.ImportUse{{
			Module:    "lodash",
			Name:      "flatten",
			Locations: []report.Location{{File: "src/incomplete-used.js", Line: 4, Column: 7}},
		}},
		SuppressedUnusedImports: []report.ImportUse{{
			Module:    "lodash",
			Name:      "filter",
			Locations: []report.Location{{File: "src/promoted.js", Line: 7, Column: 11}},
		}},
		Vulnerabilities: []report.VulnerabilityFinding{{AdvisoryID: "GHSA-1"}},
	}
	if !setUsageIncompleteForMergeTest(&incomplete) {
		t.Fatal("expected usage-incomplete marker to be available")
	}
	wantUsedImports := []report.ImportUse{
		{Module: "lodash", Name: "filter", Locations: []report.Location{{File: "src/used-filter.js", Line: 5, Column: 4}}},
		{Module: "lodash", Name: "flatten", Locations: []report.Location{{File: "src/incomplete-used.js", Line: 4, Column: 7}}},
		{Module: "lodash", Name: "map", Locations: []report.Location{{File: "src/confirmed.js", Line: 3, Column: 5}}},
	}
	wantSuppressedUnused := []report.ImportUse{
		{Module: "lodash", Name: "chunk", Locations: []report.Location{{File: "src/promoted.js", Line: 9, Column: 2}}},
		{Module: "lodash", Name: "filter", Locations: []report.Location{{File: "src/promoted.js", Line: 7, Column: 11}}},
	}

	for _, test := range []struct {
		name  string
		left  report.DependencyReport
		right report.DependencyReport
	}{
		{name: "incomplete left", left: incomplete, right: complete},
		{name: "incomplete right", left: complete, right: incomplete},
	} {
		t.Run(test.name, func(t *testing.T) {
			assertIncompleteRemovalSignalsSuppressedAfterMerge(t, mergeDependency(test.left, test.right), wantUsedImports, wantSuppressedUnused)
		})
	}
}

func TestMergeReportsPreservesCoverageGapsForSingleAndMergedReports(t *testing.T) {
	singleGap := report.CoverageGap{
		Code:     report.CoverageGapRubyOversizedGemspec,
		Language: "ruby",
		Path:     "gems/oversized.gem\u017fpec",
		Evidence: []string{"single warning"},
	}
	sharedGap := report.CoverageGap{
		Code:     report.CoverageGapRubyOversizedGemspec,
		Language: "ruby",
		Path:     "pkg because it exceeds old/dependencies.gemspec",
		Evidence: []string{"first warning"},
	}
	baselineNewGap := report.CoverageGap{
		Code:     report.CoverageGapRubyOversizedGemspec,
		Language: "ruby",
		Path:     "baseline-new.gemspec",
		Evidence: []string{"baseline comparison warning"},
	}

	for _, tc := range []struct {
		name    string
		reports []report.Report
		want    []report.CoverageGap
	}{
		{
			name: "single report",
			reports: []report.Report{{
				CoverageGaps: []report.CoverageGap{singleGap},
			}},
			want: []report.CoverageGap{singleGap},
		},
		{
			name: "single report baseline comparison coverage gap",
			reports: []report.Report{{
				BaselineComparison: &report.BaselineComparison{
					NewCoverageGaps: []report.CoverageGap{baselineNewGap},
				},
			}},
			want: []report.CoverageGap{baselineNewGap},
		},
		{
			name: "merged reports",
			reports: []report.Report{
				{CoverageGaps: []report.CoverageGap{sharedGap}},
				{CoverageGaps: []report.CoverageGap{{Code: sharedGap.Code, Language: sharedGap.Language, Path: sharedGap.Path, Evidence: []string{"second warning"}}}},
			},
			want: []report.CoverageGap{{
				Code:     sharedGap.Code,
				Language: sharedGap.Language,
				Path:     sharedGap.Path,
				Evidence: []string{"first warning", "second warning"},
			}},
		},
		{
			name: "merged top-level and baseline comparison coverage gaps",
			reports: []report.Report{
				{CoverageGaps: []report.CoverageGap{singleGap}},
				{BaselineComparison: &report.BaselineComparison{NewCoverageGaps: []report.CoverageGap{baselineNewGap}}},
			},
			want: []report.CoverageGap{baselineNewGap, singleGap},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			merged := mergeReports("/repo", tc.reports)
			if !reflect.DeepEqual(merged.CoverageGaps, tc.want) {
				t.Fatalf("mergeReports() coverage gaps = %#v, want %#v", merged.CoverageGaps, tc.want)
			}
		})
	}
}

func TestMergeReportsRebasesCoverageGapsByCandidateRoot(t *testing.T) {
	reports := []report.Report{
		{
			RepoPath: "/repo/packages/a",
			CoverageGaps: []report.CoverageGap{{
				Code:     report.CoverageGapRubyOversizedGemspec,
				Language: "ruby",
				Path:     "foo.gemspec",
				Evidence: []string{"package a"},
			}},
		},
		{
			RepoPath: "/repo/packages/b",
			CoverageGaps: []report.CoverageGap{{
				Code:     report.CoverageGapRubyOversizedGemspec,
				Language: "ruby",
				Path:     "foo.gemspec",
				Evidence: []string{"package b"},
			}},
		},
		{
			RepoPath: "/repo/packages/a",
			CoverageGaps: []report.CoverageGap{{
				Code:     report.CoverageGapRubyOversizedGemspec,
				Language: "ruby",
				Path:     "packages/a/already-rebased.gemspec",
				Evidence: []string{"already rebased"},
			}},
		},
	}

	merged := mergeReports("/repo", reports)
	want := []report.CoverageGap{
		{
			Code:     report.CoverageGapRubyOversizedGemspec,
			Language: "ruby",
			Path:     "packages/a/already-rebased.gemspec",
			Evidence: []string{"already rebased"},
		},
		{
			Code:     report.CoverageGapRubyOversizedGemspec,
			Language: "ruby",
			Path:     "packages/a/foo.gemspec",
			Evidence: []string{"package a"},
		},
		{
			Code:     report.CoverageGapRubyOversizedGemspec,
			Language: "ruby",
			Path:     "packages/b/foo.gemspec",
			Evidence: []string{"package b"},
		},
	}
	if !reflect.DeepEqual(merged.CoverageGaps, want) {
		t.Fatalf("mergeReports() coverage gaps = %#v, want %#v", merged.CoverageGaps, want)
	}
}

func TestMergeReportsKeepsMovedBaselineCoverageGapDifferential(t *testing.T) {
	baseline := mergeReports("/repo", []report.Report{{
		RepoPath: "/repo/packages/a",
		CoverageGaps: []report.CoverageGap{{
			Code:     report.CoverageGapRubyOversizedGemspec,
			Language: "ruby",
			Path:     "foo.gemspec",
			Evidence: []string{"baseline package"},
		}},
	}})
	current := mergeReports("/repo", []report.Report{{
		RepoPath: "/repo/packages/b",
		CoverageGaps: []report.CoverageGap{{
			Code:     report.CoverageGapRubyOversizedGemspec,
			Language: "ruby",
			Path:     "foo.gemspec",
			Evidence: []string{"current package"},
		}},
	}})

	comparison := report.ComputeBaselineComparison(current, baseline)
	want := []report.CoverageGap{{
		Code:     report.CoverageGapRubyOversizedGemspec,
		Language: "ruby",
		Path:     "packages/b/foo.gemspec",
		Evidence: []string{"current package"},
	}}
	if !reflect.DeepEqual(comparison.NewCoverageGaps, want) {
		t.Fatalf("ComputeBaselineComparison() new coverage gaps = %#v, want %#v", comparison.NewCoverageGaps, want)
	}
}

func TestSuppressedUnusedImportsPreserveVulnerabilityScoringAndPathEvidence(t *testing.T) {
	reportData := report.Report{Dependencies: []report.DependencyReport{{
		Name: "example-lib",
		SuppressedUnusedImports: []report.ImportUse{{
			Name:      "hidden",
			Module:    "example-lib",
			Locations: []report.Location{{File: "src/hidden.js", Line: 12}},
		}},
		RiskCues: []report.RiskCue{{
			Code:     "dynamic-loader",
			Severity: "medium",
		}},
	}}}
	report.AnnotateReachabilityConfidence(&reportData)
	report.AnnotateVulnerabilities(&reportData, []report.VulnerabilityAdvisory{{
		ID:       "GHSA-hidden",
		Package:  "example-lib",
		Severity: "critical",
	}})

	if len(reportData.Dependencies[0].Vulnerabilities) != 1 {
		t.Fatalf("expected one matching vulnerability, got %#v", reportData.Dependencies[0].Vulnerabilities)
	}
	finding := reportData.Dependencies[0].Vulnerabilities[0]
	if !finding.Reachable || finding.Priority != report.VulnerabilityPriorityHigh || finding.PriorityScore != 74.1 {
		t.Fatalf("expected suppressed static evidence to preserve vulnerability scoring, got %#v", finding)
	}
	if !slices.Contains(finding.Evidence, "static_imports: conservative evidence retained internally because usage coverage is incomplete") {
		t.Fatalf("expected suppressed imports to remain redacted security evidence, got %#v", finding.Evidence)
	}
	if slices.Contains(finding.Evidence, "static_location: src/hidden.js:12") {
		t.Fatalf("expected suppressed import locations to stay internal, got %#v", finding.Evidence)
	}

	sarifOutput, err := report.NewFormatter().Format(reportData, report.FormatSARIF)
	if err != nil {
		t.Fatalf("format vulnerability SARIF: %v", err)
	}
	if !strings.Contains(sarifOutput, `"ruleId": "lopper/vulnerability/ghsa-hidden"`) {
		t.Fatalf("expected vulnerability result in SARIF, got %s", sarifOutput)
	}
	for _, forbidden := range []string{`"ruleId": "lopper/waste/unused-import"`, `"ruleId": "lopper/waste/unused-export"`, "src/hidden.js"} {
		if strings.Contains(sarifOutput, forbidden) {
			t.Fatalf("expected suppressed removal details to stay out of SARIF, found %q in %s", forbidden, sarifOutput)
		}
	}

	exception := report.VulnerabilityException{
		VulnerabilityID: "GHSA-hidden",
		Path:            "src/hidden.js",
		Status:          "not-affected",
		Expires:         "2026-12-31",
	}
	now := time.Date(2026, time.July, 30, 0, 0, 0, 0, time.UTC)
	report.ApplyVulnerabilityExceptions(&reportData, []report.VulnerabilityException{exception}, now)
	decision := reportData.Dependencies[0].Vulnerabilities[0].Decision
	if decision == nil || decision.Status != "not-affected" {
		t.Fatalf("expected hidden path evidence to match vulnerability exception, got %#v", decision)
	}
}

func assertIncompleteRemovalSignalsSuppressedAfterMerge(t *testing.T, merged report.DependencyReport, wantUsedImports []report.ImportUse, wantSuppressedUnused []report.ImportUse) {
	t.Helper()

	if !usageIncompleteForMergeTest(merged) {
		t.Fatal("expected merged dependency usage to remain incomplete")
	}
	if merged.UsedExportsCount != 0 || merged.TotalExportsCount != 0 || merged.UsedPercent != 0 {
		t.Fatalf("expected incomplete usage aggregates to be suppressed, got %#v", merged)
	}
	if merged.EstimatedUnusedBytes != 0 || len(merged.UnusedExports) != 0 {
		t.Fatalf("expected incomplete unused-export signals to be suppressed, got %#v", merged)
	}
	if len(merged.UnusedImports) != 0 {
		t.Fatalf("expected incomplete unused-import signals to be suppressed, got %#v", merged.UnusedImports)
	}
	if !reflect.DeepEqual(merged.UsedImports, wantUsedImports) {
		t.Fatalf("expected confirmed used imports to remain after incomplete merge, got %#v want %#v", merged.UsedImports, wantUsedImports)
	}
	if !reflect.DeepEqual(merged.SuppressedUnusedImports, wantSuppressedUnused) {
		t.Fatalf("expected hidden unused-import path evidence to be preserved, got %#v want %#v", merged.SuppressedUnusedImports, wantSuppressedUnused)
	}
	if len(merged.Recommendations) != 0 || merged.Codemod != nil || merged.RemovalCandidate != nil {
		t.Fatalf("expected incomplete removal advice to be suppressed, got %#v", merged)
	}

	reportData := report.Report{Dependencies: []report.DependencyReport{merged}}
	exceptions := []report.VulnerabilityException{{
		VulnerabilityID: "GHSA-1",
		Path:            "src/promoted.js",
		Status:          "not-affected",
	}}
	report.ApplyVulnerabilityExceptions(&reportData, exceptions, time.Date(2026, time.July, 13, 0, 0, 0, 0, time.UTC))
	decision := reportData.Dependencies[0].Vulnerabilities[0].Decision
	if decision == nil || decision.Status != "not-affected" {
		t.Fatalf("expected promoted import location to satisfy path-scoped vulnerability exception, got %#v", decision)
	}
}

func setUsageIncompleteForMergeTest(dependency *report.DependencyReport) bool {
	field := reflect.ValueOf(dependency).Elem().FieldByName("UsageIncomplete")
	if !field.IsValid() || !field.CanSet() {
		return false
	}
	field.SetBool(true)
	return true
}

func usageIncompleteForMergeTest(dependency report.DependencyReport) bool {
	field := reflect.ValueOf(dependency).FieldByName("UsageIncomplete")
	return field.IsValid() && field.Kind() == reflect.Bool && field.Bool()
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
