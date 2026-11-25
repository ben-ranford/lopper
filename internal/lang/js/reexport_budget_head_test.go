package js

import (
	"strings"
	"testing"
)

func TestReExportResolutionLimitStopsRemainingCandidatesAndMarksUsageIncomplete(t *testing.T) {
	scanResult := ScanResult{Files: []FileScan{
		{Path: "src/index.ts", Imports: []ImportBinding{{Module: "./barrel", ExportName: "map", LocalName: "map", Kind: ImportNamed}}},
		{Path: "src/barrel.ts", ReExports: []ReExportBinding{
			{SourceModule: "unrelated", SourceExportName: "map", ExportName: "*"},
			{SourceModule: "lodash", SourceExportName: "map", ExportName: "*"},
		}},
	}}
	resolver := newReExportResolver(scanResult)
	resolver.resolutionLimit = 4

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

func TestResolveReExportCandidatesBranches(t *testing.T) {
	req := resolveExportRequest{dependency: "lodash", requestedExport: "map"}

	t.Run("stops an exact candidate at the budget", func(t *testing.T) {
		resolver := newReExportResolver(ScanResult{})
		resolver.resolutionLimit = 1
		if _, ok := resolver.resolveReExportCandidates(req, []ReExportBinding{{ExportName: "map", SourceModule: "lodash"}}, nil); ok {
			t.Fatal("expected the exhausted budget to stop the exact candidate")
		}
	})

	t.Run("counts bindings that do not match", func(t *testing.T) {
		resolver := newReExportResolver(ScanResult{})
		resolver.resolutionLimit = 1
		bindings := []ReExportBinding{{ExportName: "first"}, {ExportName: "second"}}
		if _, ok := resolver.resolveReExportCandidates(req, bindings, nil); ok || !resolver.resolutionLimited {
			t.Fatal("expected the binding scan to stop at the budget")
		}
		if _, ok := resolver.resolveExportOrigin(req); ok || resolver.resolutionSteps != 0 {
			t.Fatal("expected exhaustion to stop further work on the same import")
		}
	})

	t.Run("skips unrelated bindings and resolves a wildcard", func(t *testing.T) {
		resolver := newReExportResolver(ScanResult{})
		origin, ok := resolver.resolveReExportCandidates(req, []ReExportBinding{
			{ExportName: "named", SourceModule: "unrelated"},
			{ExportName: "*", SourceModule: "lodash", SourceExportName: "map"},
		}, nil)
		if !ok || origin.dependencyModule != "lodash" || origin.dependencyExport != "map" {
			t.Fatalf("expected wildcard dependency origin, got %#v and %v", origin, ok)
		}
	})

	t.Run("returns unresolved when no candidates match", func(t *testing.T) {
		resolver := newReExportResolver(ScanResult{})
		if _, ok := resolver.resolveReExportCandidates(req, []ReExportBinding{{ExportName: "named", SourceModule: "unrelated"}}, nil); ok {
			t.Fatal("expected no matching re-export candidate")
		}
	})
}
