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
			DependencyCountDelta:             safeSummaryField(currentSummary, func(s *Summary) int { return s.DependencyCount }) - safeSummaryField(baselineSummary, func(s *Summary) int { return s.DependencyCount }),
			UsedExportsCountDelta:            safeSummaryField(currentSummary, func(s *Summary) int { return s.UsedExportsCount }) - safeSummaryField(baselineSummary, func(s *Summary) int { return s.UsedExportsCount }),
			TotalExportsCountDelta:           safeSummaryField(currentSummary, func(s *Summary) int { return s.TotalExportsCount }) - safeSummaryField(baselineSummary, func(s *Summary) int { return s.TotalExportsCount }),
			UsedPercentDelta:                 safeSummaryFloat(currentSummary, func(s *Summary) float64 { return s.UsedPercent }) - safeSummaryFloat(baselineSummary, func(s *Summary) float64 { return s.UsedPercent }),
			WastePercentDelta:                wasteFromSummary(currentSummary) - wasteFromSummary(baselineSummary),
			UnusedBytesDelta:                 currentUnused - baselineUnused,
			KnownLicenseCountDelta:           safeSummaryField(currentSummary, func(s *Summary) int { return s.KnownLicenseCount }) - safeSummaryField(baselineSummary, func(s *Summary) int { return s.KnownLicenseCount }),
			UnknownLicenseCountDelta:         safeSummaryField(currentSummary, func(s *Summary) int { return s.UnknownLicenseCount }) - safeSummaryField(baselineSummary, func(s *Summary) int { return s.UnknownLicenseCount }),
			DeniedLicenseCountDelta:          safeSummaryField(currentSummary, func(s *Summary) int { return s.DeniedLicenseCount }) - safeSummaryField(baselineSummary, func(s *Summary) int { return s.DeniedLicenseCount }),
			ReachableVulnerabilityCountDelta: safeReachableVulnerabilityCount(currentSummary) - safeReachableVulnerabilityCount(baselineSummary),
		},
	}

	currentByKey := make(map[string]DependencyReport, len(current.Dependencies))
	currentByKeyInstances := make(map[string][]DependencyReport, len(current.Dependencies))
	for _, dep := range current.Dependencies {
		key := dependencyKey(dep)
		currentByKey[key] = dep
		currentByKeyInstances[key] = append(currentByKeyInstances[key], dep)
	}
	baselineByKey := make(map[string]DependencyReport, len(baseline.Dependencies))
	baselineByKeyInstances := make(map[string][]DependencyReport, len(baseline.Dependencies))
	for _, dep := range baseline.Dependencies {
		key := dependencyKey(dep)
		baselineByKey[key] = dep
		baselineByKeyInstances[key] = append(baselineByKeyInstances[key], dep)
	}

	keys := make([]string, 0, len(currentByKeyInstances)+len(baselineByKeyInstances))
	seen := make(map[string]struct{}, len(currentByKeyInstances)+len(baselineByKeyInstances))
	for key := range currentByKeyInstances {
		keys = append(keys, key)
		seen[key] = struct{}{}
	}
	for key := range baselineByKeyInstances {
		if _, ok := seen[key]; ok {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)

	for _, key := range keys {
		currentInstances := currentByKeyInstances[key]
		baselineInstances := baselineByKeyInstances[key]
		instanceCount := max(len(currentInstances), len(baselineInstances))
		for index := range instanceCount {
			hasCurrent := index < len(currentInstances)
			hasBaseline := index < len(baselineInstances)
			curr := DependencyReport{}
			base := DependencyReport{}
			if hasCurrent {
				curr = currentInstances[index]
			}
			if hasBaseline {
				base = baselineInstances[index]
			}

			delta, ok := dependencyDelta(curr, hasCurrent, base, hasBaseline)
			if !ok {
				comparison.UnchangedRows++
				continue
			}
			appendDependencyDelta(&comparison, delta)
		}
	}
	comparison.NewDeniedLicenses = newlyDeniedLicenses(currentByKey, baselineByKey)
	comparison.NewReachableVulnerabilities = newlyReachableVulnerabilities(currentByKey, baselineByKey)

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
		if runtimeDeltaIsRegression(delta.RuntimeDelta) {
			comparison.RuntimeRegressions = append(comparison.RuntimeRegressions, delta)
		}
		if runtimeDeltaIsImprovement(delta.RuntimeDelta) {
			comparison.RuntimeImprovements = append(comparison.RuntimeImprovements, delta)
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
		delta.ReachableVulnerabilityCountDelta = reachableVulnerabilityCount(curr)
		delta.ReachableVulnerabilitiesIntroduced = delta.ReachableVulnerabilityCountDelta > 0
		return delta, true
	case !hasCurrent && hasBaseline:
		delta.Kind = DependencyDeltaRemoved
		delta.UsedExportsCountDelta = -base.UsedExportsCount
		delta.TotalExportsCountDelta = -base.TotalExportsCount
		delta.UsedPercentDelta = -base.UsedPercent
		delta.EstimatedUnusedBytesDelta = -base.EstimatedUnusedBytes
		delta.WastePercentDelta = -wasteFromDependency(base)
		delta.ReachableVulnerabilityCountDelta = -reachableVulnerabilityCount(base)
		return delta, true
	default:
		delta.Kind = DependencyDeltaChanged
		delta.UsedExportsCountDelta = curr.UsedExportsCount - base.UsedExportsCount
		delta.TotalExportsCountDelta = curr.TotalExportsCount - base.TotalExportsCount
		delta.UsedPercentDelta = curr.UsedPercent - base.UsedPercent
		delta.EstimatedUnusedBytesDelta = curr.EstimatedUnusedBytes - base.EstimatedUnusedBytes
		delta.WastePercentDelta = wasteFromDependency(curr) - wasteFromDependency(base)
		runtimeDelta, runtimeChanged := dependencyRuntimeDelta(curr.RuntimeUsage, base.RuntimeUsage)
		delta.RuntimeDelta = runtimeDelta
		delta.DeniedIntroduced = isDenied(curr) && !isDenied(base)
		delta.ReachableVulnerabilityCountDelta = reachableVulnerabilityCount(curr) - reachableVulnerabilityCount(base)
		delta.ReachableVulnerabilitiesIntroduced = hasNewReachableVulnerabilities(curr, base)
		if delta.UsedExportsCountDelta == 0 &&
			delta.TotalExportsCountDelta == 0 &&
			delta.UsedPercentDelta == 0 &&
			delta.EstimatedUnusedBytesDelta == 0 &&
			!runtimeChanged &&
			!delta.DeniedIntroduced &&
			!delta.ReachableVulnerabilitiesIntroduced {
			return DependencyDelta{}, false
		}
		return delta, true
	}
}

