package report

import (
	"reflect"
	"testing"
)

func TestSortByStringKeysPreservesLiteralLexicalOrder(t *testing.T) {
	items := [][2]string{{"b", "a"}, {"a", "z"}, {"a", "a"}, {" a", "z"}, {"a\x00", "a"}}
	SortByStringKeys(items, func(item [2]string) string { return item[0] }, func(item [2]string) string { return item[1] })
	want := [][2]string{{" a", "z"}, {"a", "a"}, {"a", "z"}, {"a\x00", "a"}, {"b", "a"}}
	if !reflect.DeepEqual(items, want) {
		t.Fatalf("literal key order = %#v, want %#v", items, want)
	}
}

func TestOrderingHelpersPreserveEmptySlices(t *testing.T) {
	for _, items := range [][]RuntimeSymbolUsage{nil, {}} {
		SortByStringKeys(items, func(item RuntimeSymbolUsage) string { return item.Module })
		got := TopRuntimeSymbols(items, func(_, _ RuntimeSymbolUsage) bool {
			t.Fatal("empty input must not compare items")
			return false
		})
		if !reflect.DeepEqual(got, items) {
			t.Fatalf("empty input changed shape: %#v -> %#v", items, got)
		}
	}
}

func TestTopVulnerabilityDeltasPreservesOrderingAndInput(t *testing.T) {
	items := []VulnerabilityDelta{
		{PriorityScore: 1, AdvisoryID: "score"},
		{PriorityScore: 2, Priority: VulnerabilityPriorityHigh, Language: "b", Name: "a", AdvisoryID: "language"},
		{PriorityScore: 2, Priority: VulnerabilityPriorityHigh, Language: "a", Name: "b", AdvisoryID: "name"},
		{PriorityScore: 2, Priority: VulnerabilityPriorityHigh, Language: "a", Name: "a", AdvisoryID: "z"},
		{PriorityScore: 2, Priority: VulnerabilityPriorityHigh, Language: "a", Name: "a", AdvisoryID: "a"},
		{PriorityScore: 2, Priority: VulnerabilityPriorityCritical, AdvisoryID: "priority"},
	}
	before := append([]VulnerabilityDelta(nil), items...)
	got := topVulnerabilityDeltas(items, 5)
	var ids []string
	for _, delta := range got {
		ids = append(ids, delta.AdvisoryID)
	}
	if want := []string{"priority", "a", "z", "name", "language"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("top advisory order = %v, want %v", ids, want)
	}
	if !reflect.DeepEqual(items, before) {
		t.Fatal("ranking mutated the input")
	}
	got[0].AdvisoryID = "changed"
	if !reflect.DeepEqual(items, before) {
		t.Fatal("ranking result aliases the input")
	}
}

func TestSingletonDependencyInstancesPreservesKeys(t *testing.T) {
	dep := DependencyReport{Language: "go", Name: "package"}
	input := map[string]DependencyReport{"caller-supplied identity": dep, "another instance": dep}
	got := singletonDependencyInstances(input)
	for key, want := range input {
		if !reflect.DeepEqual(got[key], []DependencyReport{want}) {
			t.Fatalf("instance %q = %#v, want %#v", key, got[key], want)
		}
	}
	got["caller-supplied identity"][0].Name = "changed"
	if input["caller-supplied identity"].Name != dep.Name {
		t.Fatal("instance conversion modified the original map")
	}
	if got := singletonDependencyInstances(nil); got == nil || len(got) != 0 {
		t.Fatalf("nil dependencies must produce an empty map, got %#v", got)
	}
}
