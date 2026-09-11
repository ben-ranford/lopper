package analysis

import (
	"context"
	"errors"
	"fmt"
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

	reportData := analyseMalformedManifestFixtureInScope(t, repo, "dotnet", "foo", ScopeModeRepo)
	if reportData.Scope == nil || !slices.Equal(reportData.Scope.Packages, []string{"."}) {
		t.Fatalf("expected one malformed-root fallback scope, got %#v", reportData.Scope)
	}
	if len(reportData.CoverageGaps) != 1 || reportData.CoverageGaps[0].Path != "Broken.csproj" {
		t.Fatalf("expected malformed root coverage gap, got %#v", reportData.CoverageGaps)
	}
	if len(reportData.Dependencies) != 1 || reportData.Dependencies[0].UsedExportsCount != 1 || !hasDotNetRiskCue(reportData.Dependencies[0], "undeclared-package-usage") || !hasDotNetRecommendation(reportData.Dependencies[0], "declare-dependency-explicitly") {
		t.Fatalf("expected valid sibling B's undeclared Foo import to remain separate from sibling A's declaration, got %#v", reportData.Dependencies)
	}
}

func TestServiceAnalyseDotNetMalformedCentralManifestKeepsSiblingProjectDeclaration(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "Directory.Packages.props"), "<Project><ItemGroup><PackageVersion Include=\"broken\"")
	writeFile(t, filepath.Join(repo, "App.csproj"), `<Project><ItemGroup><PackageReference Include="Foo" /></ItemGroup></Project>`)
	writeFile(t, filepath.Join(repo, "Program.cs"), "using Foo;\nclass Program { Foo.Value value; }\n")

	reportData := analyseMalformedManifestFixtureInScope(t, repo, "dotnet", "foo", ScopeModeRepo)
	if len(reportData.CoverageGaps) != 1 || reportData.CoverageGaps[0].Path != "Directory.Packages.props" {
		t.Fatalf("expected malformed central manifest coverage gap, got %#v", reportData.CoverageGaps)
	}
	if len(reportData.Dependencies) != 1 || reportData.Dependencies[0].UsedExportsCount != 1 || len(reportData.Dependencies[0].UsedImports) != 1 {
		t.Fatalf("expected valid sibling project declaration to authorize its source import, got %#v", reportData.Dependencies)
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

	reportData := analyseMalformedManifestFixture(t, repo, "dotnet", "newtonsoft.json")
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
	writeFile(t, filepath.Join(repo, "apps", "child", "Child.csproj"), `<Project><ItemGroup><PackageReference Include="Child.Foo" /></ItemGroup></Project>`)
	writeFile(t, filepath.Join(repo, "apps", "child", "Child.cs"), "using Child.Foo;\nclass Child {}\n")

	reportData, err := NewService().Analyse(context.Background(), Request{
		RepoPath: repo, Language: "dotnet", ScopeMode: ScopeModeRepo, TopN: 2, Cache: &CacheOptions{Enabled: false},
	})
	if err != nil {
		t.Fatalf("analyse repo-scoped valid root with malformed child: %v", err)
	}
	if reportData.Scope == nil || !slices.Equal(reportData.Scope.Packages, []string{"."}) {
		t.Fatalf("expected repository scope to retain nested project analysis, got %#v", reportData.Scope)
	}
	if len(reportData.CoverageGaps) != 1 || reportData.CoverageGaps[0].Path != "apps/Broken.csproj" {
		t.Fatalf("expected malformed child coverage gap, got %#v", reportData.CoverageGaps)
	}
	if len(reportData.Dependencies) != 2 {
		t.Fatalf("expected root and nested project dependencies, got %#v", reportData.Dependencies)
	}
	for _, dependency := range reportData.Dependencies {
		if dependency.UsedExportsCount != 1 || dependency.TotalExportsCount != 1 || len(dependency.Recommendations) != 0 || len(dependency.UsedImports) != 1 || len(dependency.UsedImports[0].Locations) != 1 {
			t.Fatalf("expected each valid declaration and source exactly once, got %#v", reportData.Dependencies)
		}
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

func TestServiceAnalyseDotNetKeepsNestedProjectMissedByDetectionCap(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "Root.csproj"), `<Project><ItemGroup><PackageReference Include="Root.Package" /></ItemGroup></Project>`)
	writeFile(t, filepath.Join(repo, "Program.cs"), "using Root.Package;\nclass Root {}\n")
	for index := 0; index <= 1024; index++ {
		writeFile(t, filepath.Join(repo, fmt.Sprintf("%04d.txt", index)), "padding")
	}
	writeFile(t, filepath.Join(repo, "nested", "Nested.csproj"), `<Project><ItemGroup><PackageReference Include="Nested.Package" /></ItemGroup></Project>`)
	writeFile(t, filepath.Join(repo, "nested", "Nested.cs"), "using Nested.Package;\nclass Nested {}\n")

	reportData, err := NewService().Analyse(context.Background(), Request{
		RepoPath: repo,
		Language: "dotnet",
		TopN:     2,
		Cache:    &CacheOptions{Enabled: false},
	})
	if err != nil {
		t.Fatalf("analyse .NET project whose nested manifest follows detection cap: %v", err)
	}
	if reportData.Scope == nil || !slices.Equal(reportData.Scope.Packages, []string{"."}) {
		t.Fatalf("expected detection to schedule only the root, got %#v", reportData.Scope)
	}
	if len(reportData.Dependencies) != 2 {
		t.Fatalf("expected root analysis to retain the unscheduled nested project, got %#v", reportData.Dependencies)
	}
	for _, dependency := range reportData.Dependencies {
		if dependency.UsedExportsCount != 1 || dependency.TotalExportsCount != 1 || len(dependency.UsedImports) != 1 {
			t.Fatalf("expected each retained project declaration and source once, got %#v", reportData.Dependencies)
		}
	}
}

