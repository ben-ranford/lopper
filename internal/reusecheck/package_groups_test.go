package reusecheck

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestAnalysisGroupsIsolateDuplicateObjects(t *testing.T) {
	for _, declarations := range [][2]string{
		{"func build() {}", "func build() {}"},
		{"type Stats struct{}", "type Stats int"},
		{"var measured int", "var measured string"},
		{"const count = 1", "const count = 2"},
		{"var first, measured int", "var measured bool"},
		{"func build() {}", "type build struct{}"},
	} {
		files := parseAnalysisGroupFiles(t, declarations[0], declarations[1], "var unrelated bool")
		groups := analysisGroups(files)
		if len(groups) != len(files) {
			t.Fatalf("declarations %q grouped into %d groups, want %d", declarations, len(groups), len(files))
		}
		if groups[0].targets != nil || len(groups[0].files) != 1 || groups[0].files[0] != files[2] {
			t.Fatal("unambiguous declarations must be analyzed together once")
		}
		for index, group := range groups[1:] {
			if !group.targets[files[index]] || len(group.files) != 2 || group.files[0] != files[index] || group.files[1] != files[2] {
				t.Fatalf("variant group %d lost its target or common declarations", index)
			}
		}
	}
}

func TestAnalysisGroupsDuplicateMethods(t *testing.T) {
	for _, receivers := range [][2]string{
		{"Holder", "Holder"},
		{"Holder", "*Holder"},
		{"Holder[T]", "Holder[U]"},
		{"Holder[T, U]", "*Holder[A, B]"},
	} {
		first := "func (" + receivers[0] + ") build() {}"
		second := "func (" + receivers[1] + ") build() {}"
		files := parseAnalysisGroupFiles(t, first, second)
		if groups := analysisGroups(files); len(groups) != 2 {
			t.Fatalf("receiver variants %q produced %d groups, want isolated files", receivers, len(groups))
		}
	}
}

func TestAnalysisGroupsCombineUnambiguousSources(t *testing.T) {
	files := parseAnalysisGroupFiles(t,
		"import shared \"example.com/one\"; type Holder struct{}; func init() {}; var _ = 1; func (Holder) build() {}",
		"import shared \"example.com/two\"; type Other struct{}; func init() {}; const _ = 2; func (Other) build() {}",
		"func build() {}; func (Holder) second() {}; func _() {}",
	)
	groups := analysisGroups(files)
	if len(groups) != 1 || len(groups[0].files) != len(files) || groups[0].targets != nil {
		t.Fatalf("unambiguous files produced %d groups, want one combined group", len(groups))
	}
	for index, file := range files {
		if groups[0].files[index] != file {
			t.Fatalf("combined group changed source order at %d", index)
		}
	}
}

func TestAnalysisGroupsSingleAndEmpty(t *testing.T) {
	if groups := analysisGroups(nil); len(groups) != 0 {
		t.Fatalf("empty input produced %d groups", len(groups))
	}
	files := parseAnalysisGroupFiles(t, "func build() {}; func init() {}; var _ = 1")
	groups := analysisGroups(files)
	if len(groups) != 1 || len(groups[0].files) != 1 || groups[0].files[0] != files[0] || groups[0].targets != nil {
		t.Fatal("single source must remain one unchanged group")
	}
}

func TestAnalysisGroupsBoundIndependentConflicts(t *testing.T) {
	files := parseAnalysisGroupFiles(t, "var first int", "var first bool", "var second int", "var second bool", "type Common struct{}")
	groups := analysisGroups(files)
	if len(groups) != 5 {
		t.Fatalf("independent conflicts produced %d groups, want common plus four targets", len(groups))
	}
	for index, group := range groups[1:] {
		if !group.targets[files[index]] || len(group.files) != 2 || group.files[1] != files[4] {
			t.Fatalf("group %d combined variants or lost common declarations", index)
		}
	}
}

func TestAnalysisGroupsKeepAllOmittedOwners(t *testing.T) {
	files := parseAnalysisGroupFiles(t, "var platform int", "var platform bool", "var platform string", "type Common struct{}")
	groups := analysisGroups(files)
	if len(groups) != 4 || len(groups[0].omitted) != 3 {
		t.Fatalf("three duplicate owners were not all isolated: %+v", groups)
	}
	for _, group := range groups[1:] {
		if len(group.omitted) != 2 {
			t.Fatalf("target scope lost an omitted owner: %+v", group)
		}
		for _, file := range group.omitted {
			if group.targets[file] || file == files[3] {
				t.Fatal("selected source was retained as an omitted variant")
			}
		}
	}
}

func TestAnalysisGroupsAliasReceiverKeys(t *testing.T) {
	files := parseAnalysisGroupFiles(t, "type Holder struct{}; type Alias = Holder", "func (Alias) build() {}", "func (Holder) build() {}")
	groups := analysisGroups(files)
	if len(groups) != 3 || len(groups[0].files) != 1 || groups[0].files[0] != files[0] {
		t.Fatalf("alias and original receiver did not collide: %+v", groups)
	}
	files = parseAnalysisGroupFiles(t, "type Holder struct{}; type Distinct Holder", "func (Distinct) build() {}", "func (Holder) build() {}")
	if groups = analysisGroups(files); len(groups) != 1 {
		t.Fatal("distinct defined receivers must keep independent method namespaces")
	}
}

func TestAnalysisReceiverNameUnproven(t *testing.T) {
	for _, source := range []string{"other.Holder", "[]Holder"} {
		expression, err := parser.ParseExpr(source)
		if err != nil {
			t.Fatal(err)
		}
		if got := analysisReceiverName(expression); got != "" {
			t.Fatalf("unproven receiver %s has key %q", source, got)
		}
		function := &ast.FuncDecl{Name: ast.NewIdent("build"), Recv: &ast.FieldList{List: []*ast.Field{{Type: expression}}}}
		if key := analysisFunctionKey(function); key != "" {
			t.Fatalf("unproven receiver %s has method key %q", source, key)
		}
	}
}

func parseAnalysisGroupFiles(t *testing.T, declarations ...string) []*ast.File {
	t.Helper()
	files := make([]*ast.File, 0, len(declarations))
	for _, declaration := range declarations {
		file, err := parser.ParseFile(token.NewFileSet(), "fixture.go", "package fixture; "+declaration, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, file)
	}
	return files
}
