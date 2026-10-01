package report

import (
	"reflect"
	"testing"
)

func TestCollectionStringContracts(t *testing.T) {
	cases := []struct {
		name        string
		input, want []string
	}{
		{"nil", nil, nil},
		{"empty", []string{}, nil},
		{"blanks", []string{"", " "}, []string{}},
		{"duplicates and whitespace", []string{"beta", "alpha", "beta", "", " alpha "}, []string{"alpha", "beta"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := append([]string(nil), tc.input...)
			got := SortedUniqueTrimmedStrings(tc.input)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %#v, want %#v", got, tc.want)
			}
			if len(tc.input) > 0 && !reflect.DeepEqual(tc.input, before) {
				t.Fatal("mutated input")
			}
			if len(got) > 0 {
				got[0] = "changed"
				if !reflect.DeepEqual(tc.input, before) {
					t.Fatal("result aliases input")
				}
			}
		})
	}
}
