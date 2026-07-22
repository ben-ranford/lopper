package cpp

import (
	"slices"
	"testing"
)

func FuzzParseIncludes(f *testing.F) {
	f.Fuzz(func(t *testing.T, content []byte) {
		includesA := parseIncludes(content)
		includesB := parseIncludes(content)

		if !slices.Equal(includesA, includesB) {
			t.Fatalf("parseIncludes is not deterministic: %#v != %#v", includesA, includesB)
		}

		lastLine := 0
		for _, include := range includesA {
			if include.Line <= 0 {
				t.Fatalf("parseIncludes returned an invalid line number: %#v", include)
			}
			if include.Column <= 0 {
				t.Fatalf("parseIncludes returned an invalid column number: %#v", include)
			}
			if include.Line < lastLine {
				t.Fatalf("parseIncludes returned out-of-order includes: %#v", includesA)
			}
			lastLine = include.Line
		}
	})
}