func dependencyRuntimeDelta(currentUsage, baselineUsage *RuntimeUsage) (*RuntimeDelta, bool) {
	if currentUsage == nil && baselineUsage == nil {
		return nil, false
	}

	delta := runtimePresenceDelta(currentUsage, baselineUsage)
	if currentUsage == nil || baselineUsage == nil {
		return delta, false
	}

	delta.Comparable = true
	appendRuntimeLoadCountChanges(delta, currentUsage, baselineUsage)
	appendRuntimeCorrelationChange(delta, currentUsage, baselineUsage)
	appendRuntimeOnlyChange(delta, currentUsage, baselineUsage)
	appendRuntimeCollectionChanges(delta, currentUsage, baselineUsage)
	return delta, len(delta.ChangeTypes) > 0
}

func runtimePresenceDelta(currentUsage, baselineUsage *RuntimeUsage) *RuntimeDelta {
	delta := &RuntimeDelta{
		BaselinePresent: baselineUsage != nil,
		CurrentPresent:  currentUsage != nil,
	}
	if baselineUsage != nil {
		delta.BaselineLoadCount = intPointer(baselineUsage.LoadCount)
	}
	if currentUsage != nil {
		delta.CurrentLoadCount = intPointer(currentUsage.LoadCount)
	}
	return delta
}

func appendRuntimeLoadCountChanges(delta *RuntimeDelta, currentUsage, baselineUsage *RuntimeUsage) {
	loadCountDelta := currentUsage.LoadCount - baselineUsage.LoadCount
	delta.LoadCountDelta = intPointer(loadCountDelta)
	if loadCountDelta != 0 {
		delta.ChangeTypes = append(delta.ChangeTypes, RuntimeChangeLoadCount)
	}
	if baselineUsage.LoadCount == 0 && currentUsage.LoadCount > 0 {
		delta.NewRuntimeLoads = true
		delta.ChangeTypes = append(delta.ChangeTypes, RuntimeChangeNewRuntimeLoads)
	}
	if baselineUsage.LoadCount > 0 && currentUsage.LoadCount == 0 {
		delta.RemovedRuntimeLoads = true
		delta.ChangeTypes = append(delta.ChangeTypes, RuntimeChangeRemovedRuntimeLoads)
	}
}

