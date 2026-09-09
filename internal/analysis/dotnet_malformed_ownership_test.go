package analysis

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestServiceAnalyseDotNetMalformedManifestsPreserveProjectOwnership(t *testing.T) {
	for _, broken := range []string{"Broken.csproj", "Directory.Packages.props", "src/Directory.Packages.props"} {
		t.Run(broken, func(t *testing.T) {
			repo := t.TempDir()
			writeFile(t, filepath.Join(repo, "Root.csproj"), `<Project><ItemGroup><PackageReference Include="Root.Package" /></ItemGroup></Project>`)
			writeFile(t, filepath.Join(repo, "src", "Program.cs"), "using Root.Package;\n")
			writeFile(t, filepath.Join(repo, "nested", "Nested.csproj"), `<Project><ItemGroup><PackageReference Include="Nested.Package" /></ItemGroup></Project>`)
			writeFile(t, filepath.Join(repo, "nested", "Nested.cs"), "using Nested.Package;\n")
			writeFile(t, filepath.Join(repo, broken), "<Project><ItemGroup>")
			for _, mode := range []string{ScopeModePackage, ScopeModeRepo} {
				req := Request{RepoPath: repo, Language: "dotnet", ScopeMode: mode, TopN: 2, Cache: &CacheOptions{Enabled: false}}
				data, err := NewService().Analyse(context.Background(), req)
				if err != nil {
					t.Fatal(err)
				}
				if len(data.CoverageGaps) != 1 || data.CoverageGaps[0].Path != broken {
					t.Fatalf("%s: expected malformed manifest gap for %s, got %#v", mode, broken, data.CoverageGaps)
				}
				if len(data.Dependencies) != 2 {
					t.Fatalf("%s: expected both project declarations, got %#v", mode, data.Dependencies)
				}
				for _, dep := range data.Dependencies {
					if dep.UsedExportsCount != 1 || dep.TotalExportsCount != 1 || len(dep.Recommendations) != 0 || len(dep.UsedImports) != 1 || len(dep.UsedImports[0].Locations) != 1 {
						t.Fatalf("%s: expected each project source once with its own declaration, got %#v", mode, dep)
					}
				}
				req.RequireCompleteCoverage = true
				if _, err := NewService().Analyse(context.Background(), req); !errors.Is(err, ErrIncompleteCoverage) {
					t.Fatalf("%s: expected strict coverage failure, got %v", mode, err)
				}
			}
		})
	}
}
