//go:build !regressionproof

package rust

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestRustPathHelpersIsolateMalformedRootsAndOrderNestedCandidates(t *testing.T) {
	repo := t.TempDir()
	apps := filepath.Join(repo, "apps")
	child := filepath.Join(apps, "child")
	other := t.TempDir()
	roots := map[string]struct{}{
		repo:  {},
		apps:  {},
		child: {},
		other: {},
	}
	isolateMalformedRustRoots(roots, map[string]struct{}{apps: {}})
	for _, root := range []string{repo, apps, other} {
		if _, ok := roots[root]; !ok {
			t.Fatalf("expected %q to remain after malformed-root isolation: %#v", root, roots)
		}
	}
	if _, ok := roots[child]; ok {
		t.Fatalf("expected nested candidate below malformed fallback to be removed: %#v", roots)
	}

	a := filepath.Join(repo, "a")
	b := filepath.Join(repo, "b")
	nested := scanRootsPreservingNested([]string{
		filepath.Join(b, cargoTomlName),
		filepath.Join(a, cargoTomlName),
	}, repo)
	if got, want := strings.Join(nested, ","), strings.Join([]string{a, b}, ","); got != want {
		t.Fatalf("expected same-depth roots to sort stably, got %q want %q", got, want)
	}
	if hits := countASCIIWordTokenHits("serde", nil); len(hits) != 0 {
		t.Fatalf("expected empty requested-token set to skip scanning, got %#v", hits)
	}
}
