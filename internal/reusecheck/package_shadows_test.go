package reusecheck

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"go/types"
	"reflect"
	"sort"
	"testing"
)

func TestAnalysisShadowsOpaqueBindings(t *testing.T) {
	fset := token.NewFileSet()
	source := `package fixture; import . "example.com/shared"
func use() { _ = len([]string{}); var _ DependencyStats; _ = maker(); _, _ = state, limit }`
	included := parseShadowSources(t, fset, source)
	omitted := parseShadowSources(t, fset, `package fixture
func len([]string) int { return 1 }; type DependencyStats struct{}
func maker() DependencyStats { return DependencyStats{} }; var state = DependencyStats{}; const limit = 3`)
	shadow := analysisShadows(included, omitted)
	if shadow == nil || len(shadow.Decls) != 5 {
		t.Fatalf("unexpected opaque declarations: %+v", shadow)
	}
	for _, declaration := range shadow.Decls {
		value := declaration.(*ast.GenDecl).Specs[0].(*ast.ValueSpec)
		_, opaque := value.Type.(*ast.BadExpr)
		if !opaque || len(value.Values) != 0 || value.Names[0].Obj == nil || value.Names[0].Obj.Decl != value {
			t.Fatalf("shadow retained provenance or lost parser binding: %+v", value)
		}
	}
	info := packageBindings(append([]*ast.File{shadow}, included...), fset)
	seen := make(map[string]bool)
	ast.Inspect(included[0], func(node ast.Node) bool {
		ident, ok := node.(*ast.Ident)
		if !ok || !shadowTestName(ident.Name) {
			return true
		}
		object, variable := info.ObjectOf(ident).(*types.Var)
		if !variable || object.Type() != types.Typ[types.Invalid] || ident.Obj == nil {
			t.Fatalf("omitted name %s regained a builtin/type binding: %v", ident.Name, object)
		}
		if sourceFunctionType(ident, info, nil, make(map[types.Object]bool)) != nil {
			t.Fatalf("omitted name %s retained a callable signature", ident.Name)
		}
		seen[ident.Name] = true
		return true
	})
	if len(seen) != 5 {
		t.Fatalf("missing lexical uses: %v", seen)
	}
	normalizePackageImports(append([]*ast.File{shadow}, included...), info)
	ast.Inspect(included[0], func(node ast.Node) bool {
		if selector, ok := node.(*ast.SelectorExpr); ok && selector.Sel.Name == "DependencyStats" {
			t.Fatal("opaque package binding reverted to dot-import ownership")
		}
		return true
	})
}

func shadowTestName(name string) bool {
	switch name {
	case "len", "DependencyStats", "maker", "state", "limit":
		return true
	default:
		return false
	}
}

func TestBindAnalysisShadowsRepairsMissingUses(t *testing.T) {
	fset := token.NewFileSet()
	included := parseShadowSources(t, fset, `package fixture
import . "example.com/shared"; import named "example.com/other"
func use() { _ = len([]string{}); var _ DependencyStats; _ = named.Value
len := func(value int) int { return value }; _ = len(0) }`)
	omitted := parseShadowSources(t, fset, "package fixture; var len func([]string) int; type DependencyStats struct{}; var named int")
	shadow := analysisShadows(included, omitted)
	info := packageBindings(append([]*ast.File{shadow}, included...), fset)
	missing := make(map[string]*ast.Ident)
	for _, ident := range included[0].Unresolved {
		if ident.Name == "len" || ident.Name == "DependencyStats" || ident.Name == "named" {
			delete(info.Uses, ident)
			ident.Obj = nil
			missing[ident.Name] = ident
		}
	}
	bindAnalysisShadows(included, shadow, info)
	for _, name := range []string{"len", "DependencyStats"} {
		ident := missing[name]
		if ident == nil || ident.Obj == nil || info.ObjectOf(ident) == nil {
			t.Fatalf("missing operand binding was not repaired: %s", name)
		}
		if canonicalName(ident, nil, imports(included[0]), info, make(map[types.Object]string)) != "local0" {
			t.Fatalf("omitted %s regained builtin/type canonical semantics", name)
		}
	}
	qualifier := missing["named"]
	if qualifier == nil || qualifier.Obj != nil || info.ObjectOf(qualifier) != nil {
		t.Fatal("opaque fallback captured an explicit import qualifier")
	}
	for ident, object := range info.Uses {
		if ident.Name == "len" && ident != missing["len"] && object == info.ObjectOf(missing["len"]) {
			t.Fatal("opaque fallback captured a local shadow")
		}
	}
}

