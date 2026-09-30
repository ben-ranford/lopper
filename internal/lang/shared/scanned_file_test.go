package shared

import (
	"reflect"
	"testing"
)

func TestFileUsagesPreservesScanOrderAndStorage(t *testing.T) {
	type annotatedFile struct {
		ScannedFile
		Metadata string
	}
	imports := []ImportRecord{{Dependency: "example", Name: "Symbol"}}
	usage := map[string]int{"Symbol": 2}
	files := []annotatedFile{
		{ScannedFile: ScannedFile{Path: "first", Imports: imports, Usage: usage}, Metadata: "adapter-specific"},
		{ScannedFile: ScannedFile{Path: "second"}},
	}
	got := FileUsages(files)
	want := []FileUsage{{Imports: imports, Usage: usage}, {}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("file usages = %#v, want %#v", got, want)
	}
	if &got[0].Imports[0] != &imports[0] {
		t.Fatal("import projection copied the backing storage")
	}
	usage["Symbol"] = 3
	if got[0].Usage["Symbol"] != 3 {
		t.Fatal("usage projection copied the map")
	}
	if files[0].Metadata != "adapter-specific" || files[0].Path != "first" {
		t.Fatal("projection changed adapter metadata")
	}
}

func TestFileUsagesEmptyScan(t *testing.T) {
	for _, files := range [][]ScannedFile{nil, {}} {
		if got := FileUsages(files); got == nil || len(got) != 0 {
			t.Fatalf("empty scan = %#v, want allocated empty slice", got)
		}
	}
}
