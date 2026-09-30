package reusecheck

import (
	"strings"
	"testing"
)

func TestDirectConvertedStatsReceivers(t *testing.T) {
	for _, target := range []string{"s.DependencyStats", "*s.DependencyStats"} {
		checkDirectStatsReceivers(t, []statsReceiverCase{{"raw " + target, "(" + target + ")(raw)", true}})
	}
	for _, target := range []string{"*s.DependencyStats", "**s.DependencyStats"} {
		checkDirectStatsReceivers(t, []statsReceiverCase{{"raw " + target, "(*(" + target + ")(raw))", true}})
	}
	for _, target := range []string{"[]s.DependencyStats", "[2]s.DependencyStats", "*[2]s.DependencyStats"} {
		checkDirectStatsReceivers(t, []statsReceiverCase{{"raw " + target, "(" + target + ")(raw)[0]", true}})
	}
	checkDirectStatsReceivers(t, []statsReceiverCase{
		{"raw map[string]s.DependencyStats", `(map[string]s.DependencyStats)(raw)["key"]`, true},
		{"values []s.DependencyStats, index int64", "values[int(index)]", true},
	})
}

func TestConvertedStatsSourceTypes(t *testing.T) {
	for _, source := range []struct{ declaration, parameter, receiver string }{
		{"type Stats = s.DependencyStats", "raw s.DependencyStats", "Stats(raw)"},
		{"type Raw s.DependencyStats", "raw Raw", "s.DependencyStats(raw)"},
		{"type Stats[T any] = s.DependencyStats", "raw s.DependencyStats", "Stats[int](raw)"},
		{"type Values[T any] []s.DependencyStats", "raw []s.DependencyStats", "Values[int](raw)[0]"},
		{"type Values = [int(2)]s.DependencyStats", "raw Values", "Values(raw)[0]"},
	} {
		checkCollectionProvenance(t, source.declaration, collectionProvenanceUse{source.parameter, "", source.receiver, false}, true)
	}
}

func TestConvertedReceiverOperandGuards(t *testing.T) {
	for _, receiver := range []string{
		"s.DependencyStats(factory())", "s.DependencyStats(<-values)",
		"s.DependencyStats()", "s.DependencyStats(raw, raw)", "s.DependencyStats(raw...)",
		"(**s.DependencyStats)(raw)", "([]s.DependencyStats)(slice)[next()]",
	} {
		checkDirectStatsReceivers(t, []statsReceiverCase{{"raw **s.DependencyStats, slice []s.DependencyStats, values chan s.DependencyStats, factory func() s.DependencyStats, next func() int", receiver, false}})
	}
	for _, declaration := range []string{
		"type Stats s.DependencyStats", "type Stats = []Stats", "type Stats = Other; type Other = Stats",
		"func Stats(s.DependencyStats) s.DependencyStats { panic(0) }",
	} {
		checkCollectionProvenance(t, declaration, collectionProvenanceUse{"raw s.DependencyStats", "", "Stats(raw)", false}, false)
	}
}

func TestConvertedArrayLengthCycle(t *testing.T) {
	declaration := "type Values = [int(Values(0))]s.DependencyStats"
	checkCollectionProvenance(t, declaration, collectionProvenanceUse{"raw Values", "", "Values(raw)[0]", false}, false)
}

func TestConvertedReceiverIdentity(t *testing.T) {
	for _, identity := range []struct {
		declaration, parameter, first, other, want string
	}{
		{"type Stats = s.DependencyStats", "raw s.DependencyStats", "Stats(raw)", "s.DependencyStats(raw)", "violation"},
		{"type Stats = *s.DependencyStats", "raw *s.DependencyStats", "Stats(raw)", "(*s.DependencyStats)(raw)", "violation"},
		{"type Stats[T any] = s.DependencyStats", "raw s.DependencyStats", "Stats[int](raw)", "s.DependencyStats(raw)", "violation"},
		{"", "raw, other s.DependencyStats", "s.DependencyStats(raw)", "s.DependencyStats(other)", "advisory"},
		{"type Stats s.DependencyStats", "raw s.DependencyStats", "s.DependencyStats(raw)", "Stats(raw)", "advisory"},
		{"type Values []s.DependencyStats", "raw []s.DependencyStats", "Values(raw)[0]", "([]s.DependencyStats)(raw)[0]", "advisory"},
		{"", "values []s.DependencyStats, index uint64", "values[uint16(index)]", "values[uint32(index)]", "advisory"},
	} {
		t.Run(identity.first+identity.other, func(t *testing.T) {
			source := strings.NewReplacer("measured s.DependencyStats", identity.parameter, "measured.", identity.first+".").Replace(mappingFixture)
			source = strings.Replace(source, identity.first+".UsedCount", identity.other+".UsedCount", 1)
			checkPromotedReportFindings(t, map[string][]byte{"fixture.go": []byte(source + "\n" + identity.declaration)}, identity.want)
		})
	}
}
