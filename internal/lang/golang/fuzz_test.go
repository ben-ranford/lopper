package golang

import (
	"maps"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ben-ranford/lopper/internal/testutil"
)

func FuzzParseModFile(f *testing.F) {
	f.Fuzz(func(t *testing.T, content []byte) {
		modulePathA, dependenciesA, replacementsA := parseGoMod(content)
		modulePathB, dependenciesB, replacementsB := parseGoMod(content)

		assertDeterministicGoModParse(t, modulePathA, dependenciesA, replacementsA, modulePathB, dependenciesB, replacementsB)
		assertSortedUniqueGoModDependencies(t, dependenciesA)
		assertNonEmptyGoModReplacements(t, replacementsA)
	})
}

func TestParseModFileCommittedFuzzCorpus(t *testing.T) {
	for _, seed := range testutil.LoadByteFuzzCorpus(t, filepath.Join("testdata", "fuzz", "FuzzParseModFile")) {
		t.Run(seed.Name, func(t *testing.T) {
			modulePathA, dependenciesA, replacementsA := parseGoMod(seed.Data)
			modulePathB, dependenciesB, replacementsB := parseGoMod(seed.Data)

			assertDeterministicGoModParse(t, modulePathA, dependenciesA, replacementsA, modulePathB, dependenciesB, replacementsB)
			assertSortedUniqueGoModDependencies(t, dependenciesA)
			assertNonEmptyGoModReplacements(t, replacementsA)
		})
	}
}

func assertDeterministicGoModParse(t *testing.T, modulePathA string, dependenciesA []string, replacementsA map[string]string, modulePathB string, dependenciesB []string, replacementsB map[string]string) {
	t.Helper()

	if modulePathA != modulePathB {
		t.Fatalf("parseGoMod module path is not deterministic: %q != %q", modulePathA, modulePathB)
	}
	if !slices.Equal(dependenciesA, dependenciesB) {
		t.Fatalf("parseGoMod dependencies are not deterministic: %#v != %#v", dependenciesA, dependenciesB)
	}
	if !maps.Equal(replacementsA, replacementsB) {
		t.Fatalf("parseGoMod replacements are not deterministic: %#v != %#v", replacementsA, replacementsB)
	}
}

func assertSortedUniqueGoModDependencies(t *testing.T, dependencies []string) {
	t.Helper()

	if !slices.IsSorted(dependencies) {
		t.Fatalf("parseGoMod returned unsorted dependencies: %#v", dependencies)
	}
	for i, dependency := range dependencies {
		if dependency == "" {
			t.Fatalf("parseGoMod returned an empty dependency in %#v", dependencies)
		}
		if i > 0 && dependencies[i-1] == dependency {
			t.Fatalf("parseGoMod returned duplicate dependencies: %#v", dependencies)
		}
	}
}

func assertNonEmptyGoModReplacements(t *testing.T, replacements map[string]string) {
	t.Helper()

	for replacement, original := range replacements {
		if replacement == "" || original == "" {
			t.Fatalf("parseGoMod returned an empty replacement mapping: %#v", replacements)
		}
	}
}