func appendRuntimeCorrelationChange(delta *RuntimeDelta, currentUsage, baselineUsage *RuntimeUsage) {
	delta.BaselineCorrelation = runtimeUsageCorrelation(baselineUsage)
	delta.CurrentCorrelation = runtimeUsageCorrelation(currentUsage)
	if delta.BaselineCorrelation != delta.CurrentCorrelation {
		delta.ChangeTypes = append(delta.ChangeTypes, RuntimeChangeCorrelation)
	}
}

func appendRuntimeOnlyChange(delta *RuntimeDelta, currentUsage, baselineUsage *RuntimeUsage) {
	baselineRuntimeOnly := isRuntimeOnlyUsage(baselineUsage)
	currentRuntimeOnly := isRuntimeOnlyUsage(currentUsage)
	if currentRuntimeOnly && !baselineRuntimeOnly {
		delta.RuntimeOnlyRegression = true
		delta.ChangeTypes = append(delta.ChangeTypes, RuntimeChangeRuntimeOnlyRegression)
	}
	if baselineRuntimeOnly && !currentRuntimeOnly {
		delta.RuntimeOnlyImprovement = true
		delta.ChangeTypes = append(delta.ChangeTypes, RuntimeChangeRuntimeOnlyImprovement)
	}
}

func appendRuntimeCollectionChanges(delta *RuntimeDelta, currentUsage, baselineUsage *RuntimeUsage) {
	delta.ModulesAdded, delta.ModulesRemoved, delta.ModulesChanged = compareRuntimeModuleUsage(currentUsage.Modules, baselineUsage.Modules)
	appendRuntimeCollectionChangeType(delta, RuntimeChangeModules, delta.ModulesAdded, delta.ModulesRemoved, delta.ModulesChanged)
	delta.ParentModulesAdded, delta.ParentModulesRemoved, delta.ParentModulesChanged = compareRuntimeModuleUsage(currentUsage.ParentModules, baselineUsage.ParentModules)
	appendRuntimeCollectionChangeType(delta, RuntimeChangeParentModules, delta.ParentModulesAdded, delta.ParentModulesRemoved, delta.ParentModulesChanged)
	delta.EntrypointsAdded, delta.EntrypointsRemoved, delta.EntrypointsChanged = compareRuntimeModuleUsage(currentUsage.Entrypoints, baselineUsage.Entrypoints)
	appendRuntimeCollectionChangeType(delta, RuntimeChangeEntrypoints, delta.EntrypointsAdded, delta.EntrypointsRemoved, delta.EntrypointsChanged)
}

func appendRuntimeCollectionChangeType(delta *RuntimeDelta, changeType RuntimeChangeType, added, removed, changed []RuntimeModuleDelta) {
	if len(added) > 0 || len(removed) > 0 || len(changed) > 0 {
		delta.ChangeTypes = append(delta.ChangeTypes, changeType)
	}
}

func compareRuntimeModuleUsage(current, baseline []RuntimeModuleUsage) ([]RuntimeModuleDelta, []RuntimeModuleDelta, []RuntimeModuleDelta) {
	currentByModule := runtimeModuleCounts(current)
	baselineByModule := runtimeModuleCounts(baseline)
	keys := make([]string, 0, len(currentByModule)+len(baselineByModule))
	seen := make(map[string]struct{}, len(currentByModule)+len(baselineByModule))
	for module := range currentByModule {
		keys = append(keys, module)
		seen[module] = struct{}{}
	}
	for module := range baselineByModule {
		if _, ok := seen[module]; ok {
			continue
		}
		keys = append(keys, module)
	}
	sort.Strings(keys)

	added := make([]RuntimeModuleDelta, 0)
	removed := make([]RuntimeModuleDelta, 0)
	changed := make([]RuntimeModuleDelta, 0)
	for _, module := range keys {
		currentCount, hasCurrent := currentByModule[module]
		baselineCount, hasBaseline := baselineByModule[module]
		moduleDelta := RuntimeModuleDelta{
			Module:        module,
			BaselineCount: baselineCount,
			CurrentCount:  currentCount,
			CountDelta:    currentCount - baselineCount,
		}
		switch {
		case hasCurrent && !hasBaseline:
			added = append(added, moduleDelta)
		case !hasCurrent && hasBaseline:
			removed = append(removed, moduleDelta)
		case moduleDelta.CountDelta != 0:
			changed = append(changed, moduleDelta)
		}
	}
	return added, removed, changed
}

