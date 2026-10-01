package reusecheck

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

func TestPromotedStatsReceivers(t *testing.T) {
	for _, tc := range []struct {
		name, declaration, parameter, receiver, alternate, want string
	}{
		{"embedded", "type Holder struct { s.DependencyStats }", "h Holder", "h", "", "violation"},
		{"pointer", "type Holder struct { *s.DependencyStats }", "h *Holder", "h", "", "violation"},
		{"nested", "type Inner struct { s.DependencyStats }; type Holder struct { *Inner }", "h Holder", "h", "", "violation"},
		{"alias", "type Stats = s.DependencyStats; type Holder struct { Stats }", "h Holder", "h", "h.Stats", "violation"},
		{"generic", "type Inner[T any] struct { s.DependencyStats }; type Holder struct { Inner[int] }", "h Holder", "h", "h.Inner.DependencyStats", "violation"},
		{"generic pointer", "type Holder[T, U any] struct { *s.DependencyStats }", "h Holder[int, string]", "h", "", "violation"},
		{"mixed explicit", "type Holder struct { s.DependencyStats }", "h Holder", "h", "h.DependencyStats", "violation"},
		{"mixed nested", "type Inner struct { s.DependencyStats }; type Holder struct { Inner }", "h Holder", "h.DependencyStats", "h.Inner.DependencyStats", "violation"},
		{"indexed", "type Holder struct { s.DependencyStats }", "h []Holder", "h[0]", "h[0].DependencyStats", "violation"},
		{"local alias", "type Holder struct { s.DependencyStats }", "original Holder", "h", "", "violation"},
		{"anonymous", "", "h struct { s.DependencyStats }", "h", "", "violation"},
		{"defined holder", "type Original struct { s.DependencyStats }; type Holder Original", "h Holder", "h", "", "violation"},
		{"methods not inherited", "type Original struct { s.DependencyStats }; func (Original) UsedCount() int { return 0 }; type Holder Original", "h Holder", "h", "", "violation"},
		{"cyclic", "type Holder struct { *Holder; s.DependencyStats }", "h Holder", "h", "", "violation"},
		{"cycle without stats", "type Holder struct { *Holder }", "h Holder", "h", "", "none"},
		{"field shadow", "type Holder struct { s.DependencyStats; UsedCount int }", "h Holder", "h", "", "advisory"},
		{"method shadow", "type Holder struct { s.DependencyStats }; func (Holder) UsedCount() int { return 0 }", "h Holder", "h", "", "advisory"},
		{"promoted method shadow", "type Other struct{}; func (Other) UsedCount() int { return 0 }; type Holder struct { s.DependencyStats; Other }", "h Holder", "h", "", "advisory"},
		{"method before visited structure", "type Base struct{}; type Later Base; func (Later) UsedCount() int { return 0 }; type Middle struct { Later }; type Stats struct { s.DependencyStats }; type Holder struct { Base; Middle; Stats }", "h Holder", "h", "", "advisory"},
		{"diamond", "type Inner struct { s.DependencyStats }; type Left struct { Inner }; type Right struct { Inner }; type Holder struct { Left; Right }", "h Holder", "h", "", "none"},
		{"ambiguous", "type Left struct { s.DependencyStats }; type Right struct { s.DependencyStats }; type Holder struct { Left; Right }", "h Holder", "h", "", "none"},
		{"different paths", "type Left struct { s.DependencyStats }; type Right struct { s.DependencyStats }; type Holder struct { Left; Right }", "h Holder", "h.Left", "h.Right", "advisory"},
		{"unknown embedding", "type Holder struct { s.DependencyStats; s.Unknown }", "h Holder", "h", "", "none"},
		{"unknown receiver field peer", "type Inner struct { stats s.DependencyStats }; type Holder struct { Inner; s.Unknown }", "h Holder", "h.stats", "", "none"},
		{"unknown deeper", "type Inner struct { s.Unknown }; type Holder struct { s.DependencyStats; Inner }", "h Holder", "h", "", "violation"},
		{"known primitive", "type Holder struct { s.DependencyStats; int }", "h Holder", "h", "", "violation"},
		{"known array", "type Other [1]int; type Holder struct { s.DependencyStats; Other }", "h Holder", "h", "", "violation"},
		{"defined stats", "type Stats s.DependencyStats; type Holder struct { Stats }", "h Holder", "h", "", "none"},
		{"dependent generic", "type Holder[T any] struct { T }", "h Holder[s.DependencyStats]", "h", "", "none"},
		{"wrong generic arity", "type Holder[T, U any] struct { s.DependencyStats }", "h Holder[int]", "h", "", "none"},
		{"unknown receiver", "", "h s.Unknown", "h", "", "none"},
		{"unstable receiver", "type Holder struct { s.DependencyStats }", "h func() Holder", "h()", "", "none"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := strings.Replace(mappingFixture, "measured s.DependencyStats", tc.parameter, 1)
			source = strings.ReplaceAll(source, "measured.", tc.receiver+".")
			if tc.alternate != "" {
				source = strings.Replace(source, tc.receiver+".UsedCount", tc.alternate+".UsedCount", 1)
			}
			if tc.name == "local alias" {
				source = strings.Replace(source, "_ = s.BuildDependencyReportFromStats", "h := original; _ = s.BuildDependencyReportFromStats", 1)
			}
			checkPromotedReportFindings(t, map[string][]byte{"fixture.go": []byte(source + "\n" + tc.declaration)}, tc.want)
		})
	}
}

