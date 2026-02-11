package js

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
)

func mustWriteFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func writeNumberedTextFiles(t *testing.T, repo string, count int) {
	t.Helper()
	for i := 0; i < count; i++ {
		mustWriteFile(t, filepath.Join(repo, "f-"+strconv.Itoa(i)+".txt"), "x")
	}
}

func TestJSDetectWithConfidenceEmptyRepoPathAndRootFallback(t *testing.T) {
	adapter := NewAdapter()

	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	repo := t.TempDir()
	mustWriteFile(t, filepath.Join(repo, "index.js"), "export const x = 1")
	if err := os.Chdir(repo); err != nil {
		t.Fatalf("chdir repo: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(originalWD) })

	detection, err := adapter.DetectWithConfidence(context.Background(), "")
	if err != nil {
		t.Fatalf("detect with confidence: %v", err)
	}
	if !detection.Matched {
		t.Fatalf("expected matched detection")
	}
	if detection.Confidence != 35 {
		t.Fatalf("expected floor confidence 35 for source-only detection, got %d", detection.Confidence)
	}
	if len(detection.Roots) != 1 || detection.Roots[0] != "." {
		t.Fatalf("expected roots fallback to repo path, got %#v", detection.Roots)
	}
}

func TestJSScanFilesForDetectionMaxFiles(t *testing.T) {
	repo := t.TempDir()
	writeNumberedTextFiles(t, repo, 260)

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
	used := applyImportUsage(
		ImportBinding{Kind: ImportKind("other"), ExportName: "x", LocalName: "x"},
		FileScan{},
		usedExports,
		counts,
	)
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
	flattened := flattenImportUses(imports)
	if len(flattened) != 1 || len(flattened[0].Locations) != 2 {
		t.Fatalf("expected merged import locations, got %#v", flattened)
	}

	filtered := removeOverlaps(
		[]report.ImportUse{{Name: "map", Module: "lodash"}, {Name: "filter", Module: "lodash"}},
		[]report.ImportUse{{Name: "map", Module: "lodash"}},
	)
	if len(filtered) != 1 || filtered[0].Name != "filter" {
		t.Fatalf("expected overlap removal, got %#v", filtered)
	}

	if score, ok := wasteScore(report.DependencyReport{TotalExportsCount: 0}); ok || score != -1 {
		t.Fatalf("expected unknown waste score for zero exports, got score=%f ok=%v", score, ok)
	}
	if score, ok := wasteScore(report.DependencyReport{UsedExportsCount: 1, TotalExportsCount: 4, UsedPercent: 0}); !ok || score != 75 {
		t.Fatalf("expected computed waste score 75, got score=%f ok=%v", score, ok)
	}

	for _, module := range []string{"./local", "@scope", "@/pkg"} {
		if dep := dependencyFromModule(module); dep != "" {
			t.Fatalf("expected empty dependency for module %q, got %q", module, dep)
		}
	}

	if dependencyExists("", "dep") {
		t.Fatalf("expected dependencyExists false for invalid repo path")
	}

	if warnings := dependencyUsageWarnings("dep", map[string]struct{}{}, true); len(warnings) != 2 {
		t.Fatalf("expected both no-usage and wildcard warnings, got %#v", warnings)
	}
}

func TestResolveSurfaceWarningsBranches(t *testing.T) {
	surface, warnings := resolveSurfaceWarnings("", "dep")
	if len(surface.Names) != 0 {
		t.Fatalf("expected empty surface on resolution error")
	}
	if len(warnings) == 0 {
		t.Fatalf("expected warning from surface resolution error")
	}

	repo := t.TempDir()
	depRoot := filepath.Join(repo, "node_modules", "wild")
	mustWriteFile(t, filepath.Join(depRoot, "package.json"), `{"main":"index.js"}`)
	source := strings.Join([]string{
		`export * from "./other.js"`,
		`export const keep = 1`,
		"",
	}, "\n")
	mustWriteFile(t, filepath.Join(depRoot, "index.js"), source)
	mustWriteFile(t, filepath.Join(depRoot, "other.js"), "export const x = 1")

	surface, warnings = resolveSurfaceWarnings(repo, "wild")
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
	reports, warnings := buildTopDependencies(repo, ScanResult{}, 5)
	if reports != nil {
		t.Fatalf("expected nil reports when no dependencies are discovered, got %#v", reports)
	}
	if len(warnings) != 0 {
		t.Fatalf("did not expect warnings from empty scan result, got %#v", warnings)
	}
}
