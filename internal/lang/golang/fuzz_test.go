package golang

import (
	"maps"
	"slices"
	"testing"
)

func FuzzParseModFile(f *testing.F) {
	f.Fuzz(func(t *testing.T, content []byte) {
		modulePathA, dependenciesA, replacementsA := parseGoMod(content)
		modulePathB, dependenciesB, replacementsB := parseGoMod(content)

		if modulePathA != modulePathB {
			t.Fatalf("parseGoMod module path is not deterministic: %q != %q", modulePathA, modulePathB)
		}
		if !slices.Equal(dependenciesA, dependenciesB) {
			t.Fatalf("parseGoMod dependencies are not deterministic: %#v != %#v", dependenciesA, dependenciesB)
		}
		if !maps.Equal(replacementsA, replacementsB) {
			t.Fatalf("parseGoMod replacements are not deterministic: %#v != %#v", replacementsA, replacementsB)
		}

		if !slices.IsSorted(dependenciesA) {
			t.Fatalf("parseGoMod returned unsorted dependencies: %#v", dependenciesA)
		}
		for i, dependency := range dependenciesA {
			if dependency == "" {
				t.Fatalf("parseGoMod returned an empty dependency in %#v", dependenciesA)
			}
			if i > 0 && dependenciesA[i-1] == dependency {
				t.Fatalf("parseGoMod returned duplicate dependencies: %#v", dependenciesA)
			}
		}
		for replacement, original := range replacementsA {
			if replacement == "" || original == "" {
				t.Fatalf("parseGoMod returned an empty replacement mapping: %#v", replacementsA)
			}
		}
	})
}
