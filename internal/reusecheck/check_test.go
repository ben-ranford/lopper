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

func TestElidedReportLiteralTypes(t *testing.T) {
	literal := strings.Split(strings.Split(mappingFixture, "return r.DependencyReport")[1], "\n}")[0]
	for _, container := range []string{
		"[]r.DependencyReport{%s}", "[1]r.DependencyReport{0:%s}",
		"[]*r.DependencyReport{%s}", "[][]r.DependencyReport{{%s}}",
		"map[string]r.DependencyReport{\"key\":%s}", "map[*r.DependencyReport]bool{%s:true}",
	} {
		source := strings.Replace(mappingFixture, "return r.DependencyReport"+literal, "_ = "+fmt.Sprintf(container, literal)+"; return r.DependencyReport{}", 1)
		findings, err := Analyze("fixture.go", []byte(source))
		if err != nil || len(findings) != 1 || findings[0].Advisory {
			t.Fatalf("%s: findings=%+v err=%v", container, findings, err)
		}
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
	for _, call := range []string{"_ = shared.SortedKeys(values)", "shared.SortedKeys(values)", "{ _ = shared.SortedKeys(values) }", "{ { shared.SortedKeys(values) } }", "_ = (shared.SortedKeys(values))", "_ = ((shared.SortedKeys)((values)))"} {
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
	for want, declarations := range map[int][]string{
		1: {
			"var measured s.DependencyStats",
			"var measured (*s.DependencyStats)",
			"var measured = s.BuildDependencyStats(name,nil,nil)",
			"var measured = s.DependencyStats{}",
			"measured := s.DependencyStats{}",
			"var measured = &s.DependencyStats{}",
			"measured := &s.DependencyStats{}",
			"measured := &(s.DependencyStats{})",
			"var measured = &((s.DependencyStats{}))",
			"var measured = new(s.DependencyStats)",
			"measured := new(s.DependencyStats)",
			"measured := new((s.DependencyStats))",
			"measured := (new)(s.DependencyStats)",
			"measured := *new(s.DependencyStats)",
			"measured := s.DependencyStats(localStats)",
			"measured := raw.(s.DependencyStats)",
			"measured, extra := s.DependencyStats{}, 0; _ = extra",
			"extra, measured := 0, &s.DependencyStats{}; _ = extra",
			"var extra, measured = 0, s.DependencyStats{}; _ = extra",
			"measured, ok := raw.(s.DependencyStats); _ = ok",
			"var measured, ok = raw.(*s.DependencyStats); _ = ok",
			"var measured = raw.(*s.DependencyStats)",
			"measured := *(raw.(*s.DependencyStats))",
			"type Stats = s.DependencyStats; measured := raw.(Stats)",
			"var measured = (s.DependencyStats)(localStats)",
			"measured := (*s.DependencyStats)(localStats)",
			"measured := *((*s.DependencyStats)(localStats))",
			"var measured = *((new)(s.DependencyStats))",
			"measured := *(&s.DependencyStats{})",
			"var measured, other = s.BuildDependencyStats(name,nil,nil), 1; _ = other",
			"measured, other := s.BuildDependencyStats(name,nil,nil), 1; _ = other",
		},
		0: {
			"measured, extra := 0, s.DependencyStats{}; _ = extra",
			"var measured, extra = 0, s.DependencyStats{}; _ = extra",
			"value, measured := raw.(s.DependencyStats); _ = value",
			"measured, extra := unknown(); _ = extra",
			"measured := raw.(s.OtherStats)",
			"measured := raw.(**s.DependencyStats)",
			"measured := *(raw.(s.DependencyStats))",
			"measured := s.OtherStats(localStats)",
			"measured := *s.OtherFactory()",
			"var measured = unknown",
			"measured := &unknown",
			"measured := new(s.OtherStats)",
			"var measured = s.BuildDependencyStats(name,nil,nil), 1",
			"var measured = s.OtherFactory(name,nil,nil)",
			"measured := unknown",
		},
	} {
		for _, declaration := range declarations {
			assertStatsDeclaration(t, declaration, want)
		}
	}
	if dependencyStats(nil, nil, nil) {
		t.Fatal("missing declaration considered proven")
	}
}

func assertStatsDeclaration(t *testing.T, declaration string, want int) {
	t.Helper()
	source := strings.Replace(mappingFixture, "measured s.DependencyStats", "unused string", 1)
	source = strings.Replace(source, "_ = s.BuildDependencyReportFromStats", declaration+"\n _ = s.BuildDependencyReportFromStats", 1)
	findings, err := Analyze("fixture.go", []byte(source))
	if err != nil || len(findings) != want {
		t.Fatalf("%s findings=%+v err=%v", declaration, findings, err)
	}
}

func TestParenthesizedReportFieldReceivers(t *testing.T) {
	for _, receiver := range []string{"(measured)", "((measured))"} {
		source := strings.ReplaceAll(mappingFixture, "measured.", receiver+".")
		findings, err := Analyze("fixture.go", []byte(source))
		if err != nil || len(findings) != 1 || findings[0].Advisory {
			t.Fatalf("receiver %s: findings=%+v err=%v", receiver, findings, err)
		}
	}
}

func TestExplicitPointerReportFieldReceivers(t *testing.T) {
	source := strings.Replace(mappingFixture, "measured s.DependencyStats", "measured *s.DependencyStats", 1)
	for _, receiver := range []string{"(*measured)", "(*((measured)))"} {
		current := strings.ReplaceAll(source, "measured.", receiver+".")
		findings, err := Analyze("fixture.go", []byte(current))
		if err != nil || len(findings) != 1 || findings[0].Advisory {
			t.Fatalf("receiver %s: findings=%+v err=%v", receiver, findings, err)
		}
	}
}

func TestStatsLocalAliasProvenance(t *testing.T) {
	for _, tc := range []struct {
		declarations string
		want         int
	}{
		{"measured := original", 1},
		{"var measured = (original)", 1},
		{"first := original; measured := first", 1},
		{"pointer := new(s.DependencyStats); measured := *pointer", 1},
		{"value := s.DependencyStats{}; measured := &value", 1},
		{"pointer := &(original); measured := *((pointer))", 1},
		{"var measured = *(&measured)", 0},
		{"var other OtherStats; measured := &other", 0},
		{"var first *s.DependencyStats; measured := first", 1},
		{"var first OtherStats; measured := first", 0},
		{"var measured = measured", 0},
		{"measured := unknown", 0},
		{"measured, other := original, original; _ = other", 1},
	} {
		source := strings.Replace(mappingFixture, "measured s.DependencyStats", "original s.DependencyStats", 1)
		source = strings.Replace(source, "_ = s.BuildDependencyReportFromStats", tc.declarations+"; _ = s.BuildDependencyReportFromStats", 1)
		findings, err := Analyze("fixture.go", []byte(source))
		if err != nil || len(findings) != tc.want {
			t.Fatalf("%s: findings=%+v err=%v", tc.declarations, findings, err)
		}
	}
}

func TestStatsTypeAliasProvenance(t *testing.T) {
	for _, tc := range []struct {
		declaration, typ string
		want             int
	}{
		{"type localStats = s.DependencyStats", "localStats", 1},
		{"type localStats = s.DependencyStats", "*localStats", 1},
		{"type localStats = *s.DependencyStats", "localStats", 1},
		{"type first = s.DependencyStats; type localStats = first", "localStats", 1},
		{"type localStats s.DependencyStats", "localStats", 0},
		{"type localStats = localStats", "localStats", 0},
		{"type first = localStats; type localStats = first", "localStats", 0},
	} {
		source := strings.Replace(mappingFixture, "func build", tc.declaration+"; func build", 1)
		source = strings.Replace(source, "measured s.DependencyStats", "measured "+tc.typ, 1)
		findings, err := Analyze("fixture.go", []byte(source))
		if err != nil || len(findings) != tc.want {
			t.Fatalf("%s: findings=%+v err=%v", tc.declaration, findings, err)
		}
	}
	for _, initializer := range []string{"localStats{}", "new(localStats)", "localStats(original)"} {
		source := strings.Replace(mappingFixture, "measured s.DependencyStats", "original s.DependencyStats", 1)
		source = strings.Replace(source, "_ = s.BuildDependencyReportFromStats", "type localStats = s.DependencyStats; measured := "+initializer+"; _ = s.BuildDependencyReportFromStats", 1)
		findings, err := Analyze("fixture.go", []byte(source))
		if err != nil || len(findings) != 1 {
			t.Fatalf("%s: findings=%+v err=%v", initializer, findings, err)
		}
	}
}

func TestShadowedNewIsNotStatsProvenance(t *testing.T) {
	source := strings.Replace(mappingFixture, "measured s.DependencyStats", "unused string", 1)
	source = strings.Replace(source, "_ = s.BuildDependencyReportFromStats", "new := func(s.DependencyStats) *s.DependencyStats { return nil }\n measured := new(s.DependencyStats)\n _ = s.BuildDependencyReportFromStats", 1)
	findings, err := Analyze("fixture.go", []byte(source))
	if err != nil || len(findings) != 0 {
		t.Fatalf("shadowed new findings=%+v err=%v", findings, err)
	}
}

func TestPointerStatsParameterMapsReport(t *testing.T) {
	source := strings.Replace(mappingFixture, "measured s.DependencyStats", "measured *s.DependencyStats", 1)
	findings, err := Analyze("fixture.go", []byte(source))
	if err != nil || len(findings) != 1 {
		t.Fatalf("pointer stats parameter findings=%+v err=%v", findings, err)
	}
}

func TestDecorativeCallBoundariesAndDomain(t *testing.T) {
	source := strings.Replace(collectionContracts, `import "sort"`, `import "sort"; import shared "github.com/ben-ranford/lopper/internal/lang/shared"`, 1)
	for _, statement := range []string{"_, _ = 1, 2", "_ = 1", "shared.SortedKeys(make(map[string]struct{}))", "{}", "{ _ = shared.SortedKeys(values); _ = 1 }"} {
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
	finding := Finding{Path: "internal/lang/jvm/reporting.go", Function: "buildDependencyReport", Rule: "dependency-report-mapping", Helper: "shared.BuildDependencyReportFromStats", Line: 5}
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
			assertDecorativeHelperCalls(t, tc.function, tc.signature, tc.helper, tc.path)
		})
	}
}