func TestAnalysisShadowsIncludedNamesDischarge(t *testing.T) {
	fset := token.NewFileSet()
	included := parseShadowSources(t, fset, "package fixture; var value int; type Kind struct{}; func makeValue() {}; func init() {}; func _() {}")
	originals := parseShadowSources(t, fset, "package fixture; const value = 1; type Kind = int; var makeValue func() int; func init() {}; func _() {}")
	if shadow := analysisShadows(included, originals); shadow != nil {
		t.Fatalf("included package names retained shadows: %+v", shadow.Decls)
	}
	if analysisShadows(nil, originals) != nil || analysisShadows(included, nil) != nil {
		t.Fatal("empty analysis scope produced shadows")
	}
	bindAnalysisShadows(included, nil, nil)
}

func TestAnalysisMethodKeysDeclarationKinds(t *testing.T) {
	fset := token.NewFileSet()
	files := parseShadowSources(t, fset, `package fixture; type Holder struct{}
func factory() {}; func init() {}; func _() {}; func (Holder) _() {}`)
	for _, declaration := range files[0].Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok {
			continue
		}
		var want []string
		if function.Name.Name == "factory" {
			want = []string{".factory"}
		}
		if keys := analysisMethodKeys(function, analysisTypeDeclarations(files)); !reflect.DeepEqual(keys, want) {
			t.Fatalf("declaration %s keys=%v want=%v", function.Name.Name, keys, want)
		}
	}
	included := parseShadowSources(t, fset, "package fixture; type Holder struct{}; func factory() {}")
	if shadow := analysisShadows(included, files); shadow != nil {
		t.Fatalf("blank method acquired an addressable name: %+v", shadow)
	}
}

func TestAnalysisShadowsReceiverAliases(t *testing.T) {
	for _, tc := range []struct {
		name    string
		omitted []string
		blocked []string
	}{
		{"direct", []string{"func (Holder) UsedCount() int { return 1 }"}, []string{"Holder"}},
		{"pointer", []string{"func (*Holder) UsedCount() int { return 1 }"}, []string{"Holder"}},
		{"alias", []string{"type Alias = Holder; func (Alias) UsedCount() int { return 1 }"}, []string{"Holder"}},
		{"cross-file alias", []string{"type Alias = Middle", "type Middle = *Holder", "func (Alias) UsedCount() int { return 1 }"}, []string{"Holder"}},
		{"distinct", []string{"type Alias Holder; func (Alias) UsedCount() int { return 1 }"}, nil},
		{"ambiguous alias", []string{"type Alias = Holder", "type Alias = Other", "func (Alias) UsedCount() int { return 1 }"}, []string{"Holder", "Other"}},
		{"cycle", []string{"type Alias = Middle; type Middle = Alias; func (Alias) UsedCount() int { return 1 }"}, nil},
		{"local cycle", []string{"type Alias = Middle; type Middle = Alias", "type Middle = Holder", "func (Alias) UsedCount() int { return 1 }"}, nil},
		{"cycle with target", []string{"type Alias = Middle", "type Middle = Alias", "type Middle = Holder", "func (Alias) UsedCount() int { return 1 }"}, []string{"Holder"}},
		{"generic", []string{"func (Generic[T]) UsedCount() T { panic(0) }"}, []string{"Generic"}},
		{"generic pair pointer", []string{"func (*Pair[A, B]) UsedCount() A { panic(0) }"}, []string{"Pair"}},
		{"pointer alternatives", []string{"type Alias = **Middle", "type Alias = Middle", "type Middle = Holder", "func (Alias) UsedCount() int { return 1 }"}, []string{"Holder"}},
		{"unnamed alias", []string{"type Alias = struct{}; func (Alias) UsedCount() int { return 1 }"}, nil},
		{"parameter-dependent alias", []string{"type Alias[T any] = T; func (Alias[Holder]) UsedCount() int { return 1 }"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checkShadowReceivers(t, tc.omitted, tc.blocked)
		})
	}
}

