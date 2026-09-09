package analysis

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
)

func TestServiceAnalyseDotNetLateMalformedRootAvoidsSelectedChildOverlap(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "aaaa", "Child.csproj"), `<Project><ItemGroup><PackageReference Include="Child.Package" /></ItemGroup></Project>`)
	writeFile(t, filepath.Join(repo, "aaaa", "Child.cs"), "using Child.Package;\nclass Child {}\n")
	for index := 0; index < 1024; index++ {
		writeFile(t, filepath.Join(repo, fmt.Sprintf("b-padding-%04d.txt", index)), "\n")
	}
	writeFile(t, filepath.Join(repo, "zzzz.csproj"), `<Project><ItemGroup><PackageReference Include="broken"`)

	reportData, err := NewService().Analyse(context.Background(), Request{
		RepoPath:  repo,
		Language:  "dotnet",
		ScopeMode: ScopeModePackage,
		TopN:      1,
		Cache:     &CacheOptions{Enabled: false},
	})
	if err != nil {
		t.Fatalf("analyse late malformed root manifest: %v", err)
	}
	if len(reportData.Dependencies) != 1 || reportData.Dependencies[0].Name != "child.package" || reportData.Dependencies[0].UsedExportsCount != 1 || reportData.Dependencies[0].TotalExportsCount != 1 || len(reportData.Dependencies[0].UsedImports) != 1 || len(reportData.Dependencies[0].UsedImports[0].Locations) != 1 {
		t.Fatalf("expected child declaration and import exactly once, got %#v", reportData.Dependencies)
	}
	if len(reportData.CoverageGaps) != 1 || reportData.CoverageGaps[0].Path != "zzzz.csproj" {
		t.Fatalf("expected late malformed root coverage gap, got %#v", reportData.CoverageGaps)
	}

	_, err = NewService().Analyse(context.Background(), Request{
		RepoPath:                repo,
		Language:                "dotnet",
		ScopeMode:               ScopeModePackage,
		Dependency:              "child.package",
		RequireCompleteCoverage: true,
		Cache:                   &CacheOptions{Enabled: false},
	})
	if !errors.Is(err, ErrIncompleteCoverage) {
		t.Fatalf("expected late malformed root to fail complete coverage, got %v", err)
	}

	_, err = NewService().Analyse(context.Background(), Request{
		RepoPath:                repo,
		Language:                "dotnet",
		ScopeMode:               ScopeModeChangedPackages,
		ChangedFilesExplicit:    true,
		ChangedFiles:            []string{"aaaa/Child.cs"},
		Dependency:              "child.package",
		RequireCompleteCoverage: true,
		Cache:                   &CacheOptions{Enabled: false},
	})
	if !errors.Is(err, ErrIncompleteCoverage) {
		t.Fatalf("expected changed child scope to retain late malformed root coverage gap, got %v", err)
	}
}