func assertDecorativeHelperCalls(t *testing.T, function, signature, helper, filePath string) {
	t.Helper()
	for _, prefix := range []string{"", "_ = "} {
		call := prefix + helper + "(" + decorativeHelperArguments(function) + ")"
		source := strings.Replace(collectionContracts, signature, signature+"\n"+call, 1)
		assertDecorativeHelperPaths(t, filePath, source, function)
	}
}

func decorativeHelperArguments(function string) string {
	if function == "union" {
		return "values..."
	}
	return "values"
}

func assertDecorativeHelperPaths(t *testing.T, filePath, source, function string) {
	t.Helper()
	paths := []string{filePath, strings.Replace(filePath, "/copy.go", "/nested/copy.go", 1)}
	for _, path := range paths {
		assertFunctionFinding(t, path, source, function, path == filePath)
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

func TestRangeStatsMapping(t *testing.T) {
	for _, tc := range []struct {
		parameter, setup, loop string
		want                   int
	}{
		{"values []s.DependencyStats", "", "for _, measured := range values {", 1},
		{"original []s.DependencyStats", "values := original;", "for _, measured := range values {", 1},
		{"original map[string]s.DependencyStats", "var first = original; values := (first);", "for _, measured := range values {", 1},
		{"original *[2]s.DependencyStats", "values := original;", "for _, measured := range values {", 1},
		{"original [2]s.DependencyStats", "values := &original;", "for _, measured := range values {", 1},
		{"unused string", "original := [2]s.DependencyStats{}; values := &(original);", "for _, measured := range values {", 1},
		{"original [2]s.DependencyStats", "", "for _, measured := range &original {", 1},
		{"original *[2]s.DependencyStats", "values := *original;", "for _, measured := range values {", 1},
		{"original [2]s.DependencyStats", "first := &original; values := *first;", "for _, measured := range values {", 1},
		{"original []s.DependencyStats", "values := &original;", "for _, measured := range values {", 0},
		{"original []s.DependencyStats", "values := *original;", "for _, measured := range values {", 0},
		{"original []s.DependencyStats", "values := -original;", "for _, measured := range values {", 0},
		{"original *[2]s.DependencyStats", "values := &original;", "for _, measured := range values {", 0},
		{"unused string", "var values = &values;", "for _, measured := range values {", 0},
		{"unused string", "values := &unknown;", "for _, measured := range values {", 0},
		{"unused string", "var values = values;", "for _, measured := range values {", 0},
		{"original []OtherStats", "values := original;", "for _, measured := range values {", 0},
		{"values []*s.DependencyStats", "", "for _, measured := range values {", 1},
		{"values [2]s.DependencyStats", "", "for _, measured := range values {", 1},
		{"values *[2]s.DependencyStats", "", "for _, measured := range values {", 1},
		{"values *([2]*s.DependencyStats)", "", "for _, measured := range values {", 1},
		{"values *[2]s.DependencyStats", "", "for measured := range values {", 0},
		{"values *[]s.DependencyStats", "", "for _, measured := range values {", 0},
		{"values *map[string]s.DependencyStats", "", "for _, measured := range values {", 0},
		{"values map[string]s.DependencyStats", "", "for _, measured := range values {", 1},
		{"values map[*s.DependencyStats]bool", "", "for measured := range values {", 1},
		{"values map[*s.DependencyStats]bool", "", "for measured, present := range values { _ = present;", 1},
		{"values map[*s.DependencyStats]bool", "", "for measured, _ := range values {", 1},
		{"values map[string]s.DependencyStats", "", "for measured := range values {", 0},
		{"unused string", "", "for measured := range make(map[*s.DependencyStats]bool) {", 1},
		{"unused string", "var values []s.DependencyStats;", "for _, measured := range values {", 1},
		{"unused string", "var values = []s.DependencyStats{};", "for _, measured := range values {", 1},
		{"unused string", "values := []s.DependencyStats{};", "for _, measured := range values {", 1},
		{"unused string", "values := make([]s.DependencyStats, 2);", "for _, measured := range values {", 1},
		{"unused string", "values := new([2]s.DependencyStats);", "for _, measured := range values {", 1},
		{"unused string", "", "for _, measured := range &[2]s.DependencyStats{} {", 1},
		{"unused string", "values := &([2]s.DependencyStats{});", "for _, measured := range values {", 1},
		{"unused string", "var values = &([2]*s.DependencyStats{});", "for _, measured := range values {", 1},
		{"unused string", "", "for _, measured := range &[]s.DependencyStats{} {", 0},
		{"unused string", "", "for _, measured := range &unknown {", 0},
		{"unused string", "", "for _, measured := range []s.DependencyStats(raw) {", 1},
		{"unused string", "values := [2]s.DependencyStats(raw);", "for _, measured := range values {", 1},
		{"unused string", "values := (map[string]s.DependencyStats)(raw);", "for _, measured := range values {", 1},
		{"unused string", "", "for measured := range (chan s.DependencyStats)(raw) {", 1},
		{"unused string", "", "for _, measured := range (*[2]s.DependencyStats)(raw) {", 1},
		{"unused string", "", "for _, measured := range []OtherStats(raw) {", 0},
		{"unused string", "var values = new([2]*s.DependencyStats);", "for _, measured := range values {", 1},
		{"unused string", "", "for _, measured := range (new)([2]s.DependencyStats) {", 1},
		{"unused string", "values := new([]s.DependencyStats);", "for _, measured := range values {", 0},
		{"unused string", "", "for _, measured := range new([2]s.DependencyStats, 2) {", 0},
		{"unused string", "new := func([2]s.DependencyStats) []OtherStats { return nil }; values := new([2]s.DependencyStats{});", "for _, measured := range values {", 0},
		{"unused string", "var values = make(map[string]s.DependencyStats);", "for _, measured := range values {", 1},
		{"unused string", "", "for _, measured := range make([]s.DependencyStats, 2) {", 1},
		{"unused string", "values := make(chan s.DependencyStats);", "for measured := range values {", 1},
		{"unused string", "make := func([]s.DependencyStats, int) []OtherStats { return nil }; values := make([]s.DependencyStats{}, 2);", "for _, measured := range values {", 0},

		{"unused string", "", "for _, measured := range ([]s.DependencyStats{}) {", 1},
		{"values []OtherStats", "", "for _, measured := range values {", 0},
		{"values []s.DependencyStats", "", "for measured := range values {", 0},
		{"unused string", "", "for _, measured := range unknown() {", 0},
		{"values string", "", "for _, measured := range values {", 0},
		{"unused string", "var values = unknown();", "for _, measured := range values {", 0},
		{"unused string", "var values, other = unknown(); _ = other;", "for _, measured := range values {", 0},
		{"unused string", "values, other := unknown(); _ = other;", "for _, measured := range values {", 0},
	} {
		source := strings.Replace(mappingFixture, "measured s.DependencyStats", tc.parameter, 1)
		source = strings.Replace(source, "_ = s.BuildDependencyReportFromStats", tc.setup+tc.loop+"\n _ = s.BuildDependencyReportFromStats", 1)
		source += "\n return r.DependencyReport{} }"
		findings, err := Analyze("fixture.go", []byte(source))
		if err != nil || len(findings) != tc.want {
			t.Fatalf("%s: findings=%+v err=%v", tc.parameter+tc.setup+tc.loop, findings, err)
		}
	}
}

func TestDecorativeCallWithinLoop(t *testing.T) {
	source := strings.Replace(collectionContracts, `import "sort"`, `import "sort"; import shared "github.com/ben-ranford/lopper/internal/lang/shared"`, 1)
	source = strings.Replace(source, "for value := range values {", "for value := range values { { _ = shared.SortedKeys(values) };", 1)
	findings, err := Analyze("internal/lang/fixture.go", []byte(source))
	if err != nil {
		t.Fatal(err)
	}
	for _, finding := range findings {
		if finding.Rule == "sorted-set-keys" {
			return
		}
	}
	t.Fatal("nested decorative call waived exact keys contract")
}

func TestConditionalDecorativeCalls(t *testing.T) {
	source := strings.Replace(collectionContracts, `import "sort"`, `import "sort"; import shared "github.com/ben-ranford/lopper/internal/lang/shared"`, 1)
	for _, tc := range []struct {
		statement string
		want      bool
	}{
		{"if enabled { _ = shared.SortedKeys(values) }", true},
		{"if 1 == 1 { _ = shared.SortedKeys(values) }", true},
		{"if (1 + 2) >= -3 { _ = shared.SortedKeys(values) }", true},
		{"if \"a\" != \"b\" { _ = shared.SortedKeys(values) }", true},
		{"if probe() == 1 { _ = shared.SortedKeys(values) }", false},
		{"if values[0] == 1 { _ = shared.SortedKeys(values) }", false},
		{"if *enabled == 1 { _ = shared.SortedKeys(values) }", false},
		{"if !enabled { _ = shared.SortedKeys(values) }", true},
		{"if enabled && (other || enabled) { _ = shared.SortedKeys(values) }", true},
		{"if enabled { _ = shared.SortedKeys(values) } else {}", true},
		{"if enabled { _ = shared.SortedKeys(values) } else if other { shared.SortedKeys(values) }", true},
		{"if enabled { if other { shared.SortedKeys(values) } }", true},
		{"if enabled() { _ = shared.SortedKeys(values) }", false},
		{"if enabled := probe(); enabled { _ = shared.SortedKeys(values) }", false},
		{"if enabled { _ = shared.SortedKeys(values); probe() }", false},
		{"if enabled { _ = shared.SortedKeys(values) } else { probe() }", false},
		{"if enabled[0] { _ = shared.SortedKeys(values) }", false},
	} {
		current := strings.Replace(source, "func keys(values map[string]struct{}) []string {", "func keys(values map[string]struct{}) []string { "+tc.statement+";", 1)
		findings, err := Analyze("internal/lang/fixture.go", []byte(current))
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, finding := range findings {
			found = found || finding.Rule == "sorted-set-keys"
		}
		if found != tc.want {
			t.Errorf("%s: match %v, want %v", tc.statement, found, tc.want)
		}
	}
}

func TestCollectionFingerprintParentheses(t *testing.T) {
	for _, replacement := range [][2]string{
		{"sort.Strings(items)", "sort.Strings((items))"},
		{"if len(values) == 0", "if (len((values)) == (0))"},
		{"range values", "range (values)"},
		{"return items", "return ((items))"},
		{"items = append(items, value)", "items = append((items), (value))"},
	} {
		source := strings.ReplaceAll(collectionContracts, replacement[0], replacement[1])
		findings, err := Analyze("internal/lang/fixture.go", []byte(source))
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, finding := range findings {
			found = found || finding.Rule == "sorted-set-keys"
		}
		if !found {
			t.Errorf("parentheses waived keys contract: %s", replacement[1])
		}
	}
}

func TestCanonicalParenthesesPreservePrecedenceAndAST(t *testing.T) {
	fingerprints := make([]string, 0, 2)
	for _, expression := range []string{"(a+b)*c", "a+b*c"} {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, "fixture.go", "package p;func f(a,b,c int) int { return "+expression+" }", 0)
		if err != nil {
			t.Fatal(err)
		}
		fn := file.Decls[0].(*ast.FuncDecl)
		original := fn.Body.List[0].(*ast.ReturnStmt).Results[0].(*ast.BinaryExpr).X
		fingerprints = append(fingerprints, canonicalFunction(fn, nil, bindings(file, fset)))
		if fn.Body.List[0].(*ast.ReturnStmt).Results[0].(*ast.BinaryExpr).X != original {
			t.Fatal("canonicalization changed the source AST")
		}
	}
	if fingerprints[0] == fingerprints[1] {
		t.Fatal("canonicalization changed operator precedence")
	}
}

func TestElidedLiteralTypeInferencePreservesAST(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "fixture.go", "package p; var reports = [][]Report{{{Name: name}}}", 0)
	if err != nil {
		t.Fatal(err)
	}
	inferred := compositeLiteralTypes(file)
	elided := 0
	for literal, typ := range inferred {
		if literal.Type == nil {
			elided++
			if typ == nil {
				t.Fatal("nested literal lost inherited type")
			}
		}
	}
	if elided != 2 {
		t.Fatalf("inference mutated elided source types: count=%d", elided)
	}
	unknown := &ast.CompositeLit{}
	for _, root := range []ast.Node{unknown, &ast.KeyValueExpr{Key: ast.NewIdent("key"), Value: unknown}, &ast.ReturnStmt{Results: []ast.Expr{unknown}}, &ast.CompositeLit{Type: ast.NewIdent("Unknown"), Elts: []ast.Expr{unknown}}} {
		if compositeLiteralTypes(root)[unknown] != nil {
			t.Fatal("unknown enclosing type was inferred")
		}
	}
}

