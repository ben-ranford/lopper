package reusecheck

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func packageReportInitializer() (string, string) {
	header, closure := compactReportClosure()
	_, returned, _ := strings.Cut(closure, "return ")
	literal := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(returned), "}"))
	return header + "var name string; var measured s.DependencyStats\n", literal
}

func TestPackageReportInitializerContexts(t *testing.T) {
	header, literal := packageReportInitializer()
	for _, wrapper := range []string{
		"var dependency = %s", "var dependency = &%s", "var dependency = (%s)",
		"var dependency = []r.DependencyReport{%s}", "var dependency = [1]r.DependencyReport{%s}",
		`var dependency = map[string]r.DependencyReport{"copy": %s}`,
		"var dependency = struct { report r.DependencyReport }{report: %s}",
		"var (unrelated = 1; dependency = %s)", "var unrelated, dependency = 1, %s",
		"func keep(value r.DependencyReport) (r.DependencyReport, error) { return value, nil }; var dependency, err = keep(%s)",
	} {
		t.Run(wrapper, func(t *testing.T) {
			source := header + fmt.Sprintf(wrapper, literal)
			findings, err := Analyze("initializer.go", []byte(source))
			if err != nil || len(findings) != 1 || findings[0].Advisory || !strings.HasPrefix(findings[0].Function, "dependency.init@") {
				t.Fatalf("initializer findings=%+v error=%v", findings, err)
			}
		})
	}
}

func TestPackageReportInitializerElidedTypes(t *testing.T) {
	header, literal := packageReportInitializer()
	elided := strings.TrimPrefix(literal, "r.DependencyReport")
	for _, expression := range []string{
		"[]r.DependencyReport{" + elided + "}",
		"[][]r.DependencyReport{{" + elided + "}}",
		"[]*r.DependencyReport{" + elided + "}",
		`map[string]*r.DependencyReport{"copy": ` + elided + "}",
	} {
		findings, err := Analyze("initializer.go", []byte(header+"var dependency = "+expression))
		if err != nil || len(findings) != 1 || findings[0].Advisory {
			t.Fatalf("inherited initializer type findings=%+v error=%v", findings, err)
		}
	}
}

func TestPackageReportInitializerExceptionIdentity(t *testing.T) {
	header, literal := packageReportInitializer()
	elided := strings.TrimPrefix(literal, "r.DependencyReport")
	for _, declarations := range []string{
		"var dependencies = []r.DependencyReport{" + literal + "," + literal + "}",
		"var dependencies = []r.DependencyReport{" + elided + "," + elided + "}",
		"var _, _ = " + literal + "," + literal,
		"var _ = " + literal + "; var _ = " + literal,
		"func keep(a, b r.DependencyReport) (r.DependencyReport, r.DependencyReport) { return a, b }; var first, second = keep(" + literal + "," + literal + ")",
		"var dependencies = []r.DependencyReport{\n//line same.go:1\n" + literal + ",\n//line same.go:1\n" + literal + ",\n}",
	} {
		checkInitializerExceptionIdentity(t, []byte(header+declarations))
	}
	source := strings.NewReplacer("import r ", "import . ", "r.DependencyReport", "DependencyReport").Replace(header + "var dependencies = []DependencyReport{" + literal + "," + literal + "}")
	checkInitializerExceptionIdentity(t, []byte(source))
}

func checkInitializerExceptionIdentity(t *testing.T, source []byte) {
	t.Helper()
	findings, err := Analyze("z.go", source)
	if err != nil || len(findings) != 2 || findings[0].Function == findings[1].Function {
		t.Fatalf("initializer identities=%+v error=%v", findings, err)
	}
	exceptions := reviewedClosureException(t, findings[0], source)
	if !Approved(findings[0], source, exceptions) || Approved(findings[1], source, exceptions) || Approved(findings[0], append(source, '\n'), exceptions) {
		t.Fatal("initializer exception escaped its exact literal/source identity")
	}
	again, err := AnalyzeSources(map[string][]byte{"a.go": []byte("package fixture\nvar unrelated = 1"), "z.go": source})
	if err != nil || !reflect.DeepEqual(findings, again) {
		t.Fatalf("unrelated source changed initializer identities: before=%+v after=%+v error=%v", findings, again, err)
	}
}

