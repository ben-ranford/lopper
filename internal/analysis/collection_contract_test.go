package analysis

import (
	"reflect"
	"slices"
	"testing"
)

func TestUniqueSortedPreservesExactStringsAndOwnership(t *testing.T) {
	input := []string{"beta", "alpha", "beta", "", " alpha "}
	original := slices.Clone(input)
	result := uniqueSorted(input)
	if !slices.Equal(result, []string{"", " alpha ", "alpha", "beta"}) {
		t.Fatalf("exact-string uniqueness changed: %#v", result)
	}
	result[0] = "changed"
	if !slices.Equal(input, original) {
		t.Fatalf("sorting or changing the result mutated input: %#v", input)
	}
	for _, empty := range [][]string{nil, {}} {
		if !reflect.DeepEqual(uniqueSorted(empty), []string(nil)) {
			t.Fatal("empty analysis collections must remain nil")
		}
	}
}
