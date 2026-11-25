package js

import (
	"fmt"
	"slices"
	"testing"
)

func TestReExportResolutionBudget(t *testing.T) {
	// A linear chain exercises the default work limit without timing or filesystem dependencies.
	const moduleCount = 48
	files := make([]FileScan, 0, moduleCount)
	for index := 0; index < moduleCount-1; index++ {
		files = append(files, FileScan{
			Path: fmt.Sprintf("src/module%d.ts", index),
			ReExports: []ReExportBinding{{
				SourceModule: fmt.Sprintf("./module%d", index+1), SourceExportName: "*", ExportName: "*",
			}},
		})
	}
	files = append(files, FileScan{
		Path:      fmt.Sprintf("src/module%d.ts", moduleCount-1),
		ReExports: []ReExportBinding{{SourceModule: "lodash", SourceExportName: "map", ExportName: "map"}},
	})
	resolver := newReExportResolver(ScanResult{Files: files})
	_, ok := resolver.resolveImportAttribution(testImporterPath, ImportBinding{
		Module: "./module0", ExportName: "map", Kind: ImportNamed,
	}, "lodash")
	warnings := resolver.warnings()
	if !slices.Contains(warnings, "re-export attribution exceeded resolution budget") {
		t.Fatalf("expected a resolution budget warning, got %v", warnings)
	}
	if ok {
		t.Fatal("expected the dependency beyond the resolution budget to remain unattributed")
	}
}
