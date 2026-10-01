package reusecheck

import (
	"strings"
	"testing"
)

type collectionProvenanceUse struct {
	parameter, setup, receiver string
	loop                       bool
}

func TestSourceFunctionCollectionProvenance(t *testing.T) {
	for _, result := range []string{"[]s.DependencyStats", "[]*s.DependencyStats", "Collection"} {
		declaration := "type Collection []s.DependencyStats; func factory() " + result + " { panic(0) }"
		for _, use := range []collectionProvenanceUse{
			{"unused string", "values := factory(); measured := values[0]", "measured", false},
			{"unused string", "measured := factory()[0]", "measured", false},
			{"unused string", "values := factory()", "values[0]", false},
			{"unused string", "for _, measured := range factory() {", "measured", true},
		} {
			t.Run(result+use.setup, func(t *testing.T) { checkCollectionProvenance(t, declaration, use, true) })
		}
	}
	mapFactory := "func factory() map[string]s.DependencyStats { panic(0) }"
	checkCollectionProvenance(t, mapFactory, collectionProvenanceUse{"unused string", `values := factory(); measured := values["key"]`, "measured", false}, true)
	checkCollectionProvenance(t, mapFactory, collectionProvenanceUse{"unused string", "for _, measured := range factory() {", "measured", true}, true)
	checkCollectionProvenance(t, "func factory() chan s.DependencyStats { panic(0) }", collectionProvenanceUse{"unused string", "for measured := range factory() {", "measured", true}, true)
	for _, call := range []string{"factory(1)", "factory[int](1)"} {
		checkCollectionProvenance(t, "func factory[T any](value T) []s.DependencyStats { panic(0) }", collectionProvenanceUse{"unused string", "values := " + call + "; for _, measured := range values {", "measured", true}, true)
	}
}

func TestSourceCollectionFunctionGuards(t *testing.T) {
	use := collectionProvenanceUse{"unused string", "values := factory(); measured := values[0]", "measured", false}
	for _, declaration := range []string{
		"type Stats s.DependencyStats; func factory() []Stats { panic(0) }",
		"func factory() ([]s.DependencyStats, error) { panic(0) }",
		"func factory(value int) []s.DependencyStats { panic(0) }",
		"var factory = factory",
		"var factory = other; var other = factory",
	} {
		t.Run(declaration, func(t *testing.T) { checkCollectionProvenance(t, declaration, use, false) })
	}
	use.setup = "values := factory(1, 2); measured := values[0]"
	checkCollectionProvenance(t, "func factory(value int) []s.DependencyStats { panic(0) }", use, false)
}

func TestAssertedCollectionProvenance(t *testing.T) {
	for _, asserted := range []string{"[]s.DependencyStats", "Collection"} {
		for _, binding := range []string{"values := raw.(", "values, ok := raw.("} {
			setup := binding + asserted + ")"
			if strings.Contains(binding, ", ok") {
				setup += "; _ = ok"
			}
			for _, read := range []collectionProvenanceUse{
				{"raw any", setup + "; measured := values[0]", "measured", false},
				{"raw any", setup + "; for _, measured := range values {", "measured", true},
			} {
				t.Run(asserted+read.setup, func(t *testing.T) { checkCollectionProvenance(t, "type Collection []s.DependencyStats", read, true) })
			}
		}
	}
	checkCollectionProvenance(t, "", collectionProvenanceUse{"raw any", "measured := raw.([]s.DependencyStats)[0]", "measured", false}, true)
	checkCollectionProvenance(t, "", collectionProvenanceUse{"raw any", "for _, measured := range raw.([]s.DependencyStats) {", "measured", true}, true)
	checkCollectionProvenance(t, "", collectionProvenanceUse{"raw any", `values := raw.(map[string]s.DependencyStats); measured, ok := values["key"]; _ = ok`, "measured", false}, true)
	checkCollectionProvenance(t, "", collectionProvenanceUse{"raw any", "values := raw.(chan s.DependencyStats); measured, ok := <-values; _ = ok", "measured", false}, true)
}

func TestAssertedCollectionProvenanceGuards(t *testing.T) {
	for _, setup := range []string{
		"values, measured := raw.([]s.DependencyStats); _ = values",
		"values, ok := raw.([]s.DependencyStats); _ = values; measured := ok[0]",
		"values := raw.([]Stats); measured := values[0]",
		"values := raw.(chan<- s.DependencyStats); measured := <-values",
	} {
		use := collectionProvenanceUse{"raw any", setup, "measured", false}
		checkCollectionProvenance(t, "type Stats s.DependencyStats", use, false)
	}
	checkCollectionProvenance(t, "", collectionProvenanceUse{"raw any", "values, ok := raw.([]s.DependencyStats); _ = values; for _, measured := range ok {", "measured", true}, false)
}

func TestVariadicStatsCollectionProvenance(t *testing.T) {
	for _, element := range []string{"s.DependencyStats", "*s.DependencyStats"} {
		for _, use := range []collectionProvenanceUse{
			{"values ..." + element, "measured := values[0]", "measured", false},
			{"values ..." + element, "", "values[0]", false},
			{"values ..." + element, "for _, measured := range values {", "measured", true},
		} {
			t.Run(element+use.setup, func(t *testing.T) { checkCollectionProvenance(t, "", use, true) })
		}
	}
	checkCollectionProvenance(t, "type Stats s.DependencyStats", collectionProvenanceUse{"values ...Stats", "measured := values[0]", "measured", false}, false)
	checkCollectionProvenance(t, "", collectionProvenanceUse{"values ...s.DependencyStats", "for measured := range values {", "measured", true}, false)
}

func checkCollectionProvenance(t *testing.T, declaration string, use collectionProvenanceUse, want bool) {
	t.Helper()
	consumer := strings.Replace(mappingFixture, "measured s.DependencyStats", use.parameter, 1)
	consumer = strings.Replace(consumer, `_ = s.BuildDependencyReportFromStats(name, "python", measured)`, use.setup, 1)
	consumer = strings.ReplaceAll(consumer, "measured.", use.receiver+".")
	if use.loop {
		consumer += "; return r.DependencyReport{} }"
	}
	provider := "package fixture\nimport s \"" + sharedPackage + "\"\n" + declaration
	for _, split := range []bool{false, true} {
		sources := []string{consumer + "\n" + declaration}
		if split {
			sources = []string{provider, consumer}
		}
		findings := packageSourceFindings(t, sources...)
		if (len(findings) == 1 && !findings[0].Advisory) != want || (!want && len(findings) != 0) {
			t.Fatalf("cross-file=%v findings=%+v want violation=%v", split, findings, want)
		}
	}
}
