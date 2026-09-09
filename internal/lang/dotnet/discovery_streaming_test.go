package dotnet

import (
	"context"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/testutil"
)

const streamingTestProgramSource = "Program.cs"

func assertDotNetScanDoesNotRetainSourceDocuments(t *testing.T) {
	t.Helper()
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, streamingTestProgramSource), "using Newtonsoft.Json;\n")

	inputs, err := discoverScanInputs(context.Background(), repo)
	if err != nil {
		t.Fatalf("discover scan inputs: %v", err)
	}
	if len(inputs.SourceFiles) != 0 {
		t.Fatalf("source discovery must not retain source documents, got %#v", inputs.SourceFiles)
	}

	scan, err := scanRepo(context.Background(), repo)
	if err != nil {
		t.Fatalf("scan repo: %v", err)
	}
	if len(scan.Files) != 1 || scan.Files[0].Path != streamingTestProgramSource {
		t.Fatalf("expected source to be parsed during scan, got %#v", scan.Files)
	}
}

func TestDotNetStreamingHelpersHonorExplicitScopeAndSelectedRoots(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "Root.csproj"), `<Project><ItemGroup><PackageReference Include="Root.Package" /></ItemGroup></Project>`)
	testutil.MustWriteFile(t, filepath.Join(repo, streamingTestProgramSource), "using Root.Package;\n")
	selected := filepath.Join(repo, "selected")
	testutil.MustWriteFile(t, filepath.Join(selected, "Child.csproj"), `<Project><ItemGroup><PackageReference Include="Child.Package" /></ItemGroup></Project>`)
	testutil.MustWriteFile(t, filepath.Join(selected, streamingTestProgramSource), "using Child.Package;\n")

	inputs, err := discoverScanInputs(context.Background(), repo, "repo")
	if err != nil {
		t.Fatalf("discover scoped scan inputs: %v", err)
	}
	if len(inputs.SourceFiles) != 0 {
		t.Fatalf("scoped source discovery retained documents: %#v", inputs.SourceFiles)
	}

	scan, err := scanRepo(context.Background(), repo, "repo")
	if err != nil {
		t.Fatalf("scan explicit repository scope: %v", err)
	}
	if len(scan.Files) != 2 {
		t.Fatalf("expected explicit repository scope to parse both sources, got %#v", scan.Files)
	}

	parent, err := scanRepoWithIsolatedProjectRoots(context.Background(), repo, "package", []string{selected})
	if err != nil {
		t.Fatalf("scan parent with selected child: %v", err)
	}
	if len(parent.Files) != 1 || parent.Files[0].Path != streamingTestProgramSource {
		t.Fatalf("expected selected child source to be deferred from parent scan, got %#v", parent.Files)
	}
}

func TestDotNetLegacyDependencyCollectorSkipsMalformedManifest(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "Broken.csproj"), `<Project><ItemGroup><PackageReference Include="Broken"`)

	dependencies, err := collectDeclaredDependencies(repo)
	if err != nil {
		t.Fatalf("collect malformed manifest dependencies: %v", err)
	}
	if len(dependencies) != 0 {
		t.Fatalf("expected malformed manifest to remain skipped by the compatibility collector, got %#v", dependencies)
	}
}

func TestDotNetStreamingScanAvoidsFirstPassSourceReads(t *testing.T) {
	const sourceCount = 3
	const sourceBytes = 1 << 20

	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "App.csproj"), "<Project></Project>\n")
	content := strings.Repeat("x", sourceBytes)
	for index := range sourceCount {
		testutil.MustWriteFile(t, filepath.Join(repo, "src", "File"+strconv.Itoa(index)+".cs"), content)
	}

	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	inputs, err := discoverScanInputs(context.Background(), repo)
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatalf("scan inputs: %v", err)
	}
	if inputs.SkippedFileLimit || inputs.SkippedGenerated != 0 {
		t.Fatalf("unexpected input discovery result: %#v", inputs)
	}
	allocated := after.TotalAlloc - before.TotalAlloc
	t.Logf("metadata scan allocated %d bytes for %d source bytes", allocated, len(content)*sourceCount)
	if allocated > 1<<20 {
		t.Fatalf("expected metadata scan to avoid loading source contents, got %d bytes", allocated)
	}

	scan, err := scanRepo(context.Background(), repo)
	if err != nil {
		t.Fatalf("streaming scan: %v", err)
	}
	if len(scan.Files) != sourceCount {
		t.Fatalf("expected %d parsed files, got %#v", sourceCount, scan.Files)
	}
}