func TestPromotedDiamondSearchBounded(t *testing.T) {
	const depth = 32
	source := promotedDiamondFixture(depth)
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "fixture.go", source, 0)
	if err != nil {
		t.Fatal(err)
	}
	// One valid diamond layer reaches each child twice. Its frontier must retain
	// two structural states and ambiguity, rather than expanding four paths.
	var nodes []sourceMemberNode
	for _, parent := range []string{"A", "B"} {
		for _, child := range []string{"A", "B"} {
			name := fmt.Sprintf("%s%d", child, depth-1)
			declaration := file.Scope.Lookup(name).Decl.(*ast.TypeSpec)
			nodes = append(nodes, sourceMemberNode{typ: declaration.Name, path: []string{fmt.Sprintf("%s%d", parent, depth), name}})
		}
	}
	matches, states, blocked := sourceMemberLevel(nodes, "UsedCount", imports(file), bindings(file, fset), nil)
	if blocked || len(matches) != 0 || len(states) != 2 {
		t.Fatalf("matches=%+v states=%d blocked=%v", matches, len(states), blocked)
	}
	for _, state := range states {
		if !state.multiple {
			t.Fatal("merged diamond lost its repeated selection paths")
		}
	}
	// The independent stats chain is unique. Mixing a fully explicit path with
	// promoted selections also checks its canonical identity at this depth.
	checkPromotedReportFindings(t, map[string][]byte{"fixture.go": []byte(source)}, "violation")
}

func promotedDiamondFixture(depth int) string {
	var declarations strings.Builder
	declarations.WriteString("type A0 struct{}; type B0 struct{}; type S0 struct { s.DependencyStats }\n")
	for level := 1; level <= depth; level++ {
		fmt.Fprintf(&declarations, "type A%d struct { *A%d; *B%d }; type B%d struct { *A%d; *B%d }; type S%d struct { S%d }\n", level, level-1, level-1, level, level-1, level-1, level, level-1)
	}
	fmt.Fprintf(&declarations, "type Holder struct { A%d; B%d; S%d }", depth, depth, depth)
	source := strings.Replace(mappingFixture, "measured s.DependencyStats", "h Holder", 1)
	source = strings.Replace(source, "_ = s.BuildDependencyReportFromStats(name, \"python\", measured)", "", 1)
	source = strings.ReplaceAll(source, "measured.", "h.")
	explicit := "h"
	for level := depth; level >= 0; level-- {
		explicit += fmt.Sprintf(".S%d", level)
	}
	source = strings.Replace(source, "h.UsedCount", explicit+".DependencyStats.UsedCount", 1)
	return source + "\n" + declarations.String()
}

func TestPromotedStatsAcrossFiles(t *testing.T) {
	source := strings.Replace(mappingFixture, "measured s.DependencyStats", "h Holder", 1)
	source = strings.ReplaceAll(source, "measured.", "h.")
	source = strings.Replace(source, "h.UsedCount", "h.Inner.DependencyStats.UsedCount", 1)
	provider := "package fixture; import r \"" + sharedPackage + "\"; type Inner struct { r.DependencyStats }; type Holder struct { Inner }"
	checkPromotedReportFindings(t, map[string][]byte{"types.go": []byte(provider), "reader.go": []byte(source)}, "violation")
	inner := "package fixture; import r \"" + sharedPackage + "\"; type Inner struct { r.DependencyStats }"
	checkPromotedReportFindings(t, map[string][]byte{"inner.go": []byte(inner), "holder.go": []byte("package fixture; type Holder struct { Inner }"), "reader.go": []byte(source)}, "violation")
	checkPromotedReportFindings(t, map[string][]byte{"types.go": []byte(provider), "elsewhere/reader.go": []byte(source)}, "none")
	foreign := strings.Replace(provider, sharedPackage, "example.invalid/other", 1)
	checkPromotedReportFindings(t, map[string][]byte{"types.go": []byte(foreign), "reader.go": []byte(source)}, "none")
	shadow := provider + "; func (*Holder) TotalCount() int { return 0 }"
	checkPromotedReportFindings(t, map[string][]byte{"types.go": []byte(shadow), "reader.go": []byte(source)}, "advisory")
}

func checkPromotedReportFindings(t *testing.T, sources map[string][]byte, want string) {
	t.Helper()
	findings, err := AnalyzeSources(sources)
	got := "none"
	if len(findings) == 1 {
		got = "violation"
		if findings[0].Advisory {
			got = "advisory"
		}
	}
	if err != nil || len(findings) > 1 || got != want {
		t.Fatalf("findings=%+v err=%v want=%s", findings, err, want)
	}
}