func TestPackageReportInitializerCrossFileProvenance(t *testing.T) {
	_, literal := packageReportInitializer()
	for _, declaration := range []string{
		"var measured Stats",
		"var measured = Stats{}",
		"var original Stats; var measured = original",
		"func makeStats() Stats { panic(0) }; var measured = makeStats()",
		"func makeStats() (Stats, error) { panic(0) }; var measured, err = makeStats()",
	} {
		provider := `package fixture; import s "` + sharedPackage + `"; type Stats = s.DependencyStats; var name string; ` + declaration
		consumer := `package fixture; import r "` + module + `report"; import s "example.com/other"; var unrelated s.Unknown; var dependency = ` + literal
		findings, err := AnalyzeSources(map[string][]byte{"provider.go": []byte(provider), "consumer.go": []byte(consumer)})
		if err != nil || len(findings) != 1 || findings[0].Advisory || !strings.HasPrefix(findings[0].Function, "dependency.init@") {
			t.Fatalf("cross-file initializer findings=%+v error=%v declaration=%s", findings, err, declaration)
		}
	}
}

func TestPackageReportInitializerClosureBoundaries(t *testing.T) {
	header, literal := packageReportInitializer()
	header = strings.Replace(header, "var name", "import \"sort\"; import \"strings\"; var name", 1)
	source := header + "var dependency = []any{" + literal + "," + collectionFunctionLiteral("trimmed") + ",func() r.DependencyReport { return " + literal + " },func() any { return func() r.DependencyReport { return " + literal + " } }}"
	findings, err := Analyze("initializer.go", []byte(source))
	if err != nil || len(findings) != 4 {
		t.Fatalf("initializer/closure findings missing or duplicated: %+v error=%v", findings, err)
	}
	initializers, collections := 0, 0
	for _, finding := range findings {
		if strings.HasPrefix(finding.Function, "dependency.init@") {
			initializers++
		}
		if finding.Rule == "sorted-unique-trimmed" {
			collections++
		}
	}
	if initializers != 1 || collections != 1 {
		t.Fatalf("initializer scope inherited or duplicated a closure: %+v", findings)
	}
}

func TestPackageReportInitializerContractGuards(t *testing.T) {
	header, literal := packageReportInitializer()
	for _, changed := range []struct{ old, replacement, declarations, want string }{
		{"r.DependencyReport", "OtherReport", "type OtherReport r.DependencyReport", "none"},
		{"var measured s.DependencyStats", "var measured OtherStats", "type OtherStats s.DependencyStats", "none"},
		{module + "report", "example.com/report", "", "none"},
		{sharedPackage, "example.com/shared", "", "none"},
		{"UsedExportsCount:measured.UsedCount", "UsedExportsCount:0", "", "advisory"},
		{"UnusedImports:measured.UnusedImports", "UnusedImports:other.UnusedImports", "var other s.DependencyStats", "advisory"},
		{"Name:name", "Name:mutate()", `func mutate() string { measured.UsedCount++; return "changed" }`, "advisory"},
	} {
		source := strings.Replace(header+"var dependency = "+literal, changed.old, changed.replacement, 1) + ";" + changed.declarations
		checkPromotedReportFindings(t, map[string][]byte{"initializer.go": []byte(source)}, changed.want)
	}
	source := header + "type OtherStats s.DependencyStats; var dependency = []any{" + literal + ",func(measured OtherStats) r.DependencyReport { return " + literal + " }}"
	findings, err := Analyze("initializer.go", []byte(source))
	if err != nil || len(findings) != 1 || !strings.HasPrefix(findings[0].Function, "dependency.init@") {
		t.Fatalf("closure shadow borrowed package statistics provenance: %+v error=%v", findings, err)
	}
}

func TestPackageReportInitializerOwnerAndDotImports(t *testing.T) {
	header, literal := packageReportInitializer()
	source := header + "var BuildDependencyReportFromStats = " + literal
	findings, err := Analyze("internal/lang/shared/dependency_usage_stats.go", []byte(source))
	if err != nil || len(findings) != 1 || findings[0].Advisory || !strings.HasPrefix(findings[0].Function, "BuildDependencyReportFromStats.init@") {
		t.Fatalf("initializer inherited canonical function exemption: %+v error=%v", findings, err)
	}
	source = strings.NewReplacer("import r ", "import . ", "import s ", "import . ", "r.DependencyReport", "DependencyReport", "s.DependencyStats", "DependencyStats").Replace(header + "var dependency = " + literal)
	checkPromotedReportFindings(t, map[string][]byte{"initializer.go": []byte(source)}, "violation")
}