func TestAliasedCollectionSignatures(t *testing.T) {
	for _, tc := range []struct {
		declaration, signature string
		want                   bool
	}{
		{"type Strings = []string", "Strings", true},
		{"type Element = string; type Strings = []Element", "Strings", true},
		{"type Strings = []string; type Chain = Strings", "Chain", true},
		{"type Strings []string", "Strings", false},
		{"type Strings = []Strings", "Strings", false},
		{"type Strings = Strings", "Strings", false},
	} {
		source := strings.Replace(collectionContracts, "func exact(values []string) []string", tc.declaration+"; func exact(values "+tc.signature+") "+tc.signature, 1)
		assertFunctionFinding(t, "internal/analysis/copy.go", source, "exact", tc.want)
	}
}

func TestAliasedRangeCollections(t *testing.T) {
	for _, tc := range []struct {
		declaration, typ, loop string
		want                   int
	}{
		{"type Stats = []s.DependencyStats", "Stats", "for _, measured := range values {", 1},
		{"type Stats = [2]s.DependencyStats", "*Stats", "for _, measured := range values {", 1},
		{"type Stats = map[s.DependencyStats]bool", "Stats", "for measured := range values {", 1},
		{"type Stats = chan s.DependencyStats", "Stats", "for measured := range values {", 1},
		{"type Stats []s.DependencyStats", "Stats", "for _, measured := range values {", 0},
		{"type Stats = Stats", "Stats", "for _, measured := range values {", 0},
	} {
		source := strings.Replace(mappingFixture, "func build", tc.declaration+"; func build", 1)
		source = strings.Replace(source, "measured s.DependencyStats", "values "+tc.typ, 1)
		source = strings.Replace(source, "_ = s.BuildDependencyReportFromStats", tc.loop+" _ = s.BuildDependencyReportFromStats", 1) + "; return r.DependencyReport{} }"
		findings, err := Analyze("fixture.go", []byte(source))
		if err != nil || len(findings) != tc.want {
			t.Fatalf("%s: findings=%+v err=%v", tc.declaration, findings, err)
		}
	}
}

