package dotnet

import (
	"path/filepath"
	"testing"

	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestParseXMLManifestIncludesStructuredBranches(t *testing.T) {
	content := []byte(`
	<Project xmlns:msb="urn:test">
	  <ItemGroup>
	    <msb:PackageReference Include="Namespace.Package" />
    <PackageReference Include=" " />
    <PackageReference />
    <PackageVersion Include="Central.Package" />
	  </ItemGroup>
	</Project>
	`)
	deps, err := parseXMLManifestIncludes(content, "PackageReference")
	if err != nil {
		t.Fatalf("parse package references: %v", err)
	}
	if len(deps) != 1 || deps[0] != "namespace.package" {
		t.Fatalf("expected only namespaced package reference include, got %#v", deps)
	}

	deps, err = parseXMLManifestIncludes([]byte(`<Project><PackageVersion Include="Central.Package" /></Project>`), "PackageVersion")
	if err != nil {
		t.Fatalf("parse package versions: %v", err)
	}
	if len(deps) != 1 || deps[0] != "central.package" {
		t.Fatalf("expected package version include, got %#v", deps)
	}

	if _, err := parseXMLManifestIncludes([]byte(`<Project><PackageReference Include="broken"`), "PackageReference"); err == nil {
		t.Fatalf("expected malformed XML to return an error")
	}
}

func TestDotNetDiscoveryPathBoundaryBranches(t *testing.T) {
	repo := t.TempDir()
	if !isRepoBoundedPath(repo, filepath.Join(repo, "src", "App.csproj")) {
		t.Fatalf("expected in-repo candidate path to be accepted")
	}
	if isRepoBoundedPath(repo, filepath.Join(filepath.Dir(repo), "outside.csproj")) {
		t.Fatalf("expected outside candidate path to be rejected")
	}
	if isRepoBoundedPath("\x00", repo) {
		t.Fatalf("expected invalid repo path to be rejected")
	}
	if isRepoBoundedPath(repo, "\x00") {
		t.Fatalf("expected invalid candidate path to be rejected")
	}

	roots := map[string]struct{}{}
	testutil.MustWriteFile(t, filepath.Join(repo, "App.sln"), `Project("{FAKE}") = "Outside", "../outside/Outside.csproj", "{ONE}"`)
	if err := addSolutionRoots(repo, filepath.Join(repo, "App.sln"), roots); err != nil {
		t.Fatalf("add solution roots: %v", err)
	}
	if len(roots) != 0 {
		t.Fatalf("expected out-of-repo solution project to be ignored, got %#v", roots)
	}

	if _, _, err := readSourceFile(repo, filepath.Join(repo, "missing.cs")); err == nil {
		t.Fatalf("expected missing source file to return an error")
	}
	deps := map[string]struct{}{}
	if err := addAncestorCentralPackages(filepath.Join(repo, "nested"), deps); err != nil {
		t.Fatalf("add ancestor central packages without ancestor file: %v", err)
	}
	if len(deps) != 0 {
		t.Fatalf("expected no ancestor central package deps, got %#v", deps)
	}
}
