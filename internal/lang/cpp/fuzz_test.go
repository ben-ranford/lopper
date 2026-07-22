package cpp

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/ben-ranford/lopper/internal/testutil"
)

func FuzzParseIncludes(f *testing.F) {
	f.Fuzz(func(t *testing.T, content []byte) {
		assertParseIncludesProperties(t, content)
	})
}

func TestParseIncludesCommittedFuzzCorpus(t *testing.T) {
	for _, seed := range testutil.LoadByteFuzzCorpus(t, filepath.Join("testdata", "fuzz", "FuzzParseIncludes")) {
		t.Run(seed.Name, func(t *testing.T) {
			assertParseIncludesProperties(t, seed.Data)
		})
	}
}

func assertParseIncludesProperties(t *testing.T, content []byte) {
	t.Helper()

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
}
