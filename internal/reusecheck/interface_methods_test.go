package reusecheck

import (
	"strings"
	"testing"
)

func TestInterfaceMethodStatsResults(t *testing.T) {
	declaration := "type Provider interface { Stats() s.DependencyStats }"
	for _, setup := range []string{
		"measured := p.Stats()",
		"produce := p.Stats; measured := produce()",
		"measured := Provider.Stats(p)",
		"produce := Provider.Stats; measured := produce(p)",
		"measured := (Provider).Stats(p)",
	} {
		checkInterfaceMapping(t, declaration, "p Provider", setup, "measured", true)
	}
	for _, provider := range []string{
		"type Base interface { Stats() s.DependencyStats }; type Provider interface { Base }",
		"type Base interface { Stats() s.DependencyStats }; type Provider = Base",
		"type Base interface { Stats() s.DependencyStats }; type Provider struct { Base }",
	} {
		checkInterfaceMapping(t, provider, "p Provider", "measured := p.Stats()", "measured", true)
		checkInterfaceMapping(t, provider, "p Provider", "measured := Provider.Stats(p)", "measured", true)
	}
	checkInterfaceMapping(t, "", "p interface { Stats() s.DependencyStats }", "measured := p.Stats()", "measured", true)
	checkInterfaceMapping(t, declaration, "p Provider", "measured := (interface { Stats() s.DependencyStats }).Stats(p)", "measured", true)
	checkInterfaceMapping(t, declaration, "p Provider", "measured := (struct { Provider }).Stats(struct { Provider }{p})", "measured", true)
}

func TestInterfaceMethodResultShapes(t *testing.T) {
	for _, tc := range []struct{ results, setup, receiver string }{
		{"*s.DependencyStats", "measured := p.Stats()", "(*measured)"},
		{"(s.DependencyStats, error)", "measured, _ := p.Stats()", "measured"},
		{"(error, s.DependencyStats)", "_, measured := p.Stats()", "(&measured)"},
		{"[]s.DependencyStats", "values := p.Stats(); measured := values[0]", "measured"},
		{"(error, []s.DependencyStats)", "_, values := p.Stats(); measured := values[0]", "measured"},
		{"chan s.DependencyStats", "measured := <-p.Stats()", "measured"},
		{"func() s.DependencyStats", "produce := p.Stats(); measured := produce()", "measured"},
	} {
		declaration := "type Provider interface { Stats() " + tc.results + " }"
		t.Run(tc.results, func(t *testing.T) {
			checkInterfaceMapping(t, declaration, "p Provider", tc.setup, tc.receiver, true)
		})
	}
	checkCollectionProvenance(t, "type Provider interface { Stats() []s.DependencyStats }", collectionProvenanceUse{"p Provider", "for _, measured := range p.Stats() {", "measured", true}, true)
}

func TestInterfaceMethodCallArity(t *testing.T) {
	declaration := "type Provider interface { Stats(int, ...string) s.DependencyStats }"
	for _, call := range []string{"p.Stats(1)", `p.Stats(1, "x")`, "p.Stats(1, []string{}...)", "Provider.Stats(p, 1)", "Provider.Stats(p, 1, []string{}...)"} {
		checkInterfaceMapping(t, declaration, "p Provider", "measured := "+call, "measured", true)
	}
	for _, call := range []string{"p.Stats()", "Provider.Stats()", "Provider.Stats(p)", "p.Stats(1, []string{}, []string{}...)"} {
		checkInterfaceMapping(t, declaration, "p Provider", "measured := "+call, "measured", false)
	}
	for _, setup := range []string{"measured := Provider.Stats()", "produce := Provider.Stats; measured := produce()", "measured := Provider.Stats(p, p)", "measured := p.Stats(p)", "measured := (interface { Stats() s.DependencyStats }).Stats()", "measured := (*Provider).Stats(p)"} {
		checkInterfaceMapping(t, "type Provider interface { Stats() s.DependencyStats }", "p Provider", setup, "measured", false)
	}
}

func TestInterfaceMethodProvenanceGuards(t *testing.T) {
	for _, declaration := range []string{
		"type Stats s.DependencyStats; type Provider interface { Stats() Stats }",
		"type Provider interface { Stats() Unknown }",
		"type Provider interface { Stats() }",
		"type Provider interface { Stats() (s.DependencyStats, error) }",
		"type Provider interface { Other() s.DependencyStats }",
		"type Provider interface { Missing }",
		"type Provider = s.Unknown",
		"type Provider struct { Stats int }",
	} {
		checkInterfaceMapping(t, declaration, "p Provider", "measured := p.Stats()", "measured", false)
	}
	checkInterfaceMapping(t, "type Provider interface { Stats() *s.DependencyStats }", "p Provider", "measured := p.Stats()", "(&measured)", false)
	checkInterfaceMapping(t, "type Provider interface { Stats() (s.DependencyStats, error) }", "p Provider", "_, measured := p.Stats()", "measured", false)
	provider := "package fixture\nimport r \"example.com/other\"\ntype Provider interface { Stats() r.DependencyStats }"
	consumer := packageMappingSource("p Provider", "measured := p.Stats()", "measured")
	if findings := packageSourceFindings(t, provider, consumer); len(findings) != 0 {
		t.Fatalf("unrelated provider import: %+v", findings)
	}
	provider = strings.Replace(provider, "example.com/other", sharedPackage, 1)
	if findings := packageSourceFindings(t, provider, consumer); len(findings) != 1 || findings[0].Advisory {
		t.Fatalf("provider import ownership: %+v", findings)
	}
}

func TestReturnedCallableCycles(t *testing.T) {
	for _, declaration := range []string{"var produce = produce()", "var produce = other(); var other = produce()"} {
		checkSourceFunctionReportMapping(t, declaration, "measured := produce()", "measured", false)
	}
}

func TestInterfaceGenericMethodExpressions(t *testing.T) {
	for _, instance := range []string{"Provider[int]", "Provider[int, string]"} {
		parameters := "T any"
		if strings.Contains(instance, ",") {
			parameters = "T, U any"
		}
		declaration := "type Provider[" + parameters + "] interface { Stats() s.DependencyStats }"
		for _, call := range []string{"p.Stats()", instance + ".Stats(p)"} {
			checkInterfaceMapping(t, declaration, "p "+instance, "measured := "+call, "measured", true)
		}
		checkInterfaceMapping(t, declaration, "p "+instance, "measured := "+instance+".Stats()", "measured", false)
	}
	checkInterfaceMapping(t, "type Provider[T any] interface { Stats() T }", "p Provider[s.DependencyStats]", "measured := p.Stats()", "measured", false)
}

func checkInterfaceMapping(t *testing.T, declaration, parameter, setup, receiver string, want bool) {
	t.Helper()
	checkCollectionProvenance(t, declaration, collectionProvenanceUse{parameter, setup, receiver, false}, want)
}
