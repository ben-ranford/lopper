package analysis

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestServiceAnalyseRustMalformedRootRetainsRootCoverageWarning(t *testing.T) {
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

	reportData := analyseMalformedManifestFixture(t, repo, "rust", "serde_json")
	if reportData.Scope == nil || !slices.Equal(reportData.Scope.Packages, []string{"."}) {
		t.Fatalf("expected one whole-repository scope after malformed root fallback, got %#v", reportData.Scope)
	}

	var serdeJSONUsage int
	for _, dependency := range reportData.Dependencies {
		if dependency.Language == "rust" && dependency.Name == "serde-json" {
			serdeJSONUsage += dependency.UsedExportsCount
		}
	}
	if serdeJSONUsage != 0 {
		t.Fatalf("expected malformed root imports not to borrow a child declaration, got %d from %#v", serdeJSONUsage, reportData.Dependencies)
	}
	if !strings.Contains(strings.Join(reportData.Warnings, "\n"), "skipped malformed Cargo manifest Cargo.toml") {
		t.Fatalf("expected malformed root Cargo.toml warning, got %#v", reportData.Warnings)
	}
	if !strings.Contains(strings.Join(reportData.Warnings, "\n"), `could not resolve Rust crate alias "serde-json" from Cargo manifests`) {
		t.Fatalf("expected unresolved malformed root source warning, got %#v", reportData.Warnings)
	}
}
