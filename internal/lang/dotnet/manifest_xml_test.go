package dotnet

import (
	"context"
	"encoding/xml"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
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

func TestDotNetAnalysisSkipsMalformedProjectManifests(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "Broken.csproj"), `<Project><PackageReference Include="broken"`)
	testutil.MustWriteFile(t, filepath.Join(repo, centralPackagesFile), `<Project><PackageVersion Include="broken"`)
	testutil.MustWriteFile(t, filepath.Join(repo, "Working.csproj"), `<Project><ItemGroup><PackageReference Include="Newtonsoft.Json" /></ItemGroup></Project>`)
	testutil.MustWriteFile(t, filepath.Join(repo, "Program.cs"), "using Newtonsoft.Json;\n")

	reportData, err := NewAdapter().Analyse(context.Background(), language.Request{RepoPath: repo, Dependency: "newtonsoft.json"})
	if err != nil {
		t.Fatalf("analyse malformed project manifests: %v", err)
	}
	if len(reportData.Dependencies) != 1 || reportData.Dependencies[0].Name != "newtonsoft.json" {
		t.Fatalf("expected dependency from valid project manifest, got %#v", reportData.Dependencies)
	}
	joinedWarnings := strings.Join(reportData.Warnings, "\n")
	for _, manifest := range []string{"Broken.csproj", centralPackagesFile} {
		if !strings.Contains(joinedWarnings, manifest) || !strings.Contains(joinedWarnings, "skipped malformed .NET manifest") {
			t.Fatalf("expected malformed-manifest warning for %s, got %#v", manifest, reportData.Warnings)
		}
	}
	if len(reportData.CoverageGaps) != 2 {
		t.Fatalf("expected one coverage gap per malformed manifest, got %#v", reportData.CoverageGaps)
	}
	for _, gap := range reportData.CoverageGaps {
		if gap.Code != report.CoverageGapDotNetMalformedManifest || gap.Language != "dotnet" {
			t.Fatalf("expected malformed .NET manifest coverage gap, got %#v", gap)
		}
		if gap.Path != "Broken.csproj" && gap.Path != centralPackagesFile {
			t.Fatalf("expected relative malformed manifest path, got %#v", gap)
		}
		if len(gap.Evidence) != 1 || !strings.Contains(gap.Evidence[0], gap.Path) || !strings.Contains(gap.Evidence[0], "skipped malformed .NET manifest") {
			t.Fatalf("expected matching malformed-manifest warning evidence, got %#v", gap)
		}
	}
}

func TestDotNetManifestErrorPreservesXMLSyntaxDetails(t *testing.T) {
	repo := t.TempDir()
	manifest := filepath.Join(repo, "Broken.csproj")
	testutil.MustWriteFile(t, manifest, "<Project>\n<PackageReference")
	_, err := parsePackageReferences(repo, manifest)
	var syntaxError *xml.SyntaxError
	if !errors.As(err, &syntaxError) || syntaxError.Line != 2 {
		t.Fatalf("expected underlying XML syntax error on line 2, got %v", err)
	}
}

func TestDotNetAnalysisReturnsUnsupportedManifestEncoding(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "Unsupported.csproj"), `<?xml version="1.0" encoding="windows-1252"?><Project><ItemGroup><PackageReference Include="Newtonsoft.Json" /></ItemGroup></Project>`)

	_, err := NewAdapter().Analyse(context.Background(), language.Request{RepoPath: repo, Dependency: "newtonsoft.json"})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "encoding") {
		t.Fatalf("expected unsupported manifest encoding to be returned, got %v", err)
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
