package analysis

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"
)

func TestServiceAnalyseDotNetMalformedRootKeepsSiblingDeclarationsScoped(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "Broken.csproj"), "<Project><PackageReference Include=\"broken\"")
	writeFile(t, filepath.Join(repo, "Program.cs"), "using Foo;\nclass Root {}\n")
	writeFile(t, filepath.Join(repo, "a", "A.csproj"), `<Project><ItemGroup><PackageReference Include="Foo" /></ItemGroup></Project>`)
	writeFile(t, filepath.Join(repo, "a", "A.cs"), "class A {}\n")
	writeFile(t, filepath.Join(repo, "b", "B.csproj"), "<Project></Project>")
	writeFile(t, filepath.Join(repo, "b", "B.cs"), "using Foo;\nclass B {}\n")

	reportData, err := NewService().Analyse(context.Background(), Request{
		RepoPath:   repo,
		Language:   "dotnet",
		Dependency: "foo",
		Cache:      &CacheOptions{Enabled: false},
	})
	if err != nil {
		t.Fatalf("analyse malformed root .NET project: %v", err)
	}
	if reportData.Scope == nil || !slices.Equal(reportData.Scope.Packages, []string{"."}) {
		t.Fatalf("expected one malformed-root fallback scope, got %#v", reportData.Scope)
	}
	if len(reportData.CoverageGaps) != 1 || reportData.CoverageGaps[0].Path != "Broken.csproj" {
		t.Fatalf("expected malformed root coverage gap, got %#v", reportData.CoverageGaps)
	}
	if len(reportData.Dependencies) != 1 || reportData.Dependencies[0].UsedExportsCount != 0 {
		t.Fatalf("expected Foo declared only by sibling A to remain unused, got %#v", reportData.Dependencies)
	}
}

func TestServiceAnalyseDotNetMalformedRootKeepsCoverageForChangedNestedProject(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "Broken.csproj"), "<Project><PackageReference Include=\"broken\"")
	writeFile(t, filepath.Join(repo, "nested", "Nested.csproj"), `<Project><ItemGroup><PackageReference Include="Newtonsoft.Json" /></ItemGroup></Project>`)
	writeFile(t, filepath.Join(repo, "nested", "Nested.cs"), "using Newtonsoft.Json;\n")

	_, err := NewService().Analyse(context.Background(), Request{
		RepoPath:                repo,
		Language:                "dotnet",
		ScopeMode:               ScopeModeChangedPackages,
		ChangedFilesExplicit:    true,
		ChangedFiles:            []string{"nested/Nested.cs"},
		RequireCompleteCoverage: true,
		Cache:                   &CacheOptions{Enabled: false},
	})
	if !errors.Is(err, ErrIncompleteCoverage) {
		t.Fatalf("expected malformed root coverage gap for nested changed package, got %v", err)
	}
}

func TestServiceAnalyseDotNetNestedMalformedRootAvoidsOverlappingChild(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "apps", "Broken.csproj"), "<Project><PackageReference Include=\"broken\"")
	writeFile(t, filepath.Join(repo, "apps", "child", "Child.csproj"), `<Project><ItemGroup><PackageReference Include="Newtonsoft.Json" /></ItemGroup></Project>`)
	writeFile(t, filepath.Join(repo, "apps", "child", "Child.cs"), "using Newtonsoft.Json;\n")

	reportData, err := NewService().Analyse(context.Background(), Request{
		RepoPath:   repo,
		Language:   "dotnet",
		Dependency: "newtonsoft.json",
		Cache:      &CacheOptions{Enabled: false},
	})
	if err != nil {
		t.Fatalf("analyse nested malformed .NET project: %v", err)
	}
	if reportData.Scope == nil || !slices.Equal(reportData.Scope.Packages, []string{"apps"}) {
		t.Fatalf("expected one isolated malformed-root scope, got %#v", reportData.Scope)
	}
	if len(reportData.Dependencies) != 1 || len(reportData.Dependencies[0].UsedImports) != 1 || len(reportData.Dependencies[0].UsedImports[0].Locations) != 1 {
		t.Fatalf("expected nested child usage exactly once, got %#v", reportData.Dependencies)
	}
}