func checkShadowReceivers(t *testing.T, omitted, blocked []string) {
	t.Helper()
	fset := token.NewFileSet()
	common := "package fixture; type Holder struct{ int }; type Other struct{ int }; type Generic[T any] struct{}; type Pair[A, B any] struct{}"
	included := parseShadowSources(t, fset, common)
	var sources []string
	for _, source := range omitted {
		sources = append(sources, "package fixture; "+source)
	}
	files := parseShadowSources(t, fset, sources...)
	shadow := analysisShadows(included, files)
	if shadow == nil {
		t.Fatal("omitted receiver scope produced no shadows")
	}
	info := packageBindings(append([]*ast.File{shadow}, included...), fset)
	bindAnalysisShadows(included, shadow, info)
	var actual []string
	for name, declarations := range analysisTypeDeclarations(included) {
		receiver := ast.Expr(declarations[0].Name)
		switch name {
		case "Generic":
			receiver = &ast.IndexExpr{X: receiver, Index: ast.NewIdent("int")}
		case "Pair":
			receiver = &ast.IndexListExpr{X: receiver, Indices: []ast.Expr{ast.NewIdent("int"), ast.NewIdent("int")}}
		}
		if sourceMethodShadows(receiver, "UsedCount", info) {
			actual = append(actual, name)
		}
	}
	sort.Strings(actual)
	if !reflect.DeepEqual(actual, blocked) {
		t.Fatalf("method shadows=%v want=%v", actual, blocked)
	}
	for _, declaration := range shadow.Decls {
		if method, ok := declaration.(*ast.FuncDecl); ok {
			if method.Body != nil || method.Type.Params.NumFields() != 0 || method.Type.Results.NumFields() != 0 || len(method.Recv.List[0].Names) != 0 {
				t.Fatal("method shadow retained its implementation or signature")
			}
			if info.Defs[method.Name] == nil {
				t.Fatal("method shadow was not indexed despite unavailable receiver types")
			}
		}
	}
}

func TestAnalysisShadowsSemanticMethodDischarge(t *testing.T) {
	fset := token.NewFileSet()
	common := "package fixture; type Holder struct{}; type Other struct{}"
	selected := "package fixture; type Alias = Holder; func (Alias) UsedCount() int { return 1 }"
	included := parseShadowSources(t, fset, common, selected)
	omitted := parseShadowSources(t, fset, "package fixture; type Alias = Other; func (Alias) UsedCount() string { return \"other\" }")
	shadow := analysisShadows(included, omitted)
	if shadow == nil || len(shadow.Decls) != 1 {
		t.Fatalf("semantic method discharge lost the alternative: %+v", shadow)
	}
	method, ok := shadow.Decls[0].(*ast.FuncDecl)
	if !ok || analysisFunctionKey(method) != "Other.UsedCount" {
		t.Fatalf("included alias discharged an unrelated target: %+v", shadow.Decls)
	}
}

