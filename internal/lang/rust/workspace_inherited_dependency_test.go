//go:build !regressionproof

package rust

import (
	"context"
	"errors"
	"os"
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

func TestRepositoryScopeDoesNotInheritAcrossMalformedWorkspaceBoundary(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, cargoTomlName), `[workspace]

[workspace.dependencies]
alias = { package = "outer", version = "1" }
`)
	writeFile(t, filepath.Join(repo, "apps", cargoTomlName), "[workspace\n")
	writeFile(t, filepath.Join(repo, "apps", "child", cargoTomlName), `[package]
name = "child"
version = "0.1.0"

[dependencies]
alias = { workspace = true }
`)
	writeFile(t, filepath.Join(repo, "apps", "child", "src", "lib.rs"), "use alias::Thing;\npub fn uses() { let _ = Thing; }\n")

	assertRepositoryWorkspaceDependencyUsage(t, repo, "outer", 0)
}

func TestRepositoryScopeKeepsLocalDependencyOverWorkspaceMetadata(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, cargoTomlName), `[workspace]
members = ["child"]

[workspace.dependencies]
alias = { package = "outer", version = "1" }
`)
	writeFile(t, filepath.Join(repo, "child", cargoTomlName), `[package]
name = "child"
version = "0.1.0"

[dependencies]
alias = { package = "local", version = "1" }
`)
	writeFile(t, filepath.Join(repo, "child", "src", "lib.rs"), "use alias::Thing;\npub fn uses() { let _ = Thing; }\n")

	assertRepositoryWorkspaceDependencyUsage(t, repo, "local", 1)
	assertRepositoryWorkspaceDependencyUsage(t, repo, "outer", 0)
}

func TestManifestDependencyLookupsByRootSkipsMalformedAndReportsReadErrors(t *testing.T) {
	repo := t.TempDir()
	validManifest := filepath.Join(repo, "valid", cargoTomlName)
	malformedManifest := filepath.Join(repo, "malformed", cargoTomlName)
	writeFile(t, validManifest, `[package]
name = "valid"
version = "0.1.0"
`)
	writeFile(t, malformedManifest, "[package\n")

	lookups, err := manifestDependencyLookupsByRoot(repo, []string{validManifest, malformedManifest}, nil)
	if err != nil {
		t.Fatalf("build lookups with malformed manifest: %v", err)
	}
	if len(lookups) != 1 || lookups[filepath.Dir(validManifest)] == nil {
		t.Fatalf("expected only valid manifest lookup, got %#v", lookups)
	}

	_, err = manifestDependencyLookupsByRoot(repo, []string{filepath.Join(repo, "missing", cargoTomlName)}, nil)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected missing manifest read error, got %v", err)
	}
}

func TestRepositoryScopeWithoutCargoManifestScansFallbackRoot(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "src", "lib.rs"), "use undeclared::Thing;\npub fn uses() { let _ = Thing; }\n")

	result, err := NewAdapter().Analyse(context.Background(), language.Request{
		RepoPath:   repo,
		ScopeMode:  "repo",
		Dependency: "undeclared",
	})
	if err != nil {
		t.Fatalf("analyse repository without Cargo manifest: %v", err)
	}
	if len(result.Dependencies) != 1 || result.Dependencies[0].UsedExportsCount != 0 {
		t.Fatalf("expected undeclared dependency to remain unused, got %#v", result.Dependencies)
	}
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
