package reusecheck

import (
	"strings"
	"testing"
)

func TestDirectAssertedStatsReceivers(t *testing.T) {
	for _, receiver := range []string{
		"raw.(s.DependencyStats)",
		"raw.(*s.DependencyStats)",
		"(*raw.(*s.DependencyStats))",
		"(*raw.(**s.DependencyStats))",
		"raw.([]s.DependencyStats)[0]",
		"raw.([2]*s.DependencyStats)[0]",
		"raw.(*[2]s.DependencyStats)[0]",
		`raw.(map[string]s.DependencyStats)["key"]`,
		"(&raw.([]s.DependencyStats)[0])",
		"(*raw.([]*s.DependencyStats)[0])",
	} {
		checkDirectStatsReceivers(t, []statsReceiverCase{{"raw any", receiver, true}})
	}
	for _, tc := range [][2]string{
		{"type Values[T any] []s.DependencyStats", "raw.(Values[int])[0]"},
		{"type Values[T comparable, U any] map[T]s.DependencyStats", `raw.(Values[string, int])["key"]`},
	} {
		checkCollectionProvenance(t, tc[0], collectionProvenanceUse{"raw any", "", tc[1], false}, true)
	}
}

func TestDirectAssertionOperandGuards(t *testing.T) {
	for _, receiver := range []string{
		"raw.(OtherStats)",
		"raw.([]OtherStats)[0]",
		"raw.(**s.DependencyStats)",
		"(*raw.(s.DependencyStats))",
		"raw.([]s.DependencyStats)[next()]",
		"factory().(s.DependencyStats)",
		"(<-values).(s.DependencyStats)",
	} {
		checkDirectStatsReceivers(t, []statsReceiverCase{{"raw any, values chan any, factory func() any, next func() int", receiver, false}})
	}
	for _, declaration := range []string{
		"type Stats s.DependencyStats",
		"type Stats = []Stats",
		"type Stats = missing.DependencyStats",
	} {
		checkCollectionProvenance(t, declaration, collectionProvenanceUse{"raw any", "", "raw.(Stats)", false}, false)
	}
	checkCollectionProvenance(t, "type Values[T comparable] = map[T]s.DependencyStats", collectionProvenanceUse{"raw any", "", `raw.(Values[string])["key"]`, false}, false)
}

func TestAssertedReceiverAliasIdentity(t *testing.T) {
	for _, alias := range [][3]string{
		{"type Stats = s.DependencyStats", "raw.(Stats)", "raw.(s.DependencyStats)"},
		{"type Stats = *s.DependencyStats", "raw.(Stats)", "raw.(*s.DependencyStats)"},
		{"type Stats[T any] = s.DependencyStats", "raw.(Stats[int])", "raw.(s.DependencyStats)"},
		{"type Stats = []s.DependencyStats", "raw.(Stats)[0]", "raw.([]s.DependencyStats)[0]"},
	} {
		checkAssertedIdentity(t, alias[0], "", alias[1], alias[2], false)
	}
}

func TestAssertedReceiverDistinctIdentity(t *testing.T) {
	for _, pair := range [][2]string{
		{"raw.(s.DependencyStats)", "raw.(*s.DependencyStats)"},
		{"raw.(s.DependencyStats)", "other.(s.DependencyStats)"},
		{"raw.([2]s.DependencyStats)[0]", "raw.([3]s.DependencyStats)[0]"},
	} {
		checkAssertedIdentity(t, "", "", pair[0], pair[1], true)
	}
	checkAssertedIdentity(t, "type Stats []s.DependencyStats", "", "raw.(Stats)[0]", "raw.([]s.DependencyStats)[0]", true)
	checkAssertedIdentity(t, "const size = 2; type Values = [size]s.DependencyStats", "const size = 3", "raw.(Values)[0]", "raw.([size]s.DependencyStats)[0]", true)
}

func checkAssertedIdentity(t *testing.T, declaration, setup, first, other string, advisory bool) {
	t.Helper()
	source := strings.Replace(mappingFixture, "measured s.DependencyStats", "raw, other any", 1)
	source = strings.Replace(source, `_ = s.BuildDependencyReportFromStats(name, "python", measured)`, setup, 1)
	source = strings.ReplaceAll(source, "measured.", first+".")
	source = strings.Replace(source, first+".UsedCount", other+".UsedCount", 1)
	provider := "package fixture; import s \"" + sharedPackage + "\"; " + declaration
	findings := packageSourceFindings(t, provider, source)
	if len(findings) != 1 || findings[0].Advisory != advisory {
		t.Fatalf("assertion identity findings=%+v want advisory=%v", findings, advisory)
	}
}
