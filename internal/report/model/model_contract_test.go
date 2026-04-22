package model

import (
	"encoding/json"
	"reflect"
	"sort"
	"testing"
	"time"
)

func TestReportJSONContractRoundTrip(t *testing.T) {
	source := representativeReport()

	payload, err := json.Marshal(source)
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}

	var decoded Report
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("unmarshal report: %v", err)
	}
	if !reflect.DeepEqual(decoded, source) {
		t.Fatalf("round-trip mismatch\nsource:  %#v\ndecoded: %#v", source, decoded)
	}
}

func TestReportJSONContractShape(t *testing.T) {
	source := representativeReport()
	payload, err := json.Marshal(source)
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}

	var root map[string]any
	if err := json.Unmarshal(payload, &root); err != nil {
		t.Fatalf("unmarshal JSON object: %v", err)
	}

	reportKeys := []string{
		"baselineComparison",
		"cache",
		"dependencies",
		"effectivePolicy",
		"effectiveThresholds",
		"generatedAt",
		"languageBreakdown",
		"repoPath",
		"schemaVersion",
		"scope",
		"summary",
		"usageUncertainty",
		"warnings",
		"wasteIncreasePercent",
	}
	assertKeys(t, root, reportKeys...)

	dependencies := jsonArray(t, root, "dependencies")
	if len(dependencies) != 1 {
		t.Fatalf("dependencies length = %d, want 1", len(dependencies))
	}
	dependency := jsonObjectValue(t, dependencies[0], "dependencies[0]")
	dependencyKeys := []string{
		"codemod",
		"estimatedUnusedBytes",
		"language",
		"license",
		"name",
		"provenance",
		"reachabilityConfidence",
		"recommendations",
		"removalCandidate",
		"riskCues",
		"runtimeUsage",
		"topUsedSymbols",
		"totalExportsCount",
		"unusedExports",
		"unusedImports",
		"usedExportsCount",
		"usedImports",
		"usedPercent",
	}
	assertKeys(t, dependency, dependencyKeys...)

	summary := jsonObject(t, root, "summary")
	summaryKeys := []string{
		"deniedLicenseCount",
		"dependencyCount",
		"knownLicenseCount",
		"reachability",
		"totalExportsCount",
		"unknownLicenseCount",
		"usedExportsCount",
		"usedPercent",
	}
	assertKeys(t, summary, summaryKeys...)

	baselineComparison := jsonObject(t, root, "baselineComparison")
	baselineComparisonKeys := []string{
		"baselineKey",
		"currentKey",
		"dependencies",
		"newDeniedLicenses",
		"regressions",
		"summaryDelta",
		"unchangedRows",
	}
	assertKeys(t, baselineComparison, baselineComparisonKeys...)

	reachability := jsonObject(t, dependency, "reachabilityConfidence")
	assertKeys(t, reachability, "model", "rationaleCodes", "score", "signals", "summary")

	removalCandidate := jsonObject(t, dependency, "removalCandidate")
	assertKeys(t, removalCandidate, "confidence", "impact", "rationale", "score", "usage", "weights")
	assertKeys(t, jsonObject(t, removalCandidate, "weights"), "confidence", "impact", "usage")
}

