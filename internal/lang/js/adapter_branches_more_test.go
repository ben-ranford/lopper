package js

import (
	"testing"

	"github.com/ben-ranford/lopper/internal/report"
)

func TestBuildTopSymbolsCapsAndSorts(t *testing.T) {
	symbols := buildTopSymbols(map[string]int{
		"z": 1,
		"a": 3,
		"b": 3,
		"c": 2,
		"d": 2,
		"e": 1,
		"f": 1,
	})
	if len(symbols) != 5 {
		t.Fatalf("expected top symbol cap of 5, got %d", len(symbols))
	}
	if symbols[0].Name != "a" || symbols[1].Name != "b" {
		t.Fatalf("expected sorted top symbols by count then name, got %#v", symbols)
	}
}

func TestFlattenImportUsesAndUnknownImportKind(t *testing.T) {
	source := map[string]*report.ImportUse{
		"b:x": {Name: "x", Module: "b"},
		"a:y": {Name: "y", Module: "a"},
		"a:x": {Name: "x", Module: "a"},
	}
	flattened := flattenImportUses(source)
	if len(flattened) != 3 {
		t.Fatalf("expected 3 flattened import uses, got %#v", flattened)
	}
	if flattened[0].Module != "a" || flattened[0].Name != "x" {
		t.Fatalf("expected deterministic module/name sort, got %#v", flattened)
	}

	used := map[string]struct{}{}
	counts := map[string]int{}
	if applyImportUsage(ImportBinding{Kind: ImportKind("unknown"), LocalName: "x", ExportName: "x"}, FileScan{}, used, counts) {
		t.Fatalf("expected unknown import kind to be ignored")
	}
}
