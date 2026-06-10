package report

import (
	"bytes"
	"testing"
	"text/template"
)

func TestAppendSummaryTemplateOutput(t *testing.T) {
	var buffer bytes.Buffer
	if err := appendSummary(&buffer, &Summary{
		DependencyCount:     2,
		UsedExportsCount:    3,
		TotalExportsCount:   4,
		UsedPercent:         75,
		KnownLicenseCount:   1,
		UnknownLicenseCount: 0,
		DeniedLicenseCount:  1,
		Reachability: &ReachabilityRollup{
			AverageScore: 88.25,
			LowestScore:  77,
			HighestScore: 99,
			Model:        "reachability-v2\x1b[31m",
		},
	}); err != nil {
		t.Fatalf("append summary: %v", err)
	}

	want := "Summary: 2 deps, Used/Total: 3/4 (75.0%)\n\n" +
		"Licenses: known=1, unknown=0, denied=1\n\n" +
		"Reachability confidence: avg=88.2 range=77.0-99.0 (reachability-v2\\x1b[31m)\n\n"
	if got := buffer.String(); got != want {
		t.Fatalf("unexpected summary output:\ngot  %q\nwant %q", got, want)
	}

	buffer.Reset()
	if err := appendSummary(&buffer, &Summary{
		DependencyCount:     1,
		UsedExportsCount:    1,
		TotalExportsCount:   2,
		UsedPercent:         50,
		KnownLicenseCount:   1,
		UnknownLicenseCount: 0,
		DeniedLicenseCount:  0,
	}); err != nil {
		t.Fatalf("append summary without reachability: %v", err)
	}

	want = "Summary: 1 deps, Used/Total: 1/2 (50.0%)\n\n" +
		"Licenses: known=1, unknown=0, denied=0\n\n"
	if got := buffer.String(); got != want {
		t.Fatalf("unexpected summary output without reachability:\ngot  %q\nwant %q", got, want)
	}
}

func TestAppendEffectiveThresholdsTemplateOutput(t *testing.T) {
	var buffer bytes.Buffer
	if err := appendEffectiveThresholds(&buffer, Report{
		EffectiveThresholds: &EffectiveThresholds{
			FailOnIncreasePercent:             2,
			LowConfidenceWarningPercent:       35,
			MinUsagePercentForRecommendations: 45,
			MaxUncertainImportCount:           1,
		},
	}); err != nil {
		t.Fatalf("append thresholds: %v", err)
	}

	want := "Effective thresholds:\n" +
		"- fail_on_increase_percent: 2\n" +
		"- low_confidence_warning_percent: 35\n" +
		"- min_usage_percent_for_recommendations: 45\n" +
		"- max_uncertain_import_count: 1\n\n"
	if got := buffer.String(); got != want {
		t.Fatalf("unexpected thresholds output:\ngot  %q\nwant %q", got, want)
	}
}

func TestAppendLanguageBreakdownTemplateOutput(t *testing.T) {
	var buffer bytes.Buffer
	if err := appendLanguageBreakdown(&buffer, []LanguageSummary{
		{Language: "go", DependencyCount: 2, UsedExportsCount: 3, TotalExportsCount: 6, UsedPercent: 50},
		{Language: "js-ts\x1b[31m", DependencyCount: 1, UsedExportsCount: 1, TotalExportsCount: 4, UsedPercent: 25},
	}); err != nil {
		t.Fatalf("append language breakdown: %v", err)
	}

	want := "Languages:\n" +
		"- go: 2 deps, Used/Total: 3/6 (50.0%)\n" +
		"- js-ts\\x1b[31m: 1 deps, Used/Total: 1/4 (25.0%)\n\n"
	if got := buffer.String(); got != want {
		t.Fatalf("unexpected language breakdown output:\ngot  %q\nwant %q", got, want)
	}
}

func TestFormatTableReturnsTemplateErrors(t *testing.T) {
	brokenTemplate := template.Must(template.New("broken").Parse(`{{.Missing}}`))
	tests := []struct {
		name         string
		templateSlot **template.Template
		report       Report
	}{
		{
			name:         "summary",
			templateSlot: &summarySectionTemplate,
			report: Report{
				Summary:      &Summary{},
				Dependencies: []DependencyReport{{Name: "dep"}},
			},
		},
		{
			name:         "thresholds in populated table",
			templateSlot: &effectiveThresholdsSectionTemplate,
			report: Report{
				EffectiveThresholds: &EffectiveThresholds{},
				Dependencies:        []DependencyReport{{Name: "dep"}},
			},
		},
		{
			name:         "thresholds in empty table",
			templateSlot: &effectiveThresholdsSectionTemplate,
			report:       Report{EffectiveThresholds: &EffectiveThresholds{}},
		},
		{
			name:         "language breakdown",
			templateSlot: &languageBreakdownSectionTemplate,
			report: Report{
				LanguageBreakdown: []LanguageSummary{{Language: "go"}},
				Dependencies:      []DependencyReport{{Name: "dep"}},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Cleanup(replaceReportTemplate(tc.templateSlot, brokenTemplate))
			if _, err := formatTable(tc.report); err == nil {
				t.Fatal("expected template error")
			}
		})
	}
}

func replaceReportTemplate(slot **template.Template, replacement *template.Template) func() {
	original := *slot
	*slot = replacement
	return func() {
		*slot = original
	}
}
