package analysis

import (
	"context"
	"path/filepath"
	"testing"
)

func TestServiceAnalyseRustMalformedRootKeepsChildDependencyBoundaries(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "Cargo.toml"), "[package\nname = \"broken-root\"\n")
	writeFile(t, filepath.Join(repo, "src", "main.rs"), "use foo::RootThing;\nfn main() { let _ = RootThing; }\n")
	writeFile(t, filepath.Join(repo, "a", "Cargo.toml"), `[package]
name = "a"
version = "0.1.0"

[dependencies]
foo = "1"
`)
	writeFile(t, filepath.Join(repo, "a", "src", "lib.rs"), "pub fn a() {}\n")
	writeFile(t, filepath.Join(repo, "b", "Cargo.toml"), `[package]
name = "b"
version = "0.1.0"
`)
	writeFile(t, filepath.Join(repo, "b", "src", "lib.rs"), "use foo::Thing;\npub fn b() { let _ = Thing; }\n")

	reportData, err := NewService().Analyse(context.Background(), Request{
		RepoPath:   repo,
		Language:   "rust",
		Dependency: "foo",
		Cache:      &CacheOptions{Enabled: false},
	})
	if err != nil {
		t.Fatalf("analyse malformed root Rust fixture: %v", err)
	}
	if len(reportData.Dependencies) != 1 || reportData.Dependencies[0].Name != "foo" {
		t.Fatalf("expected only child a foo declaration, got %#v", reportData.Dependencies)
	}
	if reportData.Dependencies[0].UsedExportsCount != 0 {
		t.Fatalf("expected child b's undeclared foo import not to mark child a's declaration used, got %#v", reportData.Dependencies)
	}
}
