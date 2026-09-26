package reusecheck

import (
	"crypto/sha256"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

func TestCollectionContracts(t *testing.T) {
	for _, tc := range []struct {
		name, old, replacement string
		count                  int
	}{
		{"original", "not present", "", 3},
		{"renamed", "values", "renamed", 3},
		{"unresolved alias", `"sort"`, `ordering "sort"`, 0},
		{"stable order", "sort.Strings(items)", "", 2},
		{"non nil empty", "return nil", "return []string{}", 0},
		{"mutates input", "append([]string(nil), values...)", "values", 2},
		{"trimming changed", "strings.TrimSpace(value)", "strings.ToLower(value)", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := strings.ReplaceAll(collectionContracts, tc.old, tc.replacement)
			findings, err := Analyze("internal/analysis/other.go", []byte(source))
			if err != nil || len(findings) != tc.count {
				t.Fatalf("findings=%+v error=%v", findings, err)
			}
		})
	}
}

const mappingFixture = `package fixture
import r "github.com/ben-ranford/lopper/internal/report"
import s "github.com/ben-ranford/lopper/internal/lang/shared"
func build(name string, measured s.DependencyStats) r.DependencyReport {
 _ = s.BuildDependencyReportFromStats(name, "python", measured)
 return r.DependencyReport{Name:name, Language:"python", UsedExportsCount:measured.UsedCount,
 TotalExportsCount:measured.TotalCount, UsedPercent:measured.UsedPercent,
 TopUsedSymbols:measured.TopSymbols, UsedImports:measured.UsedImports, UnusedImports:measured.UnusedImports,
 EstimatedUnusedBytes:0}
}`

func TestReportMappingContract(t *testing.T) {
	for _, tc := range []struct {
		name, old, replacement string
		count                  int
	}{
		{"decorative helper does not waive mapping", "absent", "", 1},
		{"renamed local", "measured", "stats", 1},
		{"intentional override", "UsedExportsCount:measured.UsedCount", "UsedExportsCount:1", 1},
		{"different source", "UnusedImports:measured.UnusedImports", "UnusedImports:other.UnusedImports", 1},
		{"unrelated type", "s.DependencyStats", "OtherStats", 0},
		{"unrelated report", "r.DependencyReport", "OtherReport", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			findings, err := Analyze("fixture.go", []byte(strings.ReplaceAll(mappingFixture, tc.old, tc.replacement)))
			if err != nil || len(findings) != tc.count {
				t.Fatalf("findings=%+v err=%v", findings, err)
			}
			if len(findings) > 0 && (findings[0].Helper != "shared.BuildDependencyReportFromStats" || findings[0].Line != 6) {
				t.Fatalf("diagnostic: %+v", findings)
			}
		})
	}
}

func TestValidHelperWithPostProcessing(t *testing.T) {
	source := `package fixture
import s "github.com/ben-ranford/lopper/internal/lang/shared"
import r "github.com/ben-ranford/lopper/internal/report"
func build(name string, measured s.DependencyStats) r.DependencyReport {
 result := s.BuildDependencyReportFromStats(name, "python", measured)
 result.EstimatedUnusedBytes = 42
 result.UsedExportsCount++
 return result
}`
	findings, err := Analyze("internal/analysis/fixture.go", []byte(source))
	if err != nil || len(findings) != 0 {
		t.Fatalf("findings=%+v err=%v", findings, err)
	}
}

func TestDecorativeCollectionCall(t *testing.T) {
	source := strings.Replace(collectionContracts, `import "sort"`, `import "sort"; import shared "github.com/ben-ranford/lopper/internal/lang/shared"`, 1)
	for _, call := range []string{"_ = shared.SortedKeys(values)", "shared.SortedKeys(values)"} {
		current := strings.Replace(source, "func keys(values map[string]struct{}) []string {", "func keys(values map[string]struct{}) []string { "+call, 1)
		findings, err := Analyze("internal/lang/fixture.go", []byte(current))
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, finding := range findings {
			if finding.Rule == "sorted-set-keys" {
				found = true
			}
		}
		if !found {
			t.Fatalf("decorative call waived duplicate keys: %s; findings=%+v", call, findings)
		}
	}
}

func TestParseFailure(t *testing.T) {
	if _, err := Analyze("broken.go", []byte("package broken; func")); err == nil {
		t.Fatal("accepted unparseable input")
	}
}

func TestAliasedImportsAndShadowedBuiltins(t *testing.T) {
	source := strings.ReplaceAll(collectionContracts, `"sort"`, `ordering "sort"`)
	source = strings.ReplaceAll(source, "sort.Strings", "ordering.Strings")
	findings, err := Analyze("internal/analysis/copy.go", []byte(source))
	if err != nil || len(findings) != 3 {
		t.Fatalf("alias findings=%+v err=%v", findings, err)
	}
	source += "\nfunc len(any) int { return 1 }\n"
	findings, err = Analyze("internal/analysis/copy.go", []byte(source))
	if err != nil || len(findings) != 0 {
		t.Fatalf("shadowed builtin findings=%+v err=%v", findings, err)
	}
}

