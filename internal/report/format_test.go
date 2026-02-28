package report

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

const unexpectedErrFmt = "unexpected error: %v"

func assertOutputContains(t *testing.T, output string, values ...string) {
	t.Helper()
	for _, value := range values {
		if !strings.Contains(output, value) {
			t.Fatalf("expected output to contain %q", value)
		}
	}
}

func sampleEffectivePolicy(source string, failOnIncrease, lowConfidence, minUsage int, usageWeight, impactWeight, confidenceWeight float64) *EffectivePolicy {
	return &EffectivePolicy{
		Sources: []string{source},
		Thresholds: EffectiveThresholds{
			FailOnIncreasePercent:             failOnIncrease,
			LowConfidenceWarningPercent:       lowConfidence,
			MinUsagePercentForRecommendations: minUsage,
		},
		RemovalCandidateWeights: RemovalCandidateWeights{
			Usage:      usageWeight,
			Impact:     impactWeight,
			Confidence: confidenceWeight,
		},
	}
}

func TestFormatTable(t *testing.T) {
	reportData := Report{
		EffectiveThresholds: &EffectiveThresholds{
			FailOnIncreasePercent:             2,
			LowConfidenceWarningPercent:       35,
			MinUsagePercentForRecommendations: 45,
		},
		EffectivePolicy: sampleEffectivePolicy("repo", 2, 35, 45, 0.6, 0.2, 0.2),
		LanguageBreakdown: []LanguageSummary{
			{Language: "js-ts", DependencyCount: 1, UsedExportsCount: 2, TotalExportsCount: 10, UsedPercent: 20.0},
		},
		Dependencies: []DependencyReport{
			{
				Language:             "js-ts",
				Name:                 "lodash",
				UsedExportsCount:     2,
				TotalExportsCount:    10,
				EstimatedUnusedBytes: 1024,
				RuntimeUsage: &RuntimeUsage{
					LoadCount:   3,
					Correlation: RuntimeCorrelationOverlap,
				},
				TopUsedSymbols: []SymbolUsage{
					{Name: "map", Count: 3},
				},
			},
		},
	}
	reportData.EffectivePolicy.Sources = []string{"repo", "defaults"}

	output, err := NewFormatter().Format(reportData, FormatTable)
	if err != nil {
		t.Fatalf(unexpectedErrFmt, err)
	}
	expected := []string{
		"lodash",
		"Language",
		"Languages:",
		"Effective thresholds:",
		"Effective policy:",
		"sources: repo > defaults",
		"map",
		"Runtime",
		"overlap (3 loads)",
	}
	assertOutputContains(t, output, expected...)
}

func TestFormatJSON(t *testing.T) {
	reportData := Report{RepoPath: "."}
	output, err := NewFormatter().Format(reportData, FormatJSON)
	if err != nil {
		t.Fatalf(unexpectedErrFmt, err)
	}
	assertOutputContains(t, output, "repoPath")
}

func TestFormatSARIF(t *testing.T) {
	reportData := sampleSARIFReport()

	output, err := NewFormatter().Format(reportData, FormatSARIF)
	if err != nil {
		t.Fatalf(unexpectedErrFmt, err)
	}
	assertOutputContains(t, output, "\"version\": \"2.1.0\"", "lopper/waste/unused-import", "src/main.ts")

	var payload map[string]any
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		t.Fatalf("expected valid SARIF JSON: %v", err)
	}
	if payload["version"] != "2.1.0" {
		t.Fatalf("expected SARIF version 2.1.0, got %#v", payload["version"])
	}
}

func TestFormatPRComment(t *testing.T) {
	reportData := Report{
		BaselineComparison: &BaselineComparison{
			SummaryDelta: SummaryDelta{
				DependencyCountDelta: 2,
				UsedPercentDelta:     -1.2,
				WastePercentDelta:    1.2,
				UnusedBytesDelta:     1024,
			},
			Dependencies: []DependencyDelta{
				{Kind: DependencyDeltaChanged, Name: "lodash", Language: "js-ts", UsedPercentDelta: -2.5, UsedExportsCountDelta: -1, TotalExportsCountDelta: 0, EstimatedUnusedBytesDelta: 512},
			},
			Regressions: []DependencyDelta{
				{Kind: DependencyDeltaChanged, Name: "lodash", Language: "js-ts", WastePercentDelta: 2.5},
			},
			UnchangedRows: 3,
		},
	}
	output, err := NewFormatter().Format(reportData, FormatPRComment)
	if err != nil {
		t.Fatalf("format pr-comment: %v", err)
	}
	assertOutputContains(t, output, "## Lopper (Delta)", "| Dependency count | +2 |", "| Estimated unused bytes | +1.0 KB |", "### Dependency deltas", "`lodash`", "+512.0 B")
}

