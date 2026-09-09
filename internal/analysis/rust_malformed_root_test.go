package analysis

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestServiceAnalyseRustMalformedRootRetainsRootUsageAndWarning(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "Cargo.toml"), "[package\nname = \"broken-root\"\n")
	writeFile(t, filepath.Join(repo, "child", "Cargo.toml"), `[package]
name = "child"
version = "0.1.0"

[dependencies]
serde_json = "1"
`)
	writeFile(t, filepath.Join(repo, "src", "main.rs"), "use serde_json::Value;\nfn main() { let _ = Value::Null; }\n")
	writeFile(t, filepath.Join(repo, "child", "src", "main.rs"), "fn main() {}\n")

	reportData, err := NewService().Analyse(context.Background(), Request{
		RepoPath:   repo,
		Language:   "rust",
		Dependency: "serde_json",
		Cache:      &CacheOptions{Enabled: false},
	})
	if err != nil {
		t.Fatalf("analyse malformed root Rust fixture: %v", err)
	}
	if reportData.Scope == nil || !slices.Equal(reportData.Scope.Packages, []string{"."}) {
		t.Fatalf("expected one whole-repository scope after malformed root fallback, got %#v", reportData.Scope)
	}

	var serdeJSONUsage int
	for _, dependency := range reportData.Dependencies {
		if dependency.Language == "rust" && dependency.Name == "serde-json" {
			serdeJSONUsage += dependency.UsedExportsCount
		}
	}
	if serdeJSONUsage != 1 {
		t.Fatalf("expected exactly one root serde-json usage without overlapping scopes, got %d from %#v", serdeJSONUsage, reportData.Dependencies)
	}
	if !strings.Contains(strings.Join(reportData.Warnings, "\n"), "skipped malformed Cargo manifest Cargo.toml") {
		t.Fatalf("expected malformed root Cargo.toml warning, got %#v", reportData.Warnings)
	}
}
