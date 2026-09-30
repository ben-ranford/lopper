package reusecheck

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

func TestPackageStatsProvenance(t *testing.T) {
	for _, tc := range []struct {
		name, importClause, declarations, parameter, setup, receiver string
		want                                                         bool
	}{
		{"alias", `source "` + sharedPackage + `"`, "type Stats = source.DependencyStats", "measured Stats", "", "measured", true},
		{"named container", `source "` + sharedPackage + `"`, "type Collection []source.DependencyStats", "values Collection", "", "values[0]", true},
		{"field", `source "` + sharedPackage + `"`, "type Holder struct { stats source.DependencyStats }", "h Holder", "", "h.stats", true},
		{"embedded field", `source "` + sharedPackage + `"`, "type Holder struct { source.DependencyStats }", "h Holder", "", "h.DependencyStats", true},
		{"source function", `source "` + sharedPackage + `"`, "func factory() source.DependencyStats { panic(0) }", "unused string", "measured := factory()", "measured", true},
		{"source function alias", `source "` + sharedPackage + `"`, "type Stats = source.DependencyStats; func factory() Stats { panic(0) }", "unused string", "measured := factory()", "measured", true},
		{"source method", `source "` + sharedPackage + `"`, "type Holder struct{}; func (Holder) stats() source.DependencyStats { panic(0) }", "h Holder", "measured := h.stats()", "measured", true},
		{"alias collision", `r "` + sharedPackage + `"`, "type Stats = r.DependencyStats", "measured Stats", "", "measured", true},
		{"dot alias", `. "` + sharedPackage + `"`, "type Stats = DependencyStats", "measured Stats", "", "measured", true},
		{"dot source function", `. "` + sharedPackage + `"`, "func factory() DependencyStats { panic(0) }", "unused string", "measured := factory()", "measured", true},
		{"distinct stats", `source "` + sharedPackage + `"`, "type Stats source.DependencyStats", "measured Stats", "", "measured", false},
		{"unrelated package", `r "example.com/other"`, "type Stats = r.DependencyStats", "measured Stats", "", "measured", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := "package fixture\nimport " + tc.importClause + "\n" + tc.declarations
			consumer := packageMappingSource(tc.parameter, tc.setup, tc.receiver)
			findings := packageSourceFindings(t, provider, consumer)
			if (len(findings) == 1 && !findings[0].Advisory) != tc.want || (!tc.want && len(findings) != 0) {
				t.Fatalf("findings=%+v want violation=%v", findings, tc.want)
			}
		})
	}
}

func TestPackageImportsRetainOriginalFileOwnership(t *testing.T) {
	provider := "package fixture\nimport source \"" + sharedPackage + "\"\ntype Stats = source.DependencyStats"
	consumer := packageMappingSource("measured Stats", "_ = source.TrimSpace(name)", "measured")
	consumer = strings.Replace(consumer, "package fixture", "package fixture\nimport source \"strings\"", 1)
	findings := packageSourceFindings(t, provider, consumer)
	if len(findings) != 1 || findings[0].Advisory {
		t.Fatalf("different same-name imports: %+v", findings)
	}
	provider = strings.Replace(provider, sharedPackage, "example.com/other", 1)
	consumer = strings.Replace(consumer, `source "strings"`, `source "`+sharedPackage+`"`, 1)
	findings = packageSourceFindings(t, provider, consumer)
	if len(findings) != 0 {
		t.Fatalf("consumer import claimed unrelated provider type: %+v", findings)
	}
}

func TestPackageImportNormalizationPreservesShadows(t *testing.T) {
	source := `package fixture
import s "` + sharedPackage + `"
import . "` + sharedPackage + `"
type Local struct { DependencyStats int }
var _reusecheck_import_0 int
func use(s Local) { _ = s.DependencyStats; DependencyStats := 1; _ = DependencyStats }
func stats(value s.DependencyStats) { _ = value }
`
	files, fset := parsePackageFixture(t, source)
	info := packageBindings(files, fset)
	packages := normalizePackageImports(files, info)
	if _, collision := packages["_reusecheck_import_0"]; collision {
		t.Fatal("generated import name collides with source declaration")
	}
	function := files[0].Decls[4].(*ast.FuncDecl)
	selector := function.Body.List[0].(*ast.AssignStmt).Rhs[0].(*ast.SelectorExpr)
	if selector.X.(*ast.Ident).Name != "s" {
		t.Fatal("local receiver renamed as imported package")
	}
	if _, ok := function.Body.List[2].(*ast.AssignStmt).Rhs[0].(*ast.Ident); !ok {
		t.Fatal("local declaration expanded as dot import")
	}
}

func TestPackageBindingsPreserveCollectionTemplates(t *testing.T) {
	provider := "package contracts\ntype Values = []string"
	consumer := strings.Replace(collectionContracts, "func exact(values []string)", "func exact(values Values)", 1)
	findings := packageSourceFindings(t, provider, consumer)
	if len(findings) != 3 {
		t.Fatalf("package normalization changed collection templates: %+v", findings)
	}
}

func packageMappingSource(parameter, setup, receiver string) string {
	source := strings.Replace(mappingFixture, "measured s.DependencyStats", parameter, 1)
	source = strings.Replace(source, `import s "`+sharedPackage+`"`, "", 1)
	source = strings.Replace(source, `_ = s.BuildDependencyReportFromStats(name, "python", measured)`, setup, 1)
	return strings.ReplaceAll(source, "measured.", receiver+".")
}

func packageSourceFindings(t *testing.T, sources ...string) []Finding {
	t.Helper()
	files := make(map[string][]byte)
	for index, source := range sources {
		files[fmt.Sprintf("internal/lang/fixture/file%d.go", index)] = []byte(source)
	}
	findings, err := AnalyzeSources(files)
	if err != nil {
		t.Fatal(err)
	}
	return findings
}

func parsePackageFixture(t *testing.T, sources ...string) ([]*ast.File, *token.FileSet) {
	t.Helper()
	fset := token.NewFileSet()
	var files []*ast.File
	for index, source := range sources {
		file, err := parser.ParseFile(fset, fmt.Sprintf("file%d.go", index), source, 0)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, file)
	}
	return files, fset
}
