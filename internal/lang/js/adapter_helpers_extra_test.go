package js

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/testutil"
	"github.com/ben-ranford/lopper/internal/thresholds"
)

const pkgLodashProvenance = "pkg:lodash"

func TestJSDetectWithConfidenceEmptyRepoPathAndRootFallback(t *testing.T) {
	adapter := NewAdapter()

	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, testIndexJS), "export const x = 1")
	testutil.Chdir(t, repo)

	if detection, err := adapter.DetectWithConfidence(context.Background(), ""); err != nil {
		t.Fatalf("detect with confidence: %v", err)
	} else if !detection.Matched || detection.Confidence != 35 || len(detection.Roots) != 1 || detection.Roots[0] != "." {
		t.Fatalf("unexpected empty-repo detection result: %#v", detection)
	}
}

func TestJSScanFilesForDetectionMaxFiles(t *testing.T) {
	repo := t.TempDir()
	testutil.WriteNumberedTextFiles(t, repo, 260)

	detect := &language.Detection{Matched: false}
	roots := map[string]struct{}{}
	err := scanFilesForJSDetection(repo, detect, roots)
	if err != io.EOF {
		t.Fatalf("expected io.EOF when max files exceeded, got %v", err)
	}
}

func TestJSAdapterHelperBranchesExtra(t *testing.T) {
	usedExports := map[string]struct{}{}
	counts := map[string]int{}
	used := applyImportUsage(ImportBinding{Kind: ImportKind("other"), ExportName: "x", LocalName: "x"}, FileScan{}, usedExports, counts)
	if used {
		t.Fatalf("expected unsupported import kind to return false")
	}

	imports := map[string]*report.ImportUse{}
	addImportUse(imports, report.ImportUse{
		Name:      "map",
		Module:    "lodash",
		Locations: []report.Location{{File: "a.js", Line: 1}},
	})
	addImportUse(imports, report.ImportUse{
		Name:      "map",
		Module:    "lodash",
		Locations: []report.Location{{File: "b.js", Line: 2}},
	})
	addImportUse(imports, report.ImportUse{
		Name:       "map",
		Module:     "lodash",
		Locations:  []report.Location{{File: "c.js", Line: 3}},
		Provenance: []string{pkgLodashProvenance, pkgLodashProvenance},
	})
	flattened := flattenImportUses(imports)
	if len(flattened) != 1 || len(flattened[0].Locations) != 3 {
		t.Fatalf("expected merged import locations, got %#v", flattened)
	}
	if len(flattened[0].Provenance) != 1 || flattened[0].Provenance[0] != pkgLodashProvenance {
		t.Fatalf("expected deduped provenance, got %#v", flattened[0].Provenance)
	}

	filtered := removeOverlappingUnusedImports([]report.ImportUse{{Name: "map", Module: "lodash"}, {Name: "filter", Module: "lodash"}}, []report.ImportUse{{Name: "map", Module: "lodash"}})
	if len(filtered) != 1 || filtered[0].Name != "filter" {
		t.Fatalf("expected overlap removal, got %#v", filtered)
	}
	usedImportList, unusedImportList := finalizeImportUsageLists(map[string]*report.ImportUse{"lodash:map": {Name: "map", Module: "lodash"}}, map[string]*report.ImportUse{"lodash:map": {Name: "map", Module: "lodash"}, "lodash:filter": {Name: "filter", Module: "lodash"}})
	if len(usedImportList) != 1 || usedImportList[0].Name != "map" {
		t.Fatalf("expected flattened used imports, got %#v", usedImportList)
	}
	if len(unusedImportList) != 1 || unusedImportList[0].Name != "filter" {
		t.Fatalf("expected overlap-filtered unused imports, got %#v", unusedImportList)
	}

	if score, ok := shared.WasteScore(report.DependencyReport{TotalExportsCount: 0}); ok || score != -1 {
		t.Fatalf("expected unknown waste score for zero exports, got score=%f ok=%v", score, ok)
	}
	if score, ok := shared.WasteScore(report.DependencyReport{UsedExportsCount: 1, TotalExportsCount: 4, UsedPercent: 25}); !ok || score != 75 {
		t.Fatalf("expected computed waste score 75, got score=%f ok=%v", score, ok)
	}

	for _, module := range []string{"./local", "@scope", "@/pkg"} {
		if dep := dependencyFromModule(module); dep != "" {
			t.Fatalf("expected empty dependency for module %q, got %q", module, dep)
		}
	}

	if got := resolveDependencyRootFromImporter(dependencyResolutionRequest{
		RepoPath:     "",
		ImporterPath: "",
		Dependency:   "dep",
	}); got != "" {
		t.Fatalf("expected empty resolution for invalid repo path, got %q", got)
	}
	repo := t.TempDir()
	outside := t.TempDir()
	if got := resolveDependencyRootFromImporter(dependencyResolutionRequest{
		RepoPath:     repo,
		ImporterPath: filepath.Join(outside, testIndexJS),
		Dependency:   "dep",
	}); got != "" {
		t.Fatalf("expected empty resolution for importer outside repo root, got %q", got)
	}

	if warnings := dependencyUsageWarnings("dep", map[string]struct{}{}, true); len(warnings) != 2 {
		t.Fatalf("expected both no-usage and wildcard warnings, got %#v", warnings)
	}

	if !isPathWithin(filepath.Join(repo, "sub", "a.js"), repo) {
		t.Fatalf("expected file under repo to be within root")
	}
	if isPathWithin(filepath.Dir(repo), repo) {
		t.Fatalf("expected parent directory to be outside root")
	}
}

