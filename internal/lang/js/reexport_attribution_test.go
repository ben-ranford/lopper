package js

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
)

func TestAdapterAnalyseReExportAttributionNestedAlias(t *testing.T) {
	repo := t.TempDir()
	mustWriteFile(t, filepath.Join(repo, "src", "index.ts"), strings.Join([]string{
		`import { mapAlias as m } from "./barrel"`,
		`m([1], (x) => x)`,
		"",
	}, "\n"))
	mustWriteFile(t, filepath.Join(repo, "src", "barrel.ts"), `export { remap as mapAlias } from "./leaf"`)
	mustWriteFile(t, filepath.Join(repo, "src", "leaf.ts"), `export { map as remap } from "lodash"`)

	lodashDir := filepath.Join(repo, "node_modules", "lodash")
	mustWriteFile(t, filepath.Join(lodashDir, "package.json"), testPackageJSONMain)
	mustWriteFile(t, filepath.Join(lodashDir, "index.js"), "export function map() {}\nexport function filter() {}\n")

	reportData, err := NewAdapter().Analyse(context.Background(), language.Request{
		RepoPath:   repo,
		Dependency: "lodash",
	})
	if err != nil {
		t.Fatalf(testAnalyseErrFmt, err)
	}
	if len(reportData.Dependencies) != 1 {
		t.Fatalf(testExpectedOneDepFmt, len(reportData.Dependencies))
	}

	dep := reportData.Dependencies[0]
	if dep.UsedExportsCount != 1 {
		t.Fatalf("expected one used export from attribution chain, got %d", dep.UsedExportsCount)
	}
	if len(dep.UsedImports) != 1 {
		t.Fatalf("expected one used import, got %#v", dep.UsedImports)
	}
	if dep.UsedImports[0].Name != "map" {
		t.Fatalf("expected barrel attribution to map to lodash export map, got %#v", dep.UsedImports[0])
	}
	if len(dep.UsedImports[0].Provenance) == 0 {
		t.Fatalf("expected provenance details on used import, got %#v", dep.UsedImports[0])
	}
	if !strings.Contains(dep.UsedImports[0].Provenance[0], "src/index.ts") ||
		!strings.Contains(dep.UsedImports[0].Provenance[0], "src/barrel.ts") ||
		!strings.Contains(dep.UsedImports[0].Provenance[0], "src/leaf.ts") ||
		!strings.Contains(dep.UsedImports[0].Provenance[0], "lodash#map") {
		t.Fatalf("unexpected provenance chain: %#v", dep.UsedImports[0].Provenance)
	}
}

func TestAdapterAnalyseReExportCycleWarning(t *testing.T) {
	repo := t.TempDir()
	mustWriteFile(t, filepath.Join(repo, "src", "index.ts"), strings.Join([]string{
		`import { x } from "./a"`,
		`console.log(x)`,
		"",
	}, "\n"))
	mustWriteFile(t, filepath.Join(repo, "src", "a.ts"), `export { x } from "./b"`)
	mustWriteFile(t, filepath.Join(repo, "src", "b.ts"), `export { x } from "./a"`)

	lodashDir := filepath.Join(repo, "node_modules", "lodash")
	mustWriteFile(t, filepath.Join(lodashDir, "package.json"), testPackageJSONMain)
	mustWriteFile(t, filepath.Join(lodashDir, "index.js"), "export function map() {}\n")

	reportData, err := NewAdapter().Analyse(context.Background(), language.Request{
		RepoPath:   repo,
		Dependency: "lodash",
	})
	if err != nil {
		t.Fatalf(testAnalyseErrFmt, err)
	}

	warnings := strings.Join(reportData.Warnings, "\n")
	if !strings.Contains(warnings, "re-export attribution cycle") {
		t.Fatalf("expected re-export cycle warning, got %#v", reportData.Warnings)
	}
}

func mustWriteFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
