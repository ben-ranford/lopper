package report

import (
	"sort"
	"strings"
)

type confidenceRollupStats struct {
	count   int
	total   float64
	lowest  float64
	highest float64
}

func ComputeSummary(dependencies []DependencyReport) *Summary {
	if len(dependencies) == 0 {
		return nil
	}

	summary := Summary{DependencyCount: len(dependencies)}
	var confidenceStats confidenceRollupStats
	for _, dep := range dependencies {
		summary.UsedExportsCount += dep.UsedExportsCount
		summary.TotalExportsCount += dep.TotalExportsCount
		updateLicenseSummary(&summary, dep.License)
		updateConfidenceRollupStats(&confidenceStats, dep.ReachabilityConfidence)
	}
	if summary.TotalExportsCount > 0 {
		summary.UsedPercent = (float64(summary.UsedExportsCount) / float64(summary.TotalExportsCount)) * 100
	}
	summary.Reachability = buildConfidenceRollup(confidenceStats)

	return &summary
}

func updateLicenseSummary(summary *Summary, license *DependencyLicense) {
	if license != nil && license.Denied {
		summary.DeniedLicenseCount++
	}
	if license == nil || license.Unknown || strings.TrimSpace(license.SPDX) == "" {
		summary.UnknownLicenseCount++
		return
	}

	summary.KnownLicenseCount++
}

func updateConfidenceRollupStats(stats *confidenceRollupStats, confidence *ReachabilityConfidence) {
	if confidence == nil {
		return
	}

	score := confidence.Score
	stats.total += score
	if stats.count == 0 || score < stats.lowest {
		stats.lowest = score
	}
	if stats.count == 0 || score > stats.highest {
		stats.highest = score
	}
	stats.count++
}

func buildConfidenceRollup(stats confidenceRollupStats) *ReachabilityRollup {
	if stats.count == 0 {
		return nil
	}

	return &ReachabilityRollup{
		Model:        reachabilityConfidenceModelV2,
		AverageScore: roundTo(stats.total/float64(stats.count), 1),
		LowestScore:  roundTo(stats.lowest, 1),
		HighestScore: roundTo(stats.highest, 1),
	}
}

func ComputeLanguageBreakdown(dependencies []DependencyReport) []LanguageSummary {
	if len(dependencies) == 0 {
		return nil
	}

	byLanguage := make(map[string]*LanguageSummary)
	for _, dep := range dependencies {
		languageID := dep.Language
		if languageID == "" {
			continue
		}
		current, ok := byLanguage[languageID]
		if !ok {
			current = &LanguageSummary{Language: languageID}
			byLanguage[languageID] = current
		}
		current.DependencyCount++
		current.UsedExportsCount += dep.UsedExportsCount
		current.TotalExportsCount += dep.TotalExportsCount
	}

	breakdown := make([]LanguageSummary, 0, len(byLanguage))
	if len(byLanguage) == 0 {
		return nil
	}
	for _, item := range byLanguage {
		if item.TotalExportsCount > 0 {
			item.UsedPercent = (float64(item.UsedExportsCount) / float64(item.TotalExportsCount)) * 100
		}
		breakdown = append(breakdown, *item)
	}
	sort.Slice(breakdown, func(i, j int) bool {
		return breakdown[i].Language < breakdown[j].Language
	})
	return breakdown
}
