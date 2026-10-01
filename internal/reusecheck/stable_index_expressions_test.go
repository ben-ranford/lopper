package reusecheck

import "testing"

func TestPureStatsIndexExpressions(t *testing.T) {
	for _, index := range []string{
		"+i", "-i", "^i", "i + 1", "i - 1", "i * 2", "i / 2", "i % 2",
		"i << 1", "i >> 1", "i & 1", "i | 1", "i ^ 1", "i &^ 1",
		"(i + 1) * (j - 1)", "*offset + 1", "holder.offset + 1",
	} {
		checkDirectStatsReceivers(t, []statsReceiverCase{{
			"values []s.DependencyStats, i, j int, offset *int, holder struct { offset int }",
			"values[" + index + "]", true,
		}})
	}
	for _, index := range []string{"!ready", "i < j", "i <= j", "i > j", "i >= j", "i == j", "i != j", "ready && other", "ready || other"} {
		checkDirectStatsReceivers(t, []statsReceiverCase{{"values map[bool]s.DependencyStats, i, j int, ready, other bool", "values[" + index + "]", true}})
	}
	checkDirectStatsReceivers(t, []statsReceiverCase{{"values map[string]s.DependencyStats, key string", `values[key + "suffix"]`, true}})
}

func TestImpureStatsIndexExpressions(t *testing.T) {
	for _, index := range []string{"next() + i", "i + next()", "-next()", "<-indices", "(<-indices) + i", "i + (<-indices)"} {
		checkDirectStatsReceivers(t, []statsReceiverCase{{"values []s.DependencyStats, i int, next func() int, indices chan int", "values[" + index + "]", false}})
	}
	for _, index := range []string{"ready && next()", "ready || (<-flags)"} {
		checkDirectStatsReceivers(t, []statsReceiverCase{{"values map[bool]s.DependencyStats, ready bool, next func() bool, flags chan bool", "values[" + index + "]", false}})
	}
}
