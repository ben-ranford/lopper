package js

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveDependencyExports(t *testing.T) {
	repo := t.TempDir()
	depDir := filepath.Join(repo, "node_modules", "sample")
	if err := os.MkdirAll(depDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	packageJSON := "{\n  \"main\": \"index.js\"\n}\n"
	if err := os.WriteFile(filepath.Join(depDir, "package.json"), []byte(packageJSON), 0o644); err != nil {
		t.Fatalf("write package.json: %v", err)
	}
	entrypoint := "export const alpha = 1\nexport function beta() {}\nexport default function () {}\n"
	if err := os.WriteFile(filepath.Join(depDir, "index.js"), []byte(entrypoint), 0o644); err != nil {
		t.Fatalf("write entrypoint: %v", err)
	}

	surface, err := resolveDependencyExports(repo, "sample")
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
	depDir := filepath.Join(repo, "node_modules", "styled")
	if err := os.MkdirAll(depDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	packageJSON := "{\n  \"exports\": {\n    \"default\": {\n      \"styles\": \"./styles.css\",\n      \"import\": \"./index.js\"\n    }\n  }\n}\n"
	if err := os.WriteFile(filepath.Join(depDir, "package.json"), []byte(packageJSON), 0o644); err != nil {
		t.Fatalf("write package.json: %v", err)
	}
	entrypoint := "export const theme = {}\n"
	if err := os.WriteFile(filepath.Join(depDir, "index.js"), []byte(entrypoint), 0o644); err != nil {
		t.Fatalf("write entrypoint: %v", err)
	}

	surface, err := resolveDependencyExports(repo, "styled")
	if err != nil {
		t.Fatalf("resolve exports: %v", err)
	}

	if len(surface.Names) == 0 {
		t.Fatalf("expected export surface to include js entrypoint")
	}

	foundWarning := false
	for _, warning := range surface.Warnings {
		if strings.Contains(warning, "styles") {
			foundWarning = true
			break
		}
	}
	if !foundWarning {
		t.Fatalf("expected warning for non-js export condition")
	}
}
