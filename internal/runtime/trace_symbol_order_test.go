package runtime

import (
	"reflect"
	"testing"

	"github.com/ben-ranford/lopper/internal/report"
)

func TestRuntimeSymbolsRanksCountsBeforeSymbolAndModule(t *testing.T) {
	got := runtimeSymbols(map[string]int{
		"z\x00top": 3,
		"a\x00z":   2,
		"z\x00a":   2,
		"a\x00a":   2,
		"b\x00a":   2,
		"legacy":   2,
	})
	want := []report.RuntimeSymbolUsage{
		{Module: "z", Symbol: "top", Count: 3},
		{Module: "a", Symbol: "a", Count: 2},
		{Module: "b", Symbol: "a", Count: 2},
		{Module: "z", Symbol: "a", Count: 2},
		{Symbol: "legacy", Count: 2},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("runtime symbols = %#v, want %#v", got, want)
	}
	if !reflect.DeepEqual(runtimeSymbols(nil), []report.RuntimeSymbolUsage(nil)) {
		t.Fatal("missing runtime counts must retain a nil slice")
	}
}
