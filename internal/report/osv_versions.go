package report

import (
	"sort"
	"strconv"
	"strings"
)

const (
	osvRangeTypeSemver     = "SEMVER"
	osvRangeTypeEcosystem  = "ECOSYSTEM"
	osvEventIntroduced     = "introduced"
	osvEventFixed          = "fixed"
	osvEventLastAffected   = "last_affected"
	osvEventLimit          = "limit"
	osvIntroducedFromStart = "0"
	osvLimitInfinityMarker = "*"
)

type VulnerabilityVersionRange struct {
	Type   string
	Events []VulnerabilityVersionEvent
}

type VulnerabilityVersionEvent struct {
	Introduced   string
	Fixed        string
	LastAffected string
	Limit        string
}

type vulnerabilityVersionMatch uint8

const (
	versionUnaffected vulnerabilityVersionMatch = iota
	versionAffected
	versionUnevaluable
)

type semanticVersionEvent struct {
	kind    string
	version string
}

func advisoryVersionMatch(advisory VulnerabilityAdvisory, installedVersion string) vulnerabilityVersionMatch {
	hasOSVMetadata := len(advisory.AffectedVersions) > 0 || len(advisory.VersionRanges) > 0
	if !hasOSVMetadata {
		if advisoryAffectsInstalledVersion(advisory.FixedVersion, installedVersion) {
			return versionAffected
		}
		return versionUnaffected
	}

	installedVersion = strings.TrimSpace(installedVersion)
	if installedVersion == "" {
		return versionUnevaluable
	}
	for _, affectedVersion := range advisory.AffectedVersions {
		if installedVersion == strings.TrimSpace(affectedVersion) {
			return versionAffected
		}
	}

	sawUnevaluableRange := false
	for _, versionRange := range advisory.VersionRanges {
		switch semanticRangeVersionMatch(versionRange, installedVersion) {
		case versionAffected:
			return versionAffected
		case versionUnevaluable:
			sawUnevaluableRange = true
		}
	}
	if sawUnevaluableRange {
		return versionUnevaluable
	}
	return versionUnaffected
}

func semanticRangeVersionMatch(versionRange VulnerabilityVersionRange, installedVersion string) vulnerabilityVersionMatch {
	rangeType := strings.ToUpper(strings.TrimSpace(versionRange.Type))
	if rangeType != osvRangeTypeSemver && rangeType != osvRangeTypeEcosystem {
		return versionUnevaluable
	}
	if _, ok := CompareSemanticVersions(installedVersion, installedVersion); !ok {
		return versionUnevaluable
	}
	if rangeType == osvRangeTypeEcosystem {
		return simpleEcosystemRangeVersionMatch(versionRange.Events, installedVersion)
	}

	timeline, limits, ok := parseSemanticVersionRange(versionRange.Events)
	if !ok {
		return versionUnevaluable
	}
	if !versionBeforeAnyLimit(installedVersion, limits) {
		return versionUnaffected
	}
	sort.SliceStable(timeline, func(i, j int) bool {
		comparison, _ := compareOSVSemanticVersions(timeline[i].version, timeline[j].version)
		return comparison < 0
	})
	if semanticTimelineAffectsVersion(timeline, installedVersion) {
		return versionAffected
	}
	return versionUnaffected
}

func simpleEcosystemRangeVersionMatch(events []VulnerabilityVersionEvent, installedVersion string) vulnerabilityVersionMatch {
	introduced, fixed, ok := simpleSemanticEcosystemBounds(events)
	if !ok {
		return versionUnevaluable
	}
	lower, _ := compareOSVSemanticVersions(installedVersion, introduced)
	upper, _ := compareOSVSemanticVersions(installedVersion, fixed)
	if lower >= 0 && upper < 0 {
		return versionAffected
	}
	return versionUnaffected
}

func simpleSemanticEcosystemBounds(events []VulnerabilityVersionEvent) (string, string, bool) {
	if len(events) != 2 {
		return "", "", false
	}
	introduced := ""
	fixed := ""
	for _, event := range events {
		kind, version, ok := vulnerabilityVersionEventKind(event)
		if !ok || !validSemanticRangeVersion(kind, version) {
			return "", "", false
		}
		switch kind {
		case osvEventIntroduced:
			if introduced != "" {
				return "", "", false
			}
			introduced = version
		case osvEventFixed:
			if fixed != "" {
				return "", "", false
			}
			fixed = version
		default:
			return "", "", false
		}
	}
	comparison, isComparable := compareOSVSemanticVersions(introduced, fixed)
	return introduced, fixed, introduced != "" && fixed != "" && isComparable && comparison < 0
}

