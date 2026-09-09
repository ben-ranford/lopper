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