func TestResolveSurfaceWarningsBranches(t *testing.T) {
	surface, warnings := resolveSurfaceWarnings("", "dep", "", "")
	if len(surface.Names) != 0 {
		t.Fatalf("expected empty surface on resolution error")
	}
	if len(warnings) == 0 {
		t.Fatalf("expected warning from surface resolution error")
	}

	repo := t.TempDir()
	depRoot := filepath.Join(repo, "node_modules", "wild")
	testutil.MustWriteFile(t, filepath.Join(depRoot, "package.json"), `{"main":"`+testIndexJS+`"}`)
	sourceLines := []string{`export * from "./other.js"`, `export const keep = 1`, ""}
	source := strings.Join(sourceLines, "\n")

	testutil.MustWriteFile(t, filepath.Join(depRoot, testIndexJS), source)
	testutil.MustWriteFile(t, filepath.Join(depRoot, "other.js"), "export const x = 1")

	surface, warnings = resolveSurfaceWarnings(repo, "wild", "", "")
	if !surface.IncludesWildcard {
		t.Fatalf("expected wildcard export surface")
	}
	joined := strings.Join(warnings, "\n")
	if !strings.Contains(joined, "wildcard re-exports") {
		t.Fatalf("expected wildcard warning, got %#v", warnings)
	}
}

func TestBuildTopDependenciesNoResolvedDependencies(t *testing.T) {
	repo := t.TempDir()
	reports, warnings := buildTopDependencies(repo, ScanResult{}, 5, "", thresholds.Defaults().MinUsagePercentForRecommendations, report.DefaultRemovalCandidateWeights(), false)
	if len(reports) != 0 {
		t.Fatalf("expected nil reports when no dependencies are discovered, got %#v", reports)
	}
	if len(warnings) != 0 {
		t.Fatalf("did not expect warnings from empty scan result, got %#v", warnings)
	}
}

func TestSummarizeUsageUncertainty(t *testing.T) {
	summary := summarizeUsageUncertainty(ScanResult{
		Files: []FileScan{
			{
				Imports: []ImportBinding{{Module: "lodash"}},
				UncertainImports: []report.ImportUse{
					{Locations: []report.Location{{File: testIndexJS, Line: 2}}},
				},
			},
		},
	})
	if summary == nil {
		t.Fatalf("expected usage uncertainty summary")
	}
	if summary.ConfirmedImportUses != 1 || summary.UncertainImportUses != 1 {
		t.Fatalf("unexpected uncertainty summary counts: %#v", summary)
	}
	if len(summary.Samples) != 1 || summary.Samples[0].File != testIndexJS {
		t.Fatalf("unexpected uncertainty samples: %#v", summary.Samples)
	}
}
