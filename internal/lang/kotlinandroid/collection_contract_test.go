package kotlinandroid

import (
	"reflect"
	"slices"
	"testing"
)

func TestLookupStringNormalizationRetainsNonNilEmptyResult(t *testing.T) {
	for _, input := range [][]string{nil, {}, {"", " "}} {
		result := sortedUniqueTrimmedStringsNonNil(input)
		if !reflect.DeepEqual(result, []string{}) {
			t.Fatalf("lookup candidates must retain non-nil empty results: %#v", result)
		}
	}
}

func TestLookupStringNormalizationDoesNotAliasCandidates(t *testing.T) {
	candidates := []string{" beta ", "alpha", "beta", "", " alpha "}
	before := slices.Clone(candidates)
	normalized := sortedUniqueTrimmedStringsNonNil(candidates)
	if !slices.Equal(candidates, before) {
		t.Fatal("normalization changed the lookup candidates")
	}
	if !slices.Equal(normalized, []string{"alpha", "beta"}) {
		t.Fatalf("unexpected normalized lookup candidates: %#v", normalized)
	}
	normalized[0] = "changed"
	if !slices.Equal(candidates, before) {
		t.Fatal("normalized candidates share their input storage")
	}
}