func TestAliasedElidedReports(t *testing.T) {
	literal := strings.Split(strings.Split(mappingFixture, "return r.DependencyReport")[1], "\n}")[0]
	for _, tc := range []struct {
		declaration, container string
		want                   int
	}{
		{"type Reports = []r.DependencyReport", "Reports{%s}", 1},
		{"type Reports = map[string]r.DependencyReport", "Reports{\"key\":%s}", 1},
		{"type Report = *r.DependencyReport; type Reports = []Report", "Reports{%s}", 1},
		{"type Reports []r.DependencyReport", "Reports{%s}", 0},
		{"type Reports = Reports", "Reports{%s}", 0},
	} {
		source := strings.Replace(mappingFixture, "func build", tc.declaration+"; func build", 1)
		source = strings.Replace(source, "return r.DependencyReport"+literal, "_ = "+fmt.Sprintf(tc.container, literal)+"; return r.DependencyReport{}", 1)
		findings, err := Analyze("fixture.go", []byte(source))
		if err != nil || len(findings) != tc.want {
			t.Fatalf("%s: findings=%+v err=%v", tc.declaration, findings, err)
		}
	}
}

func TestSignatureAliasExpansionRestoresFields(t *testing.T) {
	for _, typ := range []string{"[]string", "map[string][]string", "*[]string", "chan []string", "...[]string"} {
		declaration := "type Values = " + typ
		signature := "Values"
		if strings.HasPrefix(typ, "...") {
			declaration = "type Values = []string"
			signature = "...Values"
		}
		source := "package p; " + declaration + "; func f(value " + signature + ") {}"
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, "fixture.go", source, 0)
		if err != nil {
			t.Fatal(err)
		}
		fn := file.Decls[1].(*ast.FuncDecl)
		original := fn.Type.Params.List[0].Type
		fingerprint := canonicalFunction(fn, imports(file), bindings(file, fset))
		if fn.Type.Params.List[0].Type != original {
			t.Fatal("signature type was not restored")
		}
		directSet := token.NewFileSet()
		direct, err := parser.ParseFile(directSet, "direct.go", "package p; func f(value "+typ+") {}", 0)
		if err != nil {
			t.Fatal(err)
		}
		if fingerprint != canonicalFunction(direct.Decls[0].(*ast.FuncDecl), imports(direct), bindings(direct, directSet)) {
			t.Fatalf("alias fingerprint differs for %s", typ)
		}
	}
}

