//go:build !regressionproof

package dotnet

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
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

func TestDotNetMalformedRootFallbackIncludesOnlyUnownedCentralManifest(t *testing.T) {
	repo := t.TempDir()

	malformedProject := newScanInputDiscoverer(repo, &sourceDiscovery{})
	malformedProject.malformedManifestRoots[repo] = struct{}{}
	if !malformedProject.hasMalformedRootFallback() {
		t.Fatal("expected malformed root project to retain fallback source ownership")
	}

	malformedCentral := newScanInputDiscoverer(repo, &sourceDiscovery{})
	malformedCentral.malformedCentralRoots[repo] = struct{}{}
	if !malformedCentral.hasMalformedRootFallback() {
		t.Fatal("expected orphan malformed central manifest to retain fallback source ownership")
	}

	ownedCentral := newScanInputDiscoverer(repo, &sourceDiscovery{})
	ownedCentral.malformedCentralRoots[repo] = struct{}{}
	ownedCentral.projectDependencies[repo] = []string{"root.package"}
	if ownedCentral.hasMalformedRootFallback() {
		t.Fatal("expected valid root project to own malformed central manifest sources")
	}
}

func TestDotNetSourceFilesKeepMapperOwnershipPolicy(t *testing.T) {
	repo := t.TempDir()
	nested := filepath.Join(repo, "nested")
	scanner := newScanInputDiscoverer(repo, &sourceDiscovery{Files: []sourceDocument{
		{RelativePath: filepath.Join("nested", programSourceFileName)},
		{RelativePath: filepath.Join("broken", programSourceFileName)},
	}})
	scanner.projectDependencies[nested] = []string{"nested.package"}
	scanner.malformedManifestRoots[filepath.Join(repo, "broken")] = struct{}{}

	files := scanner.sourceFiles("package")
	if len(files) != 2 {
		t.Fatalf("expected valid and malformed-boundary sources to remain without selected roots, got %#v", files)
	}
	if files[0].ProjectRoot != nested || !strings.HasPrefix(files[0].MapperKey, "fallback-enabled\x00") {
		t.Fatalf("expected valid project source to keep undeclared-import fallback, got %#v", files[0])
	}
	if files[1].ProjectRoot != "" || !strings.HasPrefix(files[1].MapperKey, "fallback-disabled\x00") {
		t.Fatalf("expected malformed-boundary source to keep restrictive fallback policy, got %#v", files[1])
	}
}

func TestDotNetSelectedProjectIsolationStaysWithinAnalysisRoot(t *testing.T) {
	repo := t.TempDir()
	selected := filepath.Join(repo, "selected")
	scanner := newScanInputDiscoverer(repo, &sourceDiscovery{}, []string{selected})
	if !scanner.isolatedProjectRoot(filepath.Join(selected, "child", programSourceFileName)) {
		t.Fatal("expected selected project subtree to be isolated")
	}
	if scanner.isolatedProjectRoot(repo) {
		t.Fatal("analysis root must not isolate itself")
	}
	if scanner.isolatedProjectRoot(filepath.Dir(repo)) {
		t.Fatal("paths outside the analysis root must not be isolated")
	}
	files := scanner.excludeIsolatedProjectSources([]sourceDocument{
		{RelativePath: programSourceFileName},
		{RelativePath: filepath.Join("selected", programSourceFileName)},
	})
	if len(files) != 1 || files[0].RelativePath != programSourceFileName {
		t.Fatalf("expected selected subtree sources to be removed, got %#v", files)
	}
}

func TestDotNetDeclaredDependenciesExcludeOnlySelectedSubtree(t *testing.T) {
	repo := t.TempDir()
	selected := filepath.Join(repo, "selected")
	unselected := filepath.Join(repo, "unselected")
	scanner := newScanInputDiscoverer(repo, &sourceDiscovery{}, []string{selected})
	scanner.projectDependencies[repo] = []string{"root.package"}
	scanner.projectDependencies[selected] = []string{"selected.package"}
	scanner.projectDependencies[unselected] = []string{"unselected.package"}
	scanner.centralDependencies[selected] = []string{"selected.central"}
	scanner.centralDependencies[unselected] = []string{"unselected.central"}
	addDependencies(scanner.dependencySet, []string{"root.package", "selected.package", "unselected.package", "selected.central", "unselected.central"})

	if dependencies := scanner.declaredDependencies("package"); !slices.Equal(dependencies, []string{"root.package", "unselected.central", "unselected.package"}) {
		t.Fatalf("expected only selected subtree declarations to be deferred, got %#v", dependencies)
	}
	if dependencies := scanner.declaredDependencies("repo"); !slices.Equal(dependencies, []string{"root.package", "selected.central", "selected.package", "unselected.central", "unselected.package"}) {
		t.Fatalf("expected repository scope to retain all declarations, got %#v", dependencies)
	}
}
