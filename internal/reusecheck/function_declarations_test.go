package reusecheck

import (
	"strings"
	"testing"
)

func TestPackageFunctionVariableMappings(t *testing.T) {
	for _, declaration := range []string{
		"var build = func",
		"var build func(string, s.DependencyStats) r.DependencyReport = func",
		"var unrelated, build = 42, func",
	} {
		t.Run(declaration, func(t *testing.T) {
			source := strings.Replace(mappingFixture, "func build", declaration, 1)
			findings, err := Analyze("fixture.go", []byte(source))
			if err != nil || len(findings) != 1 || findings[0].Function != "build" || findings[0].Advisory {
				t.Fatalf("findings=%+v error=%v", findings, err)
			}
		})
	}
}

func TestPackageFunctionVariableCollections(t *testing.T) {
	source := collectionContracts
	for name := range collectionOwners {
		source = strings.Replace(source, "func "+name+"(", "var "+name+" = func(", 1)
	}
	findings, err := Analyze("internal/lang/other.go", []byte(source))
	if err != nil || len(findings) != 3 {
		t.Fatalf("findings=%+v error=%v", findings, err)
	}
	for _, finding := range findings {
		if finding.Function == "" || finding.Function == "exact" || finding.Advisory {
			t.Fatalf("unexpected function attribution: %+v", finding)
		}
	}
}

func TestPackageFunctionVariableForms(t *testing.T) {
	source := strings.Replace(mappingFixture, "func build", "var (build = (func", 1) + "))"
	findings, err := Analyze("fixture.go", []byte(source))
	if err != nil || len(findings) != 1 || findings[0].Function != "build" {
		t.Fatalf("parenthesized variable: findings=%+v error=%v", findings, err)
	}
	source = `package fixture
const ignored = 1
var uninitialized func()
var first, second = returnsTwo()
func declaredWithoutBody()
`
	findings, err = Analyze("fixture.go", []byte(source))
	if err != nil || len(findings) != 0 {
		t.Fatalf("unproven declarations: findings=%+v error=%v", findings, err)
	}
}
