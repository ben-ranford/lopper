package analysis

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestServiceAnalyseDotNetMalformedRootAvoidsOverlappingNestedRoots(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "Broken.csproj"), "<Project><PackageReference Include=\"broken\"")
	writeFile(t, filepath.Join(repo, "Program.cs"), "using Newtonsoft.Json;\nclass Root { void Run() { JsonConvert.SerializeObject(null); } }\n")
	writeFile(t, filepath.Join(repo, "nested", "Nested.csproj"), "<Project><ItemGroup><PackageReference Include=\"Newtonsoft.Json\" /></ItemGroup></Project>")
	writeFile(t, filepath.Join(repo, "nested", "Nested.cs"), "using Newtonsoft.Json;\nclass Nested { void Run() { JsonConvert.SerializeObject(null); } }\n")

	reportData := analyseMalformedManifestFixture(t, repo, "dotnet", "newtonsoft.json")
	if reportData.Scope == nil || !slices.Equal(reportData.Scope.Packages, []string{"."}) {
		t.Fatalf("expected one repository fallback scope without nested overlap, got %#v", reportData.Scope)
	}
	if !strings.Contains(strings.Join(reportData.Warnings, "\n"), "skipped malformed .NET manifest Broken.csproj") {
		t.Fatalf("expected malformed root manifest warning, got %#v", reportData.Warnings)
	}
	if len(reportData.CoverageGaps) != 1 || reportData.CoverageGaps[0].Path != "Broken.csproj" {
		t.Fatalf("expected malformed root manifest coverage gap, got %#v", reportData.CoverageGaps)
	}
	if len(reportData.Dependencies) != 1 || len(reportData.Dependencies[0].UsedImports) != 1 || len(reportData.Dependencies[0].UsedImports[0].Locations) != 1 {
		t.Fatalf("expected only the nested project declaration to authorize its source import, got %#v", reportData.Dependencies)
	}
}