func TestMapLookupStatsProvenance(t *testing.T) {
	for _, tc := range []struct {
		typ, declaration string
		want             int
	}{
		{"map[string]s.DependencyStats", "measured, ok := values[name]; _ = ok", 1},
		{"map[string]*s.DependencyStats", "var measured, ok = values[name]; _ = ok", 1},
		{"map[string]s.DependencyStats", "measured := values[name]", 1},
		{"map[string]s.DependencyStats", "copied := values; measured, ok := copied[name]; _ = ok", 1},
		{"map[string]s.DependencyStats", "value, measured := values[name]; _ = value", 0},
		{"map[string]OtherStats", "measured, ok := values[name]; _ = ok", 0},
		{"[]s.DependencyStats", "measured, ok := values[0]; _ = ok", 0},
	} {
		source := strings.Replace(mappingFixture, "measured s.DependencyStats", "values "+tc.typ, 1)
		source = strings.Replace(source, "_ = s.BuildDependencyReportFromStats", tc.declaration+"; _ = s.BuildDependencyReportFromStats", 1)
		findings, err := Analyze("fixture.go", []byte(source))
		if err != nil || len(findings) != tc.want {
			t.Fatalf("%s %s: findings=%+v err=%v", tc.typ, tc.declaration, findings, err)
		}
	}
}

