package analysis

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
)

func TestServiceAnalyseRustMalformedWorkspaceMemberOwnsNestedCrate(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "Cargo.toml"), `[workspace]
members = ["apps"]
`)
	writeFile(t, filepath.Join(repo, "apps", "Cargo.toml"), "[package\nname = \"broken-app\"\n")
	writeFile(t, filepath.Join(repo, "apps", "src", "main.rs"), "use foo::RootThing;\nfn main() { let _ = RootThing; }\n")
	writeFile(t, filepath.Join(repo, "apps", "child", "Cargo.toml"), `[package]
name = "child"
version = "0.1.0"

[dependencies]
foo = "1"
`)
	writeFile(t, filepath.Join(repo, "apps", "child", "src", "lib.rs"), "use foo::Thing;\npub fn child() { let _ = Thing; }\n")

	reportData, err := NewService().Analyse(context.Background(), Request{
		RepoPath:   repo,
		Language:   "rust",
		Dependency: "foo",
		Cache:      &CacheOptions{Enabled: false},
	})
	if err != nil {
		t.Fatalf("analyse malformed workspace member: %v", err)
	}
	if reportData.Scope == nil || !slices.Equal(reportData.Scope.Packages, []string{"apps"}) {
		t.Fatalf("expected malformed member fallback as the only package scope, got %#v", reportData.Scope)
	}
	if len(reportData.Dependencies) != 1 {
		t.Fatalf("expected one child foo dependency report, got %#v", reportData.Dependencies)
	}
	dependency := reportData.Dependencies[0]
	if dependency.Name != "foo" || dependency.UsedExportsCount != 1 || dependency.TotalExportsCount != 1 || len(dependency.UsedImports) != 1 || len(dependency.UsedImports[0].Locations) != 1 {
		t.Fatalf("expected nested child import counted once, got %#v", dependency)
	}
}