func runtimeModuleCounts(items []RuntimeModuleUsage) map[string]int {
	counts := make(map[string]int, len(items))
	for _, item := range items {
		counts[item.Module] += item.Count
	}
	return counts
}

func runtimeDeltaIsRegression(delta *RuntimeDelta) bool {
	if delta == nil || !delta.Comparable {
		return false
	}
	return delta.NewRuntimeLoads || delta.RuntimeOnlyRegression || runtimeDeltaLoadCount(delta) > 0
}

func runtimeDeltaIsImprovement(delta *RuntimeDelta) bool {
	if delta == nil || !delta.Comparable {
		return false
	}
	return delta.RemovedRuntimeLoads || delta.RuntimeOnlyImprovement || runtimeDeltaLoadCount(delta) < 0
}

func runtimeDeltaLoadCount(delta *RuntimeDelta) int {
	if delta == nil || delta.LoadCountDelta == nil {
		return 0
	}
	return *delta.LoadCountDelta
}

func isRuntimeOnlyUsage(usage *RuntimeUsage) bool {
	return usage != nil && (usage.RuntimeOnly || runtimeUsageCorrelation(usage) == RuntimeCorrelationRuntimeOnly)
}

func intPointer(value int) *int {
	return &value
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

func safeReachableVulnerabilityCount(summary *Summary) int {
	if summary == nil || summary.Vulnerabilities == nil {
		return 0
	}
	return summary.Vulnerabilities.ReachableFindings
}

func reachableVulnerabilityCount(dep DependencyReport) int {
	count := 0
	for _, finding := range dep.Vulnerabilities {
		if finding.Reachable {
			count++
		}
	}
	return count
}

func hasNewReachableVulnerabilities(current, baseline DependencyReport) bool {
	return len(newReachableVulnerabilityFindings(current, baseline)) > 0
}

func newlyReachableVulnerabilities(currentByKey, baselineByKey map[string]DependencyReport) []VulnerabilityDelta {
	items := make([]VulnerabilityDelta, 0)
	for key, current := range currentByKey {
		baseline := baselineByKey[key]
		for _, finding := range newReachableVulnerabilityFindings(current, baseline) {
			items = append(items, VulnerabilityDelta{
				Language:      current.Language,
				Name:          current.Name,
				AdvisoryID:    finding.AdvisoryID,
				Package:       finding.Package,
				Severity:      finding.Severity,
				FixedVersion:  finding.FixedVersion,
				Source:        finding.Source,
				Priority:      finding.Priority,
				PriorityScore: finding.PriorityScore,
				Evidence:      append([]string{}, finding.Evidence...),
			})
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].PriorityScore != items[j].PriorityScore {
			return items[i].PriorityScore > items[j].PriorityScore
		}
		if priorityRank(items[i].Priority) != priorityRank(items[j].Priority) {
			return priorityRank(items[i].Priority) > priorityRank(items[j].Priority)
		}
		if items[i].Language != items[j].Language {
			return items[i].Language < items[j].Language
		}
		if items[i].Name != items[j].Name {
			return items[i].Name < items[j].Name
		}
		return items[i].AdvisoryID < items[j].AdvisoryID
	})
	return items
}

func newReachableVulnerabilityFindings(current, baseline DependencyReport) []VulnerabilityFinding {
	baselineReachable := make(map[string]struct{}, len(baseline.Vulnerabilities))
	for _, finding := range baseline.Vulnerabilities {
		if !finding.Reachable {
			continue
		}
		baselineReachable[vulnerabilityFindingKey(finding)] = struct{}{}
	}
	items := make([]VulnerabilityFinding, 0)
	for _, finding := range current.Vulnerabilities {
		if !finding.Reachable {
			continue
		}
		if _, ok := baselineReachable[vulnerabilityFindingKey(finding)]; ok {
			continue
		}
		items = append(items, finding)
	}
	sortVulnerabilityFindings(items)
	return items
}
