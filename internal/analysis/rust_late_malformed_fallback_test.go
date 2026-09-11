package analysis

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/ben-ranford/lopper/internal/report"
)

func TestServiceAnalyseRustLateMalformedManifestFallsBackWithoutBorrowingRootDeclaration(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "Cargo.toml"), `[package]
name = "root"
version = "0.1.0"

[dependencies]
foo = "1"

[workspace]
members = ["zapps/child"]
`)
	writeFile(t, filepath.Join(repo, "src", "main.rs"), "fn main() {}\n")
	for index := 0; index < 2049; index++ {
		writeFile(t, filepath.Join(repo, "a-padding", fmt.Sprintf("%04d.txt", index)), "\n")
	}
	writeFile(t, filepath.Join(repo, "zapps", "Cargo.toml"), "[package\nname = \"broken-app\"\n")
	writeFile(t, filepath.Join(repo, "zapps", "src", "main.rs"), "use foo::App;\nfn main() { let _ = App; }\n")
	writeFile(t, filepath.Join(repo, "zapps", "child", "Cargo.toml"), `[package]
name = "child"
version = "0.1.0"

[dependencies]
bar = "1"
`)
	writeFile(t, filepath.Join(repo, "zapps", "child", "src", "lib.rs"), "use bar::Child;\npub fn child() { let _ = Child; }\n")

	for _, scopeMode := range []string{ScopeModePackage, ScopeModeChangedPackages} {
		t.Run(scopeMode, func(t *testing.T) {
			req := Request{
				RepoPath:   repo,
				Language:   "rust",
				ScopeMode:  scopeMode,
				Dependency: "foo",
				Cache:      &CacheOptions{Enabled: false},
			}
			if scopeMode == ScopeModeChangedPackages {
				req.ChangedFilesExplicit = true
				req.ChangedFiles = []string{"zapps/child/src/lib.rs"}
			}
			reportData, err := NewService().Analyse(context.Background(), req)
			if err != nil {
				t.Fatalf("analyse Rust late malformed manifest: %v", err)
			}
			assertRustLateMalformedRootDependency(t, reportData.Dependencies, reportData.CoverageGaps, reportData.Warnings)

			req.Dependency = "bar"
			reportData, err = NewService().Analyse(context.Background(), req)
			if err != nil {
				t.Fatalf("analyse Rust late malformed descendant: %v", err)
			}
			assertRustLateMalformedDescendantDependency(t, reportData.Dependencies, reportData.CoverageGaps)
		})
	}

	_, err := NewService().Analyse(context.Background(), Request{
		RepoPath:                repo,
		Language:                "rust",
		ScopeMode:               ScopeModeChangedPackages,
		ChangedFilesExplicit:    true,
		ChangedFiles:            []string{"zapps/child/src/lib.rs"},
		Dependency:              "bar",
		RequireCompleteCoverage: true,
		Cache:                   &CacheOptions{Enabled: false},
	})
	if !errors.Is(err, ErrIncompleteCoverage) {
		t.Fatalf("expected late malformed Cargo manifest to fail complete coverage, got %v", err)
	}
}

func assertRustLateMalformedRootDependency(t *testing.T, dependencies []report.DependencyReport, gaps []report.CoverageGap, warnings []string) {
	t.Helper()
	if len(gaps) != 1 || gaps[0].Path != "zapps/Cargo.toml" {
		t.Fatalf("expected late malformed Cargo manifest coverage gap, got %#v", gaps)
	}
	if len(dependencies) != 1 || dependencies[0].Name != "foo" || dependencies[0].UsedExportsCount != 0 || dependencies[0].TotalExportsCount != 0 {
		t.Fatalf("expected malformed fallback source not to borrow root foo declaration, got %#v", dependencies)
	}
	if !containsWarning(warnings, `could not resolve Rust crate alias "foo" from Cargo manifests`) {
		t.Fatalf("expected malformed fallback source to be scanned with an empty declaration set, got %#v", warnings)
	}
}

func assertRustLateMalformedDescendantDependency(t *testing.T, dependencies []report.DependencyReport, gaps []report.CoverageGap) {
	t.Helper()
	if len(gaps) != 1 || gaps[0].Path != "zapps/Cargo.toml" {
		t.Fatalf("expected late malformed Cargo manifest coverage gap, got %#v", gaps)
	}
	if len(dependencies) != 1 || dependencies[0].Name != "bar" || dependencies[0].UsedExportsCount != 1 || dependencies[0].TotalExportsCount != 1 || len(dependencies[0].UsedImports) != 1 || len(dependencies[0].UsedImports[0].Locations) != 1 {
		t.Fatalf("expected valid descendant bar declaration and import exactly once, got %#v", dependencies)
	}
}