func TestFormatPRCommentNoBaseline(t *testing.T) {
	output, err := NewFormatter().Format(Report{}, FormatPRComment)
	if err != nil {
		t.Fatalf("format pr-comment without baseline: %v", err)
	}
	assertOutputContains(t, output, "## Lopper (Delta)", "_No baseline comparison available.")
}

func TestFormatPRCommentNoDependencyDeltas(t *testing.T) {
	reportData := Report{
		BaselineComparison: &BaselineComparison{
			SummaryDelta:  SummaryDelta{},
			Dependencies:  nil,
			Regressions:   nil,
			Progressions:  nil,
			Added:         nil,
			Removed:       nil,
			UnchangedRows: 4,
		},
	}
	output, err := NewFormatter().Format(reportData, FormatPRComment)
	if err != nil {
		t.Fatalf("format pr-comment with no deltas: %v", err)
	}
	assertOutputContains(t, output, "_No dependency-surface deltas detected._")
}

func TestFormatEmptyAndWarnings(t *testing.T) {
	reportData := Report{
		Warnings: []string{"warning-1"},
		EffectiveThresholds: &EffectiveThresholds{
			FailOnIncreasePercent:             1,
			LowConfidenceWarningPercent:       40,
			MinUsagePercentForRecommendations: 35,
		},
		EffectivePolicy: sampleEffectivePolicy("defaults", 1, 40, 35, 0.5, 0.3, 0.2),
	}
	output, err := NewFormatter().Format(reportData, FormatTable)
	if err != nil {
		t.Fatalf(unexpectedErrFmt, err)
	}
	assertOutputContains(t, output, "No dependencies to report.", "Warnings:", "fail_on_increase_percent", "Effective policy:")
}

func TestFormattingHelpers(t *testing.T) {
	if got := formatBytes(0); got != "0 B" {
		t.Fatalf("unexpected 0-byte format: %q", got)
	}
	if got := formatBytes(1024); !strings.Contains(got, "KB") {
		t.Fatalf("expected KB format, got %q", got)
	}
	if got := formatBytes(-1024 * 1024); !strings.HasPrefix(got, "-") || !strings.Contains(got, "MB") {
		t.Fatalf("expected negative MB format, got %q", got)
	}
	if got := formatTopSymbols(nil); got != "-" {
		t.Fatalf("expected top symbols dash for empty list, got %q", got)
	}
	if got := formatTopSymbols([]SymbolUsage{{Name: "map", Count: 2}}); !strings.Contains(got, "map (2)") {
		t.Fatalf("expected symbol count annotation, got %q", got)
	}
	if hasLanguageColumn([]DependencyReport{{Name: "x"}}) {
		t.Fatalf("did not expect language column without language values")
	}
	if hasRuntimeColumn([]DependencyReport{{Name: "x"}}) {
		t.Fatalf("did not expect runtime column without runtime data")
	}
	if !hasRuntimeColumn([]DependencyReport{{Name: "x", RuntimeUsage: &RuntimeUsage{LoadCount: 1}}}) {
		t.Fatalf("expected runtime column with runtime data")
	}
}

func TestFormatUnknownFormat(t *testing.T) {
	if _, err := NewFormatter().Format(Report{}, Format("weird")); err == nil {
		t.Fatalf("expected unknown format error")
	}
}

func TestFormatTableUsedPercentFallbackAndNoLanguageColumn(t *testing.T) {
	reportData := Report{
		Dependencies: []DependencyReport{
			{
				Name:              "dep",
				UsedExportsCount:  1,
				TotalExportsCount: 4,
				UsedPercent:       0,
			},
		},
	}
	output, err := NewFormatter().Format(reportData, FormatTable)
	if err != nil {
		t.Fatalf("format table: %v", err)
	}
	if strings.Contains(output, "Language\tDependency") {
		t.Fatalf("did not expect language column for single-language anonymous rows")
	}
	if !strings.Contains(output, "25.0") {
		t.Fatalf("expected used-percent fallback calculation in output, got %q", output)
	}
}