func TestServiceAnalyseDotNetSelectedChildOwnsLateGrandchild(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "Root.csproj"), `<Project><ItemGroup><PackageReference Include="Root.Package" /></ItemGroup></Project>`)
	writeFile(t, filepath.Join(repo, "Program.cs"), "using Root.Package;\nclass Root {}\n")
	writeFile(t, filepath.Join(repo, "apps", "App.csproj"), `<Project><ItemGroup><PackageReference Include="App.Package" /></ItemGroup></Project>`)
	writeFile(t, filepath.Join(repo, "apps", "App.cs"), "using App.Package;\nclass App {}\n")
	for index := 0; index <= 1024; index++ {
		writeFile(t, filepath.Join(repo, "apps", fmt.Sprintf("b%04d.txt", index)), "padding")
	}
	writeFile(t, filepath.Join(repo, "apps", "zlate", "Late.csproj"), `<Project><ItemGroup><PackageReference Include="Late.Package" /></ItemGroup></Project>`)
	writeFile(t, filepath.Join(repo, "apps", "zlate", "Late.cs"), "using Late.Package;\nclass Late {}\n")

	reportData, err := NewService().Analyse(context.Background(), Request{RepoPath: repo, Language: "dotnet", TopN: 3, Cache: &CacheOptions{Enabled: false}})
	if err != nil {
		t.Fatalf("analyse selected child with late grandchild: %v", err)
	}
	if reportData.Scope == nil || !slices.Equal(reportData.Scope.Packages, []string{".", "apps"}) {
		t.Fatalf("expected only root and detected child scopes, got %#v", reportData.Scope)
	}
	if len(reportData.Dependencies) != 3 {
		t.Fatalf("expected root, child, and late-grandchild declarations, got %#v", reportData.Dependencies)
	}
	for _, dependency := range reportData.Dependencies {
		if dependency.UsedExportsCount != 1 || dependency.TotalExportsCount != 1 || len(dependency.UsedImports) != 1 {
			t.Fatalf("expected each project source exactly once, got %#v", reportData.Dependencies)
		}
	}
}

func TestServiceAnalyseDotNetKeepsLateMalformedFallbackSubtree(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "Root.csproj"), `<Project><ItemGroup><PackageReference Include="Root.Package" /></ItemGroup></Project>`)
	writeFile(t, filepath.Join(repo, "Program.cs"), "using Root.Package;\nclass Root {}\n")
	for index := 0; index <= 1024; index++ {
		writeFile(t, filepath.Join(repo, fmt.Sprintf("%04d.txt", index)), "padding")
	}
	writeFile(t, filepath.Join(repo, "nested", "Broken.csproj"), "<Project><PackageReference Include=\"broken\"")
	writeFile(t, filepath.Join(repo, "nested", "Broken.cs"), "using Root.Package;\nclass Broken {}\n")
	writeFile(t, filepath.Join(repo, "nested", "child", "Child.csproj"), `<Project><ItemGroup><PackageReference Include="Child.Package" /></ItemGroup></Project>`)
	writeFile(t, filepath.Join(repo, "nested", "child", "Child.cs"), "using Child.Package;\nclass Child {}\n")

	reportData, err := NewService().Analyse(context.Background(), Request{RepoPath: repo, Language: "dotnet", TopN: 2, Cache: &CacheOptions{Enabled: false}})
	if err != nil {
		t.Fatalf("analyse late malformed fallback subtree: %v", err)
	}
	if reportData.Scope == nil || !slices.Equal(reportData.Scope.Packages, []string{"."}) {
		t.Fatalf("expected only root candidate after bounded detection, got %#v", reportData.Scope)
	}
	if len(reportData.Dependencies) != 2 || len(reportData.CoverageGaps) != 1 || reportData.CoverageGaps[0].Path != "nested/Broken.csproj" {
		t.Fatalf("expected root and valid child with malformed coverage gap, got dependencies=%#v gaps=%#v", reportData.Dependencies, reportData.CoverageGaps)
	}
	for _, dependency := range reportData.Dependencies {
		if dependency.UsedExportsCount != 1 || dependency.TotalExportsCount != 1 || len(dependency.UsedImports) != 1 {
			t.Fatalf("expected retained valid project sources exactly once, got %#v", reportData.Dependencies)
		}
	}
}
