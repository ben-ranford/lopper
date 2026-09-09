//go:build !regressionproof

package rust

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
)

func TestRepositoryScopeInheritsWorkspaceDependencyAliasMetadata(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, cargoTomlName), `[workspace]
members = ["crates/member"]

[workspace.dependencies]
alias = { package = "actual", version = "1" }
`)
	writeFile(t, filepath.Join(repo, "crates", "member", cargoTomlName), `[package]
name = "member"
version = "0.1.0"

[dependencies]
alias = { workspace = true }
`)
	writeFile(t, filepath.Join(repo, "crates", "member", "src", "lib.rs"), "use alias::Thing;\npub fn uses() { let _ = Thing; }\n")

	result, err := NewAdapter().Analyse(context.Background(), language.Request{
		RepoPath:   repo,
		ScopeMode:  "repo",
		Dependency: "actual",
	})
	if err != nil {
		t.Fatalf("analyse repository workspace dependency: %v", err)
	}
	if len(result.Dependencies) != 1 {
		t.Fatalf("expected requested dependency report, got %#v", result.Dependencies)
	}
	dependency := result.Dependencies[0]
	if dependency.Name != "actual" || dependency.TotalExportsCount != 1 || dependency.UsedExportsCount != 1 {
		t.Fatalf("expected inherited alias attributed to actual, got %#v", dependency)
	}
}

func TestRepositoryScopeUsesNearestWorkspaceDependencyMetadata(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, cargoTomlName), `[workspace]
members = ["apps"]

[workspace.dependencies]
alias = { package = "outer", version = "1" }
`)
	writeFile(t, filepath.Join(repo, "apps", cargoTomlName), `[workspace]
members = ["child"]

[workspace.dependencies]
alias = { package = "nested", version = "1" }
`)
	writeFile(t, filepath.Join(repo, "apps", "child", cargoTomlName), `[package]
name = "child"
version = "0.1.0"

[dependencies]
alias = { workspace = true }
`)
	writeFile(t, filepath.Join(repo, "apps", "child", "src", "lib.rs"), "use alias::Thing;\npub fn uses() { let _ = Thing; }\n")

	assertRepositoryWorkspaceDependencyUsage(t, repo, "nested", 1)
	assertRepositoryWorkspaceDependencyUsage(t, repo, "outer", 0)
}

func assertRepositoryWorkspaceDependencyUsage(t *testing.T, repo, dependencyName string, wantUsed int) {
	t.Helper()
	result, err := NewAdapter().Analyse(context.Background(), language.Request{
		RepoPath:   repo,
		ScopeMode:  "repo",
		Dependency: dependencyName,
	})
	if err != nil {
		t.Fatalf("analyse repository workspace dependency: %v", err)
	}
	if len(result.Dependencies) != 1 {
		t.Fatalf("expected requested dependency report, got %#v", result.Dependencies)
	}
	dependency := result.Dependencies[0]
	if dependency.Name != dependencyName || dependency.UsedExportsCount != wantUsed {
		t.Fatalf("expected %s usage %d, got %#v", dependencyName, wantUsed, dependency)
	}
}
