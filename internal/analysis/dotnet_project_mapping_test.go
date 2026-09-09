package analysis

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/ben-ranford/lopper/internal/report"
)

func TestServiceAnalyseDotNetProjectPreservesUndeclaredImportFinding(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "App.csproj"), `<Project><ItemGroup><PackageReference Include="Foo" /></ItemGroup></Project>`)
	writeFile(t, filepath.Join(repo, "Program.cs"), "using Bar;\n")

	data, err := NewService().Analyse(context.Background(), Request{
		RepoPath: repo, Language: "dotnet", ScopeMode: ScopeModePackage, TopN: 2, Cache: &CacheOptions{Enabled: false},
	})
	if err != nil {
		t.Fatalf("analyse project with undeclared import: %v", err)
	}
	for _, dependency := range data.Dependencies {
		if dependency.Name != "bar" {
			continue
		}
		if dependency.UsedExportsCount != 1 || dependency.TotalExportsCount != 1 || !hasDotNetRiskCue(dependency, "undeclared-package-usage") || !hasDotNetRecommendation(dependency, "declare-dependency-explicitly") {
			t.Fatalf("expected undeclared Bar import finding, got %#v", dependency)
		}
		return
	}
	t.Fatalf("expected undeclared Bar dependency in %#v", data.Dependencies)
}

func TestServiceAnalyseDotNetDoesNotReuseOwnedMapperForMalformedSource(t *testing.T) {
	for _, validProjectDir := range []string{"", "zvalid"} {
		t.Run(validProjectDir, func(t *testing.T) {
			repo := t.TempDir()
			writeFile(t, filepath.Join(repo, validProjectDir, "App.csproj"), "<Project></Project>")
			writeFile(t, filepath.Join(repo, validProjectDir, "Program.cs"), "using Bar;\n")
			writeFile(t, filepath.Join(repo, "apps", "Broken.csproj"), "<Project><ItemGroup>")
			writeFile(t, filepath.Join(repo, "apps", "Program.cs"), "using Baz;\n")
			writeFile(t, filepath.Join(repo, "apps", "child", "Child.csproj"), `<Project><ItemGroup><PackageReference Include="Child" /></ItemGroup></Project>`)
			writeFile(t, filepath.Join(repo, "apps", "child", "Child.cs"), "using Child;\n")

			data, err := NewService().Analyse(context.Background(), Request{
				RepoPath: repo, Language: "dotnet", ScopeMode: ScopeModeRepo, TopN: 3, Cache: &CacheOptions{Enabled: false},
			})
			if err != nil {
				t.Fatalf("analyse mixed valid and malformed project sources: %v", err)
			}
			if !hasDotNetDependency(data.Dependencies, "bar") || !hasDotNetDependency(data.Dependencies, "child") || hasDotNetDependency(data.Dependencies, "baz") {
				t.Fatalf("expected valid project fallback and malformed source isolation, got %#v", data.Dependencies)
			}
		})
	}
}

func hasDotNetRiskCue(dependency report.DependencyReport, code string) bool {
	for _, cue := range dependency.RiskCues {
		if cue.Code == code {
			return true
		}
	}
	return false
}

func hasDotNetRecommendation(dependency report.DependencyReport, code string) bool {
	for _, recommendation := range dependency.Recommendations {
		if recommendation.Code == code {
			return true
		}
	}
	return false
}

func hasDotNetDependency(dependencies []report.DependencyReport, name string) bool {
	for _, dependency := range dependencies {
		if dependency.Name == name {
			return true
		}
	}
	return false
}
