package reusecheck

import (
	"strings"
	"testing"
)

func TestGenericAliasStatsTypes(t *testing.T) {
	for _, tc := range []struct{ declaration, parameter string }{
		{"type Stats[T any] = s.DependencyStats", "Stats[int]"},
		{"type Stats[T, U any] = s.DependencyStats", "Stats[int, string]"},
		{"type Stats[T any] = *s.DependencyStats", "Stats[[]int]"},
		{"type Stats[T any] = s.DependencyStats", "*Stats[int]"},
		{"type Base[T any] = s.DependencyStats; type Stats = Base[int]", "Stats"},
		{"type Base[T any] = s.DependencyStats; type Stats[T, U any] = Base[U]", "Stats[int, string]"},
	} {
		t.Run(tc.declaration+tc.parameter, func(t *testing.T) {
			checkGenericAliasMapping(t, tc.declaration, tc.parameter, true)
		})
	}
}

func TestGenericAliasStatsGuards(t *testing.T) {
	for _, tc := range []struct{ declaration, parameter string }{
		{"type Stats[T any] s.DependencyStats", "Stats[int]"},
		{"type Named s.DependencyStats; type Stats[T any] = Named", "Stats[int]"},
		{"type Stats[T any] = s.DependencyStats", "Stats"},
		{"type Stats[T, U any] = s.DependencyStats", "Stats[int]"},
		{"type Stats[T any] = s.DependencyStats", "Stats[int, string]"},
		{"type Stats = s.DependencyStats", "Stats[int]"},
		{"type Stats[T any] = T", "Stats[s.DependencyStats]"},
		{"type Stats[T any] = *T", "Stats[s.DependencyStats]"},
		{"type Stats[T any] = Stats[T]", "Stats[int]"},
		{"type Base[T any] = Stats[T]; type Stats[T any] = Base[T]", "Stats[int]"},
		{"type Stats[T any] = **s.DependencyStats", "Stats[int]"},
		{"", "s.DependencyStats[int]"},
		{"type Stats[T any] = s.Unknown[T]", "Stats[int]"},
	} {
		t.Run(tc.declaration+tc.parameter, func(t *testing.T) {
			checkGenericAliasMapping(t, tc.declaration, tc.parameter, false)
		})
	}
}

func TestGenericAliasCollectionTypes(t *testing.T) {
	for _, assignment := range []string{"=", ""} {
		declaration := "type Collection[T any] " + assignment + " []s.DependencyStats"
		checkCollectionProvenance(t, declaration, collectionProvenanceUse{"values Collection[int]", "measured := values[0]", "measured", false}, true)
		checkCollectionProvenance(t, declaration, collectionProvenanceUse{"values Collection[int]", "for _, measured := range values {", "measured", true}, true)
	}
	checkCollectionProvenance(t, "type Collection[T any] = []T", collectionProvenanceUse{"values Collection[s.DependencyStats]", "measured := values[0]", "measured", false}, false)
}

func TestGenericAliasImportOwnership(t *testing.T) {
	provider := "package fixture\nimport r \"" + sharedPackage + "\"\ntype Stats[T any] = r.DependencyStats"
	consumer := packageMappingSource("measured Stats[int]", "", "measured")
	findings := packageSourceFindings(t, provider, consumer)
	if len(findings) != 1 || findings[0].Advisory {
		t.Fatalf("provider import alias: %+v", findings)
	}
	provider = strings.Replace(provider, sharedPackage, "example.com/other", 1)
	if findings = packageSourceFindings(t, provider, consumer); len(findings) != 0 {
		t.Fatalf("unrelated import claimed stats ownership: %+v", findings)
	}
	provider = "package fixture\nimport . \"" + sharedPackage + "\"\ntype Stats[T any] = DependencyStats"
	if findings = packageSourceFindings(t, provider, consumer); len(findings) != 1 || findings[0].Advisory {
		t.Fatalf("dot-imported alias: %+v", findings)
	}
}

func TestGenericReportTypeAlias(t *testing.T) {
	source := strings.Replace(mappingFixture, "return r.DependencyReport{", "return Report[int]{", 1)
	source += "\ntype Report[T any] = r.DependencyReport"
	findings := packageSourceFindings(t, source)
	if len(findings) != 1 || findings[0].Advisory {
		t.Fatalf("generic report alias: %+v", findings)
	}
}

func checkGenericAliasMapping(t *testing.T, declaration, parameter string, want bool) {
	t.Helper()
	checkCollectionProvenance(t, declaration, collectionProvenanceUse{"measured " + parameter, "", "measured", false}, want)
}
