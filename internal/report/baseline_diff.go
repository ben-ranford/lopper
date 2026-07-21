package report

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

var ErrBaselineMissing = errors.New("baseline report is missing summary data")
var ErrBaselineAmbiguousIdentityBridge = errors.New("baseline dependency identity bridge is ambiguous")

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

	comparison, err := ComputeBaselineComparisonChecked(current, baseline)
	if err != nil {
		return current, err
	}
	comparison.BaselineKey = strings.TrimSpace(baselineKey)
	comparison.CurrentKey = strings.TrimSpace(currentKey)
	current.BaselineComparison = &comparison

	return current, nil
}

func ComputeBaselineComparisonChecked(current, baseline Report) (BaselineComparison, error) {
	return computeBaselineComparison(current, baseline, PairDependencyInstancesChecked)
}

func ComputeBaselineComparison(current, baseline Report) BaselineComparison {
	return computeBaselineComparisonFromPairs(current, baseline, PairDependencyInstances(current.Dependencies, baseline.Dependencies))
}

func computeBaselineComparison(current, baseline Report, pairer func([]DependencyReport, []DependencyReport) ([]DependencyInstancePair, error)) (BaselineComparison, error) {
	pairs, err := pairer(current.Dependencies, baseline.Dependencies)
	if err != nil {
		return BaselineComparison{}, err
	}
	return computeBaselineComparisonFromPairs(current, baseline, pairs), nil
}

func computeBaselineComparisonFromPairs(current, baseline Report, pairs []DependencyInstancePair) BaselineComparison {
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
	appendDependencyInstanceDeltas(&comparison, pairs)
	comparison.NewDeniedLicenses = newlyDeniedLicensesFromPairs(pairs)
	comparison.NewReachableVulnerabilities = newlyReachableVulnerabilitiesFromPairs(pairs)

	return comparison
}

type dependencyInstanceIndex struct {
	instances map[string][]DependencyReport
}

type DependencyInstancePair struct {
	Key             string
	Current         DependencyReport
	HasCurrent      bool
	CurrentOrdinal  int
	Baseline        DependencyReport
	HasBaseline     bool
	BaselineOrdinal int
}

func indexDependencyInstances(dependencies []DependencyReport) dependencyInstanceIndex {
	index := dependencyInstanceIndex{
		instances: make(map[string][]DependencyReport, len(dependencies)),
	}
	for _, dep := range dependencies {
		key := DependencyVersionlessKey(dep)
		index.instances[key] = append(index.instances[key], dep)
	}
	return index
}