func TestFormatBytesGB(t *testing.T) {
	value := int64(1024 * 1024 * 1024)
	if got := formatBytes(value); !strings.Contains(got, "GB") {
		t.Fatalf("expected GB output, got %q", got)
	}
}

func TestTopDependencyDeltasAndSignedBytesBranches(t *testing.T) {
	deltas := []DependencyDelta{
		{Name: "a", Language: "js", EstimatedUnusedBytesDelta: -200},
		{Name: "b", Language: "go", EstimatedUnusedBytesDelta: 100},
	}
	if got := topDependencyDeltas(deltas, 0); len(got) != 0 {
		t.Fatalf("expected nil deltas when limit <= 0, got %#v", got)
	}
	got := topDependencyDeltas(deltas, 10)
	if len(got) != 2 {
		t.Fatalf("expected full delta set when limit exceeds length, got %#v", got)
	}
	if got[0].Name != "a" {
		t.Fatalf("expected magnitude sort to prioritize biggest absolute delta, got %#v", got)
	}
	if signedBytes(-1024) != "-1.0 KB" {
		t.Fatalf("expected negative byte formatting branch")
	}
}

func TestFormatCandidateFields(t *testing.T) {
	candidate := &RemovalCandidate{
		Score:      87.34,
		Usage:      91.2,
		Impact:     45.6,
		Confidence: 78.9,
	}
	if got := formatCandidateScore(candidate); got != "87.3" {
		t.Fatalf("unexpected candidate score format: %q", got)
	}
	if got := formatScoreComponents(candidate); got != "U:91.2 I:45.6 C:78.9" {
		t.Fatalf("unexpected score components format: %q", got)
	}
}

func TestFormatTopSymbolsSingleCountOmitsCounter(t *testing.T) {
	if got := formatTopSymbols([]SymbolUsage{{Name: "uniq", Count: 1}}); got != "uniq" {
		t.Fatalf("expected single-count symbol without annotation, got %q", got)
	}
}

func TestFormatTableIncludesSummary(t *testing.T) {
	reportData := Report{
		Summary: &Summary{
			DependencyCount:   1,
			UsedExportsCount:  2,
			TotalExportsCount: 4,
			UsedPercent:       50,
		},
		Dependencies: []DependencyReport{
			{Name: "dep", UsedExportsCount: 2, TotalExportsCount: 4, UsedPercent: 50},
		},
	}
	output, err := NewFormatter().Format(reportData, FormatTable)
	if err != nil {
		t.Fatalf("format table with summary: %v", err)
	}
	if !strings.Contains(output, "Summary: 1 deps, Used/Total: 2/4 (50.0%)") {
		t.Fatalf("expected summary header in output, got %q", output)
	}
}

func TestFormatTableIncludesCacheMetadata(t *testing.T) {
	reportData := Report{
		Cache: &CacheMetadata{
			Enabled:  true,
			Path:     "/tmp/lopper-cache",
			ReadOnly: true,
			Hits:     3,
			Misses:   1,
			Writes:   0,
			Invalidations: []CacheInvalidation{
				{Key: "js-ts:/repo", Reason: "input-changed"},
			},
		},
		Dependencies: []DependencyReport{
			{Name: "dep", UsedExportsCount: 2, TotalExportsCount: 4, UsedPercent: 50},
		},
	}
	output, err := NewFormatter().Format(reportData, FormatTable)
	if err != nil {
		t.Fatalf("format table with cache metadata: %v", err)
	}
	if !strings.Contains(output, "Cache:") || !strings.Contains(output, "hits: 3") || !strings.Contains(output, "invalidation: js-ts:/repo (input-changed)") {
		t.Fatalf("expected cache metadata in output, got %q", output)
	}
}

