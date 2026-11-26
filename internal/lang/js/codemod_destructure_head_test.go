package js

import "testing"

func TestSuggestOnlyCodemodRecognizesNamedImportStaticDestructure(t *testing.T) {
	repo, _, _ := setupLodashFixture(t, `import { map } from "lodash";
const { iteratee } = map;
void iteratee;
`)

	reportData := analyseSuggestOnly(t, repo)
	if len(reportData.Dependencies) != 1 {
		t.Fatalf("expected one dependency report, got %#v", reportData.Dependencies)
	}
	codemod := reportData.Dependencies[0].Codemod
	if codemod == nil || len(codemod.Suggestions) != 1 {
		t.Fatalf("expected one codemod suggestion, got %#v", codemod)
	}
	suggestion := codemod.Suggestions[0]
	if suggestion.ImportName != "map" || suggestion.ToModule != lodashMapSubpath {
		t.Fatalf("expected known lodash map subpath suggestion, got %#v", suggestion)
	}
}
