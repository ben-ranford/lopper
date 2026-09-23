package python

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestParseRequirementsDependenciesRetainsNamedDirectReferences(t *testing.T) {
	repo := t.TempDir()
	path := filepath.Join(repo, pythonRequirementsTxt)
	testutil.MustWriteFile(t, path, `demo@https://example.test/demo.whl
other[extra]@git+https://example.test/other.git
My_Package @https://example.test/package.whl
spaced @ https://example.test/spaced.whl
https://user@example.test/unnamed.whl
git+ssh://git@example.test/unnamed.git
!invalid@https://example.test/invalid.whl
`)
	dependencies, warnings, err := parseRequirementsDependencies(repo, path)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]struct{}{"demo": {}, "other": {}, "my-package": {}, "spaced": {}}
	if !reflect.DeepEqual(dependencies, want) {
		t.Fatalf("dependencies = %#v, want %#v", dependencies, want)
	}
	wantWarnings := []string{"requirements.txt: skipped 3 requirements entries with unsupported format"}
	if !reflect.DeepEqual(warnings, wantWarnings) {
		t.Fatalf("warnings = %#v, want %#v", warnings, wantWarnings)
	}
}
