package reusecheck

import (
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"strings"
	"testing"
)

func TestPackageVariantsKeepCommonProvenance(t *testing.T) {
	sources := map[string][]byte{
		"common.go":           []byte(`package fixture; import shared "` + sharedPackage + `"; type Stats = shared.DependencyStats`),
		"consumer.go":         []byte(packageMappingSource("measured Stats", "", "measured")),
		"platform_linux.go":   []byte("package fixture; const platform = 1"),
		"platform_windows.go": []byte("package fixture; const platform = 2"),
	}
	findings, err := AnalyzeSources(sources)
	if err != nil || len(findings) != 1 || findings[0].Path != "consumer.go" || findings[0].Advisory {
		t.Fatalf("unrelated variants lost common provenance: %+v error=%v", findings, err)
	}
}

func TestPackageVariantsDoNotRevealBuiltins(t *testing.T) {
	for _, other := range []string{"func len(any) int { return 1 }", "const platform = 2"} {
		sources := map[string][]byte{
			"common.go":           []byte(`package fixture; import shared "` + sharedPackage + `"; type Stats = shared.DependencyStats`),
			"consumer.go":         []byte(packageMappingSource("measured Stats", "", "measured")),
			"collection.go":       []byte(localCollectionSource("var copy = " + collectionFunctionLiteral("trimmed"))),
			"platform_linux.go":   []byte("package fixture; const platform = 1; func len(any) int { return 0 }"),
			"platform_windows.go": []byte("package fixture; " + other),
		}
		findings, err := AnalyzeSources(sources)
		if err != nil || len(findings) != 1 || findings[0].Path != "consumer.go" || findings[0].Advisory {
			t.Fatalf("omitted shadow revealed a builtin or hid unrelated provenance: %+v error=%v", findings, err)
		}
	}
}

func TestPackageVariantTargetsHaveIsolatedImports(t *testing.T) {
	provider := `package fixture; import . "` + sharedPackage + `"; type Stats = DependencyStats`
	for _, order := range [][2]string{{"a_variant.go", "z_variant.go"}, {"z_variant.go", "a_variant.go"}} {
		first := packageMappingSource("measured Stats", "", "measured")
		second := strings.Replace(first, `import r "`+module+`report"`, `import r "example.com/report"`, 1)
		sources := map[string][]byte{
			"common.go": []byte(provider),
			order[0]:    []byte(first),
			order[1]:    []byte(second),
		}
		findings, err := AnalyzeSources(sources)
		if err != nil || len(findings) != 1 || findings[0].Path != order[0] || findings[0].Advisory {
			t.Fatalf("variant normalization leaked across source order %v: %+v error=%v", order, findings, err)
		}
		again, err := AnalyzeSources(sources)
		if err != nil || !reflect.DeepEqual(findings, again) {
			t.Fatalf("repeated analysis changed variant findings: before=%+v after=%+v error=%v", findings, again, err)
		}
	}
}

func TestPackageVariantsUsePhysicalPaths(t *testing.T) {
	method := strings.Replace(collectionFunctionLiteral("trimmed"), "func(", "func SortedUniqueTrimmedStrings(", 1)
	sources := map[string][]byte{
		"copy.go":             []byte("//line internal/report/strings.go:1\n" + localCollectionSource(method)),
		"platform_linux.go":   []byte("package fixture; const platform = 1"),
		"platform_windows.go": []byte("package fixture; const platform = 2"),
	}
	findings, err := AnalyzeSources(sources)
	if err != nil || len(findings) != 1 || findings[0].Path != "copy.go" || findings[0].Advisory {
		t.Fatalf("line directive redirected source provenance or owner exemption: %+v error=%v", findings, err)
	}
}

func TestPackageVariantsEmitEachSourceOnce(t *testing.T) {
	sources := map[string][]byte{
		"common.go":   []byte(`package fixture; import s "` + sharedPackage + `"; type Stats = s.DependencyStats`),
		"consumer.go": []byte(packageMappingSource("measured Stats", "", "measured")),
	}
	for index, name := range []string{"alpha", "beta", "gamma", "delta"} {
		mapping := strings.Replace(packageMappingSource("measured Stats", "", "measured"), "func build(", "func "+name+"(", 1)
		key := "first"
		if index >= 2 {
			key = "second"
		}
		sources[name+".go"] = []byte(mapping + "; const " + key + " = 0")
	}
	findings, err := AnalyzeSources(sources)
	if err != nil || len(findings) != 5 {
		t.Fatalf("independent variants duplicated or lost source findings: %+v error=%v", findings, err)
	}
	seen := make(map[string]bool)
	for _, finding := range findings {
		if seen[finding.Path] || finding.Advisory {
			t.Fatalf("source was emitted twice or lost common provenance: %+v", findings)
		}
		seen[finding.Path] = true
	}
}

