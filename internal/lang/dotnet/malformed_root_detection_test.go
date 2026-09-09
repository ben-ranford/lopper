//go:build !regressionproof

package dotnet

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestDotNetDetectionKeepsNestedRootsWhenRootManifestIsValid(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "Root.csproj"), "<Project><ItemGroup><PackageReference Include=\"Root.Package\" /></ItemGroup></Project>")
	testutil.MustWriteFile(t, filepath.Join(repo, "nested", "Nested.csproj"), "<Project><ItemGroup><PackageReference Include=\"Nested.Package\" /></ItemGroup></Project>")

	detection, err := NewAdapter().DetectWithConfidence(context.Background(), repo)
	if err != nil {
		t.Fatalf("detect .NET projects: %v", err)
	}
	want := []string{repo, filepath.Join(repo, "nested")}
	if !slices.Equal(detection.Roots, want) {
		t.Fatalf("expected valid root and nested project roots to remain separate, got %#v", detection.Roots)
	}
}

func TestDotNetDetectionSeparatesMalformedChildFromValidAncestor(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "Root.csproj"), `<Project><ItemGroup><PackageReference Include="Root.Package" /></ItemGroup></Project>`)
	testutil.MustWriteFile(t, filepath.Join(repo, "apps", "Broken.csproj"), "<Project><PackageReference Include=\"broken\"")
	testutil.MustWriteFile(t, filepath.Join(repo, "apps", "child", "Child.csproj"), `<Project><ItemGroup><PackageReference Include="Child.Package" /></ItemGroup></Project>`)

	detection, err := NewAdapter().DetectWithConfidence(context.Background(), repo)
	if err != nil {
		t.Fatalf("detect .NET projects: %v", err)
	}
	want := []string{repo, filepath.Join(repo, "apps")}
	if !slices.Equal(detection.Roots, want) {
		t.Fatalf("expected valid ancestor and malformed child fallback roots, got %#v", detection.Roots)
	}
}

func TestDotNetDetectionCentralManifestFallbackOwnership(t *testing.T) {
	t.Run("nested central manifest belongs to its valid project ancestor", func(t *testing.T) {
		repo := t.TempDir()
		testutil.MustWriteFile(t, filepath.Join(repo, "Root.csproj"), `<Project><ItemGroup><PackageReference Include="Root.Package" /></ItemGroup></Project>`)
		testutil.MustWriteFile(t, filepath.Join(repo, "src", centralPackagesFile), `<Project><ItemGroup><PackageVersion Include="Central.Package" /></ItemGroup></Project>`)

		detection, err := NewAdapter().DetectWithConfidence(context.Background(), repo)
		if err != nil {
			t.Fatalf("detect .NET project with nested central manifest: %v", err)
		}
		if !slices.Equal(detection.Roots, []string{repo}) {
			t.Fatalf("expected the valid project root to own nested central packages, got %#v", detection.Roots)
		}
	})

	t.Run("orphan malformed central manifest remains a fallback root", func(t *testing.T) {
		repo := t.TempDir()
		orphan := filepath.Join(repo, "orphan")
		testutil.MustWriteFile(t, filepath.Join(orphan, centralPackagesFile), `<Project><ItemGroup><PackageVersion Include="broken"`)

		detection, err := NewAdapter().DetectWithConfidence(context.Background(), repo)
		if err != nil {
			t.Fatalf("detect orphan malformed central manifest: %v", err)
		}
		if !slices.Equal(detection.Roots, []string{orphan}) {
			t.Fatalf("expected orphan malformed central manifest fallback root, got %#v", detection.Roots)
		}
	})
}

func TestDotNetMalformedProjectBoundaryDoesNotBorrowAncestorDeclarations(t *testing.T) {
	repo := t.TempDir()
	broken := filepath.Join(repo, "broken")
	scanner := newScanInputDiscoverer(repo, &sourceDiscovery{})
	scanner.projectDependencies[repo] = []string{"root.package"}
	scanner.malformedManifestRoots[broken] = struct{}{}

	dependencies, hasProject, projectRoot := scanner.sourceDependencies(filepath.Join("broken", programSourceFileName))
	if len(dependencies) != 0 || !hasProject || projectRoot != "" {
		t.Fatalf("expected malformed child source to use an empty isolated declaration set, got dependencies=%#v hasProject=%t projectRoot=%q", dependencies, hasProject, projectRoot)
	}

	files := excludeNestedProjectSources([]sourceDocument{
		{RelativePath: programSourceFileName, ProjectRoot: repo},
		{RelativePath: filepath.Join("nested", programSourceFileName), ProjectRoot: filepath.Join(repo, "nested")},
		{RelativePath: filepath.Join("broken", programSourceFileName)},
	}, repo, scanner.malformedManifestRoots)
	if len(files) != 1 || files[0].RelativePath != programSourceFileName {
		t.Fatalf("expected valid root source only after excluding nested and malformed-project sources, got %#v", files)
	}
}