func representativeReport() Report {
	wasteIncreasePercent := 12.5
	delta := DependencyDelta{
		Kind:                      DependencyDeltaChanged,
		Language:                  "js",
		Name:                      "lodash",
		UsedExportsCountDelta:     -2,
		TotalExportsCountDelta:    1,
		UsedPercentDelta:          -20,
		EstimatedUnusedBytesDelta: 1024,
		WastePercentDelta:         20,
		DeniedIntroduced:          true,
	}

	return Report{
		SchemaVersion: SchemaVersion,
		GeneratedAt:   time.Date(2026, 4, 22, 1, 2, 3, 0, time.UTC),
		RepoPath:      "/repo",
		Scope: &ScopeMetadata{
			Mode:     "repo",
			Packages: []string{"app"},
		},
		Dependencies: []DependencyReport{
			{
				Language:             "js",
				Name:                 "lodash",
				UsedExportsCount:     3,
				TotalExportsCount:    10,
				UsedPercent:          30,
				EstimatedUnusedBytes: 2048,
				TopUsedSymbols: []SymbolUsage{
					{Name: "map", Module: "lodash/map", Count: 2},
				},
				UsedImports: []ImportUse{
					{
						Name:                  "map",
						Module:                "lodash",
						Locations:             []Location{{File: "src/app.js", Line: 7, Column: 3}},
						Provenance:            []string{"static"},
						ConfidenceScore:       0.92,
						ConfidenceReasonCodes: []string{"literal-import"},
					},
				},
				UnusedImports: []ImportUse{
					{Name: "chunk", Module: "lodash"},
				},
				UnusedExports: []SymbolRef{
					{Name: "filter", Module: "lodash/filter", ConfidenceScore: 0.75, ConfidenceReasonCodes: []string{"no-reference"}},
				},
				RiskCues: []RiskCue{
					{Code: "dynamic", Severity: "medium", Message: "dynamic require", ConfidenceScore: 0.6, ConfidenceReasonCodes: []string{"runtime-only"}},
				},
				Recommendations: []Recommendation{
					{
						Code:                  "remove-unused",
						Priority:              "medium",
						Message:               "Trim unused exports",
						Rationale:             "unused",
						ConfidenceScore:       0.7,
						ConfidenceReasonCodes: []string{"high-waste"},
					},
				},
				Codemod: &CodemodReport{
					Mode: "suggest",
					Suggestions: []CodemodSuggestion{
						{
							File:        "src/app.js",
							Line:        7,
							ImportName:  "map",
							FromModule:  "lodash",
							ToModule:    "lodash/map",
							Original:    "import { map } from 'lodash'",
							Replacement: "import map from 'lodash/map'",
							Patch:       "@@",
						},
					},
					Skips: []CodemodSkip{
						{File: "src/skip.js", Line: 1, ImportName: "get", Module: "lodash", ReasonCode: "dynamic", Message: "dynamic import"},
					},
					Apply: &CodemodApplyReport{
						AppliedFiles: 1,
						Results:      []CodemodApplyResult{{File: "src/app.js", Status: "applied", PatchCount: 1, Message: "ok"}},
					},
				},
				RuntimeUsage: &RuntimeUsage{
					LoadCount:   4,
					Correlation: RuntimeCorrelationOverlap,
					RuntimeOnly: true,
					Modules:     []RuntimeModuleUsage{{Module: "lodash", Count: 4}},
					TopSymbols:  []RuntimeSymbolUsage{{Symbol: "map", Module: "lodash", Count: 4}},
				},
				ReachabilityConfidence: &ReachabilityConfidence{
					Model:          "reachability-v2",
					Score:          0.81,
					Summary:        "mostly static",
					RationaleCodes: []string{"static-import"},
					Signals: []ReachabilitySignal{
						{Code: "static-import", Score: 1, Weight: 0.7, Contribution: 0.7, Rationale: "imported"},
					},
				},
				RemovalCandidate: &RemovalCandidate{
					Score:      0.77,
					Usage:      0.2,
					Impact:     0.4,
					Confidence: 0.8,
					Weights:    RemovalCandidateWeights{Usage: 0.5, Impact: 0.3, Confidence: 0.2},
					Rationale:  []string{"low usage"},
				},
				License: &DependencyLicense{
					SPDX:       "MIT",
					Raw:        "MIT",
					Source:     "package",
					Confidence: "high",
					Denied:     true,
					Evidence:   []string{"package.json"},
				},
				Provenance: &DependencyProvenance{
					Source:     "manifest",
					Confidence: "high",
					Signals:    []string{"lockfile"},
				},
			},
		},
		UsageUncertainty: &UsageUncertainty{
			ConfirmedImportUses: 1,
			UncertainImportUses: 1,
			Samples:             []Location{{File: "src/dynamic.js", Line: 12, Column: 8}},
		},
		Summary: &Summary{
			DependencyCount:     1,
			UsedExportsCount:    3,
			TotalExportsCount:   10,
			UsedPercent:         30,
			KnownLicenseCount:   1,
			UnknownLicenseCount: 0,
			DeniedLicenseCount:  1,
			Reachability:        &ReachabilityRollup{Model: "reachability-v2", AverageScore: 0.8, LowestScore: 0.8, HighestScore: 0.8},
		},
		LanguageBreakdown: []LanguageSummary{
			{Language: "js", DependencyCount: 1, UsedExportsCount: 3, TotalExportsCount: 10, UsedPercent: 30},
		},
		Cache: &CacheMetadata{
			Enabled:       true,
			Path:          ".lopper/cache",
			Hits:          3,
			Misses:        1,
			Writes:        1,
			Invalidations: []CacheInvalidation{{Key: "js:lodash", Reason: "changed"}},
		},
		EffectiveThresholds: &EffectiveThresholds{
			FailOnIncreasePercent:             5,
			LowConfidenceWarningPercent:       30,
			MinUsagePercentForRecommendations: 75,
			MaxUncertainImportCount:           10,
		},
		EffectivePolicy: &EffectivePolicy{
			Sources:                 []string{"defaults"},
			Thresholds:              EffectiveThresholds{FailOnIncreasePercent: 5},
			RemovalCandidateWeights: RemovalCandidateWeights{Usage: 0.5, Impact: 0.3, Confidence: 0.2},
			License:                 LicensePolicy{Deny: []string{"GPL-3.0"}, FailOnDenied: true, IncludeRegistryProvenance: true},
		},
		Warnings:             []string{"dynamic import detected"},
		WasteIncreasePercent: &wasteIncreasePercent,
		BaselineComparison: &BaselineComparison{
			BaselineKey:       "base",
			CurrentKey:        "head",
			SummaryDelta:      SummaryDelta{DependencyCountDelta: 1, UsedExportsCountDelta: -2, TotalExportsCountDelta: 1, UsedPercentDelta: -20, WastePercentDelta: 20, UnusedBytesDelta: 1024, KnownLicenseCountDelta: 1, DeniedLicenseCountDelta: 1},
			Dependencies:      []DependencyDelta{delta},
			Regressions:       []DependencyDelta{delta},
			NewDeniedLicenses: []DeniedLicenseDelta{{Language: "js", Name: "lodash", SPDX: "MIT"}},
			UnchangedRows:     1,
		},
	}
}

func assertKeys(t *testing.T, object map[string]any, expected ...string) {
	t.Helper()

	actual := make([]string, 0, len(object))
	for key := range object {
		actual = append(actual, key)
	}
	sort.Strings(actual)
	sort.Strings(expected)
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("JSON keys = %v, want %v", actual, expected)
	}
}

func jsonObject(t *testing.T, object map[string]any, key string) map[string]any {
	t.Helper()

	value, ok := object[key]
	if !ok {
		t.Fatalf("missing JSON key %q", key)
	}
	return jsonObjectValue(t, value, key)
}

func jsonObjectValue(t *testing.T, value any, path string) map[string]any {
	t.Helper()

	object, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("%s has type %T, want object", path, value)
	}
	return object
}

func jsonArray(t *testing.T, object map[string]any, key string) []any {
	t.Helper()

	value, ok := object[key]
	if !ok {
		t.Fatalf("missing JSON key %q", key)
	}
	array, ok := value.([]any)
	if !ok {
		t.Fatalf("%s has type %T, want array", key, value)
	}
	return array
}