func TestPackageVariantsDoNotBorrowFactoryResults(t *testing.T) {
	for _, tc := range []struct{ call, first, second string }{
		{"makeStats()", "func makeStats() Stats { return Stats{} }", "func makeStats() int { return 0 }"},
		{"holder.makeStats()", "type Alias = Holder; func (Alias) makeStats() Stats { return Stats{} }", "func (Holder) makeStats() int { return 0 }"},
	} {
		consumer := packageMappingSource("holder Holder", "measured := "+tc.call, "measured")
		first := strings.Replace(consumer, "func build(", "func first(", 1)
		second := strings.Replace(consumer, "func build(", "func second(", 1)
		sources := map[string][]byte{
			"common.go":   []byte(`package fixture; import s "` + sharedPackage + `"; type Stats = s.DependencyStats; type Holder struct{}`),
			"consumer.go": []byte(consumer),
			"first.go":    []byte(first + "; " + tc.first),
			"second.go":   []byte(second + "; " + tc.second),
		}
		findings, err := AnalyzeSources(sources)
		if err != nil || len(findings) != 1 || findings[0].Path != "first.go" || findings[0].Advisory {
			t.Fatalf("call %s borrowed another variant's result or lost its own: %+v error=%v", tc.call, findings, err)
		}
	}
}

func TestPackageVariantsKeepOpaqueMethodShadows(t *testing.T) {
	for _, tc := range []struct {
		name, declarations, parameter string
		advisory                      bool
	}{
		{"alias", "type Alias = Holder; func (Alias) UsedCount() int { return 0 }", "Holder", true},
		{"defined", "type Alias Holder; func (Alias) UsedCount() int { return 0 }", "Holder", false},
		{"generic", "func (Generic[T]) UsedCount() int { return 0 }", "Generic[int]", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sources := map[string][]byte{
				"common.go":   []byte(`package fixture; import s "` + sharedPackage + `"; type Holder struct { s.DependencyStats }; type Generic[T any] struct { s.DependencyStats }`),
				"consumer.go": []byte(packageMappingSource("measured "+tc.parameter, "", "measured")),
				"first.go":    []byte("package fixture; const platform = 1; " + tc.declarations),
				"second.go":   []byte("package fixture; const platform = 2"),
			}
			findings, err := AnalyzeSources(sources)
			if err != nil || len(findings) != 1 || findings[0].Advisory != tc.advisory {
				t.Fatalf("omitted method changed selected receiver semantics: %+v error=%v", findings, err)
			}
		})
	}
}

func TestPackageVariantAliasDoesNotReplaySelectedMethod(t *testing.T) {
	consumer := packageMappingSource("measured Holder", "", "measured")
	sources := map[string][]byte{
		"common.go":   []byte(`package fixture; import s "` + sharedPackage + `"; type Holder struct { s.DependencyStats }; type Other struct{}`),
		"consumer.go": []byte(consumer),
		"first.go":    []byte(strings.Replace(consumer, "func build(", "func first(", 1) + "; type Alias = Other; func (Alias) UsedCount() int { return 0 }"),
		"second.go":   []byte("package fixture; type Alias = Holder"),
	}
	findings, err := AnalyzeSources(sources)
	if err != nil || len(findings) != 2 {
		t.Fatalf("selected alias method was lost or replayed: %+v error=%v", findings, err)
	}
	for _, finding := range findings {
		if finding.Advisory {
			t.Fatalf("omitted alias redirected a selected method onto Holder: %+v", finding)
		}
	}
}

func TestPackageVariantsRetainShadowsAfterTypeErrors(t *testing.T) {
	source := strings.Replace(mappingFixture, "import s ", "import . ", 1)
	source = strings.Replace(source, "func build(name string, measured s.DependencyStats)", "type Generic[T any] struct{}; var copy = Generic(func()", 1)
	source = strings.Replace(source, `_ = s.BuildDependencyReportFromStats(name, "python", measured)`, "", 1)
	source = strings.ReplaceAll(source, "measured.", "DependencyStats(0).")
	source = strings.Replace(source, "Name:name", `Name:"name"`, 1) + ")"
	sources := map[string][]byte{
		"consumer.go": []byte(source),
		"first.go":    []byte("package fixture; const platform = 1; type DependencyStats int"),
		"second.go":   []byte("package fixture; const platform = 2"),
	}
	findings, err := AnalyzeSources(sources)
	if err != nil || len(findings) != 0 {
		t.Fatalf("skipped type-checker operand exposed a shadowed dot import: %+v error=%v", findings, err)
	}
}

func TestAnalysisGroupReparseFailure(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "source.go", "package fixture", 0)
	if err != nil {
		t.Fatal(err)
	}
	group := analysisGroup{files: []*ast.File{file}, targets: map[*ast.File]bool{file: true}}
	if _, _, err := parseAnalysisGroup(group, map[string][]byte{"source.go": []byte("package")}, fset); err == nil {
		t.Fatal("invalid source bytes must fail without partial group findings")
	}
}
