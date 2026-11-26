package js

import (
	"fmt"
	"slices"
	"testing"
)

func TestAppendUniqueDependencyWarningsCapsAndSkips(t *testing.T) {
	seen := map[string]struct{}{"already": {}}
	dst := appendUniqueDependencyWarnings([]string{"already"}, seen, []string{"", "already", "fresh"})
	if len(dst) != 2 || dst[1] != "fresh" {
		t.Fatalf("expected empty and duplicate warnings to be skipped, got %#v", dst)
	}

	full := make([]string, maxAggregatedDependencyWarnings)
	for i := range full {
		full[i] = fmt.Sprintf("w%d", i)
	}
	capped := appendUniqueDependencyWarnings(full, map[string]struct{}{}, []string{"overflow", "another"})
	if len(capped) != maxAggregatedDependencyWarnings+1 || capped[len(capped)-1] != aggregatedWarningCapNotice {
		t.Fatalf("expected one cap notice, got %#v", capped[len(capped)-1:])
	}
	again := appendUniqueDependencyWarnings(capped, map[string]struct{}{aggregatedWarningCapNotice: {}}, []string{"still-more"})
	if len(again) != len(capped) {
		t.Fatalf("expected a second overflow to keep the existing cap notice, got %d warnings", len(again))
	}
}

func TestIncompleteCoverageWarningRespectsDependencyWarningCap(t *testing.T) {
	full := make([]string, maxAggregatedDependencyWarnings)
	for index := range full {
		full[index] = fmt.Sprintf("warning %d", index)
	}
	for _, input := range [][]string{full, append(slices.Clone(full), aggregatedWarningCapNotice)} {
		got := addIncompleteCoverageWarning(input, nil)
		if len(got) != maxAggregatedDependencyWarnings+1 ||
			got[maxAggregatedDependencyWarnings-1] != incompleteTopDependenciesWarning ||
			got[maxAggregatedDependencyWarnings] != aggregatedWarningCapNotice {
			t.Fatalf("expected the coverage warning and cap notice within the bound, got %#v", got)
		}
	}
	got := addIncompleteCoverageWarning(nil, nil)
	if !slices.Equal(got, []string{incompleteTopDependenciesWarning}) {
		t.Fatalf("expected the coverage warning when room is available, got %#v", got)
	}
	got = addIncompleteCoverageWarning(got, map[string]struct{}{incompleteTopDependenciesWarning: {}})
	if !slices.Equal(got, []string{incompleteTopDependenciesWarning}) {
		t.Fatalf("expected a single coverage warning, got %#v", got)
	}
}
