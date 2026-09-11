package analysis

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/ben-ranford/lopper/internal/report"
)

func TestServiceAnalyseRustValidAncestorExcludesMalformedNestedFallback(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "Cargo.toml"), `[package]
name = "root"
version = "0.1.0"

[dependencies]
foo = "1"
`)
	writeFile(t, filepath.Join(repo, "src", "main.rs"), "use foo::Root;\nfn main() { let _ = Root; }\n")
	writeFile(t, filepath.Join(repo, "apps", "Cargo.toml"), "[package\nname = \"broken-app\"\n")
	writeFile(t, filepath.Join(repo, "apps", "src", "main.rs"), "use foo::App;\nfn main() { let _ = App; }\n")
	writeFile(t, filepath.Join(repo, "apps", "child", "Cargo.toml"), `[package]
name = "child"
version = "0.1.0"

[dependencies]
bar = "1"
`)
	writeFile(t, filepath.Join(repo, "apps", "child", "src", "lib.rs"), "use bar::Child;\npub fn child() { let _ = Child; }\n")

	for _, scopeMode := range []string{"", ScopeModeRepo} {
		t.Run(scopeMode, func(t *testing.T) {
			reportData, err := NewService().Analyse(context.Background(), Request{
				RepoPath:  repo,
				Language:  "rust",
				ScopeMode: scopeMode,
				TopN:      2,
				Cache:     &CacheOptions{Enabled: false},
			})
			if err != nil {
				t.Fatalf("analyse nested malformed Rust manifest: %v", err)
			}
			assertRustMalformedNestedBoundary(t, reportData)
		})
	}

	var err error
	_, err = NewService().Analyse(context.Background(), Request{
		RepoPath:                repo,
		Language:                "rust",
		ScopeMode:               ScopeModeRepo,
		Dependency:              "foo",
		RequireCompleteCoverage: true,
		Cache:                   &CacheOptions{Enabled: false},
	})
	if !errors.Is(err, ErrIncompleteCoverage) {
		t.Fatalf("expected malformed nested manifest to fail complete coverage, got %v", err)
	}
}

func assertRustMalformedNestedBoundary(t *testing.T, reportData report.Report) {
	t.Helper()
	if len(reportData.CoverageGaps) != 1 || reportData.CoverageGaps[0].Path != "apps/Cargo.toml" {
		t.Fatalf("expected malformed nested manifest coverage gap, got %#v", reportData.CoverageGaps)
	}
	if len(reportData.Dependencies) != 2 {
		t.Fatalf("expected isolated root and nested child reports, got %#v", reportData.Dependencies)
	}
	expectedImports := map[string]string{"foo": "Root", "bar": "Child"}
	for _, dependency := range reportData.Dependencies {
		if dependency.UsedExportsCount != 1 || dependency.TotalExportsCount != 1 || len(dependency.UsedImports) != 1 {
			t.Fatalf("expected each declared dependency counted once, got %#v", dependency)
		}
		imported := dependency.UsedImports[0]
		expectedImport, ok := expectedImports[dependency.Name]
		if !ok || len(imported.Locations) != 1 || imported.Name != expectedImport {
			t.Fatalf("expected isolated root and child declarations without malformed-app attribution, got %#v", reportData.Dependencies)
		}
	}
}