func TestStatsFactoryMappingAndOwners(t *testing.T) {
	source := strings.Replace(mappingFixture, "measured s.DependencyStats", "other string", 1)
	source = strings.Replace(source, "_ = s.BuildDependencyReportFromStats", "measured := s.BuildDependencyStats(name, nil, nil)\n _ = s.BuildDependencyReportFromStats", 1)
	findings, err := Analyze("internal/lang/python/new.go", []byte(source))
	if err != nil || len(findings) != 1 {
		t.Fatalf("factory findings=%+v err=%v", findings, err)
	}
	source = strings.Replace(mappingFixture, "func build(", "func BuildDependencyReportFromStats(", 1)
	findings, err = Analyze("internal/lang/shared/dependency_usage_stats.go", []byte(source))
	if err != nil || len(findings) != 0 {
		t.Fatalf("owner findings=%+v err=%v", findings, err)
	}
}

func TestUncertainReportProvenanceIsNotBlocking(t *testing.T) {
	for _, source := range []string{
		strings.Replace(mappingFixture, "Name:name,", "", 1),
		strings.Replace(mappingFixture, "Name:name", `"Name":name`, 1),
		strings.Replace(mappingFixture, "Name:name", "name", 1),
		strings.Replace(strings.Replace(mappingFixture, "measured s.DependencyStats", "measured, other s.DependencyStats", 1), "UnusedImports:measured.UnusedImports", "UnusedImports:other.UnusedImports", 1),
	} {
		findings, err := Analyze("fixture.go", []byte(source))
		if err != nil || (len(findings) != 0 && !findings[0].Advisory) {
			t.Fatalf("uncertain findings=%+v err=%v", findings, err)
		}
	}
}

func TestStatsDeclarationForms(t *testing.T) {
	for _, tc := range []struct {
		declaration string
		want        int
	}{
		{"var measured s.DependencyStats", 1},
		{"var measured = s.BuildDependencyStats(name,nil,nil)", 1},
		{"var measured = unknown", 0},
		{"var measured = s.BuildDependencyStats(name,nil,nil), 1", 0},
		{"var measured, other = s.BuildDependencyStats(name,nil,nil), 1; _ = other", 0},
		{"var measured = s.OtherFactory(name,nil,nil)", 0},
		{"measured, other := s.BuildDependencyStats(name,nil,nil), 1; _ = other", 0},
		{"measured := unknown", 0},
	} {
		source := strings.Replace(mappingFixture, "measured s.DependencyStats", "unused string", 1)
		source = strings.Replace(source, "_ = s.BuildDependencyReportFromStats", tc.declaration+"\n _ = s.BuildDependencyReportFromStats", 1)
		findings, err := Analyze("fixture.go", []byte(source))
		if err != nil || len(findings) != tc.want {
			t.Fatalf("%s findings=%+v err=%v", tc.declaration, findings, err)
		}
	}
	if dependencyStats(nil, nil) {
		t.Fatal("missing declaration considered proven")
	}
}

