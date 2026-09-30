package reusecheck

import (
	"bytes"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

const statsFactoryDeclaration = "func factory() s.DependencyStats { panic(0) }"

func TestSourceDeclaredFunctionResults(t *testing.T) {
	for _, result := range []string{"s.DependencyStats", "*s.DependencyStats", "**s.DependencyStats", "Stats", "int"} {
		t.Run(result, func(t *testing.T) {
			declaration := "type Stats s.DependencyStats; func factory() " + result + " { panic(0) }"
			checkSourceStatsCall(t, declaration, "", "", "factory()", result)
		})
	}
	for _, results := range []string{"", "(s.DependencyStats, error)", "(first, second s.DependencyStats)"} {
		checkSourceStatsCall(t, "func factory() "+results+" { panic(0) }", "", "", "factory()", "")
	}
	checkSourceStatsCall(t, "func factory() (stats s.DependencyStats) { panic(0) }", "", "", "factory()", "s.DependencyStats")
	checkSourceStatsCall(t, statsFactoryDeclaration, "", "", "(factory)()", "s.DependencyStats")
}

func TestSourceLocalFunctionBindings(t *testing.T) {
	for _, setup := range []string{
		"var factory func() s.DependencyStats",
		"factory := func() s.DependencyStats { panic(0) }",
		"factory := build",
		"first := build; factory := first",
		"unused, factory := 1, build",
		"factory := raw.(func() s.DependencyStats)",
	} {
		t.Run(setup, func(t *testing.T) {
			checkSourceStatsCall(t, "func build() s.DependencyStats { panic(0) }", "raw any", setup, "factory()", "s.DependencyStats")
		})
	}
	checkSourceStatsCall(t, "", "factory func() s.DependencyStats", "", "factory()", "s.DependencyStats")
	checkSourceStatsCall(t, "", "", "", "func() s.DependencyStats { panic(0) }()", "s.DependencyStats")
	checkSourceStatsCall(t, statsFactoryDeclaration, "", "factory := func() int { return 0 }", "factory()", "int")
}

func TestSourcePackageFunctionBindings(t *testing.T) {
	for _, declaration := range []string{
		"var factory func() s.DependencyStats",
		"var factory = func() s.DependencyStats { panic(0) }",
		"var factory = build",
		"var unused, factory = 1, build",
	} {
		t.Run(declaration, func(t *testing.T) {
			checkSourceStatsCall(t, "func build() s.DependencyStats { panic(0) }; "+declaration, "", "", "factory()", "s.DependencyStats")
		})
	}
}

func TestSourceNamedFunctionTypes(t *testing.T) {
	for _, declaration := range []string{"type Factory func() *s.DependencyStats", "type Factory = func() *s.DependencyStats"} {
		checkSourceStatsCall(t, declaration, "factory Factory", "", "factory()", "*s.DependencyStats")
	}
	for _, declaration := range []string{"type Factory int", "type Factory Factory"} {
		checkSourceStatsCall(t, declaration, "factory Factory", "", "factory()", "")
	}
}

func TestSourceMethodsAndFunctionFields(t *testing.T) {
	declaration := "type Holder struct{}; func (Holder) build() s.DependencyStats { panic(0) }"
	for _, call := range []string{"h.build()", "Holder.build(h)", "holders[0].build()"} {
		checkSourceStatsCall(t, declaration, "h Holder, holders []Holder", "", call, "s.DependencyStats")
	}
	for _, method := range []string{"h.build", "Holder.build"} {
		call := "factory()"
		if method == "Holder.build" {
			call = "factory(h)"
		}
		checkSourceStatsCall(t, declaration, "h Holder", "factory := "+method, call, "s.DependencyStats")
	}
	checkSourceStatsCall(t, declaration, "", "", "Holder.build()", "")
	checkSourceStatsCall(t, "type Holder struct { factory func() s.DependencyStats }", "h Holder", "", "h.factory()", "s.DependencyStats")
	pointerMethod := "type Holder struct{}; func (*Holder) factory() *s.DependencyStats { panic(0) }"
	for _, call := range []string{"h.factory()", "(*Holder).factory(h)"} {
		checkSourceStatsCall(t, pointerMethod, "h *Holder", "", call, "*s.DependencyStats")
	}
}

func TestSourceFunctionCallArity(t *testing.T) {
	for _, tc := range []struct{ parameters, call, want string }{
		{"a, b int", "factory(1, 2)", "s.DependencyStats"},
		{"a, b int", "factory(1)", ""},
		{"", "factory(1)", ""},
		{"values ...int", "factory()", "s.DependencyStats"},
		{"first int, values ...int", "factory(1, 2, 3)", "s.DependencyStats"},
		{"first int, values ...int", "factory(1, values...)", "s.DependencyStats"},
		{"first int, values ...int", "factory()", ""},
		{"first int, values ...int", "factory(values...)", ""},
		{"values []int", "factory(values...)", ""},
	} {
		declaration := "func factory(" + tc.parameters + ") s.DependencyStats { panic(0) }"
		checkSourceStatsCall(t, declaration, "values []int", "", tc.call, tc.want)
	}
}

func TestUnprovenSourceFunctionCalls(t *testing.T) {
	for _, declaration := range []string{"", "var factory = factory", "var first = factory; var factory = first"} {
		checkSourceStatsCall(t, declaration, "", "", "factory()", "")
	}
	for _, parameter := range []string{"factory int", "factory s.UnknownFactory"} {
		checkSourceStatsCall(t, "", parameter, "", "factory()", "")
	}
	for _, call := range []string{"factory()", "factory[int]()"} {
		checkSourceStatsCall(t, "func factory[T any]() s.DependencyStats { panic(0) }", "", "", call, "")
	}
	checkSourceStatsCall(t, "", "", "", "s.UnknownFactory()", "")
	checkSourceStatsCall(t, "", "", "", "s.BuildDependencyStats(\"name\", nil, nil)", "s.DependencyStats")
}

func TestSourceStatsCallPointerAndNamedProvenance(t *testing.T) {
	for _, tc := range []struct {
		name, declaration, result string
		operations                []collectionOperation
		want                      bool
	}{
		{"value address", "", "s.DependencyStats", []collectionOperation{collectionAddress}, true},
		{"value dereference", "", "s.DependencyStats", []collectionOperation{collectionDereference}, false},
		{"pointer dereference", "", "*s.DependencyStats", []collectionOperation{collectionDereference}, true},
		{"pointer address", "", "*s.DependencyStats", []collectionOperation{collectionAddress}, false},
		{"double pointer dereference", "", "**s.DependencyStats", []collectionOperation{collectionDereference}, true},
		{"named stats", "type Stats s.DependencyStats", "Stats", nil, false},
		{"aliased stats", "type Stats = s.DependencyStats", "Stats", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			declaration := tc.declaration + "\nfunc factory() " + tc.result + " { panic(0) }"
			file, fset, function, call := parseSourceStatsCall(t, declaration, "", "", "factory()")
			info := bindings(file, fset)
			indexFunctionDeclarations(file, info)
			packages := imports(file)
			result := inferredStatsCallType(call, packages, info, localDeclarations(function, info))
			if got := dependencyStatsType(collectionIndirection(result, tc.operations), packages); got != tc.want {
				t.Fatalf("stats provenance = %v, want %v", got, tc.want)
			}
		})
	}
}

