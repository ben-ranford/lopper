package report

import (
	"fmt"
	"testing"
	"time"
)

func BenchmarkFormatLargeTable(b *testing.B) {
	formatter := NewFormatter()
	reportData := largeTableFixture()

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		if _, err := formatter.Format(reportData, FormatTable); err != nil {
			b.Fatalf("format table: %v", err)
		}
	}
}

func largeTableFixture() Report {
	dependencies := make([]DependencyReport, 0, 120)
	comparison := make([]DependencyDelta, 0, 40)
	regressions := make([]DependencyDelta, 0, 10)
	progressions := make([]DependencyDelta, 0, 10)

	for i := 0; i < cap(dependencies); i++ {
		name := fmt.Sprintf("dep-%03d", i)
		dependencies = append(dependencies, DependencyReport{
			Language:             "js-ts",
			Name:                 name,
			UsedExportsCount:     4 + (i % 7),
			TotalExportsCount:    20 + (i % 11),
			UsedPercent:          32.5 + float64(i%9),
			EstimatedUnusedBytes: int64(2_048 + (i * 128)),
			TopUsedSymbols: []SymbolUsage{
				{Name: "render", Count: 6 + (i % 3)},
				{Name: "hydrate", Count: 2 + (i % 5)},
			},
			RuntimeUsage: &RuntimeUsage{
				LoadCount:   3 + (i % 4),
				Correlation: RuntimeCorrelationOverlap,
			},
			RemovalCandidate: &RemovalCandidate{
				Score:      0.55 + float64(i%5)/10,
				Usage:      0.40,
				Impact:     0.35,
				Confidence: 0.25,
				Weights: RemovalCandidateWeights{
					Usage:      0.5,
					Impact:     0.3,
					Confidence: 0.2,
				},
				Rationale: []string{"unused exports dominate", "runtime overlap remains low"},
			},
			License: &DependencyLicense{
				SPDX:       "MIT",
				Source:     "package.json",
				Confidence: "high",
			},
			Provenance: &DependencyProvenance{
				Source:     "registry",
				Confidence: "high",
				Signals:    []string{"lockfile", "manifest"},
			},
		})

		if i < cap(comparison) {
			delta := DependencyDelta{
				Kind:                      DependencyDeltaChanged,
				Language:                  "js-ts",
				Name:                      name,
				UsedExportsCountDelta:     -1,
				TotalExportsCountDelta:    0,
				UsedPercentDelta:          -1.5,
				EstimatedUnusedBytesDelta: int64(512 + i*32),
				WastePercentDelta:         1.2 + float64(i%3),
			}
			comparison = append(comparison, delta)
			if i < cap(regressions) {
				regressions = append(regressions, delta)
			} else if len(progressions) < cap(progressions) {
				delta.WastePercentDelta *= -1
				progressions = append(progressions, delta)
			}
		}
	}

	return Report{
		SchemaVersion: SchemaVersion,
		GeneratedAt:   time.Unix(1_731_048_000, 0).UTC(),
		RepoPath:      "/repo",
		Scope: &ScopeMetadata{
			Mode:     "repo",
			Packages: []string{".", "packages/app"},
		},
		Dependencies:     dependencies,
		UsageUncertainty: &UsageUncertainty{ConfirmedImportUses: 180, UncertainImportUses: 4},
		Summary: &Summary{
			DependencyCount:     len(dependencies),
			UsedExportsCount:    640,
			TotalExportsCount:   2_640,
			UsedPercent:         24.2,
			KnownLicenseCount:   len(dependencies),
			UnknownLicenseCount: 0,
			DeniedLicenseCount:  0,
		},
		LanguageBreakdown: []LanguageSummary{
			{Language: "js-ts", DependencyCount: len(dependencies), UsedExportsCount: 640, TotalExportsCount: 2_640, UsedPercent: 24.2},
		},
		Cache: &CacheMetadata{
			Enabled:  true,
			Path:     ".artifacts/cache",
			ReadOnly: false,
			Hits:     42,
			Misses:   5,
			Writes:   3,
		},
		EffectiveThresholds: &EffectiveThresholds{
			FailOnIncreasePercent:             3,
			LowConfidenceWarningPercent:       35,
			MinUsagePercentForRecommendations: 45,
			MaxUncertainImportCount:           2,
		},
		EffectivePolicy: &EffectivePolicy{
			Sources: []string{"repo", "defaults"},
			Thresholds: EffectiveThresholds{
				FailOnIncreasePercent:             3,
				LowConfidenceWarningPercent:       35,
				MinUsagePercentForRecommendations: 45,
				MaxUncertainImportCount:           2,
			},
			RemovalCandidateWeights: RemovalCandidateWeights{
				Usage:      0.5,
				Impact:     0.3,
				Confidence: 0.2,
			},
			License: LicensePolicy{
				Deny:                      []string{"GPL-3.0-ONLY"},
				FailOnDenied:              true,
				IncludeRegistryProvenance: true,
			},
		},
		Warnings: []string{"runtime trace unavailable for 2 packages"},
		BaselineComparison: &BaselineComparison{
			BaselineKey: "commit:base",
			CurrentKey:  "commit:head",
			SummaryDelta: SummaryDelta{
				DependencyCountDelta:     2,
				UsedExportsCountDelta:    -4,
				TotalExportsCountDelta:   0,
				UsedPercentDelta:         -1.3,
				WastePercentDelta:        1.3,
				UnusedBytesDelta:         2_048,
				KnownLicenseCountDelta:   2,
				UnknownLicenseCountDelta: 0,
				DeniedLicenseCountDelta:  0,
			},
			Dependencies:  comparison,
			Regressions:   regressions,
			Progressions:  progressions,
			UnchangedRows: 18,
		},
	}
}