func TestServiceAnalyseDotNetValidRootDoesNotRescanNestedProject(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "Root.csproj"), `<Project><ItemGroup><PackageReference Include="Root.Package" /></ItemGroup></Project>`)
	writeFile(t, filepath.Join(repo, "src", "deep", "Program.cs"), "using Root.Package;\n")
	writeFile(t, filepath.Join(repo, "nested", "Nested.csproj"), `<Project><ItemGroup><PackageReference Include="Nested.Package" /></ItemGroup></Project>`)
	writeFile(t, filepath.Join(repo, "nested", "Nested.cs"), "using Nested.Package;\n")

	reportData, err := NewService().Analyse(context.Background(), Request{
		RepoPath: repo,
		Language: "dotnet",
		TopN:     2,
		Cache:    &CacheOptions{Enabled: false},
	})
	if err != nil {
		t.Fatalf("analyse root and nested .NET projects: %v", err)
	}
	if reportData.Scope == nil || !slices.Equal(reportData.Scope.Packages, []string{".", "nested"}) {
		t.Fatalf("expected separate valid project scopes, got %#v", reportData.Scope)
	}
	if len(reportData.Dependencies) != 2 {
		t.Fatalf("expected one report for each valid project declaration, got %#v", reportData.Dependencies)
	}
	for _, dependency := range reportData.Dependencies {
		if dependency.UsedExportsCount != 1 || dependency.TotalExportsCount != 1 || len(dependency.Recommendations) != 0 {
			t.Fatalf("expected each valid project declaration and source exactly once, got %#v", reportData.Dependencies)
		}
	}
}

func TestServiceAnalyseDotNetValidRootSeparatesMalformedChildBoundary(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "Root.csproj"), `<Project><ItemGroup><PackageReference Include="Foo" /></ItemGroup></Project>`)
	writeFile(t, filepath.Join(repo, "Program.cs"), "using Foo;\nclass Root {}\n")
	writeFile(t, filepath.Join(repo, "apps", "Broken.csproj"), "<Project><PackageReference Include=\"broken\"")
	writeFile(t, filepath.Join(repo, "apps", "App.cs"), "using Foo;\nclass BrokenApp {}\n")
	writeFile(t, filepath.Join(repo, "apps", "child", "Child.csproj"), `<Project><ItemGroup><PackageReference Include="Foo" /></ItemGroup></Project>`)
	writeFile(t, filepath.Join(repo, "apps", "child", "Child.cs"), "using Foo;\nclass Child {}\n")

	reportData, err := NewService().Analyse(context.Background(), Request{
		RepoPath:   repo,
		Language:   "dotnet",
		Dependency: "foo",
		Cache:      &CacheOptions{Enabled: false},
	})
	if err != nil {
		t.Fatalf("analyse valid root with malformed child .NET project: %v", err)
	}
	if reportData.Scope == nil || !slices.Equal(reportData.Scope.Packages, []string{".", "apps"}) {
		t.Fatalf("expected valid root and malformed child fallback scopes, got %#v", reportData.Scope)
	}
	if len(reportData.CoverageGaps) != 1 || reportData.CoverageGaps[0].Path != "apps/Broken.csproj" {
		t.Fatalf("expected malformed child coverage gap, got %#v", reportData.CoverageGaps)
	}
	if len(reportData.Dependencies) != 1 {
		t.Fatalf("expected merged Foo report, got %#v", reportData.Dependencies)
	}
	dependency := reportData.Dependencies[0]
	if dependency.UsedExportsCount != 2 || dependency.TotalExportsCount != 2 || len(dependency.Recommendations) != 0 {
		t.Fatalf("expected root and valid child Foo declarations exactly once, got %#v", dependency)
	}
	if len(dependency.UsedImports) != 1 || len(dependency.UsedImports[0].Locations) != 2 {
		t.Fatalf("expected only root and valid child Foo locations, got %#v", dependency.UsedImports)
	}

	_, err = NewService().Analyse(context.Background(), Request{
		RepoPath:                repo,
		Language:                "dotnet",
		ScopeMode:               ScopeModeChangedPackages,
		ChangedFilesExplicit:    true,
		ChangedFiles:            []string{"apps/App.cs"},
		RequireCompleteCoverage: true,
		Cache:                   &CacheOptions{Enabled: false},
	})
	if !errors.Is(err, ErrIncompleteCoverage) {
		t.Fatalf("expected malformed child coverage gap in strict changed scope, got %v", err)
	}
}
