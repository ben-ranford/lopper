package shared

import (
	"slices"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/report"
)

func TestFirstContentColumn(t *testing.T) {
	if got := FirstContentColumn("\t  abc"); got != 4 {
		t.Fatalf("expected first content column 4, got %d", got)
	}
	if got := FirstContentColumn("   "); got != 1 {
		t.Fatalf("expected fallback column 1, got %d", got)
	}
}

func TestMapSliceAndMapFileUsages(t *testing.T) {
	numbers := []int{1, 2, 3}
	mapped := MapSlice(numbers, func(v int) string { return strings.Repeat("x", v) })
	if !slices.Equal(mapped, []string{"x", "xx", "xxx"}) {
		t.Fatalf("unexpected mapped values: %#v", mapped)
	}

	type raw struct {
		imports []ImportRecord
		usage   map[string]int
	}
	files := []raw{{imports: []ImportRecord{{Dependency: "alpha"}}, usage: map[string]int{"a": 1}}}
	usages := MapFileUsages(files, func(v raw) []ImportRecord { return v.imports }, func(v raw) map[string]int { return v.usage })
	if len(usages) != 1 || len(usages[0].Imports) != 1 || usages[0].Imports[0].Dependency != "alpha" {
		t.Fatalf("unexpected file usages: %#v", usages)
	}
}

func TestCountUsage(t *testing.T) {
	imports := []ImportRecord{{Local: "foo"}, {Local: "bar"}, {Local: "baz", Wildcard: true}}
	content := []byte("foo(); foo(); bar(); baz();")
	usage := CountUsage(content, imports)
	if usage["foo"] != 1 {
		t.Fatalf("expected foo usage 1, got %d", usage["foo"])
	}
	if usage["bar"] != 0 {
		t.Fatalf("expected bar usage 0, got %d", usage["bar"])
	}
	if _, ok := usage["baz"]; ok {
		t.Fatalf("expected wildcard import to be skipped")
	}
}

func TestBuildDependencyStats(t *testing.T) {
	files := []FileUsage{
		{
			Imports: []ImportRecord{
				{Dependency: "Alpha", Module: "alpha", Name: "map", Local: "map", Location: report.Location{File: "a.js", Line: 1}},
				{Dependency: "alpha", Module: "alpha", Name: "filter", Local: "filter", Location: report.Location{File: "a.js", Line: 2}},
				{Dependency: "alpha", Module: "alpha", Name: "*", Local: "pkg", Wildcard: true, Location: report.Location{File: "a.js", Line: 3}},
				{Dependency: "beta", Module: "beta", Name: "other", Local: "other", Location: report.Location{File: "b.js", Line: 1}},
			},
			Usage: map[string]int{"map": 2, "filter": 0},
		},
	}
	stats := BuildDependencyStats("alpha", files, strings.ToLower)
	if !stats.HasImports {
		t.Fatalf("expected HasImports=true")
	}
	if stats.TotalCount != 3 {
		t.Fatalf("expected TotalCount=3, got %d", stats.TotalCount)
	}
	if stats.UsedCount != 2 {
		t.Fatalf("expected UsedCount=2, got %d", stats.UsedCount)
	}
	if stats.WildcardImports != 1 {
		t.Fatalf("expected WildcardImports=1, got %d", stats.WildcardImports)
	}
	if len(stats.UsedImports) == 0 || len(stats.UnusedImports) == 0 {
		t.Fatalf("expected both used and unused imports, got used=%#v unused=%#v", stats.UsedImports, stats.UnusedImports)
	}
	if !HasWildcardImport(stats.UsedImports) {
		t.Fatalf("expected wildcard import in used imports")
	}
}

func TestListDependenciesAndTopReports(t *testing.T) {
	files := []FileUsage{
		{Imports: []ImportRecord{{Dependency: "Alpha"}, {Dependency: "beta"}}},
		{Imports: []ImportRecord{{Dependency: "alpha"}, {Dependency: "gamma"}}},
	}
	deps := ListDependencies(files, strings.ToLower)
	if !slices.Equal(deps, []string{"alpha", "beta", "gamma"}) {
		t.Fatalf("unexpected dependencies: %#v", deps)
	}

	reports, warnings := BuildTopReports(2, deps, func(dep string) (report.DependencyReport, []string) {
		score := 10.0
		switch dep {
		case "alpha":
			score = 20
		case "beta":
			score = 80
		case "gamma":
			score = 40
		}
		return report.DependencyReport{Name: dep, UsedPercent: 100 - score, TotalExportsCount: 100}, []string{dep + "-warn"}
	})
	if len(reports) != 2 {
		t.Fatalf("expected 2 reports, got %d", len(reports))
	}
	if reports[0].Name != "beta" {
		t.Fatalf("expected highest waste report first, got %q", reports[0].Name)
	}
	if len(warnings) != 3 {
		t.Fatalf("expected one warning per dependency, got %d", len(warnings))
	}
}

func TestSortReportsByWasteAndHelpers(t *testing.T) {
	reports := []report.DependencyReport{
		{Name: "unknown", TotalExportsCount: 0},
		{Name: "b", UsedPercent: 20, TotalExportsCount: 10},
		{Name: "a", UsedPercent: 20, TotalExportsCount: 10},
	}
	SortReportsByWaste(reports)
	if reports[0].Name != "a" {
		t.Fatalf("expected alpha tie-break on name, got %q", reports[0].Name)
	}

	if score, ok := WasteScore(report.DependencyReport{TotalExportsCount: 0}); ok || score != -1 {
		t.Fatalf("expected unknown waste score for empty exports")
	}
	if score, ok := WasteScore(report.DependencyReport{TotalExportsCount: 10, UsedPercent: 30}); !ok || score != 70 {
		t.Fatalf("unexpected waste score: score=%v ok=%v", score, ok)
	}

	keys := SortedKeys(map[string]struct{}{"z": {}, "a": {}})
	if !slices.Equal(keys, []string{"a", "z"}) {
		t.Fatalf("unexpected sorted keys: %#v", keys)
	}
	if got := SortedKeys(nil); got != nil {
		t.Fatalf("expected nil sorted keys for nil map, got %#v", got)
	}

	if used := calculateUsedPercent(0, 0); used != 0 {
		t.Fatalf("expected used percent 0 for zero totals, got %f", used)
	}

	dest := map[string]*report.ImportUse{}
	addImport(dest, report.ImportUse{Name: "map", Module: "lodash", Locations: []report.Location{{File: "a.js", Line: 1}}})
	addImport(dest, report.ImportUse{Name: "map", Module: "lodash", Locations: []report.Location{{File: "b.js", Line: 2}}})
	if len(dest["lodash:map"].Locations) != 2 {
		t.Fatalf("expected duplicate import locations to merge, got %#v", dest["lodash:map"])
	}
}
