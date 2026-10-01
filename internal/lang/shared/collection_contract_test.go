package shared

import (
	"maps"
	"reflect"
	"testing"
)

func TestDependencyUnionContract(t *testing.T) {
	for _, inputs := range [][]map[string]struct{}{nil, {nil}, {{}}} {
		if got := SortedDependencyUnion(inputs...); !reflect.DeepEqual(got, []string(nil)) {
			t.Fatalf("empty union = %#v; want nil", got)
		}
	}
	// Whitespace is deliberately part of this dependency key.
	whitespaceKey := " alpha "
	first := map[string]struct{}{"beta": {}, whitespaceKey: {}, "": {}}
	second := map[string]struct{}{"beta": {}, "alpha": {}}
	beforeFirst, beforeSecond := maps.Clone(first), maps.Clone(second)
	got := SortedDependencyUnion(first, nil, second)
	if want := []string{"", " alpha ", "alpha", "beta"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
	if !maps.Equal(first, beforeFirst) || !maps.Equal(second, beforeSecond) {
		t.Fatal("mutated input sets")
	}
}
