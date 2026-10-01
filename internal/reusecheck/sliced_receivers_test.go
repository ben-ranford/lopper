package reusecheck

import (
	"strings"
	"testing"
)

func TestDirectSlicedStatsReceivers(t *testing.T) {
	for _, receiver := range []string{
		"values[:][0]", "values[1:][0]", "values[:2][0]", "values[1:2][0]",
		"values[:2:3][0]", "values[1:2:3][0]", "values[low:high:maximum][index]",
		"values[(low):high+1:maximum-1][index]", "values[0:3][:2][0]",
		"values[int(low):][0]", "values[bounds[:][0]:][0]",
		"([]s.DependencyStats)(values)[:][0]",
	} {
		checkDirectStatsReceivers(t, []statsReceiverCase{{"values []s.DependencyStats, low, high, maximum, index int, bounds []int", receiver, true}})
	}
	checkDirectStatsReceivers(t, []statsReceiverCase{
		{"values [3]s.DependencyStats", "values[0:2:3][0]", true},
		{"values *[3]s.DependencyStats", "values[:][0]", true},
		{"values []*s.DependencyStats", "(*values[:][0])", true},
		{"raw any", "raw.([]s.DependencyStats)[:][0]", true},
	})
}

func TestSlicedReceiverOperandGuards(t *testing.T) {
	for _, receiver := range []string{
		"values[next():][0]", "values[:next()][0]", "values[:1:next()][0]",
		"values[<-bounds:][0]", "values[:<-bounds][0]", "values[:1:<-bounds][0]",
		"factory()[:][0]", "(<-collections)[:][0]", "values[missing:][0]",
	} {
		checkDirectStatsReceivers(t, []statsReceiverCase{{"values []s.DependencyStats, bounds chan int, collections chan []s.DependencyStats, next func() int, factory func() []s.DependencyStats", receiver, false}})
	}
	checkDirectStatsReceivers(t, []statsReceiverCase{
		{"values map[int]s.DependencyStats", "values[:][0]", false},
		{"values []OtherStats", "values[:][0]", false},
	})
}

func TestSlicedReceiverIdentity(t *testing.T) {
	for _, pair := range [][2]string{
		{"values[:][0]", "values[0:][0]"},
		{"values[:2][0]", "values[2:][0]"},
		{"values[low:high][0]", "values[high:low][0]"},
		{"values[:2][0]", "values[:2:3][0]"},
		{"values[:2:3][0]", "values[:2:4][0]"},
		{"values[:2:3][0]", "values[0:2:3][0]"},
		{"values[:][0]", "other[:][0]"},
		{"values[:][0]", "values[:][1]"},
		{"values[int16(low):][0]", "values[int32(low):][0]"},
	} {
		source := strings.NewReplacer("measured s.DependencyStats", "values, other []s.DependencyStats, low, high int", "measured.", pair[0]+".").Replace(mappingFixture)
		source = strings.Replace(source, pair[0]+".UsedCount", pair[1]+".UsedCount", 1)
		checkPromotedReportFindings(t, map[string][]byte{"fixture.go": []byte(source)}, "advisory")
	}
}

func TestSlicedReceiverAliases(t *testing.T) {
	for _, declaration := range []string{
		"type Values = []s.DependencyStats", "type Values []s.DependencyStats",
	} {
		checkCollectionProvenance(t, declaration, collectionProvenanceUse{"values Values", "", "values[:][0]", false}, true)
	}
	checkCollectionProvenance(t, "type Values[T any] []s.DependencyStats", collectionProvenanceUse{"values Values[int]", "", "values[:][0]", false}, true)
	checkAssertedIdentity(t, "type Values = []s.DependencyStats", "", "raw.(Values)[:][0]", "raw.([]s.DependencyStats)[:][0]", false)
	for _, declaration := range []string{
		"type Values = []Values", "type Values = Other; type Other = Values",
		"type Stats s.DependencyStats; type Values []Stats",
		"type Values = [int(Values(0)[:][0].UsedCount)]s.DependencyStats",
		"type Values = [([]int)(nil)[int(Values(0)[0].UsedCount):][0]]s.DependencyStats",
	} {
		checkCollectionProvenance(t, declaration, collectionProvenanceUse{"values Values", "", "Values(values)[:][0]", false}, false)
	}
}

func TestSlicedPromotedReceiverIdentity(t *testing.T) {
	for _, pair := range [][2]string{
		{"h.values[h.low:h.high:h.maximum][0]", "h.Inner.values[h.Inner.low:h.Inner.high:h.Inner.maximum][0]"},
		{"h.values[int(h.low):int(h.high):int(h.maximum)][0]", "h.Inner.values[int(h.Inner.low):int(h.Inner.high):int(h.Inner.maximum)][0]"},
		{"([]s.DependencyStats)(h.values)[:][0]", "([]s.DependencyStats)(h.Inner.values)[:][0]"},
	} {
		source := strings.NewReplacer("measured s.DependencyStats", "h Holder", "measured.", pair[0]+".").Replace(mappingFixture)
		source = strings.Replace(source, pair[0]+".UsedCount", pair[1]+".UsedCount", 1)
		source += "\ntype Inner struct { values []s.DependencyStats; low, high, maximum int }; type Holder struct { Inner }"
		checkPromotedReportFindings(t, map[string][]byte{"fixture.go": []byte(source)}, "violation")
	}
}
