package collections

import (
	"reflect"
	"strings"
	"testing"
)

func TestUniqueTrimmedStringsContract(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input []string
		want  []string
	}{
		{name: "nil input", want: []string{}},
		{name: "empty input", input: []string{}, want: []string{}},
		{name: "blank values", input: []string{"", " ", "\t\n"}, want: []string{}},
		{name: "stable normalized order", input: []string{" beta ", "Alpha", "beta", "\tAlpha\n", "alpha"}, want: []string{"beta", "Alpha", "alpha"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := append([]string{}, tc.input...)
			got := UniqueTrimmedStrings(tc.input)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %#v, want %#v", got, tc.want)
			}
			if len(got) > 0 {
				got[0] = "changed"
			}
			if !reflect.DeepEqual(append([]string{}, tc.input...), before) {
				t.Fatal("normalization or result mutation changed input")
			}
		})
	}
}

func TestUniqueNormalizedStringsAppliesNormalizerBeforeFiltering(t *testing.T) {
	got := UniqueNormalizedStrings([]string{"REMOVE", "B", "b", "A"}, func(value string) string {
		if value == "REMOVE" {
			return ""
		}
		return strings.ToLower(value)
	})
	if want := []string{"b", "a"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}
