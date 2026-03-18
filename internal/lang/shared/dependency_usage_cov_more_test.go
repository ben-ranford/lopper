package shared

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/report"
)

const sharedYAMLAppFile = "app.yaml"

func TestSharedAdditionalHelperBranches(t *testing.T) {
	symbols := buildTopSymbols(map[string]int{
		"beta":  2,
		"alpha": 2,
	})
	if len(symbols) != 2 || symbols[0].Name != "alpha" || symbols[1].Name != "beta" {
		t.Fatalf("expected equal-count symbols to sort by name, got %#v", symbols)
	}

	deps := ListDependencies([]FileUsage{{Imports: []ImportRecord{{Dependency: ""}, {Dependency: "Beta"}, {Dependency: "alpha"}}}}, NormalizeDependencyID)
	if !slices.Equal(deps, []string{"alpha", "beta"}) {
		t.Fatalf("unexpected dependencies: %#v", deps)
	}

	reports, warnings := BuildTopReports(3, nil, func(string) (report.DependencyReport, []string) {
		t.Fatal("did not expect report builder to run for empty dependency list")
		return report.DependencyReport{}, nil
	})
	if len(reports) != 0 {
		t.Fatalf("expected no reports for empty dependency list, got %#v", reports)
	}
	if !slices.Equal(warnings, []string{"no dependency data available for top-N ranking"}) {
		t.Fatalf("unexpected warnings for empty dependency list: %#v", warnings)
	}

	items := flattenImports(map[string]*report.ImportUse{
		"pkg:z":   {Module: "pkg", Name: "z"},
		"pkg:a":   {Module: "pkg", Name: "a"},
		"alpha:b": {Module: "alpha", Name: "b"},
	})
	gotOrder := MapSlice(items, func(item report.ImportUse) string {
		return item.Module + ":" + item.Name
	})
	if !slices.Equal(gotOrder, []string{"alpha:b", "pkg:a", "pkg:z"}) {
		t.Fatalf("unexpected flattenImports ordering: %#v", gotOrder)
	}

	filtered := dedupeUnused([]report.ImportUse{{Module: "pkg", Name: "a"}, {Module: "pkg", Name: "b"}}, []report.ImportUse{{Module: "pkg", Name: "a"}})
	if len(filtered) != 1 || filtered[0].Name != "b" {
		t.Fatalf("expected used imports to be removed from unused list, got %#v", filtered)
	}
}

func TestYAMLDisplayPathAdditionalBranches(t *testing.T) {
	repo := t.TempDir()
	inside := filepath.Join(repo, "configs", sharedYAMLAppFile)
	outside := filepath.Join(t.TempDir(), sharedYAMLAppFile)

	if got := yamlDisplayPath(repo, filepath.Join(".", "configs", "..", "configs", sharedYAMLAppFile)); got != filepath.Join("configs", sharedYAMLAppFile) {
		t.Fatalf("expected relative path to be cleaned, got %q", got)
	}
	if got := yamlDisplayPath(repo, inside); got != filepath.Join("configs", sharedYAMLAppFile) {
		t.Fatalf("expected repo-relative display path, got %q", got)
	}
	if got := yamlDisplayPath(repo, outside); got != sharedYAMLAppFile {
		t.Fatalf("expected outside absolute path to fall back to basename, got %q", got)
	}
}

func TestFallbackDependencyEmptyModule(t *testing.T) {
	if got := FallbackDependency("", strings.ToUpper); got != "" {
		t.Fatalf("expected empty module fallback to stay empty, got %q", got)
	}
}
