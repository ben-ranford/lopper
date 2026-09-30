package reusecheck

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

func TestReportConversionEffects(t *testing.T) {
	for _, tc := range []struct {
		expression, declarations, setup string
		advisory                        bool
	}{
		{`string(name)`, "", "", false},
		{`string([]byte(name))`, "", "", false},
		{`string(([len("x")]byte)([1]byte{65})[0])`, "", "", false},
		{`text(name)`, `type text = string`, "", false},
		{`string(text(name))`, `type text string`, "", false},
		{`text[int](name)`, `type text[T any] = string`, "", false},
		{`struct{ Value string }(struct{ Value string }{name}).Value`, "", "", false},
		{`s.DependencyStats(measured).UnusedImports[0]`, "", "", false},
		{`(*s.DependencyStats)(&measured).UnusedImports[0]`, "", "", false},
		{`text(name)`, `type text = string`, `text := func(value string) string { measured.UsedCount++; return value };`, true},
		{`string(name)`, "", `string := func(value string) string { measured.UsedCount++; return value };`, true},
		{`string(mutate(&measured))`, "", "", true},
		{`string([]byte(mutate(&measured)))`, "", "", true},
		{`string(<-names)`, `var names chan string`, "", true},
		{`string(name + mutate(&measured))`, "", "", true},
		{`mutate(&measured) + string(name)`, "", "", true},
		{`unknown(name)`, "", "", true},
		{`s.Unknown(name)`, "", "", true},
		{`s.DependencyStats(name)`, "", `s := struct { DependencyStats func(string) string }{func(value string) string { measured.UsedCount++; return value }};`, true},
	} {
		t.Run(tc.expression+tc.setup, func(t *testing.T) {
			source := strings.Replace(mappingFixture, "Name:name", "Name:"+tc.expression, 1)
			source = strings.Replace(source, " return r.DependencyReport", tc.setup+" return r.DependencyReport", 1)
			source += "\n" + tc.declarations + `
func mutate(stats *s.DependencyStats) string { stats.UsedCount++; return "changed" }
`
			findings, err := Analyze("conversion.go", []byte(source))
			if err != nil || len(findings) != 1 || findings[0].Advisory != tc.advisory {
				t.Fatalf("findings=%+v err=%v want advisory=%v", findings, err, tc.advisory)
			}
		})
	}
}

func TestConversionTypeAcrossFiles(t *testing.T) {
	for _, conversion := range []string{"Text(name)", "Stats(measured).UnusedImports[0]"} {
		source := strings.Replace(mappingFixture, "Name:name", "Name:"+conversion, 1)
		findings, err := AnalyzeSources(map[string][]byte{
			"mapping.go": []byte(source),
			"types.go":   []byte(`package fixture; import shared "github.com/ben-ranford/lopper/internal/lang/shared"; type Text = string; type Stats = shared.DependencyStats`),
		})
		if err != nil || len(findings) != 1 || findings[0].Advisory {
			t.Fatalf("conversion=%s findings=%+v err=%v", conversion, findings, err)
		}
	}
}

func TestOwnedStatsConversionTypeCycles(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "cycles.go", `package fixture
type Loop = *Loop
type First = *Second
type Second = *First`, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, declaration := range file.Decls {
		typ := declaration.(*ast.GenDecl).Specs[0].(*ast.TypeSpec)
		if ownedStatsConversionType(typ.Type, nil) {
			t.Fatalf("cyclic alias %s establishes an owned conversion target", typ.Name)
		}
	}
}
