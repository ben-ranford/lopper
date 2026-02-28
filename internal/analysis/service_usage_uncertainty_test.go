package analysis

import (
	"testing"

	"github.com/ben-ranford/lopper/internal/report"
)

func TestMergeUsageUncertaintyNilInputs(t *testing.T) {
	if got := mergeUsageUncertainty(nil, nil); got != nil {
		t.Fatalf("expected nil merge result, got %#v", got)
	}
}

func TestMergeUsageUncertaintyClonesSingleSide(t *testing.T) {
	right := &report.UsageUncertainty{
		ConfirmedImportUses: 2,
		UncertainImportUses: 1,
		Samples:             []report.Location{{File: "a.js", Line: 1}},
	}

	got := mergeUsageUncertainty(nil, right)
	if got == nil || got.ConfirmedImportUses != 2 || got.UncertainImportUses != 1 || len(got.Samples) != 1 {
		t.Fatalf("unexpected merge result: %#v", got)
	}

	right.Samples[0].File = "mutated.js"
	if got.Samples[0].File != "a.js" {
		t.Fatalf("expected clone of sample slice, got %#v", got.Samples)
	}
}

func TestMergeUsageUncertaintyCombinesAndCapsSamples(t *testing.T) {
	left := &report.UsageUncertainty{
		ConfirmedImportUses: 3,
		UncertainImportUses: 2,
		Samples: []report.Location{
			{File: "l1.js", Line: 1},
			{File: "l2.js", Line: 2},
			{File: "l3.js", Line: 3},
		},
	}
	right := &report.UsageUncertainty{
		ConfirmedImportUses: 4,
		UncertainImportUses: 5,
		Samples: []report.Location{
			{File: "r1.js", Line: 4},
			{File: "r2.js", Line: 5},
			{File: "r3.js", Line: 6},
		},
	}

	got := mergeUsageUncertainty(left, right)
	if got == nil {
		t.Fatalf("expected merge result")
	}
	if got.ConfirmedImportUses != 7 || got.UncertainImportUses != 7 {
		t.Fatalf("unexpected aggregate counts: %#v", got)
	}
	if len(got.Samples) != 5 {
		t.Fatalf("expected capped sample count 5, got %d", len(got.Samples))
	}
	if got.Samples[0].File != "l1.js" || got.Samples[4].File != "r2.js" {
		t.Fatalf("unexpected sample merge order: %#v", got.Samples)
	}
}