func TestCollectionValueStatsProvenance(t *testing.T) {
	for _, tc := range []struct {
		typ, declaration string
		want             int
	}{
		{"[]s.DependencyStats", "measured := values[0]", 1},
		{"[2]*s.DependencyStats", "var measured = values[0]", 1},
		{"*[2]s.DependencyStats", "measured := values[0]", 1},
		{"[]s.DependencyStats", "measured, ok := values[0]; _ = ok", 0},
		{"<-chan s.DependencyStats", "measured := <-values", 1},
		{"chan *s.DependencyStats", "var measured, ok = <-values; _ = ok", 1},
		{"chan s.DependencyStats", "value, measured := <-values; _ = value", 0},
		{"chan s.DependencyStats", "copied := values; measured, ok := <-copied; _ = ok", 1},
		{"[]OtherStats", "measured := values[0]", 0},
		{"chan OtherStats", "measured := <-values", 0},
		{"string", "measured := values[0]", 0},
		{"*[]s.DependencyStats", "measured := values[0]", 0},
		{"*string", "measured := values[0]", 0},
		{"chan<- s.DependencyStats", "measured := <-values", 0},
		{"chan s.DependencyStats", "measured, ok := -values; _ = ok", 0},
		{"[]s.DependencyStats", "measured := <-values", 0},
	} {
		source := strings.Replace(mappingFixture, "measured s.DependencyStats", "values "+tc.typ, 1)
		source = strings.Replace(source, "_ = s.BuildDependencyReportFromStats", tc.declaration+"; _ = s.BuildDependencyReportFromStats", 1)
		findings, err := Analyze("fixture.go", []byte(source))
		if err != nil || len(findings) != tc.want {
			t.Fatalf("%s %s: findings=%+v err=%v", tc.typ, tc.declaration, findings, err)
		}
	}
}