func parseSourceStatsCall(t *testing.T, declaration, parameters, setup, expression string) (*ast.File, *token.FileSet, *ast.FuncDecl, *ast.CallExpr) {
	t.Helper()
	source := "package fixture\nimport s \"" + sharedPackage + "\"\n" + declaration + "\nfunc use(" + parameters + ") { " + setup + "; _ = " + expression + " }"
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "fixture.go", source, 0)
	if err != nil {
		t.Fatal(err)
	}
	function := file.Decls[len(file.Decls)-1].(*ast.FuncDecl)
	assignment := function.Body.List[len(function.Body.List)-1].(*ast.AssignStmt)
	return file, fset, function, assignment.Rhs[0].(*ast.CallExpr)
}

func TestSourceFunctionResultReportMappings(t *testing.T) {
	for _, tc := range []struct {
		result, setup, receiver string
		want                    bool
	}{
		{"s.DependencyStats", "measured := factory()", "(&measured)", true},
		{"*s.DependencyStats", "measured := factory()", "(*measured)", true},
		{"**s.DependencyStats", "measured := factory()", "(*measured)", true},
		{"s.DependencyStats", "local := factory; measured := local()", "(&measured)", true},
		{"s.DependencyStats", "new := factory; measured := new()", "measured", true},
		{"Stats", "measured := factory()", "measured", false},
		{"*s.DependencyStats", "measured := factory()", "(&measured)", false},
	} {
		t.Run(tc.result+tc.setup+tc.receiver, func(t *testing.T) {
			source := strings.Replace(mappingFixture, "measured s.DependencyStats", "unused string", 1)
			source = strings.Replace(source, `_ = s.BuildDependencyReportFromStats(name, "python", measured)`, tc.setup, 1)
			source = strings.ReplaceAll(source, "measured.", tc.receiver+".")
			source += "\ntype Stats s.DependencyStats\nfunc factory() " + tc.result + " { panic(0) }"
			findings, err := Analyze("fixture.go", []byte(source))
			if err != nil || (len(findings) == 1 && !findings[0].Advisory) != tc.want || (!tc.want && len(findings) != 0) {
				t.Fatalf("findings=%+v err=%v want violation=%v", findings, err, tc.want)
			}
		})
	}
}

func checkSourceStatsCall(t *testing.T, declaration, parameters, setup, expression, want string) {
	t.Helper()
	file, fset, function, call := parseSourceStatsCall(t, declaration, parameters, setup, expression)
	info := bindings(file, fset)
	result := inferredStatsCallType(call, imports(file), info, localDeclarations(function, info))
	var formatted bytes.Buffer
	if result != nil {
		if err := format.Node(&formatted, fset, result); err != nil {
			t.Fatal(err)
		}
	}
	if got := formatted.String(); got != want {
		t.Fatalf("call %s result type = %q, want %q", expression, got, want)
	}
}
