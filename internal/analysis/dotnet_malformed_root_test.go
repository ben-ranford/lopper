package analysis

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestServiceAnalyseDotNetMalformedRootAvoidsOverlappingNestedRoots(t *testing.T) {
	for _, manifestName := range []string{"Broken.csproj", "Directory.Packages.props"} {
		t.Run(manifestName, func(t *testing.T) {
			assertDotNetMalformedRootAvoidsOverlap(t, manifestName)
		})
	}
}

func assertDotNetMalformedRootAvoidsOverlap(t *testing.T, manifestName string) {
	t.Helper()
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, manifestName), "<Project><PackageReference Include=\"broken\"")
	writeFile(t, filepath.Join(repo, "Program.cs"), "using Newtonsoft.Json;\nclass Root { void Run() { JsonConvert.SerializeObject(null); } }\n")
	writeFile(t, filepath.Join(repo, "nested", "Nested.csproj"), "<Project><ItemGroup><PackageReference Include=\"Newtonsoft.Json\" /></ItemGroup></Project>")
	writeFile(t, filepath.Join(repo, "nested", "Nested.cs"), "using Newtonsoft.Json;\nclass Nested { void Run() { JsonConvert.SerializeObject(null); } }\n")

	reportData := analyseMalformedManifestFixture(t, repo, "dotnet", "newtonsoft.json")
	if reportData.Scope == nil || !slices.Equal(reportData.Scope.Packages, []string{"."}) {
		t.Fatalf("expected one repository fallback scope without nested overlap, got %#v", reportData.Scope)
	}
	if !strings.Contains(strings.Join(reportData.Warnings, "\n"), "skipped malformed .NET manifest "+manifestName) {
		t.Fatalf("expected malformed root manifest warning, got %#v", reportData.Warnings)
	}
	if len(reportData.CoverageGaps) != 1 || reportData.CoverageGaps[0].Path != manifestName {
		t.Fatalf("expected malformed root manifest coverage gap, got %#v", reportData.CoverageGaps)
	}
	if len(reportData.Dependencies) != 1 || len(reportData.Dependencies[0].UsedImports) != 1 || len(reportData.Dependencies[0].UsedImports[0].Locations) != 1 {
		t.Fatalf("expected only the nested project declaration to authorize its source import, got %#v", reportData.Dependencies)
	}
}