func TestWrappedIndexedStats(t *testing.T) {
	for _, tc := range []struct {
		typ, expression string
		want            int
	}{
		{"[]s.DependencyStats", "&values[0]", 1},
		{"[]*s.DependencyStats", "*values[0]", 1},
		{"[]s.DependencyStats", "*(&values[0])", 1},
		{"[]*s.DependencyStats", "&values[0]", 0},
		{"[]s.DependencyStats", "*values[0]", 0},
		{"[]s.DependencyStats", "-values[0]", 0},
	} {
		source := strings.Replace(mappingFixture, "measured s.DependencyStats", "values "+tc.typ, 1)
		source = strings.Replace(source, "_ = s.BuildDependencyReportFromStats", "measured := "+tc.expression+"; _ = s.BuildDependencyReportFromStats", 1)
		findings, err := Analyze("fixture.go", []byte(source))
		if err != nil || len(findings) != tc.want {
			t.Fatalf("%s: findings=%+v err=%v", tc.expression, findings, err)
		}
	}
}

func TestDotImportedContracts(t *testing.T) {
	source := strings.ReplaceAll(strings.ReplaceAll(mappingFixture, "import r ", "import . "), "import s ", "import . ")
	source = strings.ReplaceAll(strings.ReplaceAll(source, "r.", ""), "s.", "")
	findings, err := Analyze("fixture.go", []byte(source))
	if err != nil || len(findings) != 1 {
		t.Fatalf("dot mapping: findings=%+v err=%v", findings, err)
	}
	shadowed := strings.Replace(source, "func build", "type DependencyStats struct{}; func build", 1)
	findings, err = Analyze("fixture.go", []byte(shadowed))
	if err != nil || len(findings) != 0 {
		t.Fatalf("shadowed dot mapping: findings=%+v err=%v", findings, err)
	}
	collections := strings.ReplaceAll(strings.ReplaceAll(collectionContracts, `import "sort"`, `import . "sort"`), `import "strings"`, `import . "strings"`)
	collections = strings.ReplaceAll(strings.ReplaceAll(collections, "sort.", ""), "strings.", "")
	assertFunctionFinding(t, "internal/analysis/copy.go", collections, "exact", true)
	assertFunctionFinding(t, "internal/report/copy.go", collections, "trimmed", true)
	shadowedCollections := strings.Replace(collections, "func exact", "func Strings([]string) {}; func exact", 1)
	assertFunctionFinding(t, "internal/analysis/copy.go", shadowedCollections, "exact", false)
}

