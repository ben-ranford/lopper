//go:build !regressionproof

package rust

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
)

func TestWorkspaceOnlyRootRetainsInheritedDependencyAfterMalformedSibling(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, cargoTomlName), `[workspace]
members = ["child"]

[workspace.dependencies]
alias = { package = "actual", version = "1" }
`)
	writeFile(t, filepath.Join(repo, "child", cargoTomlName), `[package]
name = "child"
version = "0.1.0"

[dependencies]
alias = { workspace = true }
`)
	writeFile(t, filepath.Join(repo, "child", "src", testRustLibRS), "use alias::Thing;\npub fn child() { let _ = Thing; }\n")
	writeFile(t, filepath.Join(repo, "broken", cargoTomlName), "[package\nname = \"broken\"\n")
	writeFile(t, filepath.Join(repo, "broken", "src", testRustMainRS), "fn main() {}\n")

	for _, scopeMode := range []string{"package", "changed-packages"} {
		t.Run(scopeMode, func(t *testing.T) {
			reportData, err := NewAdapter().Analyse(context.Background(), language.Request{
				RepoPath:   repo,
				ScopeMode:  scopeMode,
				Dependency: "actual",
			})
			if err != nil {
				t.Fatalf("analyse workspace-only root: %v", err)
			}
			if len(reportData.Dependencies) != 1 || reportData.Dependencies[0].Name != "actual" || reportData.Dependencies[0].UsedExportsCount != 1 || reportData.Dependencies[0].TotalExportsCount != 1 || len(reportData.Dependencies[0].UsedImports) != 1 {
				t.Fatalf("expected inherited alias usage after malformed sibling fallback, got %#v", reportData.Dependencies)
			}
		})
	}
}