func TestFormatTableIncludesBaselineComparison(t *testing.T) {
	reportData := Report{
		BaselineComparison: &BaselineComparison{
			BaselineKey: "commit:abc123",
			CurrentKey:  "commit:def456",
			SummaryDelta: SummaryDelta{
				WastePercentDelta: 1.5,
			},
			Dependencies: []DependencyDelta{
				{Kind: DependencyDeltaChanged, Language: "js-ts", Name: "lodash", WastePercentDelta: 3.5, UsedPercentDelta: -3.5},
			},
			Regressions: []DependencyDelta{
				{Kind: DependencyDeltaChanged, Language: "js-ts", Name: "lodash", WastePercentDelta: 3.5, UsedPercentDelta: -3.5},
			},
		},
		Dependencies: []DependencyReport{
			{Name: "lodash", Language: "js-ts", UsedExportsCount: 2, TotalExportsCount: 10, UsedPercent: 20},
		},
	}
	output, err := NewFormatter().Format(reportData, FormatTable)
	if err != nil {
		t.Fatalf("format table with baseline comparison: %v", err)
	}
	if !strings.Contains(output, "Baseline comparison:") {
		t.Fatalf("expected baseline comparison section, got %q", output)
	}
	if !strings.Contains(output, "baseline_key: commit:abc123") {
		t.Fatalf("expected baseline key in output, got %q", output)
	}
	if !strings.Contains(output, "regression js-ts/lodash") {
		t.Fatalf("expected regression line in output, got %q", output)
	}
}

func TestFormatJSONReturnsMarshalErrorForNonFiniteValue(t *testing.T) {
	reportData := Report{
		Dependencies: []DependencyReport{
			{
				Name: "dep",
				RemovalCandidate: &RemovalCandidate{
					Score: math.NaN(),
				},
			},
		},
	}
	if _, err := NewFormatter().Format(reportData, FormatJSON); err == nil {
		t.Fatalf("expected json marshal error for NaN candidate score")
	}
}

func TestFormatRuntimeUsageFallbacks(t *testing.T) {
	if got := formatRuntimeUsage(nil); got != "-" {
		t.Fatalf("expected runtime dash for nil usage, got %q", got)
	}
	if got := formatRuntimeUsage(&RuntimeUsage{LoadCount: 1, RuntimeOnly: true}); !strings.Contains(got, "runtime-only") {
		t.Fatalf("expected runtime-only fallback, got %q", got)
	}
	if got := formatRuntimeUsage(&RuntimeUsage{LoadCount: 0}); !strings.Contains(got, "static-only") {
		t.Fatalf("expected static-only fallback, got %q", got)
	}
}

func TestTopWasteDeltasSortingAndLimit(t *testing.T) {
	if got := topWasteDeltas(nil, 3); len(got) != 0 {
		t.Fatalf("expected nil for empty deltas, got %#v", got)
	}
	if got := topWasteDeltas([]DependencyDelta{{Name: "x", WastePercentDelta: 1}}, 0); len(got) != 0 {
		t.Fatalf("expected nil when limit is zero, got %#v", got)
	}

	input := []DependencyDelta{
		{Name: "c", Language: "js-ts", WastePercentDelta: 1},
		{Name: "a", Language: "go", WastePercentDelta: -5},
		{Name: "b", Language: "go", WastePercentDelta: 5},
		{Name: "d", Language: "python", WastePercentDelta: 2},
	}
	got := topWasteDeltas(input, 3)
	if len(got) != 3 {
		t.Fatalf("expected top 3 deltas, got %#v", got)
	}
	if got[0].Language != "go" || got[0].Name != "a" {
		t.Fatalf("expected tie-break by language/name for equal magnitudes, got %#v", got[0])
	}
	if got[1].Language != "go" || got[1].Name != "b" {
		t.Fatalf("expected second tie-break result to be go/b, got %#v", got[1])
	}
}

func TestFormatTableRuntimeColumnWithoutLanguage(t *testing.T) {
	reportData := Report{
		Dependencies: []DependencyReport{
			{
				Name:              "dep",
				UsedExportsCount:  1,
				TotalExportsCount: 2,
				UsedPercent:       50,
				RuntimeUsage: &RuntimeUsage{
					LoadCount:   1,
					Correlation: RuntimeCorrelationOverlap,
				},
			},
		},
	}
	output, err := NewFormatter().Format(reportData, FormatTable)
	if err != nil {
		t.Fatalf("format table: %v", err)
	}
	if !strings.Contains(output, "Runtime") {
		t.Fatalf("expected runtime column in table output, got %q", output)
	}
	if strings.Contains(output, "Language\tDependency") {
		t.Fatalf("did not expect language column in single-language runtime table")
	}
}