type transformedCollectionCase struct{ parameter, setup, expression string }

func TestTransformedCollectionProvenance(t *testing.T) {
	for want, cases := range map[int][]transformedCollectionCase{
		1: {
			{"values []s.DependencyStats", "", "values[:]"},
			{"values [2]s.DependencyStats", "", "values[0:1:2]"},
			{"values *[2]s.DependencyStats", "", "values[:]"},
			{"values []s.DependencyStats", "", "append(values, s.DependencyStats{})"},
			{"original []s.DependencyStats", "values := append([]s.DependencyStats(nil), original...);", "values"},
			{"original []s.DependencyStats", "values := original[:];", "values"},
		},
		0: {
			{"values []s.DependencyStats", "append := func([]s.DependencyStats) []OtherStats { return nil };", "append(values)"},
			{"values [2]s.DependencyStats", "", "append(values)"},
			{"values string", "", "values[:]"},
			{"values []OtherStats", "", "values[:]"},
			{"values *[]s.DependencyStats", "", "values[:]"},
			{"unused string", "var values = append(values, s.DependencyStats{});", "values"},
		},
	} {
		for _, tc := range cases {
			assertTransformedCollection(t, tc, want)
		}
	}
}

func assertTransformedCollection(t *testing.T, tc transformedCollectionCase, want int) {
	t.Helper()
	for _, body := range []string{"measured := " + tc.expression + "[0];", "for _, measured := range " + tc.expression + " {"} {
		source := strings.Replace(mappingFixture, "measured s.DependencyStats", tc.parameter, 1)
		source = strings.Replace(source, "_ = s.BuildDependencyReportFromStats", tc.setup+body+" _ = s.BuildDependencyReportFromStats", 1)
		if strings.HasPrefix(body, "for ") {
			source += "; return r.DependencyReport{} }"
		}
		findings, err := Analyze("fixture.go", []byte(source))
		if err != nil || len(findings) != want {
			t.Fatalf("%s %s: findings=%+v err=%v", tc.parameter, body, findings, err)
		}
	}
}

func TestTypeSwitchStatsProvenance(t *testing.T) {
	for _, tc := range []struct {
		clause string
		want   int
	}{
		{"case s.DependencyStats:", 1},
		{"case *s.DependencyStats:", 1},
		{"case OtherStats:", 0},
		{"case s.DependencyStats, OtherStats:", 0},
		{"default:", 0},
	} {
		source := strings.Replace(mappingFixture, "measured s.DependencyStats", "raw any", 1)
		source = strings.Replace(source, "_ = s.BuildDependencyReportFromStats", "switch measured := raw.(type) { "+tc.clause+" _ = s.BuildDependencyReportFromStats", 1) + "; return r.DependencyReport{} }"
		findings, err := Analyze("fixture.go", []byte(source))
		if err != nil || len(findings) != tc.want {
			t.Fatalf("%s findings=%+v err=%v", tc.clause, findings, err)
		}
	}
}
