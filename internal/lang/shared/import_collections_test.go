package shared

import (
	"reflect"
	"slices"
	"testing"

	"github.com/ben-ranford/lopper/internal/report"
)

func TestSortedImportUsesPreservesImportEvidence(t *testing.T) {
	location := []report.Location{{File: "src/main.js", Line: 3, Column: 2}}
	first := &report.ImportUse{Module: "alpha", Name: "symbol", Locations: location}
	entries := map[string]*report.ImportUse{
		"third":  {Module: "beta", Name: "z"},
		"first":  first,
		"second": {Module: "beta", Name: "a"},
	}
	got := SortedImportUses(entries)
	if len(got) != 3 || got[0].Module != "alpha" || got[1].Name != "a" || got[2].Name != "z" {
		t.Fatalf("unexpected sorted imports: %#v", got)
	}
	if &got[0].Locations[0] != &location[0] {
		t.Fatal("nested import evidence was copied")
	}
	got[0].Name = "changed"
	if first.Name != "symbol" {
		t.Fatal("changing a result entry mutated the map entry")
	}
	if got := SortedImportUses(nil); !reflect.DeepEqual(got, []report.ImportUse{}) {
		t.Fatalf("empty imports = %#v, want allocated empty slice", got)
	}
}

func TestUniqueTrimmedStringsRetainsEncounterOrder(t *testing.T) {
	input := []string{" beta ", "", "alpha", "beta", "\talpha\n", "gamma"}
	original := slices.Clone(input)
	if got := UniqueTrimmedStrings(input); !slices.Equal(got, []string{"beta", "alpha", "gamma"}) {
		t.Fatalf("unique strings = %#v", got)
	}
	if !slices.Equal(input, original) {
		t.Fatal("deduplication mutated input")
	}
	if got := UniqueTrimmedStrings(nil); !reflect.DeepEqual(got, []string{}) {
		t.Fatalf("empty strings = %#v, want allocated empty slice", got)
	}
}
