package report

import (
	"path/filepath"
	"sort"
	"strings"
)

const CoverageGapRubyOversizedGemspec = "ruby-oversized-gemspec-declaration"

func StableCoverageGaps(gaps []CoverageGap) []CoverageGap {
	if len(gaps) == 0 {
		return nil
	}

	mergedByIdentity := make(map[string]CoverageGap, len(gaps))
	for _, gap := range gaps {
		normalized := normalizeCoverageGap(gap)
		identity := coverageGapIdentity(normalized)
		if existing, ok := mergedByIdentity[identity]; ok {
			existing.Evidence = sortedUniqueStrings(append(existing.Evidence, normalized.Evidence...))
			mergedByIdentity[identity] = existing
			continue
		}
		mergedByIdentity[identity] = normalized
	}

	stable := make([]CoverageGap, 0, len(mergedByIdentity))
	for _, gap := range mergedByIdentity {
		stable = append(stable, gap)
	}
	sort.Slice(stable, func(i, j int) bool {
		return coverageGapSortKey(stable[i]) < coverageGapSortKey(stable[j])
	})
	return stable
}

func newCoverageGaps(current, baseline []CoverageGap) []CoverageGap {
	stableCurrent := StableCoverageGaps(current)
	if len(stableCurrent) == 0 {
		return nil
	}

	stableBaseline := StableCoverageGaps(baseline)
	baselineIdentities := make(map[string]struct{}, len(stableBaseline))
	for _, gap := range stableBaseline {
		baselineIdentities[coverageGapIdentity(gap)] = struct{}{}
	}

	differential := make([]CoverageGap, 0, len(stableCurrent))
	for _, gap := range stableCurrent {
		if _, ok := baselineIdentities[coverageGapIdentity(gap)]; ok {
			continue
		}
		differential = append(differential, gap)
	}
	if len(differential) == 0 {
		return nil
	}
	return differential
}

func normalizeCoverageGap(gap CoverageGap) CoverageGap {
	return CoverageGap{
		Code:     strings.TrimSpace(gap.Code),
		Language: strings.TrimSpace(gap.Language),
		Path:     normalizeCoverageGapPath(gap.Path),
		Evidence: sortedUniqueStrings(gap.Evidence),
	}
}

func normalizeCoverageGapPath(path string) string {
	return strings.ReplaceAll(filepath.ToSlash(path), "\\", "/")
}

func coverageGapIdentity(gap CoverageGap) string {
	return gap.Code + "\x00" + gap.Language + "\x00" + gap.Path
}

func coverageGapSortKey(gap CoverageGap) string {
	return coverageGapIdentity(gap) + "\x00" + strings.Join(gap.Evidence, "\x00")
}
