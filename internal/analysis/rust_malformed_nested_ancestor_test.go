package analysis

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
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
foo = "1"
`)
	writeFile(t, filepath.Join(repo, "apps", "child", "src", "lib.rs"), "use foo::Child;\npub fn child() { let _ = Child; }\n")

	reportData := analyseMalformedManifestFixture(t, repo, "rust", "foo")
	if len(reportData.CoverageGaps) != 1 || reportData.CoverageGaps[0].Path != "apps/Cargo.toml" {
		t.Fatalf("expected malformed nested manifest coverage gap, got %#v", reportData.CoverageGaps)
	}
	if len(reportData.Dependencies) != 1 {
		t.Fatalf("expected merged root and nested child foo report, got %#v", reportData.Dependencies)
	}
	dependency := reportData.Dependencies[0]
	if dependency.UsedExportsCount != 2 || dependency.TotalExportsCount != 2 || len(dependency.UsedImports) != 2 {
		t.Fatalf("expected root and child imports counted once, got %#v", dependency)
	}
	for _, imported := range dependency.UsedImports {
		if len(imported.Locations) != 1 || imported.Name == "App" {
			t.Fatalf("expected no malformed-app attribution or duplicate locations, got %#v", dependency.UsedImports)
		}
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
