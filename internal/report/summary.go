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
	vulnerabilitySummary := newVulnerabilitySummaryBuilder()
	for _, dep := range dependencies {
		summary.UsedExportsCount += dep.UsedExportsCount
		summary.TotalExportsCount += dep.TotalExportsCount
		updateLicenseSummary(&summary, dep.License)
		updateConfidenceRollupStats(&confidenceStats, dep.ReachabilityConfidence)
		vulnerabilitySummary.add(dep.Vulnerabilities)
	}
	if summary.TotalExportsCount > 0 {
		summary.UsedPercent = (float64(summary.UsedExportsCount) / float64(summary.TotalExportsCount)) * 100
	}
	summary.Reachability = buildConfidenceRollup(confidenceStats)
	summary.Vulnerabilities = vulnerabilitySummary.summary()

	return &summary
}

func updateLicenseSummary(summary *Summary, license *DependencyLicense) {
	if license != nil && license.Denied {
		summary.DeniedLicenseCount++
		return
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

type vulnerabilitySummaryBuilder struct {
	totalFindings     int
	reachableFindings int
	highestSeverity   string
	highestPriority   string
	bySeverity        map[string]int
	byPriority        map[string]int
	sources           map[string]struct{}
}

func newVulnerabilitySummaryBuilder() *vulnerabilitySummaryBuilder {
	return &vulnerabilitySummaryBuilder{
		bySeverity: make(map[string]int),
		byPriority: make(map[string]int),
		sources:    make(map[string]struct{}),
	}
}

func (b *vulnerabilitySummaryBuilder) add(findings []VulnerabilityFinding) {
	for _, finding := range findings {
		b.totalFindings++
		severity := normalizeSeverity(finding.Severity)
		priority := NormalizeVulnerabilityPriorityThreshold(finding.Priority)
		b.bySeverity[severity]++
		b.byPriority[priority]++
		if finding.Reachable && !FindingSuppressedByException(finding) {
			b.reachableFindings++
		}
		if severityRank(severity) > severityRank(b.highestSeverity) {
			b.highestSeverity = severity
		}
		if priorityRank(priority) > priorityRank(b.highestPriority) {
			b.highestPriority = priority
		}
		if source := strings.TrimSpace(finding.Source); source != "" {
			b.sources[source] = struct{}{}
		}
	}
}

func (b *vulnerabilitySummaryBuilder) summary() *VulnerabilitySummary {
	if b.totalFindings == 0 {
		return nil
	}
	return &VulnerabilitySummary{
		TotalFindings:     b.totalFindings,
		ReachableFindings: b.reachableFindings,
		HighestSeverity:   b.highestSeverity,
		HighestPriority:   b.highestPriority,
		BySeverity:        copyNonZeroCounts(b.bySeverity),
		ByPriority:        copyNonZeroCounts(b.byPriority),
		Sources:           sortedSourceSet(b.sources),
	}
}

func copyNonZeroCounts(counts map[string]int) map[string]int {
	if len(counts) == 0 {
		return nil
	}
	copied := make(map[string]int, len(counts))
	for key, value := range counts {
		if value == 0 {
			continue
		}
		copied[key] = value
	}
	if len(copied) == 0 {
		return nil
	}
	return copied
}

func sortedSourceSet(sources map[string]struct{}) []string {
	if len(sources) == 0 {
		return nil
	}
	items := make([]string, 0, len(sources))
	for source := range sources {
		items = append(items, source)
	}
	sort.Strings(items)
	return items
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
