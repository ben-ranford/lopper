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

func TestUnusedMethodReceiverCollectionContracts(t *testing.T) {
	for name, owner := range collectionOwners {
		for _, receiver := range []string{"h Holder", "h *Holder", "_ Holder", "Holder", "value Holder", "h Generic[T]"} {
			t.Run(name+"/"+receiver, func(t *testing.T) {
				method := strings.Replace(collectionFunctionLiteral(name), "func(", "func ("+receiver+") duplicate(", 1)
				source := localCollectionSource("type Holder struct{}; type Generic[T any] struct{}; " + method)
				findings, err := Analyze(owner.owner, []byte(source))
				if err != nil || len(findings) != 1 || findings[0].Rule != owner.rule || findings[0].Advisory || !strings.HasPrefix(findings[0].Function, "duplicate.func@") {
					t.Fatalf("findings=%+v error=%v want %s", findings, err, owner.rule)
				}
			})
		}
	}
}

func TestMethodReceiverCollectionSemantics(t *testing.T) {
	literal := collectionFunctionLiteral("trimmed")
	for _, change := range []struct{ receiver, declaration, old, replacement string }{
		{"h Holder", "type Holder []string", "range values", "range h"},
		{"h Holder", "type Holder []string", "len(values)", "len(h)"},
		{"h Holder", "type Holder []string", "func(values []string)", "func()"},
		{"sort Holder", "type Holder struct { Strings func([]string) }", "not present", ""},
		{"strings Holder", "type Holder struct { TrimSpace func(string) string }", "not present", ""},
		{"h Holder", "type Holder struct{}", "return nil", "return []string{}"},
		{"h Holder", "type Holder struct{}", "sort.Strings(out)", ""},
		{"h Holder", "type Holder struct{}", "func(values []string)", "func(unused int, values []string)"},
		{"h Holder", "type Holder struct{}", "func(values []string)", "func(values []string, unused int)"},
	} {
		t.Run(change.receiver+change.old+change.replacement, func(t *testing.T) {
			body := strings.Replace(literal, change.old, change.replacement, 1)
			if change.replacement == "func()" {
				body = strings.ReplaceAll(body, "values", "h")
			}
			method := strings.Replace(body, "func(", "func ("+change.receiver+") duplicate(", 1)
			findings, err := Analyze("fixture.go", []byte(localCollectionSource(change.declaration+"; "+method)))
			if err != nil || len(findings) != 0 {
				t.Fatalf("receiver-dependent or distinct method matched: %+v error=%v", findings, err)
			}
		})
	}
}

func TestMethodCanonicalizationPreservesReceiverAndAST(t *testing.T) {
	fset := token.NewFileSet()
	source := localCollectionSource("type Holder struct{}; " + strings.Replace(collectionFunctionLiteral("trimmed"), "func(", "func (value *Holder) duplicate(", 1))
	file, err := parser.ParseFile(fset, "method.go", source, 0)
	if err != nil {
		t.Fatal(err)
	}
	method := file.Decls[len(file.Decls)-1].(*ast.FuncDecl)
	info := bindings(file, fset)
	var before, after bytes.Buffer
	if err := format.Node(&before, fset, method); err != nil {
		t.Fatal(err)
	}
	first := canonicalFunction(method, imports(file), info)
	if err := format.Node(&after, fset, method); err != nil {
		t.Fatal(err)
	}
	if first == "" || first != canonicalFunction(method, imports(file), info) || before.String() != after.String() {
		t.Fatal("canonicalization changed the method AST or its repeated fingerprint")
	}
}

func TestMethodCanonicalParameterOrder(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "order.go", `package fixture
type Holder []int
func direct(first, second int) int { return first + second }
func (h Holder) same(a, b int) int { return a + b }
func (h Holder) reversed(a, b int) int { return b + a }
func (h Holder) receiver(a, b int) int { return h[0] + b }`, 0)
	if err != nil {
		t.Fatal(err)
	}
	info := bindings(file, fset)
	fingerprints := make(map[string]string)
	for _, declaration := range file.Decls {
		if fn, ok := declaration.(*ast.FuncDecl); ok {
			fingerprints[fn.Name.Name] = canonicalFunction(fn, nil, info)
		}
	}
	if fingerprints["direct"] != fingerprints["same"] || fingerprints["direct"] == fingerprints["reversed"] || fingerprints["direct"] == fingerprints["receiver"] {
		t.Fatal("method canonicalization lost parameter order or receiver ownership")
	}
}
