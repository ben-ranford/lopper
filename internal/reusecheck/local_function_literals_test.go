package reusecheck

import (
	"fmt"
	"strings"
	"testing"
)

func TestLocalFunctionLiteralCollections(t *testing.T) {
	literal := collectionFunctionLiteral("trimmed")
	for _, wrapper := range []string{
		"func outer() { copy := %s; _ = copy }",
		"func consume(func([]string) []string) {}; func outer() { consume(%s) }",
		"func outer() func([]string) []string { return %s }",
		"func outer() { nested := func() func([]string) []string { return %s }; _ = nested }",
		"var outer = func() { copy := %s; _ = copy }",
	} {
		t.Run(wrapper, func(t *testing.T) {
			source := localCollectionSource(fmt.Sprintf(wrapper, literal))
			findings, err := Analyze("fixture.go", []byte(source))
			if err != nil || len(findings) != 1 {
				t.Fatalf("findings=%+v error=%v", findings, err)
			}
			finding := findings[0]
			prefix, _, found := strings.Cut(source, literal)
			if !found {
				t.Fatal("fixture does not contain the function literal")
			}
			line := strings.Count(prefix, "\n") + 1
			if !strings.HasPrefix(finding.Function, "outer.func@") || finding.Rule != "sorted-unique-trimmed" || finding.Line != line || finding.Advisory {
				t.Fatalf("literal diagnostic lost its enclosing function, contract or location: %+v", finding)
			}
		})
	}
}

func TestLocalFunctionLiteralContractsAndOwner(t *testing.T) {
	for name, owner := range collectionOwners {
		t.Run(name, func(t *testing.T) {
			source := localCollectionSource("func " + owner.function + "() { _ = " + collectionFunctionLiteral(name) + " }")
			findings, err := Analyze(owner.owner, []byte(source))
			if err != nil || len(findings) != 1 || findings[0].Rule != owner.rule || !strings.HasPrefix(findings[0].Function, owner.function+".func@") {
				t.Fatalf("nested implementation inherited the canonical owner's exemption: %+v error=%v", findings, err)
			}
		})
	}
}

func TestLocalFunctionLiteralDistinctContracts(t *testing.T) {
	literal := collectionFunctionLiteral("trimmed")
	for _, replacement := range [][2]string{
		{"return nil", "return []string{}"},
		{"strings.TrimSpace(value)", "strings.ToLower(value)"},
		{"sort.Strings(out)", ""},
		{"func(values []string)", "func()"},
	} {
		source := localCollectionSource("func outer(values []string) { _ = " + strings.Replace(literal, replacement[0], replacement[1], 1) + " }")
		findings, err := Analyze("fixture.go", []byte(source))
		if err != nil || len(findings) != 0 {
			t.Fatalf("distinct closure contract produced findings: %+v error=%v", findings, err)
		}
	}
}

func TestLocalFunctionLiteralReportFindingsAreNotRepeated(t *testing.T) {
	header, mapping, _ := strings.Cut(mappingFixture, "func build")
	source := header + "import \"sort\"; import \"strings\"\nfunc outer() {\n_ = " + collectionFunctionLiteral("trimmed") + "\n_ = func" + mapping + "\n}"
	findings, err := Analyze("fixture.go", []byte(source))
	if err != nil || len(findings) != 2 {
		t.Fatalf("collection or report findings missing/repeated: %+v error=%v", findings, err)
	}
	if findings[0].Rule != "sorted-unique-trimmed" || findings[1].Rule != "dependency-report-mapping" || !strings.HasPrefix(findings[1].Function, "outer.func@") || findings[1].Advisory {
		t.Fatalf("unexpected mixed closure findings: %+v", findings)
	}
}

func collectionFunctionLiteral(name string) string {
	_, declaration, _ := strings.Cut(collectionContracts, "func "+name)
	body, _, _ := strings.Cut(declaration, "\nfunc ")
	return "func" + body
}

func localCollectionSource(body string) string {
	return "package fixture\nimport \"sort\"\nimport \"strings\"\n" + body
}
