package reusecheck

import (
	"go/ast"
	"go/parser"
	"strings"
	"testing"
)

func TestAddressTakenStatsReceivers(t *testing.T) {
	for _, tc := range []struct {
		parameter, receiver string
		want                bool
	}{
		{"measured s.DependencyStats", "(&measured)", true},
		{"measured s.DependencyStats", "(&((measured)))", true},
		{"measured *s.DependencyStats", "(&(*measured))", true},
		{"measured s.DependencyStats", "(*(&measured))", true},
		{"measured *s.DependencyStats", "(&measured)", false},
		{"measured OtherStats", "(&measured)", false},
		{"measured chan s.DependencyStats", "(<-measured)", false},
		{"measured s.DependencyStats", "(-measured)", false},
		{"measured func() s.DependencyStats", "(&measured())", false},
	} {
		t.Run(tc.parameter+tc.receiver, func(t *testing.T) {
			source := strings.Replace(mappingFixture, "measured s.DependencyStats", tc.parameter, 1)
			source = strings.ReplaceAll(source, "measured.", tc.receiver+".")
			findings, err := Analyze("fixture.go", []byte(source))
			if err != nil || (len(findings) == 1 && !findings[0].Advisory) != tc.want || (!tc.want && len(findings) != 0) {
				t.Fatalf("findings=%+v err=%v want violation=%v", findings, err, tc.want)
			}
		})
	}
}

func TestReportMappingEvaluationEffects(t *testing.T) {
	for _, tc := range []struct {
		name, old, replacement string
		advisory               bool
	}{
		{"interleaved receiver mutation", "Name:name, Language:", "UsedExportsCount:measured.UsedCount, Name:mutate(&measured), Language:", true},
		{"name call", "Name:name", "Name:mutate(&measured)", true},
		{"trailing call", "EstimatedUnusedBytes:0", "EstimatedUnusedBytes:mutateBytes(&measured)", true},
		{"nested call", "EstimatedUnusedBytes:0", "EstimatedUnusedBytes:1 + mutateBytes(&measured)", true},
		{"receive", "Name:name", "Name:<-names", true},
		{"immediately invoked closure", "Name:name", "Name:func() string { mutate(&measured); return name }()", true},
		{"pure arithmetic", "EstimatedUnusedBytes:0", "EstimatedUnusedBytes:1 + 2", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := mappingFixture
			if tc.name == "interleaved receiver mutation" {
				source = strings.Replace(source, " UsedExportsCount:measured.UsedCount,", "", 1)
			}
			source = strings.Replace(source, tc.old, tc.replacement, 1)
			source += `
var names chan string
func mutate(stats *s.DependencyStats) string { stats.UsedCount++; return "changed" }
func mutateBytes(stats *s.DependencyStats) int64 { stats.UsedCount++; return 1 }
`
			findings, err := Analyze("fixture.go", []byte(source))
			if err != nil || len(findings) != 1 || findings[0].Advisory != tc.advisory {
				t.Fatalf("findings=%+v err=%v want advisory=%v", findings, err, tc.advisory)
			}
		})
	}
}

func TestReportLiteralIgnoresUncalledClosureBody(t *testing.T) {
	expression, err := parser.ParseExpr(`struct { callback func() }{callback:func() { mutate() }}`)
	if err != nil {
		t.Fatal(err)
	}
	if reportLiteralHasEffects(expression.(*ast.CompositeLit)) {
		t.Fatal("constructing a closure does not execute its body")
	}
}
