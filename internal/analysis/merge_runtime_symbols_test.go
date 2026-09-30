package analysis

import (
	"reflect"
	"testing"

	"github.com/ben-ranford/lopper/internal/report"
)

func TestMergeRuntimeSymbolsRanksCountsBeforeModuleAndSymbol(t *testing.T) {
	left := []report.RuntimeSymbolUsage{
		{Module: "b", Symbol: "a", Count: 2},
		{Module: "a", Symbol: "z", Count: 2},
		{Module: "a", Symbol: "b", Count: 2},
	}
	right := []report.RuntimeSymbolUsage{
		{Module: "z", Symbol: "top", Count: 2},
		{Module: "z", Symbol: "top", Count: 1},
		{Module: "c", Symbol: "a", Count: 2},
		{Module: "d", Symbol: "a", Count: 2},
	}
	beforeLeft := append([]report.RuntimeSymbolUsage(nil), left...)
	beforeRight := append([]report.RuntimeSymbolUsage(nil), right...)
	got := mergeRuntimeSymbolUsage(left, right)
	want := []report.RuntimeSymbolUsage{
		{Module: "z", Symbol: "top", Count: 3},
		{Module: "a", Symbol: "b", Count: 2},
		{Module: "a", Symbol: "z", Count: 2},
		{Module: "b", Symbol: "a", Count: 2},
		{Module: "c", Symbol: "a", Count: 2},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("merged symbols = %#v, want %#v", got, want)
	}
	if !reflect.DeepEqual(left, beforeLeft) || !reflect.DeepEqual(right, beforeRight) {
		t.Fatal("merging modified the source slices")
	}
	if got := mergeRuntimeSymbolUsage(nil, nil); !reflect.DeepEqual(got, []report.RuntimeSymbolUsage{}) {
		t.Fatalf("empty merge must retain a non-nil slice, got %#v", got)
	}
}