func parseSemanticVersionRange(events []VulnerabilityVersionEvent) ([]semanticVersionEvent, []string, bool) {
	timeline := make([]semanticVersionEvent, 0, len(events))
	limits := make([]string, 0)
	seenIntroduced := false
	seenFixed := false
	seenLastAffected := false
	for _, event := range events {
		kind, version, ok := vulnerabilityVersionEventKind(event)
		if !ok || !validSemanticRangeVersion(kind, version) {
			return nil, nil, false
		}
		switch kind {
		case osvEventIntroduced:
			seenIntroduced = true
		case osvEventFixed:
			seenFixed = true
		case osvEventLastAffected:
			seenLastAffected = true
		case osvEventLimit:
			limits = append(limits, version)
			continue
		}
		timeline = append(timeline, semanticVersionEvent{kind: kind, version: version})
	}
	if !seenIntroduced || (seenFixed && seenLastAffected) {
		return nil, nil, false
	}
	return timeline, limits, true
}

func vulnerabilityVersionEventKind(event VulnerabilityVersionEvent) (string, string, bool) {
	candidates := []semanticVersionEvent{
		{kind: osvEventIntroduced, version: strings.TrimSpace(event.Introduced)},
		{kind: osvEventFixed, version: strings.TrimSpace(event.Fixed)},
		{kind: osvEventLastAffected, version: strings.TrimSpace(event.LastAffected)},
		{kind: osvEventLimit, version: strings.TrimSpace(event.Limit)},
	}
	var selected semanticVersionEvent
	for _, candidate := range candidates {
		if candidate.version == "" {
			continue
		}
		if selected.kind != "" {
			return "", "", false
		}
		selected = candidate
	}
	return selected.kind, selected.version, selected.kind != ""
}

func validSemanticRangeVersion(kind, version string) bool {
	if kind == osvEventLimit && strings.Contains(version, osvLimitInfinityMarker) {
		return true
	}
	if kind == osvEventIntroduced && version == osvIntroducedFromStart {
		return true
	}
	_, ok := CompareSemanticVersions(version, version)
	return ok
}

func versionBeforeAnyLimit(installedVersion string, limits []string) bool {
	if len(limits) == 0 {
		return true
	}
	for _, limit := range limits {
		if strings.Contains(limit, osvLimitInfinityMarker) {
			return true
		}
		comparison, _ := CompareSemanticVersions(installedVersion, limit)
		if comparison < 0 {
			return true
		}
	}
	return false
}

func semanticTimelineAffectsVersion(timeline []semanticVersionEvent, installedVersion string) bool {
	affected := false
	for _, event := range timeline {
		comparison, _ := compareOSVSemanticVersions(installedVersion, event.version)
		switch event.kind {
		case osvEventIntroduced:
			if comparison >= 0 {
				affected = true
			}
		case osvEventFixed:
			if comparison >= 0 {
				affected = false
			}
		case osvEventLastAffected:
			if comparison > 0 {
				affected = false
			}
		}
	}
	return affected
}

func compareOSVSemanticVersions(left, right string) (int, bool) {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	if left == right {
		return 0, true
	}
	if left == osvIntroducedFromStart {
		return -1, true
	}
	if right == osvIntroducedFromStart {
		return 1, true
	}
	return CompareSemanticVersions(left, right)
}

func normalizeVulnerabilityVersionRanges(ranges []VulnerabilityVersionRange) []VulnerabilityVersionRange {
	if len(ranges) == 0 {
		return nil
	}
	byKey := make(map[string]VulnerabilityVersionRange, len(ranges))
	for _, versionRange := range ranges {
		versionRange.Type = strings.ToUpper(strings.TrimSpace(versionRange.Type))
		for index := range versionRange.Events {
			event := &versionRange.Events[index]
			event.Introduced = strings.TrimSpace(event.Introduced)
			event.Fixed = strings.TrimSpace(event.Fixed)
			event.LastAffected = strings.TrimSpace(event.LastAffected)
			event.Limit = strings.TrimSpace(event.Limit)
		}
		byKey[vulnerabilityVersionRangeKey(versionRange)] = versionRange
	}
	keys := make([]string, 0, len(byKey))
	for key := range byKey {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	normalized := make([]VulnerabilityVersionRange, 0, len(keys))
	for _, key := range keys {
		normalized = append(normalized, byKey[key])
	}
	return normalized
}

func vulnerabilityVersionRangeKey(versionRange VulnerabilityVersionRange) string {
	parts := make([]string, 0, 1+(len(versionRange.Events)*4))
	parts = append(parts, strconv.Quote(versionRange.Type))
	for _, event := range versionRange.Events {
		parts = append(parts, strconv.Quote(event.Introduced))
		parts = append(parts, strconv.Quote(event.Fixed))
		parts = append(parts, strconv.Quote(event.LastAffected))
		parts = append(parts, strconv.Quote(event.Limit))
	}
	return strings.Join(parts, "\x00")
}

func appendOSVEvaluationWarnings(existing, warnings []string) []string {
	warnings = sortedUniqueStrings(warnings)
	if len(warnings) == 0 {
		return existing
	}
	seen := make(map[string]struct{}, len(existing)+len(warnings))
	for _, warning := range existing {
		seen[strings.TrimSpace(warning)] = struct{}{}
	}
	result := append([]string(nil), existing...)
	for _, warning := range warnings {
		if _, ok := seen[warning]; ok {
			continue
		}
		seen[warning] = struct{}{}
		result = append(result, warning)
	}
	return result
}
