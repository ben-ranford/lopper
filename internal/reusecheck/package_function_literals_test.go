package reusecheck

import (
	"fmt"
	"strings"
	"testing"
)

func TestPackageInitializerCollectionLiterals(t *testing.T) {
	literal := collectionFunctionLiteral("trimmed")
	for _, wrapper := range []string{
		`var builders = map[string]func([]string) []string{"x": %s}`,
		"var builders = []func([]string) []string{%s}",
		"var builders = [1]func([]string) []string{%s}",
		"var builders = struct { build func([]string) []string }{build: %s}",
		"func keep(build func([]string) []string) any { return build }; var builders = keep(%s)",
		"func keep(build func([]string) []string) (any, error) { return build, nil }; var builders, err = keep(%s)",
		"var unrelated, builders = 1, []any{%s}",
		"var (builders = []any{(%s)})",
	} {
		t.Run(wrapper, func(t *testing.T) {
			source := localCollectionSource(fmt.Sprintf(wrapper, literal))
			findings, err := Analyze("fixture.go", []byte(source))
			if err != nil || len(findings) != 1 {
				t.Fatalf("findings=%+v error=%v", findings, err)
			}
			prefix, _, _ := strings.Cut(source, literal)
			finding := findings[0]
			if !strings.HasPrefix(finding.Function, "builders.func@") || finding.Rule != "sorted-unique-trimmed" || finding.Line != strings.Count(prefix, "\n")+1 || finding.Advisory {
				t.Fatalf("initializer attribution or source location: %+v", finding)
			}
		})
	}
}

func TestPackageInitializerLiteralOwnerExemptions(t *testing.T) {
	for name, owner := range collectionOwners {
		source := localCollectionSource("var " + owner.function + " = []any{" + strings.TrimSpace(collectionFunctionLiteral(name)) + "}")
		findings, err := Analyze(owner.owner, []byte(source))
		if err != nil || len(findings) != 1 || !strings.HasPrefix(findings[0].Function, owner.function+".func@") || findings[0].Rule != owner.rule {
			t.Fatalf("initializer inherited canonical owner exemption: %+v error=%v", findings, err)
		}
	}
	header, mapping, _ := strings.Cut(mappingFixture, "func build")
	source := header + "var BuildDependencyReportFromStats = []any{func" + mapping + "}"
	findings, err := Analyze("internal/lang/shared/dependency_usage_stats.go", []byte(source))
	if err != nil || len(findings) != 1 || !strings.HasPrefix(findings[0].Function, "BuildDependencyReportFromStats.func@") || findings[0].Advisory {
		t.Fatalf("report initializer inherited canonical owner exemption: %+v error=%v", findings, err)
	}
}

func TestPackageInitializerReportLiterals(t *testing.T) {
	header, mapping, _ := strings.Cut(mappingFixture, "func build")
	for _, wrapper := range []string{
		"var builders = []any{func%s}",
		`var builders = map[string]any{"x": func%s}`,
		"func keep(build any) (any, error) { return build, nil }; var builders, err = keep(func%s)",
	} {
		source := header + fmt.Sprintf(wrapper, mapping)
		findings, err := Analyze("fixture.go", []byte(source))
		if err != nil || len(findings) != 1 || !strings.HasPrefix(findings[0].Function, "builders.func@") || findings[0].Advisory || findings[0].Rule != "dependency-report-mapping" {
			t.Fatalf("package report closure: %+v error=%v", findings, err)
		}
	}
}

func TestPackageInitializerNestedFindingsAreUnique(t *testing.T) {
	header, mapping, _ := strings.Cut(mappingFixture, "func build")
	source := header + "import \"sort\"; import \"strings\"\nvar builders = []any{func() {\n_ = " + collectionFunctionLiteral("trimmed") + "\n_ = func" + mapping + "\n}}"
	findings, err := Analyze("fixture.go", []byte(source))
	if err != nil || len(findings) != 2 {
		t.Fatalf("nested package findings missing/repeated: %+v error=%v", findings, err)
	}
	if strings.Count(findings[0].Function, ".func@") != 2 || findings[0].Rule != "sorted-unique-trimmed" || strings.Count(findings[1].Function, ".func@") != 2 || findings[1].Rule != "dependency-report-mapping" || findings[1].Advisory {
		t.Fatalf("nested package attribution: %+v", findings)
	}
	source = localCollectionSource("var builders = []any{\n" + collectionFunctionLiteral("trimmed") + ",\n" + collectionFunctionLiteral("trimmed") + ",\n}")
	if findings, err = Analyze("fixture.go", []byte(source)); err != nil || len(findings) != 2 || findings[0].Line == findings[1].Line {
		t.Fatalf("sibling package closures missing/repeated: %+v error=%v", findings, err)
	}
	source = localCollectionSource("var builders = func() func([]string) []string { return " + collectionFunctionLiteral("trimmed") + " }()")
	if findings, err = Analyze("fixture.go", []byte(source)); err != nil || len(findings) != 1 || strings.Count(findings[0].Function, ".func@") != 2 {
		t.Fatalf("immediately invoked package closure: %+v error=%v", findings, err)
	}
}

func TestPackageInitializerLiteralDistinctContract(t *testing.T) {
	literal := strings.Replace(collectionFunctionLiteral("trimmed"), "return nil", "return []string{}", 1)
	findings, err := Analyze("fixture.go", []byte(localCollectionSource("var builders = []any{"+literal+"}")))
	if err != nil || len(findings) != 0 {
		t.Fatalf("different initializer contract: %+v error=%v", findings, err)
	}
}
