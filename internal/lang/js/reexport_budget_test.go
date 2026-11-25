package js

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ben-ranford/lopper/internal/language"
)

func TestReExportResolutionBudget(t *testing.T) {
	const files = 200
	repo := t.TempDir()
	for i := 0; i < files-1; i++ {
		body := fmt.Sprintf("export * from \"./f%d\"\n", i+1)
		mustWriteFile(t, filepath.Join(repo, "src", fmt.Sprintf("f%d.ts", i)), body)
	}
	mustWriteFile(t, filepath.Join(repo, "src", fmt.Sprintf("f%d.ts", files-1)), "export { map as x } from \"lodash\"\n")
	mustWriteFile(t, filepath.Join(repo, "src", "index.ts"), "import { x } from \"./f0\"\nconsole.log(x)\n")
	lodashDir := filepath.Join(repo, "node_modules", "lodash")
	mustWriteFile(t, filepath.Join(lodashDir, "package.json"), testPackageJSONMain)
	mustWriteFile(t, filepath.Join(lodashDir, "index.js"), "export function map() {}\n")

	started := time.Now()
	reportData, err := NewAdapter().Analyse(context.Background(), language.Request{
		RepoPath:   repo,
		Dependency: "lodash",
	})
	if err != nil {
		t.Fatalf(testAnalyseErrFmt, err)
	}
	if time.Since(started) > 3*time.Second {
		t.Fatalf("re-export resolution exceeded 3s for %d files", files)
	}
	warnings := strings.Join(reportData.Warnings, "\n")
	if !strings.Contains(warnings, "re-export attribution exceeded resolution budget") {
		t.Fatalf("expected resolution budget warning, got %#v", reportData.Warnings)
	}
}

func TestReExportResolutionLimitStopsRemainingCandidatesAndMarksUsageIncomplete(t *testing.T) {
	scanResult := ScanResult{Files: []FileScan{
		{Path: "src/index.ts", Imports: []ImportBinding{{Module: "./barrel", ExportName: "map", LocalName: "map", Kind: ImportNamed}}},
		{Path: "src/barrel.ts", ReExports: []ReExportBinding{
			{SourceModule: "./first", SourceExportName: "*", ExportName: "*"},
			{SourceModule: "lodash", SourceExportName: "map", ExportName: "*"},
		}},
		{Path: "src/first.ts", ReExports: []ReExportBinding{{SourceModule: "./second", SourceExportName: "*", ExportName: "*"}}},
		{Path: "src/second.ts", ReExports: []ReExportBinding{{SourceModule: "./third", SourceExportName: "*", ExportName: "*"}}},
		{Path: "src/third.ts"},
	}}
	resolver := newReExportResolver(scanResult)
	resolver.resolutionLimit = 2

	usage := collectDependencyUsageSummaryWithResolver(scanResult, "lodash", resolver)
	if !usage.usageIncomplete {
		t.Fatalf("expected resolution limit to mark usage incomplete, got %#v", usage)
	}
	if len(usage.usedImports) != 0 || len(usage.unusedImports) != 0 {
		t.Fatalf("expected the fallback dependency candidate to remain unprocessed, got %#v", usage)
	}
	if !strings.Contains(strings.Join(usage.warnings, "\n"), reExportResolutionBudgetWarning) {
		t.Fatalf("expected resolution budget warning, got %#v", usage.warnings)
	}
	if !dependencyCoverageIncomplete(scanResult, ExportSurface{}, usage) {
		t.Fatal("expected incomplete re-export attribution to suppress removal signals")
	}
}
