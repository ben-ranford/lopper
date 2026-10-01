package thresholds

import (
	"reflect"
	"testing"
)

func TestPrependPolicySource(t *testing.T) {
	for _, tc := range []struct {
		name    string
		source  string
		sources []string
		want    []string
	}{
		{name: "nil input", source: "cli", want: []string{"cli"}},
		{name: "precedence and duplicates", source: "mcp", sources: []string{"defaults", "mcp", "repo", "defaults"}, want: []string{"mcp", "defaults", "repo"}},
		{name: "exact identifiers", source: "", sources: []string{"cli", " cli ", "", "cli"}, want: []string{"", "cli", " cli "}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := append([]string(nil), tc.sources...)
			got := PrependPolicySource(tc.source, tc.sources)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %#v, want %#v", got, tc.want)
			}
			got[0] = "changed"
			if !reflect.DeepEqual(tc.sources, before) {
				t.Fatal("result mutated source identifiers")
			}
		})
	}
}
