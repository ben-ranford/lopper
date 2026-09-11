package rust

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
)

func TestRustMalformedRootFallbackPreservesChildCrateLocalModules(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, cargoTomlName), "[package\nname = \"broken-root\"\n")
	writeFile(t, filepath.Join(repo, "parent", cargoTomlName), `[package]
name = "parent"
version = "0.1.0"
`)
	writeFile(t, filepath.Join(repo, "parent", "child", cargoTomlName), `[package]
name = "child"
version = "0.1.0"
`)
	writeFile(t, filepath.Join(repo, "parent", "child", "grandchild", cargoTomlName), `[package]
name = "grandchild"
version = "0.1.0"

[dependencies]
foo = "1"
`)
	writeFile(t, filepath.Join(repo, "parent", "child", "grandchild", "src", "lib.rs"), "use foo::Thing;\npub fn use_local() { let _ = Thing; }\n")
	writeFile(t, filepath.Join(repo, "parent", "child", "grandchild", "src", "foo.rs"), "pub struct Thing;\n")
	writeFile(t, filepath.Join(repo, "src", "main.rs"), "use foo::RootThing;\nfn main() { let _ = RootThing; }\n")

	result, err := NewAdapter().Analyse(context.Background(), language.Request{RepoPath: repo, Dependency: "foo"})
	if err != nil {
		t.Fatalf("analyse malformed root Cargo manifest: %v", err)
	}
	if len(result.Dependencies) != 1 || result.Dependencies[0].Name != "foo" {
		t.Fatalf("expected nested child dependency report, got %#v", result.Dependencies)
	}
	if result.Dependencies[0].UsedExportsCount != 0 {
		t.Fatalf("expected malformed root source not to borrow a nested crate declaration, got %#v", result.Dependencies)
	}
	if !strings.Contains(strings.Join(result.Warnings, "\n"), `could not resolve Rust crate alias "foo" from Cargo manifests`) {
		t.Fatalf("expected unresolved malformed root import warning, got %#v", result.Warnings)
	}
}
