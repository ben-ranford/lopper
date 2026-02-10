package js

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
)

func TestAdapterAnalyseDependency(t *testing.T) {
	repo := t.TempDir()
	source := "import { map, filter as f } from \"lodash\"\nmap([1], (x) => x)\nf([1], Boolean)\n"
	path := filepath.Join(repo, "index.js")
	if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	depDir := filepath.Join(repo, "node_modules", "lodash")
	if err := os.MkdirAll(depDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	packageJSON := "{\n  \"main\": \"index.js\"\n}\n"
	if err := os.WriteFile(filepath.Join(depDir, "package.json"), []byte(packageJSON), 0o644); err != nil {
		t.Fatalf("write package.json: %v", err)
	}
	entrypoint := "export function map() {}\nexport function filter() {}\n"
	if err := os.WriteFile(filepath.Join(depDir, "index.js"), []byte(entrypoint), 0o644); err != nil {
		t.Fatalf("write entrypoint: %v", err)
	}

	adapter := NewAdapter()
	report, err := adapter.Analyse(context.Background(), language.Request{
		RepoPath:   repo,
		Dependency: "lodash",
	})
	if err != nil {
		t.Fatalf("analyse: %v", err)
	}
	if len(report.Dependencies) != 1 {
		t.Fatalf("expected 1 dependency report, got %d", len(report.Dependencies))
	}

	dep := report.Dependencies[0]
	if dep.UsedExportsCount != 2 {
		t.Fatalf("expected 2 used exports, got %d (imports=%v)", dep.UsedExportsCount, dep.UsedImports)
	}

	found := make(map[string]bool)
	for _, imp := range dep.UsedImports {
		if imp.Module == "lodash" {
			found[imp.Name] = true
		}
	}
	if !found["map"] || !found["filter"] {
		t.Fatalf("expected used imports to include map and filter")
	}
}

func TestAdapterAnalyseTopN(t *testing.T) {
	repo := t.TempDir()
	source := "import { used } from \"alpha\"\nimport { unused } from \"beta\"\nused()\n"
	path := filepath.Join(repo, "index.js")
	if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	if err := writeDependency(repo, "alpha", "export function used() {}\n"); err != nil {
		t.Fatalf("write alpha dependency: %v", err)
	}
	if err := writeDependency(repo, "beta", "export function unused() {}\n"); err != nil {
		t.Fatalf("write beta dependency: %v", err)
	}

	adapter := NewAdapter()
	report, err := adapter.Analyse(context.Background(), language.Request{
		RepoPath: repo,
		TopN:     1,
	})
	if err != nil {
		t.Fatalf("analyse: %v", err)
	}
	if len(report.Dependencies) != 1 {
		t.Fatalf("expected 1 dependency report, got %d", len(report.Dependencies))
	}
	if report.Dependencies[0].Name != "beta" {
		t.Fatalf("expected top dependency to be beta, got %q", report.Dependencies[0].Name)
	}
}

func TestAdapterDetectWithPackageJSON(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "package.json"), []byte("{\"name\":\"fixture\"}\n"), 0o644); err != nil {
		t.Fatalf("write package.json: %v", err)
	}

	ok, err := NewAdapter().Detect(context.Background(), repo)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if !ok {
		t.Fatalf("expected detect=true when package.json exists")
	}
}

func TestAdapterDetectWithJSSource(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "index.js"), []byte("export const x = 1\n"), 0o644); err != nil {
		t.Fatalf("write index.js: %v", err)
	}

	ok, err := NewAdapter().Detect(context.Background(), repo)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if !ok {
		t.Fatalf("expected detect=true when JS sources exist")
	}
}

func TestAdapterDetectNoJSSignals(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("# no js\n"), 0o644); err != nil {
		t.Fatalf("write readme: %v", err)
	}

	ok, err := NewAdapter().Detect(context.Background(), repo)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if ok {
		t.Fatalf("expected detect=false when no JS/TS signals exist")
	}
}

func writeDependency(repo string, name string, entrypoint string) error {
	depDir := filepath.Join(repo, "node_modules", name)
	if err := os.MkdirAll(depDir, 0o755); err != nil {
		return err
	}
	packageJSON := "{\n  \"main\": \"index.js\"\n}\n"
	if err := os.WriteFile(filepath.Join(depDir, "package.json"), []byte(packageJSON), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(depDir, "index.js"), []byte(entrypoint), 0o644); err != nil {
		return err
	}
	return nil
}