func TestAnalysisShadowsPreserveOriginalAST(t *testing.T) {
	fset := token.NewFileSet()
	sources := []string{
		"package fixture; type Holder struct{}",
		"package fixture; type Alias = *Holder; func (receiver Alias) UsedCount(argument int) int { return argument }; var value = 42",
	}
	originals := parseShadowSources(t, fset, sources...)
	included := parseShadowSources(t, fset, sources[0])
	originalIdentifiers := make(map[*ast.Ident]bool)
	var before bytes.Buffer
	for _, file := range originals {
		if err := format.Node(&before, fset, file); err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			if ident, ok := node.(*ast.Ident); ok {
				originalIdentifiers[ident] = true
			}
			return true
		})
	}
	shadow := analysisShadows(included, originals[1:])
	ast.Inspect(shadow, func(node ast.Node) bool {
		if ident, ok := node.(*ast.Ident); ok && originalIdentifiers[ident] {
			t.Fatal("shadow reused an original identifier")
		}
		return true
	})
	info := packageBindings(append([]*ast.File{shadow}, included...), fset)
	normalizePackageImports(append([]*ast.File{shadow}, included...), info)
	var after bytes.Buffer
	for _, file := range originals {
		if err := format.Node(&after, fset, file); err != nil {
			t.Fatal(err)
		}
	}
	if before.String() != after.String() {
		t.Fatal("shadow construction or binding changed original files")
	}
}

func TestAnalysisShadowsAliasScope(t *testing.T) {
	common := "package fixture; type Holder struct{}; type Other struct{}"
	method := "package fixture; func (Alias) UsedCount() int { return 1 }"
	for _, tc := range []struct {
		name              string
		included, omitted []string
		methods           []string
	}{
		{"selected local alias", []string{"package fixture; type Alias = Other; func (Alias) UsedCount() int { return 1 }"}, []string{"package fixture; type Alias = Holder"}, nil},
		{"omitted local alias", nil, []string{"package fixture; type Alias = Other; func (Alias) UsedCount() int { return 1 }", "package fixture; type Alias = Holder"}, []string{"Other.UsedCount"}},
		{"selected cross-file alias", []string{"package fixture; type Alias = Other", method}, []string{"package fixture; type Alias = Holder"}, nil},
		{"unresolved selected alias", []string{method}, []string{"package fixture; type Alias = Other", "package fixture; type Alias = Holder"}, []string{"Holder.UsedCount", "Other.UsedCount"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fset := token.NewFileSet()
			included := parseShadowSources(t, fset, append([]string{common}, tc.included...)...)
			omitted := parseShadowSources(t, fset, tc.omitted...)
			shadow := analysisShadows(included, omitted)
			var methods []string
			if shadow != nil {
				for _, declaration := range shadow.Decls {
					if function, ok := declaration.(*ast.FuncDecl); ok {
						methods = append(methods, analysisFunctionKey(function))
					}
				}
			}
			sort.Strings(methods)
			if !reflect.DeepEqual(methods, tc.methods) {
				t.Fatalf("shadow method owners=%v want=%v", methods, tc.methods)
			}
		})
	}
}

func TestAnalysisReceiverAliasTraversalIsBounded(t *testing.T) {
	fset := token.NewFileSet()
	sources := []string{"package fixture; type Holder struct{}"}
	const depth = 12
	for index := 0; index < depth; index++ {
		target := fmt.Sprintf("Alias%d", index+1)
		if index == depth-1 {
			target = "Holder"
		}
		declaration := fmt.Sprintf("package fixture; type Alias%d = %s", index, target)
		sources = append(sources, declaration, declaration)
	}
	types := analysisTypeDeclarations(parseShadowSources(t, fset, sources...))
	seen := make(map[*ast.TypeSpec]bool)
	receivers := analysisReceiverAliases(ast.NewIdent("Alias0"), types, seen)
	if len(receivers) != 2 || len(seen) != 2*depth {
		t.Fatalf("alias diamond expanded duplicate paths: receivers=%d visited=%d", len(receivers), len(seen))
	}
}

func parseShadowSources(t *testing.T, fset *token.FileSet, sources ...string) []*ast.File {
	t.Helper()
	var files []*ast.File
	for _, source := range sources {
		file, err := parser.ParseFile(fset, "shadow_fixture.go", source, 0)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, file)
	}
	return files
}
