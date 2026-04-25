package report

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

var ErrBaselineMissing = errors.New("baseline report is missing summary data")

func ApplyBaseline(current, baseline Report) (Report, error) {
	return ApplyBaselineWithKeys(current, baseline, "", "")
}

func ApplyBaselineWithKeys(current, baseline Report, baselineKey, currentKey string) (Report, error) {
	currentSummary := current.Summary
	if currentSummary == nil {
		currentSummary = ComputeSummary(current.Dependencies)
		current.Summary = currentSummary
	}

	baselineSummary := baseline.Summary
	if baselineSummary == nil {
		baselineSummary = ComputeSummary(baseline.Dependencies)
	}
	if baselineSummary == nil {
		return current, ErrBaselineMissing
	}
	if baselineSummary.TotalExportsCount == 0 {
		return current, fmt.Errorf("baseline total exports count is zero")
	}

	currentWaste, ok := WastePercent(currentSummary)
	if !ok {
		return current, fmt.Errorf("current report has no export totals")
	}
	baselineWaste, _ := WastePercent(baselineSummary)
	delta := currentWaste - baselineWaste
	current.WasteIncreasePercent = &delta

	comparison := ComputeBaselineComparison(current, baseline)
	comparison.BaselineKey = strings.TrimSpace(baselineKey)
	comparison.CurrentKey = strings.TrimSpace(currentKey)
	current.BaselineComparison = &comparison

	return current, nil
}