func TestDecorativeCallBoundariesAndDomain(t *testing.T) {
	source := strings.Replace(collectionContracts, `import "sort"`, `import "sort"; import shared "github.com/ben-ranford/lopper/internal/lang/shared"`, 1)
	for _, statement := range []string{"_, _ = 1, 2", "_ = 1", "shared.SortedKeys(make(map[string]struct{}))"} {
		current := strings.Replace(source, "func keys(values map[string]struct{}) []string {", "func keys(values map[string]struct{}) []string { "+statement, 1)
		findings, err := Analyze("internal/lang/fixture.go", []byte(current))
		for _, finding := range findings {
			if finding.Function == "keys" {
				t.Fatalf("uncertain additional operation matched: %s", statement)
			}
		}
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestDiagnosticAndLegacyScope(t *testing.T) {
	finding := Finding{Path: "internal/lang/python/reporting.go", Function: "buildDependencyReport", Rule: "dependency-report-mapping", Helper: "shared.BuildDependencyReportFromStats", Line: 5}
	source := []byte("reviewed source")
	old := legacyDigests[finding.Path]
	legacyDigests[finding.Path] = fmt.Sprintf("%x", sha256.Sum256(source))
	t.Cleanup(func() { legacyDigests[finding.Path] = old })
	if LegacyAdvisory(finding, []byte("edited source")) {
		t.Fatal("edited legacy source waived")
	}
	if !LegacyAdvisory(finding, source) || !strings.Contains(finding.String(), "violation") {
		t.Fatalf("legacy diagnostic: %s", finding.String())
	}
	finding.Advisory = true
	if !strings.Contains(finding.String(), "advisory") {
		t.Fatal(finding.String())
	}
	finding.Function = "newCopy"
	if LegacyAdvisory(finding, source) {
		t.Fatal("new function inherited legacy waiver")
	}
}

func TestShadowedFactoryDoesNotProveStatsType(t *testing.T) {
	source := strings.Replace(mappingFixture, "measured s.DependencyStats", "s OtherFactory", 1)
	source = strings.Replace(source, "_ = s.BuildDependencyReportFromStats", "measured := s.BuildDependencyStats(name,nil,nil)\n _ = s.BuildDependencyReportFromStats", 1)
	findings, err := Analyze("fixture.go", []byte(source))
	if err != nil || len(findings) != 0 {
		t.Fatalf("shadowed factory findings=%+v err=%v", findings, err)
	}
}

func TestDeliberateFieldOverrideIsAdvisory(t *testing.T) {
	source := strings.Replace(mappingFixture, "UsedExportsCount:measured.UsedCount", "UsedExportsCount:42", 1)
	findings, err := Analyze("fixture.go", []byte(source))
	if err != nil || len(findings) != 1 || !findings[0].Advisory {
		t.Fatalf("findings=%+v err=%v", findings, err)
	}
}

func TestConservativeSyntaxBoundaries(t *testing.T) {
	for _, source := range []string{
		"package fixture; func unrelated() {}",
		strings.Replace(mappingFixture, "measured.UsedCount", "(measured).UsedCount", 1),
		strings.Replace(mappingFixture, "UsedExportsCount:measured.UsedCount,", "", 1),
	} {
		findings, err := Analyze("fixture.go", []byte(source))
		if err != nil {
			t.Fatal(err)
		}
		for _, finding := range findings {
			if !finding.Advisory {
				t.Fatalf("uncertain syntax blocked: %+v", finding)
			}
		}
	}
	malformed := &ast.File{Imports: []*ast.ImportSpec{{Path: &ast.BasicLit{Kind: token.STRING, Value: "not quoted"}}}}
	if len(imports(malformed)) != 0 {
		t.Fatal("invalid import was trusted")
	}
}

func TestInvalidRuleTemplateFailsClosed(t *testing.T) {
	if _, err := contractFingerprints("package contracts; func"); err == nil {
		t.Fatal("invalid embedded rule was accepted")
	}
}

func TestSamePackageDecorativeHelperCalls(t *testing.T) {
	for _, tc := range []struct {
		function, signature, helper, path string
	}{
		{"exact", "func exact(values []string) []string {", "uniqueSorted", "internal/analysis/copy.go"},
		{"trimmed", "func trimmed(values []string) []string {", "SortedUniqueTrimmedStrings", "internal/report/copy.go"},
		{"keys", "func keys(values map[string]struct{}) []string {", "SortedKeys", "internal/lang/shared/copy.go"},
		{"union", "func union(values ...map[string]struct{}) []string {", "SortedDependencyUnion", "internal/lang/shared/copy.go"},
	} {
		t.Run(tc.helper, func(t *testing.T) {
			for _, prefix := range []string{"", "_ = "} {
				arguments := "values"
				if tc.function == "union" {
					arguments += "..."
				}
				call := prefix + tc.helper + "(" + arguments + ")"
				source := strings.Replace(collectionContracts, tc.signature, tc.signature+"\n"+call, 1)
				for _, path := range []string{tc.path, strings.Replace(tc.path, "/copy.go", "/nested/copy.go", 1)} {
					assertFunctionFinding(t, path, source, tc.function, path == tc.path)
				}
			}
		})
	}
}

func assertFunctionFinding(t *testing.T, path, source, function string, want bool) {
	t.Helper()
	findings, err := Analyze(path, []byte(source))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, finding := range findings {
		found = found || finding.Function == function
	}
	if found != want {
		t.Fatalf("path=%s function=%s findings=%+v", path, function, findings)
	}
}

func TestSamePackageHelperRejectsShadowedNames(t *testing.T) {
	for _, tc := range []struct {
		declaration string
		want        bool
	}{
		{"var SortedKeys func(any)", false},
		{"const SortedKeys = 1", false},
		{"func SortedKeys(any) {}", true},
	} {
		file, err := parser.ParseFile(token.NewFileSet(), "fixture.go", "package fixture;"+tc.declaration+";func copy(){SortedKeys(values)}", 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			if call, ok := node.(*ast.CallExpr); ok {
				if got := samePackageHelper("internal/lang/shared/copy.go", call.Fun); got != tc.want {
					t.Fatalf("declaration=%s owned=%v, want %v", tc.declaration, got, tc.want)
				}
			}
			return true
		})
	}
}
