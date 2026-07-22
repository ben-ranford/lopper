package swift

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/ben-ranford/lopper/internal/testutil"
)

func FuzzParseCarthageDependencies(f *testing.F) {
	f.Fuzz(func(t *testing.T, content []byte) {
		assertParseCarthageDependenciesProperties(t, content)
	})
}

func TestParseCarthageDependenciesCommittedFuzzCorpus(t *testing.T) {
	for _, seed := range testutil.LoadByteFuzzCorpus(t, filepath.Join("testdata", "fuzz", "FuzzParseCarthageDependencies")) {
		t.Run(seed.Name, func(t *testing.T) {
			assertParseCarthageDependenciesProperties(t, seed.Data)
		})
	}
}

func assertParseCarthageDependenciesProperties(t *testing.T, content []byte) {
	t.Helper()

	manifestA := parseCarthageManifestDependencies(content)
	manifestB := parseCarthageManifestDependencies(content)
	if !slices.Equal(manifestA, manifestB) {
		t.Fatalf("parseCarthageManifestDependencies is not deterministic: %#v != %#v", manifestA, manifestB)
	}

	resolvedA := parseCarthageResolvedDependencies(content)
	resolvedB := parseCarthageResolvedDependencies(content)
	if !slices.Equal(resolvedA, resolvedB) {
		t.Fatalf("parseCarthageResolvedDependencies is not deterministic: %#v != %#v", resolvedA, resolvedB)
	}

	assertSortedUniqueCarthageDependencies(t, manifestA)
	assertSortedUniqueCarthageDependencies(t, resolvedA)
}

func assertSortedUniqueCarthageDependencies(t *testing.T, entries []carthageDependency) {
	t.Helper()

	lastDependency := ""
	for _, entry := range entries {
		if entry.Dependency == "" {
			t.Fatalf("Carthage parser returned an empty dependency: %#v", entries)
		}
		if lastDependency != "" && lastDependency >= entry.Dependency {
			t.Fatalf("Carthage parser returned unsorted or duplicate dependencies: %#v", entries)
		}
		lastDependency = entry.Dependency
	}
}