func ComputeBaselineComparison(current, baseline Report) BaselineComparison {
	currentSummary := current.Summary
	if currentSummary == nil {
		currentSummary = ComputeSummary(current.Dependencies)
	}
	baselineSummary := baseline.Summary
	if baselineSummary == nil {
		baselineSummary = ComputeSummary(baseline.Dependencies)
	}

	currentUnused := sumEstimatedUnusedBytes(current.Dependencies)
	baselineUnused := sumEstimatedUnusedBytes(baseline.Dependencies)

	comparison := BaselineComparison{
		SummaryDelta: SummaryDelta{
			DependencyCountDelta:     safeSummaryField(currentSummary, func(s *Summary) int { return s.DependencyCount }) - safeSummaryField(baselineSummary, func(s *Summary) int { return s.DependencyCount }),
			UsedExportsCountDelta:    safeSummaryField(currentSummary, func(s *Summary) int { return s.UsedExportsCount }) - safeSummaryField(baselineSummary, func(s *Summary) int { return s.UsedExportsCount }),
			TotalExportsCountDelta:   safeSummaryField(currentSummary, func(s *Summary) int { return s.TotalExportsCount }) - safeSummaryField(baselineSummary, func(s *Summary) int { return s.TotalExportsCount }),
			UsedPercentDelta:         safeSummaryFloat(currentSummary, func(s *Summary) float64 { return s.UsedPercent }) - safeSummaryFloat(baselineSummary, func(s *Summary) float64 { return s.UsedPercent }),
			WastePercentDelta:        wasteFromSummary(currentSummary) - wasteFromSummary(baselineSummary),
			UnusedBytesDelta:         currentUnused - baselineUnused,
			KnownLicenseCountDelta:   safeSummaryField(currentSummary, func(s *Summary) int { return s.KnownLicenseCount }) - safeSummaryField(baselineSummary, func(s *Summary) int { return s.KnownLicenseCount }),
			UnknownLicenseCountDelta: safeSummaryField(currentSummary, func(s *Summary) int { return s.UnknownLicenseCount }) - safeSummaryField(baselineSummary, func(s *Summary) int { return s.UnknownLicenseCount }),
			DeniedLicenseCountDelta:  safeSummaryField(currentSummary, func(s *Summary) int { return s.DeniedLicenseCount }) - safeSummaryField(baselineSummary, func(s *Summary) int { return s.DeniedLicenseCount }),
		},
	}

	currentByKey := make(map[string]DependencyReport, len(current.Dependencies))
	for _, dep := range current.Dependencies {
		currentByKey[dependencyKey(dep)] = dep
	}
	baselineByKey := make(map[string]DependencyReport, len(baseline.Dependencies))
	for _, dep := range baseline.Dependencies {
		baselineByKey[dependencyKey(dep)] = dep
	}

	keys := make([]string, 0, len(currentByKey)+len(baselineByKey))
	seen := make(map[string]struct{}, len(currentByKey)+len(baselineByKey))
	for key := range currentByKey {
		keys = append(keys, key)
		seen[key] = struct{}{}
	}
	for key := range baselineByKey {
		if _, ok := seen[key]; ok {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)

	for _, key := range keys {
		curr, hasCurrent := currentByKey[key]
		base, hasBaseline := baselineByKey[key]

		delta, ok := dependencyDelta(curr, hasCurrent, base, hasBaseline)
		if !ok {
			comparison.UnchangedRows++
			continue
		}
		appendDependencyDelta(&comparison, delta)
	}
	comparison.NewDeniedLicenses = newlyDeniedLicenses(currentByKey, baselineByKey)

	return comparison
}

func appendDependencyDelta(comparison *BaselineComparison, delta DependencyDelta) {
	comparison.Dependencies = append(comparison.Dependencies, delta)
	switch delta.Kind {
	case DependencyDeltaAdded:
		comparison.Added = append(comparison.Added, delta)
	case DependencyDeltaRemoved:
		comparison.Removed = append(comparison.Removed, delta)
	case DependencyDeltaChanged:
		if delta.WastePercentDelta > 0 {
			comparison.Regressions = append(comparison.Regressions, delta)
		} else if delta.WastePercentDelta < 0 {
			comparison.Progressions = append(comparison.Progressions, delta)
		}
	}
}

func sumEstimatedUnusedBytes(dependencies []DependencyReport) int64 {
	total := int64(0)
	for _, dep := range dependencies {
		total += dep.EstimatedUnusedBytes
	}
	return total
}

func safeSummaryField(summary *Summary, selector func(*Summary) int) int {
	if summary == nil {
		return 0
	}
	return selector(summary)
}

func safeSummaryFloat(summary *Summary, selector func(*Summary) float64) float64 {
	if summary == nil {
		return 0
	}
	return selector(summary)
}

func wasteFromSummary(summary *Summary) float64 {
	if summary == nil || summary.TotalExportsCount == 0 {
		return 0
	}
	return 100 - summary.UsedPercent
}

func dependencyKey(dep DependencyReport) string {
	return dep.Language + "\x00" + dep.Name
}

func dependencyDelta(curr DependencyReport, hasCurrent bool, base DependencyReport, hasBaseline bool) (DependencyDelta, bool) {
	name := curr.Name
	language := curr.Language
	if !hasCurrent {
		name = base.Name
		language = base.Language
	}

	delta := DependencyDelta{
		Language: language,
		Name:     name,
	}

	switch {
	case hasCurrent && !hasBaseline:
		delta.Kind = DependencyDeltaAdded
		delta.UsedExportsCountDelta = curr.UsedExportsCount
		delta.TotalExportsCountDelta = curr.TotalExportsCount
		delta.UsedPercentDelta = curr.UsedPercent
		delta.EstimatedUnusedBytesDelta = curr.EstimatedUnusedBytes
		delta.WastePercentDelta = wasteFromDependency(curr)
		delta.DeniedIntroduced = isDenied(curr) && !isDenied(base)
		return delta, true
	case !hasCurrent && hasBaseline:
		delta.Kind = DependencyDeltaRemoved
		delta.UsedExportsCountDelta = -base.UsedExportsCount
		delta.TotalExportsCountDelta = -base.TotalExportsCount
		delta.UsedPercentDelta = -base.UsedPercent
		delta.EstimatedUnusedBytesDelta = -base.EstimatedUnusedBytes
		delta.WastePercentDelta = -wasteFromDependency(base)
		return delta, true
	default:
		delta.Kind = DependencyDeltaChanged
		delta.UsedExportsCountDelta = curr.UsedExportsCount - base.UsedExportsCount
		delta.TotalExportsCountDelta = curr.TotalExportsCount - base.TotalExportsCount
		delta.UsedPercentDelta = curr.UsedPercent - base.UsedPercent
		delta.EstimatedUnusedBytesDelta = curr.EstimatedUnusedBytes - base.EstimatedUnusedBytes
		delta.WastePercentDelta = wasteFromDependency(curr) - wasteFromDependency(base)
		delta.DeniedIntroduced = isDenied(curr) && !isDenied(base)
		if delta.UsedExportsCountDelta == 0 &&
			delta.TotalExportsCountDelta == 0 &&
			delta.UsedPercentDelta == 0 &&
			delta.EstimatedUnusedBytesDelta == 0 &&
			!delta.DeniedIntroduced {
			return DependencyDelta{}, false
		}
		return delta, true
	}
}

func wasteFromDependency(dep DependencyReport) float64 {
	if dep.TotalExportsCount == 0 {
		return 0
	}
	usedPercent := dep.UsedPercent
	if usedPercent <= 0 {
		usedPercent = (float64(dep.UsedExportsCount) / float64(dep.TotalExportsCount)) * 100
	}
	return 100 - usedPercent
}

func WastePercent(summary *Summary) (float64, bool) {
	if summary == nil {
		return 0, false
	}
	if summary.TotalExportsCount == 0 {
		return 0, false
	}
	return 100 - summary.UsedPercent, true
}

func newlyDeniedLicenses(currentByKey, baselineByKey map[string]DependencyReport) []DeniedLicenseDelta {
	items := make([]DeniedLicenseDelta, 0)
	for key, current := range currentByKey {
		if !isDenied(current) {
			continue
		}
		baseline, ok := baselineByKey[key]
		if ok && isDenied(baseline) {
			continue
		}
		spdx := ""
		if current.License != nil {
			spdx = current.License.SPDX
		}
		items = append(items, DeniedLicenseDelta{
			Language: current.Language,
			Name:     current.Name,
			SPDX:     spdx,
		})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Language != items[j].Language {
			return items[i].Language < items[j].Language
		}
		return items[i].Name < items[j].Name
	})
	return items
}

func isDenied(dep DependencyReport) bool {
	return dep.License != nil && dep.License.Denied
}
