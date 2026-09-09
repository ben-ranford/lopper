package rust

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
)

func TestRustMalformedRootFallbackPreservesChildCrateLocalModules(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, cargoTomlName), "[package\nname = \"broken-root\"\n")
	writeFile(t, filepath.Join(repo, "child", cargoTomlName), `[package]
name = "child"
version = "0.1.0"

[dependencies]
foo = "1"
`)
	writeFile(t, filepath.Join(repo, "child", "src", "lib.rs"), "use foo::Thing;\npub fn use_local() { let _ = Thing; }\n")
	writeFile(t, filepath.Join(repo, "child", "src", "foo.rs"), "pub struct Thing;\n")

	result, err := NewAdapter().Analyse(context.Background(), language.Request{RepoPath: repo, Dependency: "foo"})
	if err != nil {
		t.Fatalf("analyse malformed root Cargo manifest: %v", err)
	}
	if len(result.Dependencies) != 1 || result.Dependencies[0].Name != "foo" {
		t.Fatalf("expected child dependency report, got %#v", result.Dependencies)
	}
	if result.Dependencies[0].UsedExportsCount != 0 {
		t.Fatalf("expected child local module to avoid external dependency attribution, got %#v", result.Dependencies)
	}
}