func sortedDependencyInstanceKeys(current, baseline map[string][]DependencyReport) []string {
	keys := make([]string, 0, len(current)+len(baseline))
	seen := make(map[string]struct{}, len(current)+len(baseline))
	for key := range current {
		keys = append(keys, key)
		seen[key] = struct{}{}
	}
	for key := range baseline {
		if _, ok := seen[key]; !ok {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

func PairDependencyInstances(current, baseline []DependencyReport) []DependencyInstancePair {
	pairs, err := PairDependencyInstancesChecked(current, baseline)
	if err == nil {
		return pairs
	}
	if !errors.Is(err, ErrBaselineAmbiguousIdentityBridge) {
		return nil
	}
	return pairDependencyInstancesUnbridged(current, baseline)
}

func pairDependencyInstancesUnbridged(current, baseline []DependencyReport) []DependencyInstancePair {
	currentIndex := indexDependencyInstances(current)
	baselineIndex := indexDependencyInstances(baseline)
	pairs := make([]DependencyInstancePair, 0, len(current)+len(baseline))
	for _, key := range sortedDependencyInstanceKeys(currentIndex.instances, baselineIndex.instances) {
		pairs = append(pairs, pairDependencyInstancesForKey(key, currentIndex.instances[key], baselineIndex.instances[key])...)
	}
	return pairs
}

func PairDependencyInstancesChecked(current, baseline []DependencyReport) ([]DependencyInstancePair, error) {
	currentIndex := indexDependencyInstances(current)
	baselineIndex := indexDependencyInstances(baseline)

	groups, err := planCheckedDependencyPairingGroups(currentIndex.instances, baselineIndex.instances)
	if err != nil {
		return nil, err
	}

	pairs := make([]DependencyInstancePair, 0, len(current)+len(baseline))
	for _, group := range groups {
		if group.bridge {
			pairs = append(pairs, DependencyInstancePair{
				Key:             group.key,
				Current:         group.current[0],
				HasCurrent:      true,
				CurrentOrdinal:  0,
				Baseline:        group.baseline[0],
				HasBaseline:     true,
				BaselineOrdinal: 0,
			})
			continue
		}
		pairs = append(pairs, pairDependencyInstancesForKey(group.key, group.current, group.baseline)...)
	}
	return pairs, nil
}

type dependencyPairingGroup struct {
	key      string
	current  []DependencyReport
	baseline []DependencyReport
	bridge   bool
}

type dependencyRawKeyGroup struct {
	rawKey         string
	anonymousKeys  []string
	anonymousCount int
	stableKeys     []string
	stableCount    int
}

type dependencyIdentityBridge struct {
	rawKey      string
	currentKey  string
	baselineKey string
}

func planCheckedDependencyPairingGroups(current, baseline map[string][]DependencyReport) ([]dependencyPairingGroup, error) {
	currentRawGroups := indexDependencyRawGroups(current)
	baselineRawGroups := indexDependencyRawGroups(baseline)
	bridges, err := planDependencyIdentityBridges(currentRawGroups, baselineRawGroups)
	if err != nil {
		return nil, err
	}

	bridgedKeys := make(map[string]struct{}, len(bridges)*2)
	groups := make([]dependencyPairingGroup, 0, len(current)+len(baseline))
	for _, bridge := range bridges {
		bridgedKeys[bridge.currentKey] = struct{}{}
		bridgedKeys[bridge.baselineKey] = struct{}{}
		groups = append(groups, dependencyPairingGroup{
			key:      bridge.rawKey,
			current:  current[bridge.currentKey],
			baseline: baseline[bridge.baselineKey],
			bridge:   true,
		})
	}

	for _, key := range sortedDependencyInstanceKeys(current, baseline) {
		if _, ok := bridgedKeys[key]; ok {
			continue
		}
		groups = append(groups, dependencyPairingGroup{
			key:      key,
			current:  current[key],
			baseline: baseline[key],
		})
	}

	sort.Slice(groups, func(i, j int) bool {
		return groups[i].key < groups[j].key
	})
	return groups, nil
}

func indexDependencyRawGroups(instances map[string][]DependencyReport) map[string]dependencyRawKeyGroup {
	groups := make(map[string]dependencyRawKeyGroup, len(instances))
	for key, deps := range instances {
		if len(deps) == 0 {
			continue
		}
		rawKey := dependencyRawKey(deps[0])
		group := groups[rawKey]
		group.rawKey = rawKey
		if dependencyHasStableIdentity(deps[0]) {
			group.stableKeys = append(group.stableKeys, key)
			group.stableCount += len(deps)
		} else {
			group.anonymousKeys = append(group.anonymousKeys, key)
			group.anonymousCount += len(deps)
		}
		groups[rawKey] = group
	}
	return groups
}

func dependencyRawKey(dep DependencyReport) string {
	return strings.TrimSpace(dep.Language) + "\x00" + strings.TrimSpace(dep.Name)
}

func planDependencyIdentityBridges(current, baseline map[string]dependencyRawKeyGroup) ([]dependencyIdentityBridge, error) {
	bridges := make([]dependencyIdentityBridge, 0)
	for _, rawKey := range sortedDependencyInstanceKeys(rawGroupKeys(current), rawGroupKeys(baseline)) {
		bridge, err := planDependencyIdentityBridge(rawKey, current[rawKey], baseline[rawKey])
		if err != nil {
			return nil, err
		}
		if bridge != nil {
			bridges = append(bridges, *bridge)
		}
	}
	return bridges, nil
}

func rawGroupKeys(groups map[string]dependencyRawKeyGroup) map[string][]DependencyReport {
	keys := make(map[string][]DependencyReport, len(groups))
	for key := range groups {
		keys[key] = nil
	}
	return keys
}

func planDependencyIdentityBridge(rawKey string, current, baseline dependencyRawKeyGroup) (*dependencyIdentityBridge, error) {
	if current.anonymousCount > 0 && baseline.stableCount > 0 {
		if current.stableCount > 0 || baseline.anonymousCount > 0 {
			return nil, ambiguousDependencyIdentityBridgeError(rawKey, current, baseline)
		}
		return singleDependencyIdentityBridge(rawKey, current.anonymousKeys, current.anonymousCount, baseline.stableKeys, baseline.stableCount)
	}
	if baseline.anonymousCount > 0 && current.stableCount > 0 {
		if baseline.stableCount > 0 || current.anonymousCount > 0 {
			return nil, ambiguousDependencyIdentityBridgeError(rawKey, current, baseline)
		}
		return singleDependencyIdentityBridge(rawKey, current.stableKeys, current.stableCount, baseline.anonymousKeys, baseline.anonymousCount)
	}
	return nil, nil
}

func singleDependencyIdentityBridge(rawKey string, currentKeys []string, currentCount int, baselineKeys []string, baselineCount int) (*dependencyIdentityBridge, error) {
	if len(currentKeys) != 1 || currentCount != 1 || len(baselineKeys) != 1 || baselineCount != 1 {
		return nil, ambiguousDependencyIdentityBridgeError(rawKey, dependencyRawKeyGroup{anonymousKeys: currentKeys, anonymousCount: currentCount}, dependencyRawKeyGroup{stableKeys: baselineKeys, stableCount: baselineCount})
	}
	return &dependencyIdentityBridge{
		rawKey:      rawKey,
		currentKey:  currentKeys[0],
		baselineKey: baselineKeys[0],
	}, nil
}

func ambiguousDependencyIdentityBridgeError(rawKey string, current, baseline dependencyRawKeyGroup) error {
	language, name, _ := strings.Cut(rawKey, "\x00")
	return fmt.Errorf("%w for %s/%s (current anonymous=%d current stable=%d baseline anonymous=%d baseline stable=%d): regenerate the baseline with an identity-capable lopper version", ErrBaselineAmbiguousIdentityBridge, language, name, current.anonymousCount, current.stableCount, baseline.anonymousCount, baseline.stableCount)
}

func pairDependencyInstancesForKey(key string, current, baseline []DependencyReport) []DependencyInstancePair {
	currentSorted := sortedDependenciesForPairing(current)
	baselineSorted := sortedDependenciesForPairing(baseline)
	currentMatched := make([]bool, len(currentSorted))
	baselineMatched := make([]bool, len(baselineSorted))
	pairs := make([]DependencyInstancePair, 0, max(len(currentSorted), len(baselineSorted)))
	appendMatchedDependencyPairsByKey(&pairs, key, currentSorted, baselineSorted, currentMatched, baselineMatched, DependencyPairingOrderKey)
	hadExactMatches := len(pairs) > 0
	appendMatchedDependencyPairsByKey(&pairs, key, currentSorted, baselineSorted, currentMatched, baselineMatched, dependencyResidualIdentityKey)

	stableCurrent, anonymousCurrent := splitDependencyIndexesByIdentity(currentSorted, unmatchedDependencyIndexes(currentMatched))
	stableBaseline, anonymousBaseline := splitDependencyIndexesByIdentity(baselineSorted, unmatchedDependencyIndexes(baselineMatched))

	appendStableDependencyPairs(&pairs, key, currentSorted, baselineSorted, stableCurrent, stableBaseline, hadExactMatches)
	appendAnonymousDependencyPairs(&pairs, key, currentSorted, baselineSorted, anonymousCurrent, anonymousBaseline)
	return pairs
}

func appendMatchedDependencyPairsByKey(pairs *[]DependencyInstancePair, key string, currentSorted, baselineSorted []DependencyReport, currentMatched, baselineMatched []bool, matchKey func(DependencyReport) string) {
	currentByFingerprint, baselineByFingerprint, fingerprints := dependencyFingerprintIndexes(currentSorted, baselineSorted, currentMatched, baselineMatched, matchKey)
	ctx := matchedDependencyPairContext{
		pairs:           pairs,
		key:             key,
		currentSorted:   currentSorted,
		baselineSorted:  baselineSorted,
		currentMatched:  currentMatched,
		baselineMatched: baselineMatched,
	}
	for _, fingerprint := range fingerprints {
		currentIndexes := currentByFingerprint[fingerprint]
		baselineIndexes := baselineByFingerprint[fingerprint]
		for index := 0; index < min(len(currentIndexes), len(baselineIndexes)); index++ {
			appendMatchedDependencyPair(ctx, currentIndexes[index], baselineIndexes[index])
		}
	}
}

func dependencyFingerprintIndexes(currentSorted, baselineSorted []DependencyReport, currentMatched, baselineMatched []bool, matchKey func(DependencyReport) string) (map[string][]int, map[string][]int, []string) {
	currentByFingerprint := make(map[string][]int, len(currentSorted))
	baselineByFingerprint := make(map[string][]int, len(baselineSorted))
	fingerprintSet := make(map[string]struct{}, len(currentSorted)+len(baselineSorted))
	addDependencyFingerprints(currentByFingerprint, fingerprintSet, currentSorted, currentMatched, matchKey)
	addDependencyFingerprints(baselineByFingerprint, fingerprintSet, baselineSorted, baselineMatched, matchKey)
	fingerprints := make([]string, 0, len(fingerprintSet))
	for fingerprint := range fingerprintSet {
		fingerprints = append(fingerprints, fingerprint)
	}
	sort.Strings(fingerprints)
	return currentByFingerprint, baselineByFingerprint, fingerprints
}

func addDependencyFingerprints(indexes map[string][]int, fingerprintSet map[string]struct{}, dependencies []DependencyReport, matched []bool, matchKey func(DependencyReport) string) {
	for index, dep := range dependencies {
		if matched[index] {
			continue
		}
		fingerprint := matchKey(dep)
		indexes[fingerprint] = append(indexes[fingerprint], index)
		fingerprintSet[fingerprint] = struct{}{}
	}
}

type matchedDependencyPairContext struct {
	pairs           *[]DependencyInstancePair
	key             string
	currentSorted   []DependencyReport
	baselineSorted  []DependencyReport
	currentMatched  []bool
	baselineMatched []bool
}

func appendMatchedDependencyPair(ctx matchedDependencyPairContext, currentOrdinal, baselineOrdinal int) {
	ctx.currentMatched[currentOrdinal] = true
	ctx.baselineMatched[baselineOrdinal] = true
	*ctx.pairs = append(*ctx.pairs, DependencyInstancePair{
		Key:             ctx.key,
		Current:         ctx.currentSorted[currentOrdinal],
		HasCurrent:      true,
		CurrentOrdinal:  currentOrdinal,
		Baseline:        ctx.baselineSorted[baselineOrdinal],
		HasBaseline:     true,
		BaselineOrdinal: baselineOrdinal,
	})
}

func unmatchedDependencyIndexes(matched []bool) []int {
	indexes := make([]int, 0, len(matched))
	for index := range matched {
		if !matched[index] {
			indexes = append(indexes, index)
		}
	}
	return indexes
}

func splitDependencyIndexesByIdentity(dependencies []DependencyReport, indexes []int) ([]int, []int) {
	stable := make([]int, 0, len(indexes))
	anonymous := make([]int, 0, len(indexes))
	for _, index := range indexes {
		if dependencyHasStableIdentity(dependencies[index]) {
			stable = append(stable, index)
			continue
		}
		anonymous = append(anonymous, index)
	}
	return stable, anonymous
}

func appendStableDependencyPairs(pairs *[]DependencyInstancePair, key string, currentSorted, baselineSorted []DependencyReport, stableCurrent, stableBaseline []int, hadExactMatches bool) {
	for len(stableCurrent) > 0 || len(stableBaseline) > 0 {
		pair, advanceCurrent, advanceBaseline := nextStableDependencyPair(key, currentSorted, baselineSorted, stableCurrent, stableBaseline, hadExactMatches)
		*pairs = append(*pairs, pair)
		if advanceCurrent {
			stableCurrent = stableCurrent[1:]
		}
		if advanceBaseline {
			stableBaseline = stableBaseline[1:]
		}
	}
}

func nextStableDependencyPair(key string, currentSorted, baselineSorted []DependencyReport, stableCurrent, stableBaseline []int, hadExactMatches bool) (DependencyInstancePair, bool, bool) {
	if len(stableCurrent) == 0 {
		return baselineOnlyDependencyPair(key, baselineSorted, stableBaseline[0]), false, true
	}
	if len(stableBaseline) == 0 {
		return currentOnlyDependencyPair(key, currentSorted, stableCurrent[0]), true, false
	}

	currentIndex := stableCurrent[0]
	baselineIndex := stableBaseline[0]
	currentDep := currentSorted[currentIndex]
	baselineDep := baselineSorted[baselineIndex]
	if dependencyStableIdentityCompatible(currentDep, baselineDep) &&
		(!hadExactMatches || dependencyStableIdentityMutualNearestAtIndexes(currentDep, baselineDep, currentSorted, baselineSorted, stableCurrent, stableBaseline)) {
		return DependencyInstancePair{
			Key:             key,
			Current:         currentDep,
			HasCurrent:      true,
			CurrentOrdinal:  currentIndex,
			Baseline:        baselineDep,
			HasBaseline:     true,
			BaselineOrdinal: baselineIndex,
		}, true, true
	}
	if dependencyResidualIdentityKey(currentDep) < dependencyResidualIdentityKey(baselineDep) {
		return currentOnlyDependencyPair(key, currentSorted, currentIndex), true, false
	}
	return baselineOnlyDependencyPair(key, baselineSorted, baselineIndex), false, true
}

func currentOnlyDependencyPair(key string, currentSorted []DependencyReport, currentOrdinal int) DependencyInstancePair {
	return DependencyInstancePair{
		Key:             key,
		Current:         currentSorted[currentOrdinal],
		HasCurrent:      true,
		CurrentOrdinal:  currentOrdinal,
		BaselineOrdinal: -1,
	}
}

func baselineOnlyDependencyPair(key string, baselineSorted []DependencyReport, baselineOrdinal int) DependencyInstancePair {
	return DependencyInstancePair{
		Key:             key,
		HasBaseline:     true,
		Baseline:        baselineSorted[baselineOrdinal],
		CurrentOrdinal:  -1,
		BaselineOrdinal: baselineOrdinal,
	}
}

func appendAnonymousDependencyPairs(pairs *[]DependencyInstancePair, key string, currentSorted, baselineSorted []DependencyReport, anonymousCurrent, anonymousBaseline []int) {
	for index := range max(len(anonymousCurrent), len(anonymousBaseline)) {
		*pairs = append(*pairs, anonymousDependencyPair(key, currentSorted, baselineSorted, anonymousCurrent, anonymousBaseline, index))
	}
}

func anonymousDependencyPair(key string, currentSorted, baselineSorted []DependencyReport, anonymousCurrent, anonymousBaseline []int, index int) DependencyInstancePair {
	pair := DependencyInstancePair{Key: key, CurrentOrdinal: -1, BaselineOrdinal: -1}
	if index < len(anonymousCurrent) {
		pair.CurrentOrdinal = anonymousCurrent[index]
		pair.Current = currentSorted[pair.CurrentOrdinal]
		pair.HasCurrent = true
	}
	if index < len(anonymousBaseline) {
		pair.BaselineOrdinal = anonymousBaseline[index]
		pair.Baseline = baselineSorted[pair.BaselineOrdinal]
		pair.HasBaseline = true
	}
	return pair
}

func appendDependencyInstanceDeltas(comparison *BaselineComparison, pairs []DependencyInstancePair) {
	for _, pair := range pairs {
		delta, ok := dependencyDelta(pair.Current, pair.HasCurrent, pair.Baseline, pair.HasBaseline)
		if !ok {
			comparison.UnchangedRows++
			continue
		}
		delta.DependencyKey = dependencyInstancePairKey(pair)
		delta.CurrentOrdinal = pair.CurrentOrdinal
		delta.BaselineOrdinal = pair.BaselineOrdinal
		appendDependencyDelta(comparison, delta)
	}
}

func dependencyInstancePairKey(pair DependencyInstancePair) string {
	switch {
	case pair.HasCurrent:
		return DependencyVersionlessKey(pair.Current)
	case pair.HasBaseline:
		return DependencyVersionlessKey(pair.Baseline)
	default:
		return strings.TrimSpace(pair.Key)
	}
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

func DependencyVersionlessKey(dep DependencyReport) string {
	if !dependencyHasStableIdentity(dep) {
		return dep.Language + "\x00" + dep.Name
	}
	if dep.Identity != nil && strings.TrimSpace(dep.Identity.PURL) != "" {
		return "purl\x00" + VersionlessCanonicalPURL(dep.Identity.PURL)
	}
	if dep.Identity != nil {
		ecosystem := CanonicalPackageEcosystem(dep.Identity.Ecosystem)
		name := CanonicalPackageNameForEcosystem(ecosystem, dep.Identity.Name)
		namespace := strings.TrimSpace(dep.Identity.Namespace)
		if ecosystem != "" && name != "" {
			if namespace != "" {
				return strings.Join([]string{"identity", ecosystem, namespace, name}, "\x00")
			}
			return strings.Join([]string{"identity", ecosystem, name}, "\x00")
		}
	}
	return dep.Language + "\x00" + dep.Name
}

func DependencyPairingOrderKey(dep DependencyReport) string {
	identity := dep.Identity
	purl := ""
	ecosystem := ""
	namespace := ""
	identityName := ""
	version := ""
	versionStatus := ""
	purlStatus := ""
	source := ""
	confidence := ""
	if identity != nil {
		purl = CanonicalPURL(identity.PURL)
		ecosystem = CanonicalPackageEcosystem(identity.Ecosystem)
		namespace = strings.TrimSpace(identity.Namespace)
		identityName = strings.TrimSpace(identity.Name)
		version = strings.TrimSpace(identity.Version)
		versionStatus = strings.TrimSpace(identity.VersionStatus)
		purlStatus = strings.TrimSpace(identity.PURLStatus)
		source = strings.TrimSpace(identity.Source)
		confidence = strings.TrimSpace(identity.Confidence)
	}

	parts := []string{
		DependencyVersionlessKey(dep),
		purl,
		ecosystem,
		namespace,
		identityName,
		version,
		versionStatus,
		purlStatus,
		source,
		confidence,
		strings.TrimSpace(dep.Language),
		strings.TrimSpace(dep.Name),
		dependencyLicensePairingKey(dep.License),
		dependencyVulnerabilityPairingKey(dep.Vulnerabilities),
		dependencyRuntimePairingKey(dep.RuntimeUsage),
		fmt.Sprintf("%020d", dep.EstimatedUnusedBytes),
		fmt.Sprintf("%020d", dep.UsedExportsCount),
		fmt.Sprintf("%020d", dep.TotalExportsCount),
		fmt.Sprintf("%020.6f", dep.UsedPercent),
	}
	return strings.Join(parts, "\x00")
}

func dependencyResidualIdentityKey(dep DependencyReport) string {
	identity := dep.Identity
	if identity == nil {
		parts := []string{
			DependencyVersionlessKey(dep),
			strings.TrimSpace(dep.Language),
			strings.TrimSpace(dep.Name),
		}
		return strings.Join(parts, "\x00")
	}
	parts := []string{
		DependencyVersionlessKey(dep),
		CanonicalPURL(identity.PURL),
		CanonicalPackageEcosystem(identity.Ecosystem),
		strings.TrimSpace(identity.Namespace),
		strings.TrimSpace(identity.Name),
		strings.TrimSpace(identity.Version),
		strings.TrimSpace(identity.VersionStatus),
		strings.TrimSpace(identity.PURLStatus),
		strings.TrimSpace(identity.Source),
		strings.TrimSpace(identity.Confidence),
		strings.TrimSpace(dep.Language),
		strings.TrimSpace(dep.Name),
	}
	return strings.Join(parts, "\x00")
}

func dependencyHasStableIdentity(dep DependencyReport) bool {
	identity := dep.Identity
	if identity == nil {
		return false
	}
	if len(identity.Evidence) > 0 || len(identity.Conflicts) > 0 {
		return true
	}
	for _, value := range []string{
		identity.PURL,
		identity.Namespace,
		identity.Version,
	} {
		if strings.TrimSpace(value) != "" {
			return true
		}
	}
	return false
}

func dependencyStableIdentityCompatible(current, baseline DependencyReport) bool {
	currentIdentity := current.Identity
	baselineIdentity := baseline.Identity
	if currentIdentity == nil || baselineIdentity == nil {
		return false
	}
	if dependencyResidualIdentityKey(current) == dependencyResidualIdentityKey(baseline) {
		return true
	}

	currentPrefix := dependencyIdentityVersionPrefix(current)
	baselinePrefix := dependencyIdentityVersionPrefix(baseline)
	return currentPrefix != "" && currentPrefix == baselinePrefix
}

func dependencyIdentityVersionForPairing(identity *DependencyIdentity) string {
	if identity == nil {
		return ""
	}
	if version := strings.TrimSpace(identity.Version); version != "" {
		return version
	}
	return PURLVersion(identity.PURL)
}

func dependencyIdentityVersionPrefix(dep DependencyReport) string {
	identity := dep.Identity
	if identity == nil {
		return ""
	}
	if strings.TrimSpace(identity.PURL) != "" {
		return strings.Join([]string{DependencyVersionlessKey(dep), strings.TrimSpace(identity.PURLStatus)}, "\x00")
	}
	parts := []string{
		DependencyVersionlessKey(dep),
		CanonicalPackageEcosystem(identity.Ecosystem),
		strings.TrimSpace(identity.Namespace),
		CanonicalPackageNameForEcosystem(identity.Ecosystem, identity.Name),
		strings.TrimSpace(identity.PURLStatus),
	}
	return strings.Join(parts, "\x00")
}

func numericDependencyVersionParts(version string) ([]int, bool) {
	parts := make([]int, 0, 4)
	var digits strings.Builder
	flush := func() bool {
		if digits.Len() == 0 {
			return true
		}
		value, err := strconv.Atoi(digits.String())
		if err != nil {
			return false
		}
		parts = append(parts, value)
		digits.Reset()
		return true
	}
	for _, r := range version {
		if r >= '0' && r <= '9' {
			digits.WriteRune(r)
			continue
		}
		if !flush() {
			return nil, false
		}
	}
	if !flush() {
		return nil, false
	}
	if len(parts) == 0 {
		return nil, false
	}
	return parts, true
}

func dependencyStableIdentityMutualNearest(current, baseline DependencyReport, currentGroup, baselineGroup []DependencyReport) bool {
	currentDistance, currentFound := dependencyNearestVersionDistance(current, baselineGroup)
	baselineDistance, baselineFound := dependencyNearestVersionDistance(baseline, currentGroup)
	return dependencyStableIdentityMutualNearestForDistances(current, baseline, currentDistance, currentFound, baselineDistance, baselineFound)
}

func dependencyStableIdentityMutualNearestAtIndexes(current, baseline DependencyReport, currentGroup, baselineGroup []DependencyReport, currentIndexes, baselineIndexes []int) bool {
	currentDistance, currentFound := dependencyNearestVersionDistanceAtIndexes(current, baselineGroup, baselineIndexes)
	baselineDistance, baselineFound := dependencyNearestVersionDistanceAtIndexes(baseline, currentGroup, currentIndexes)
	return dependencyStableIdentityMutualNearestForDistances(current, baseline, currentDistance, currentFound, baselineDistance, baselineFound)
}

func dependencyStableIdentityMutualNearestForDistances(current, baseline DependencyReport, currentDistance int, currentFound bool, baselineDistance int, baselineFound bool) bool {
	currentPrefix := dependencyIdentityVersionPrefix(current)
	baselinePrefix := dependencyIdentityVersionPrefix(baseline)
	if currentPrefix == "" || currentPrefix != baselinePrefix {
		return false
	}

	pairDistance, pairFound := dependencyVersionDistance(current, baseline)
	if !pairFound {
		return true
	}
	if currentFound && pairDistance > currentDistance {
		return false
	}
	if baselineFound && pairDistance > baselineDistance {
		return false
	}
	return true
}

func dependencyNearestVersionDistance(source DependencyReport, candidates []DependencyReport) (int, bool) {
	best := 0
	found := false
	for _, candidate := range candidates {
		best, found = nearerDependencyVersionDistance(source, candidate, best, found)
	}
	return best, found
}

func dependencyNearestVersionDistanceAtIndexes(source DependencyReport, candidates []DependencyReport, indexes []int) (int, bool) {
	best := 0
	found := false
	for _, index := range indexes {
		best, found = nearerDependencyVersionDistance(source, candidates[index], best, found)
	}
	return best, found
}

func nearerDependencyVersionDistance(source, candidate DependencyReport, best int, found bool) (int, bool) {
	if dependencyIdentityVersionPrefix(source) != dependencyIdentityVersionPrefix(candidate) {
		return best, found
	}
	distance, ok := dependencyVersionDistance(source, candidate)
	if !ok || (found && distance >= best) {
		return best, found
	}
	return distance, true
}

func dependencyVersionDistance(left, right DependencyReport) (int, bool) {
	leftVersion := dependencyIdentityVersionForPairing(left.Identity)
	rightVersion := dependencyIdentityVersionForPairing(right.Identity)
	if leftVersion == "" || rightVersion == "" {
		return 0, false
	}
	leftParts, leftOK := numericDependencyVersionParts(leftVersion)
	rightParts, rightOK := numericDependencyVersionParts(rightVersion)
	if leftOK && rightOK {
		length := max(len(leftParts), len(rightParts))
		leftParts = append(leftParts, make([]int, length-len(leftParts))...)
		rightParts = append(rightParts, make([]int, length-len(rightParts))...)
		distance := 0
		weight := 1
		for index := length - 1; index >= 0; index-- {
			diff := leftParts[index] - rightParts[index]
			if diff < 0 {
				diff = -diff
			}
			distance += diff * weight
			weight *= 100
		}
		return distance, true
	}
	if leftVersion == rightVersion {
		return 0, true
	}
	return 1, true
}

func dependencyLicensePairingKey(license *DependencyLicense) string {
	if license == nil {
		return ""
	}
	denied := "0"
	if license.Denied {
		denied = "1"
	}
	parts := []string{
		denied,
		strings.TrimSpace(license.SPDX),
		strings.TrimSpace(license.Raw),
		strings.TrimSpace(license.Source),
		strings.TrimSpace(license.Confidence),
	}
	return strings.Join(parts, "\x00")
}

func dependencyVulnerabilityPairingKey(findings []VulnerabilityFinding) string {
	if len(findings) == 0 {
		return ""
	}
	keys := make([]string, 0, len(findings))
	for _, finding := range findings {
		parts := []string{
			vulnerabilityFindingKey(finding),
			fmt.Sprintf("%020.6f", finding.PriorityScore),
			strings.TrimSpace(finding.Priority),
			strings.TrimSpace(finding.Severity),
		}
		keys = append(keys, strings.Join(parts, "\x00"))
	}
	sort.Strings(keys)
	return strings.Join(keys, "\x01")
}

func dependencyRuntimePairingKey(runtime *RuntimeUsage) string {
	if runtime == nil {
		return ""
	}
	parts := []string{
		fmt.Sprintf("%020d", runtime.LoadCount),
		string(runtime.Correlation),
		fmt.Sprintf("%t", runtime.RuntimeOnly),
		runtimeModuleUsagePairingKey(runtime.Modules),
		runtimeModuleUsagePairingKey(runtime.ParentModules),
		runtimeModuleUsagePairingKey(runtime.Entrypoints),
	}
	return strings.Join(parts, "\x00")
}

func runtimeModuleUsagePairingKey(items []RuntimeModuleUsage) string {
	if len(items) == 0 {
		return ""
	}
	keys := make([]string, 0, len(items))
	for _, item := range items {
		parts := []string{
			strings.TrimSpace(item.Module),
			fmt.Sprintf("%020d", item.Count),
		}
		keys = append(keys, strings.Join(parts, "\x00"))
	}
	sort.Strings(keys)
	return strings.Join(keys, "\x01")
}

func sortedDependenciesForPairing(dependencies []DependencyReport) []DependencyReport {
	if len(dependencies) == 0 {
		return nil
	}
	sorted := append([]DependencyReport(nil), dependencies...)
	sort.Slice(sorted, func(i, j int) bool {
		return DependencyPairingOrderKey(sorted[i]) < DependencyPairingOrderKey(sorted[j])
	})
	return sorted
}

type baselineIndexedDependency struct {
	index int
	dep   DependencyReport
}

func baselineDependencyDeltasForDependencies(dependencies []DependencyReport, comparison *BaselineComparison) []*DependencyDelta {
	aligned := make([]*DependencyDelta, len(dependencies))
	if len(dependencies) == 0 || comparison == nil || len(comparison.Dependencies) == 0 {
		return aligned
	}
	grouped := groupIndexedDependenciesByKey(dependencies)
	deltasByKeyAndOrdinal, fallbackByKey := indexDependencyDeltasByKey(comparison.Dependencies)

	for key, items := range grouped {
		sorted := append([]baselineIndexedDependency(nil), items...)
		sort.Slice(sorted, func(i, j int) bool {
			return DependencyPairingOrderKey(sorted[i].dep) < DependencyPairingOrderKey(sorted[j].dep)
		})

		if alignDependencyDeltasByOrdinal(aligned, sorted, deltasByKeyAndOrdinal[key]) {
			continue
		}
		alignFallbackDependencyDeltas(aligned, sorted, fallbackByKey[key])
	}

	return aligned
}

func groupIndexedDependenciesByKey(dependencies []DependencyReport) map[string][]baselineIndexedDependency {
	grouped := make(map[string][]baselineIndexedDependency, len(dependencies))
	for index, dep := range dependencies {
		key := DependencyVersionlessKey(dep)
		grouped[key] = append(grouped[key], baselineIndexedDependency{index: index, dep: dep})
	}
	return grouped
}

func indexDependencyDeltasByKey(deltas []DependencyDelta) (map[string]map[int]DependencyDelta, map[string][]DependencyDelta) {
	deltasByKeyAndOrdinal := make(map[string]map[int]DependencyDelta, len(deltas))
	fallbackByKey := make(map[string][]DependencyDelta, len(deltas))
	for _, delta := range deltas {
		key := dependencyDeltaKey(delta)
		if delta.CurrentOrdinal >= 0 {
			if deltasByKeyAndOrdinal[key] == nil {
				deltasByKeyAndOrdinal[key] = make(map[int]DependencyDelta)
			}
			deltasByKeyAndOrdinal[key][delta.CurrentOrdinal] = delta
			continue
		}
		fallbackByKey[key] = append(fallbackByKey[key], delta)
	}
	return deltasByKeyAndOrdinal, fallbackByKey
}

func dependencyDeltaKey(delta DependencyDelta) string {
	if delta.DependencyKey != "" {
		return delta.DependencyKey
	}
	return DependencyVersionlessKey(DependencyReport{Name: delta.Name, Language: delta.Language})
}

func alignDependencyDeltasByOrdinal(aligned []*DependencyDelta, sorted []baselineIndexedDependency, byOrdinal map[int]DependencyDelta) bool {
	if len(byOrdinal) == 0 {
		return false
	}
	for ordinal, item := range sorted {
		if delta, ok := byOrdinal[ordinal]; ok {
			copyDelta := delta
			aligned[item.index] = &copyDelta
		}
	}
	return true
}

func alignFallbackDependencyDeltas(aligned []*DependencyDelta, sorted []baselineIndexedDependency, fallback []DependencyDelta) {
	if len(fallback) == 0 {
		return
	}
	sortedFallback := append([]DependencyDelta(nil), fallback...)
	sort.Slice(sortedFallback, func(i, j int) bool {
		return dependencyDeltaFallbackSortKey(sortedFallback[i]) < dependencyDeltaFallbackSortKey(sortedFallback[j])
	})
	for index := 0; index < min(len(sorted), len(sortedFallback)); index++ {
		copyDelta := sortedFallback[index]
		aligned[sorted[index].index] = &copyDelta
	}
}

func BaselineRuntimeDeltasForDependencies(dependencies []DependencyReport, comparison *BaselineComparison) []*RuntimeDelta {
	alignedDependencyDeltas := baselineDependencyDeltasForDependencies(dependencies, comparison)
	alignedRuntimeDeltas := make([]*RuntimeDelta, len(alignedDependencyDeltas))
	for index, delta := range alignedDependencyDeltas {
		if delta != nil {
			alignedRuntimeDeltas[index] = delta.RuntimeDelta
		}
	}
	return alignedRuntimeDeltas
}

func dependencyDeltaFallbackSortKey(delta DependencyDelta) string {
	parts := []string{
		string(delta.Kind),
		strings.TrimSpace(delta.Language),
		strings.TrimSpace(delta.Name),
		fmt.Sprintf("%020d", delta.UsedExportsCountDelta),
		fmt.Sprintf("%020d", delta.TotalExportsCountDelta),
		fmt.Sprintf("%020.6f", delta.UsedPercentDelta),
		fmt.Sprintf("%020d", delta.EstimatedUnusedBytesDelta),
		fmt.Sprintf("%020.6f", delta.WastePercentDelta),
		fmt.Sprintf("%t", delta.DeniedIntroduced),
		fmt.Sprintf("%020d", delta.ReachableVulnerabilityCountDelta),
		fmt.Sprintf("%t", delta.ReachableVulnerabilitiesIntroduced),
	}
	if delta.RuntimeDelta != nil {
		runtimeParts := []string{
			fmt.Sprintf("%t", delta.RuntimeDelta.BaselinePresent),
			fmt.Sprintf("%t", delta.RuntimeDelta.CurrentPresent),
			string(delta.RuntimeDelta.BaselineCorrelation),
			string(delta.RuntimeDelta.CurrentCorrelation),
			fmt.Sprintf("%t", delta.RuntimeDelta.NewRuntimeLoads),
			fmt.Sprintf("%t", delta.RuntimeDelta.RemovedRuntimeLoads),
			fmt.Sprintf("%t", delta.RuntimeDelta.RuntimeOnlyRegression),
			fmt.Sprintf("%t", delta.RuntimeDelta.RuntimeOnlyImprovement),
		}
		parts = append(parts, runtimeParts...)
	}
	return strings.Join(parts, "\x00")
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
			!delta.ReachableVulnerabilitiesIntroduced &&
			delta.ReachableVulnerabilityCountDelta == 0 {
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
		return delta, true
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

func newlyDeniedLicensesByInstances(currentByKey, baselineByKey map[string][]DependencyReport) []DeniedLicenseDelta {
	items := make([]DeniedLicenseDelta, 0)
	for _, key := range sortedDependencyInstanceKeys(currentByKey, baselineByKey) {
		appendNewlyDeniedLicenseDeltas(&items, pairDependencyInstancesForKey(key, currentByKey[key], baselineByKey[key]))
	}
	sortDeniedLicenseDeltas(items)
	return items
}

func newlyDeniedLicensesFromPairs(pairs []DependencyInstancePair) []DeniedLicenseDelta {
	items := make([]DeniedLicenseDelta, 0)
	appendNewlyDeniedLicenseDeltas(&items, pairs)
	sortDeniedLicenseDeltas(items)
	return items
}

func sortDeniedLicenseDeltas(items []DeniedLicenseDelta) {
	sort.Slice(items, func(i, j int) bool {
		if items[i].Language != items[j].Language {
			return items[i].Language < items[j].Language
		}
		return items[i].Name < items[j].Name
	})
}

func appendNewlyDeniedLicenseDeltas(items *[]DeniedLicenseDelta, pairs []DependencyInstancePair) {
	for _, pair := range pairs {
		if !pairIntroducesDeniedLicense(pair) {
			continue
		}
		*items = append(*items, DeniedLicenseDelta{
			Language: pair.Current.Language,
			Name:     pair.Current.Name,
			SPDX:     deniedLicenseSPDX(pair.Current),
		})
	}
}

func pairIntroducesDeniedLicense(pair DependencyInstancePair) bool {
	if !pair.HasCurrent || !isDenied(pair.Current) {
		return false
	}
	return !pair.HasBaseline || !isDenied(pair.Baseline)
}

func deniedLicenseSPDX(dep DependencyReport) string {
	if dep.License == nil {
		return ""
	}
	return dep.License.SPDX
}

func newlyDeniedLicenses(currentByKey, baselineByKey map[string]DependencyReport) []DeniedLicenseDelta {
	currentInstances := make(map[string][]DependencyReport, len(currentByKey))
	for key, dep := range currentByKey {
		currentInstances[key] = append(currentInstances[key], dep)
	}
	baselineInstances := make(map[string][]DependencyReport, len(baselineByKey))
	for key, dep := range baselineByKey {
		baselineInstances[key] = append(baselineInstances[key], dep)
	}
	return newlyDeniedLicensesByInstances(currentInstances, baselineInstances)
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
		if finding.Reachable && !FindingSuppressedByException(finding) {
			count++
		}
	}
	return count
}

func hasNewReachableVulnerabilities(current, baseline DependencyReport) bool {
	return len(newReachableVulnerabilityFindings(current, baseline)) > 0
}

func newlyReachableVulnerabilitiesByInstances(currentByKey, baselineByKey map[string][]DependencyReport) []VulnerabilityDelta {
	items := make([]VulnerabilityDelta, 0)
	for _, key := range sortedDependencyInstanceKeys(currentByKey, baselineByKey) {
		appendNewReachableVulnerabilityDeltas(&items, pairDependencyInstancesForKey(key, currentByKey[key], baselineByKey[key]))
	}
	sortReachableVulnerabilityDeltas(items)
	return items
}

func newlyReachableVulnerabilitiesFromPairs(pairs []DependencyInstancePair) []VulnerabilityDelta {
	items := make([]VulnerabilityDelta, 0)
	appendNewReachableVulnerabilityDeltas(&items, pairs)
	sortReachableVulnerabilityDeltas(items)
	return items
}

func sortReachableVulnerabilityDeltas(items []VulnerabilityDelta) {
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
}

func appendNewReachableVulnerabilityDeltas(items *[]VulnerabilityDelta, pairs []DependencyInstancePair) {
	for _, pair := range pairs {
		if !pair.HasCurrent {
			continue
		}
		for _, finding := range newReachableVulnerabilityFindings(pair.Current, baselineDependencyForPair(pair)) {
			*items = append(*items, vulnerabilityDeltaFromPair(pair, finding))
		}
	}
}

func baselineDependencyForPair(pair DependencyInstancePair) DependencyReport {
	if pair.HasBaseline {
		return pair.Baseline
	}
	return DependencyReport{}
}

func vulnerabilityDeltaFromPair(pair DependencyInstancePair, finding VulnerabilityFinding) VulnerabilityDelta {
	return VulnerabilityDelta{
		Language:       pair.Current.Language,
		Name:           pair.Current.Name,
		AdvisoryID:     finding.AdvisoryID,
		Package:        finding.Package,
		Severity:       finding.Severity,
		FixedVersion:   finding.FixedVersion,
		Source:         finding.Source,
		Priority:       finding.Priority,
		PriorityScore:  finding.PriorityScore,
		Evidence:       append([]string{}, finding.Evidence...),
		DependencyKey:  dependencyInstancePairKey(pair),
		CurrentOrdinal: pair.CurrentOrdinal,
	}
}

func newlyReachableVulnerabilities(currentByKey, baselineByKey map[string]DependencyReport) []VulnerabilityDelta {
	currentInstances := make(map[string][]DependencyReport, len(currentByKey))
	for key, dep := range currentByKey {
		currentInstances[key] = append(currentInstances[key], dep)
	}
	baselineInstances := make(map[string][]DependencyReport, len(baselineByKey))
	for key, dep := range baselineByKey {
		baselineInstances[key] = append(baselineInstances[key], dep)
	}
	return newlyReachableVulnerabilitiesByInstances(currentInstances, baselineInstances)
}

func newReachableVulnerabilityFindings(current, baseline DependencyReport) []VulnerabilityFinding {
	baselineReachable := make(map[string]struct{}, len(baseline.Vulnerabilities))
	for _, finding := range baseline.Vulnerabilities {
		if !finding.Reachable || FindingSuppressedByException(finding) {
			continue
		}
		baselineReachable[vulnerabilityFindingKey(finding)] = struct{}{}
	}
	items := make([]VulnerabilityFinding, 0)
	for _, finding := range current.Vulnerabilities {
		if !finding.Reachable || FindingSuppressedByException(finding) {
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
