package js

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveDependencyExports(t *testing.T) {
	repo := t.TempDir()
	writeDependencyFixture(t, repo, "sample", "{\n  \"main\": \"index.js\"\n}\n", "export const alpha = 1\nexport function beta() {}\nexport default function () {}\n")

	surface, err := resolveDependencyExports(repo, "sample", "", "")
	if err != nil {
		t.Fatalf("resolve exports: %v", err)
	}

	for _, name := range []string{"alpha", "beta", "default"} {
		if _, ok := surface.Names[name]; !ok {
			t.Fatalf("expected export %q", name)
		}
	}
}

func TestResolveDependencyExportsSkipsNonCodeAssets(t *testing.T) {
	repo := t.TempDir()
	writeDependencyFixture(t, repo, "styled", "{\n  \"exports\": {\n    \"default\": {\n      \"styles\": \"./styles.css\",\n      \"import\": \"./index.js\"\n    }\n  }\n}\n", "export const theme = {}\n")

	surface, err := resolveDependencyExports(repo, "styled", "", "")
	if err != nil {
		t.Fatalf("resolve exports: %v", err)
	}

	if len(surface.Names) == 0 {
		t.Fatalf("expected export surface to include js entrypoint")
	}
	if _, ok := surface.Names["theme"]; !ok {
		t.Fatalf("expected resolved exports to include import branch symbol")
	}
}

func TestResolveDependencyExportsUsesRuntimeProfile(t *testing.T) {
	repo := t.TempDir()
	pkg := "{\n  \"exports\": {\n    \".\": {\n      \"import\": \"./import.js\",\n      \"require\": \"./require.js\"\n    }\n  }\n}\n"
	writeDependencyFiles(t, repo, "profiled", pkg, map[string]string{
		"import.js":  "export const importOnly = 1\n",
		"require.js": "export const requireOnly = 1\n",
	})

	importSurface, err := resolveDependencyExports(repo, "profiled", "", "node-import")
	if err != nil {
		t.Fatalf("resolve import profile exports: %v", err)
	}
	if _, ok := importSurface.Names["importOnly"]; !ok {
		t.Fatalf("expected import profile to resolve import branch, got %#v", importSurface.Names)
	}
	if _, ok := importSurface.Names["requireOnly"]; ok {
		t.Fatalf("did not expect require export in import profile")
	}

	requireSurface, err := resolveDependencyExports(repo, "profiled", "", "node-require")
	if err != nil {
		t.Fatalf("resolve require profile exports: %v", err)
	}
	if _, ok := requireSurface.Names["requireOnly"]; !ok {
		t.Fatalf("expected require profile to resolve require branch, got %#v", requireSurface.Names)
	}
	if _, ok := requireSurface.Names["importOnly"]; ok {
		t.Fatalf("did not expect import export in require profile")
	}
}

func TestResolveDependencyExportsWarnsOnAmbiguousConditionMap(t *testing.T) {
	repo := t.TempDir()
	pkg := "{\n  \"exports\": {\n    \".\": {\n      \"node\": \"./node.js\",\n      \"import\": \"./import.js\",\n      \"default\": \"./default.js\"\n    }\n  }\n}\n"
	writeDependencyFiles(t, repo, "ambiguous", pkg, map[string]string{
		"node.js":    "export const fromNode = 1\n",
		"import.js":  "export const fromImport = 1\n",
		"default.js": "export const fromDefault = 1\n",
	})

	surface, err := resolveDependencyExports(repo, "ambiguous", "", "node-import")
	if err != nil {
		t.Fatalf("resolve exports: %v", err)
	}
	if _, ok := surface.Names["fromNode"]; !ok {
		t.Fatalf("expected node branch selection for node-import profile, got %#v", surface.Names)
	}

	joined := strings.Join(surface.Warnings, "\n")
	if !strings.Contains(joined, "ambiguous export conditions") {
		t.Fatalf("expected ambiguous-condition warning, got %#v", surface.Warnings)
	}
}

func writeDependencyFixture(t *testing.T, repoPath string, depName string, packageJSON string, entrypoint string) {
	t.Helper()
	depDir := filepath.Join(repoPath, "node_modules", depName)
	if err := os.MkdirAll(depDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(depDir, "package.json"), []byte(packageJSON), 0o644); err != nil {
		t.Fatalf("write package.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(depDir, "index.js"), []byte(entrypoint), 0o644); err != nil {
		t.Fatalf("write entrypoint: %v", err)
	}
}

func writeDependencyFiles(t *testing.T, repoPath string, depName string, packageJSON string, files map[string]string) {
	t.Helper()
	depDir := filepath.Join(repoPath, "node_modules", depName)
	if err := os.MkdirAll(depDir, 0o755); err != nil {
		t.Fatalf("mkdir dep dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(depDir, "package.json"), []byte(packageJSON), 0o644); err != nil {
		t.Fatalf("write package.json: %v", err)
	}
	for name, contents := range files {
		if err := os.WriteFile(filepath.Join(depDir, name), []byte(contents), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
}
